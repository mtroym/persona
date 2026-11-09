package pgdocker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestURLPreservesPassword(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	for _, tc := range []struct{ name, password string }{
		{"space", "has space"},
		{"plus", "literal+plus"},
		{"reserved", "colon:at@slash/question?hash#percent%"},
		{"generated", strings.Repeat("ab", 24)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := pgx.ParseConfig(URL(tc.password, 25432))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Password != tc.password {
				t.Errorf("password = %q, want %q", cfg.Password, tc.password)
			}
			if cfg.User != User || cfg.Host != "127.0.0.1" || cfg.Port != 25432 || cfg.Database != Database || cfg.TLSConfig != nil {
				t.Error("database URL changed its connection settings")
			}
		})
	}
	retained := &Container{User: "retained_role", Database: "retained_db"}
	cfg, err := pgx.ParseConfig(retained.URL("has space", 25432))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "has space" || cfg.User != retained.User || cfg.Database != retained.Database {
		t.Error("retained container credentials changed in its URL")
	}
}

// fake is docker on a machine with no containers yet: run makes one (with the password it was given), rm removes it.
type fake struct {
	calls     []string
	password  string
	user      string // the POSTGRES_USER (and POSTGRES_DB) it was made with
	exists    bool
	port      int  // the port the container publishes (docker port); 0: none
	taken     int  // a port someone else holds now: docker start fails while the container publishes it
	published bool // the container is published at all (false: made on a network of its own)
}

func (f *fake) run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.HasPrefix(call, "docker ps -a") && f.exists:
		return []byte(ManagerContainer("m-0a1b2c") + "\n"), nil
	case strings.HasPrefix(call, "docker run"):
		f.exists, f.published = true, true
		for i, a := range args {
			if a == "-p" {
				_, _ = fmt.Sscanf(args[i+1], "127.0.0.1:%d:5432", &f.port)
			}
		}
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "POSTGRES_PASSWORD="); ok {
				f.password = v
			}
		}
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "POSTGRES_USER="); ok {
				f.user = v
			}
		}
	case strings.HasPrefix(call, "docker inspect -f {{range .Config.Env}}"):
		return []byte("POSTGRES_USER=" + f.user + "\nPOSTGRES_PASSWORD=" + f.password + "\nPOSTGRES_DB=" + f.user + "\n"), nil
	case strings.HasPrefix(call, "docker port"):
		if !f.published {
			return nil, errors.New("exit status 1")
		}
		return []byte(fmt.Sprintf("127.0.0.1:%d\n", f.port)), nil
	case strings.HasPrefix(call, "docker start") && f.taken != 0 && f.port == f.taken:
		return []byte("Error response from daemon: driver failed programming external connectivity: Bind for 127.0.0.1:25432 failed: port is already allocated"), errors.New("exit status 1")
	case strings.HasPrefix(call, "docker rm -f"):
		f.exists = false
	}
	return nil, nil
}

func (f *fake) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// The first call makes the container with a new password, on 127.0.0.1, and returns its URL; later calls (with the
// kept password, or none: read back from the container) make nothing and return the same URL. It writes no file.
func TestProvisionManagerDBIsIdempotent(t *testing.T) {
	f := &fake{}
	o := ManagerDBOptions{Run: f.run, LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		FreePort: func() (int, error) { return 25432, nil }, Sleep: func(time.Duration) {}, Wait: time.Second}
	ctx := context.Background()
	url, created, err := ProvisionManagerDB(ctx, "m-0a1b2c", o)
	if err != nil || !created || len(f.password) != 48 || url != URL(f.password, 25432) {
		t.Fatalf("first: %q %v %v", url, created, err)
	}
	if f.count("docker volume create persona-mgr-m-0a1b2c-data") != 1 ||
		f.count("docker run -d --name persona-mgr-m-0a1b2c-pg --restart unless-stopped -p 127.0.0.1:25432:5432") != 1 {
		t.Fatalf("calls: %s", strings.Join(f.calls, "\n"))
	}
	for _, c := range f.calls {
		if strings.Contains(c, f.password) {
			t.Fatalf("the password on a command line: %s", c)
		}
	}
	for _, kept := range []string{f.password, ""} {
		o.Password = kept
		again, created, err := ProvisionManagerDB(ctx, "m-0a1b2c", o)
		if err != nil || created || again != url || f.count("docker run") != 1 {
			t.Fatalf("again (kept %q): %q %v %v", kept, again, created, err)
		}
	}
	f2 := &fake{}
	o.Run, o.LookPath = f2.run, func(string) (string, error) { return "", errors.New("none") }
	if _, _, err := ProvisionManagerDB(ctx, "m-0a1b2c", o); !errors.Is(err, ErrNoDocker) || len(f2.calls) != 0 {
		t.Fatalf("no docker: %v %v", err, f2.calls)
	}
}

// A stopped container whose port someone took meanwhile is made again on a new free port, over the same volume and
// with the same password, and the new URL comes back for the caller to keep.
func TestProvisionManagerDBMovesOffATakenPort(t *testing.T) {
	f := &fake{}
	ports := []int{25432, 25433}
	o := ManagerDBOptions{Run: f.run, LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		FreePort: func() (int, error) { p := ports[0]; ports = ports[1:]; return p, nil }, Sleep: func(time.Duration) {}, Wait: time.Second}
	ctx := context.Background()
	first, _, err := ProvisionManagerDB(ctx, "m-0a1b2c", o)
	if err != nil {
		t.Fatal(err)
	}
	f.taken = 25432
	o.Password = f.password
	moved, created, err := ProvisionManagerDB(ctx, "m-0a1b2c", o)
	if err != nil || created || moved == first || moved != URL(o.Password, 25433) || f.count("docker volume rm") != 0 ||
		f.count("docker rm -f persona-mgr-m-0a1b2c-pg") != 1 || f.count("docker volume create persona-mgr-m-0a1b2c-data") != 2 {
		t.Fatalf("moved: %q %v %v\n%s", moved, created, err, strings.Join(f.calls, "\n"))
	}
}

// A container by its name that publishes no port (made on a network of its own by the docker script) can't be used
// from this machine: a clear error, not a URL with port 0.
func TestProvisionManagerDBNotPublished(t *testing.T) {
	f := &fake{exists: true, password: "pw"}
	o := ManagerDBOptions{Run: f.run, LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		FreePort: func() (int, error) { return 25432, nil }, Sleep: func(time.Duration) {}, Wait: time.Second}
	if url, _, err := ProvisionManagerDB(context.Background(), "m-0a1b2c", o); !errors.Is(err, ErrNotPublished) || url != "" ||
		!strings.Contains(err.Error(), "docker rm -f persona-mgr-m-0a1b2c-pg") {
		t.Fatalf("not published: %q %v", url, err)
	}
}

// A manager's container made before the rename has the role and database jarvis (#6149): its URL keeps them, also when
// it is made again on another port, instead of today's persona, which it has no role for.
func TestProvisionManagerDBKeepsAnOlderContainersOwnUser(t *testing.T) {
	f := &fake{exists: true, published: true, port: 25432, user: "jarvis", password: "kept"}
	ports := []int{25433}
	o := ManagerDBOptions{Run: f.run, LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		FreePort: func() (int, error) { p := ports[0]; ports = ports[1:]; return p, nil }, Sleep: func(time.Duration) {}, Wait: time.Second}
	ctx := context.Background()
	got, created, err := ProvisionManagerDB(ctx, "m-0a1b2c", o)
	want := "postgres://jarvis:kept@127.0.0.1:25432/jarvis?sslmode=disable"
	if err != nil || created || got != want || f.count("docker run") != 0 {
		t.Fatalf("existing: %q %v %v", got, created, err)
	}
	if f.count("docker exec persona-mgr-m-0a1b2c-pg pg_isready -h 127.0.0.1 -p 5432 -U jarvis -d jarvis") == 0 {
		t.Fatalf("asked as another role:\n%s", strings.Join(f.calls, "\n"))
	}
	// its port taken meanwhile: made again over the same volume with its own role and database
	f.taken = 25432
	o.Password = "kept"
	moved, _, err := ProvisionManagerDB(ctx, "m-0a1b2c", o)
	if err != nil || moved != "postgres://jarvis:kept@127.0.0.1:25433/jarvis?sslmode=disable" || f.user != "jarvis" ||
		f.count("docker run -d --name persona-mgr-m-0a1b2c-pg --restart unless-stopped -p 127.0.0.1:25433:5432 -v persona-mgr-m-0a1b2c-data:/var/lib/postgresql -e POSTGRES_USER=jarvis -e POSTGRES_PASSWORD -e POSTGRES_DB=jarvis") != 1 {
		t.Fatalf("moved: %q %v\n%s", moved, err, strings.Join(f.calls, "\n"))
	}
	// a new one has today's names
	if c := (&Container{}); c.URL("pw", 1) != URL("pw", 1) || !strings.Contains(URL("pw", 1), "//"+User+":pw@") {
		t.Fatalf("new: %q", URL("pw", 1))
	}
}
