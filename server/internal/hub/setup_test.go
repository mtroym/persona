package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

var updateGolden = flag.Bool("update-hubjson-golden", false, "rewrite testdata/hubjson/*.golden.json")

func setupTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, k := range []string{"PERSONA_HOME", "PERSONA_LOCAL_NODE",
		"PERSONA_DATABASE_URL", "PERSONA_PUBLIC_URL", "PERSONA_LISTEN"} {
		t.Setenv(k, "")
	}
	return home
}

// realPath is p with symlinks resolved, as hubBinary returns the binary: macOS's default temp dir, where t.TempDir()
// goes without GOTMPDIR, is under /var, a symlink to /private/var.
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// normalizeHost takes this machine's host name and a generated token out of a config, for goldens.
func normalizeHost(s, token string) string {
	host, _ := os.Hostname()
	short := strings.Split(host, ".")[0]
	if token != "" {
		s = strings.ReplaceAll(s, token, "<generated token>")
	}
	s = strings.ReplaceAll(s, host, "<host>")
	if short != host {
		s = strings.ReplaceAll(s, `"`+short+`"`, `"<host>"`)
	}
	return s
}

// Old hub.json files load exactly as before: the new-install defaults (docs/assistant/onboarding.md 4.1) are not in
// defaultConfig(), so a file without "public" or "admin.first_setup" gets no TLS and no LAN setup code.
func TestLegacyHubJSONLoadsUnchanged(t *testing.T) {
	for _, name := range []string{"dbsh-only", "handwritten-no-public"} {
		t.Run(name, func(t *testing.T) {
			home := setupTestHome(t)
			in, err := os.ReadFile(filepath.Join("testdata", "hubjson", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, ".persona")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "hub.json"), in, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Public.TLSListen != "" || cfg.Admin.FirstSetup != "" {
				t.Fatalf("new-install defaults leaked into an old file: public %+v admin %+v", cfg.Public, cfg.Admin)
			}
			loaded, _ := json.MarshalIndent(cfg, "", "  ")
			file, _ := os.ReadFile(filepath.Join(dir, "hub.json"))
			generated := ""
			if !bytes.Contains(in, []byte(`"token"`)) {
				generated = cfg.Token
			}
			got := "// the config the hub runs with\n" + normalizeHost(string(loaded), generated) +
				"\n// hub.json after loading\n" + normalizeHost(string(file), generated) + "\n"
			golden := filepath.Join("testdata", "hubjson", name+".golden.json")
			if *updateGolden {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test -run TestLegacyHubJSONLoadsUnchanged -update-hubjson-golden)", err)
			}
			if got != string(want) {
				t.Fatalf("an old hub.json loads differently now:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestNewInstallDefaultsAreNotDefaults(t *testing.T) {
	setupTestHome(t)
	d := defaultConfig()
	if d.Public.TLSListen != "" || d.Admin.FirstSetup != "" {
		t.Fatalf("defaultConfig has new-install defaults: %+v %+v", d.Public, d.Admin)
	}
	lan := ip4(192, 168, 1, 20)
	n := newInstallConfig(installInputs{lanIP: lan, listen: ":18660", tls: ":18662", agents: true})
	if n.Public.TLSListen != lan+":18662" || n.PublicURL != "http://"+lan+":18660" || n.Admin.FirstSetup != "code" || len(n.Machines) != 1 {
		t.Fatalf("new install: %+v %s %+v %+v", n.Public, n.PublicURL, n.Admin, n.Machines)
	}
	if n := newInstallConfig(installInputs{lanIP: lan, listen: ":18660", tls: ":18662", outside: true}); n.Public.TLSListen != ":18662" || len(n.Machines) != 0 || n.Machines == nil {
		t.Fatalf("outside, no agents: %+v %#v", n.Public, n.Machines)
	}
	if n := newInstallConfig(installInputs{listen: ":18660", tls: ":18662"}); n.Public.TLSListen != "" || !strings.Contains(n.PublicURL, ":18660") {
		t.Fatalf("no LAN address: TLS stays off: %+v %s", n.Public, n.PublicURL)
	}
}

// fakeHub answers what setup asks the running hub: health, endpoints, a pair code and a setup code, and the roster
// with the built-in manager online as a service of its own.
func fakeHub(t *testing.T, token *string, passwordSet bool) (port int) {
	t.Helper()
	return fakeHubWith(t, token, passwordSet, func() bool { return true })
}

// fakeHubWith is fakeHub with builtinOnline saying whether the built-in manager's service is on the bus.
func fakeHubWith(t *testing.T, token *string, passwordSet bool, builtinOnline func() bool) (port int) {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+*token }
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /api/v1/endpoints", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, map[string]any{"endpoints": []any{}})
	})
	mux.HandleFunc("POST /api/v1/auth/pair-codes", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, map[string]any{"token": "jp_test", "url": "http://hub.example.com:1", "expires_at": time.Now().Add(10 * time.Minute)})
	})
	mux.HandleFunc("GET /api/v1/managers", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, map[string]any{"managers": []managerInfo{{ID: "assistant", Builtin: true, Online: builtinOnline(), Deploy: "service"}}})
	})
	mux.HandleFunc("POST /api/v1/admin/setup-code", func(w http.ResponseWriter, r *http.Request) {
		if passwordSet {
			writeErrCode(w, 409, "already_set", errors.New("set"))
			return
		}
		writeJSON(w, map[string]any{"code": "ABCD-EFGH", "expires_at": time.Now().Add(10 * time.Minute)})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ = strconv.Atoi(p)
	return port
}

type fakeCommands struct {
	calls   []string
	envs    []string
	replies map[string]fakeReply // by the call's prefix
}

type fakeReply struct {
	out string
	err bool
}

func (f *fakeCommands) run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	f.envs = append(f.envs, env...)
	best := ""
	for prefix := range f.replies {
		if strings.HasPrefix(call, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return nil, nil
	}
	r := f.replies[best]
	if r.err {
		return []byte(r.out), errors.New("exit status 1")
	}
	return []byte(r.out), nil
}

func (f *fakeCommands) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func testSetupEnv(t *testing.T, home string, o setupOptions, cmds *fakeCommands, have ...string) (*setupEnv, *bytes.Buffer) {
	t.Helper()
	root := filepath.Join(home, ".persona")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "personad"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PERSONA_HUB_SOCKET", "")
	var out bytes.Buffer
	e := &setupEnv{
		home: home, root: root, goos: "linux", uid: 1000, user: "someone", out: &out, o: o,
		run:    cmds.run,
		runTTY: func(context.Context, string, ...string) error { return errors.New("no terminal") },
		lookPath: func(name string) (string, error) {
			for _, h := range have {
				if h == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		},
		ask:          func(string) (bool, bool) { return false, false },
		lanIPv4:      func() string { return ip4(192, 168, 1, 20) },
		portFree:     func(int) bool { return true },
		exe:          func() (string, error) { return "/nonexistent", nil },
		pingDB:       func(context.Context, string) error { return nil },
		doctor:       func(context.Context, string, bool) []string { return nil },
		getenv:       func(string) string { return "" },
		sleep:        func(time.Duration) {},
		isContainer:  func() bool { return false },
		builtinCode:  func(context.Context, *Config) (string, error) { return "mi_setup", nil },
		voidCode:     func(context.Context, *Config, string) error { return nil },
		keptMachines: func(context.Context, *Config) (map[string]string, error) { return nil, nil },
		healthWait:   time.Second, dbWait: time.Second,
		dbContainer: setupDBContainer, dbVolume: setupDBVolume, dbPort: setupDBPort,
	}
	return e, &out
}

// A new install: the whole file with the new defaults, the database container db.sh would make, the socket units,
// and at the end the pair link and the admin URL with the setup code (--json: one object a line).
func TestSetupNewInstall(t *testing.T) {
	home := setupTestHome(t)
	var token string
	port := fakeHub(t, &token, false)
	cmds := &fakeCommands{replies: map[string]fakeReply{
		"docker ps -a":               {out: ""},
		"docker port":                {out: "127.0.0.1:18661\n"},
		"systemctl --user is-active": {err: true},
		"loginctl show-user":         {out: "no\n"},
		"loginctl enable-linger":     {err: true, out: "Access denied"},
		"docker info":                {out: "27.0\n"},
	}}
	o := setupOptions{listen: ":" + strconv.Itoa(port), tls: ":18662", json: true}
	e, out := testSetupEnv(t, home, o, cmds, "docker", "systemctl", "loginctl", "herdr")
	// The hub "starts" when the units do: the token is what setup wrote.
	e.pingDB = func(context.Context, string) error {
		cfg, _ := readHubJSON(e.root)
		raw, _ := cfg.Get("token")
		_ = json.Unmarshal(raw, &token)
		return nil
	}
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var cfg Config
	b, _ := os.ReadFile(filepath.Join(e.root, "hub.json"))
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	lan := ip4(192, 168, 1, 20)
	if cfg.Public.TLSListen != lan+":18662" || cfg.PublicURL != "http://"+lan+":"+strconv.Itoa(port) || cfg.Admin.FirstSetup != "code" ||
		cfg.Token == "" || len(cfg.Machines) != 1 || !strings.HasPrefix(cfg.DatabaseURL, "postgres://persona:") || !strings.Contains(cfg.DatabaseURL, "@127.0.0.1:18661/persona") {
		t.Fatalf("hub.json: %s", b)
	}
	if fi, _ := os.Stat(filepath.Join(e.root, "hub.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("hub.json mode %v", fi.Mode())
	}
	// db.sh's container, the password through the environment only.
	run := ""
	for _, c := range cmds.calls {
		if strings.HasPrefix(c, "docker run") {
			run = c
		}
	}
	if !strings.Contains(run, "--name persona-postgres") || !strings.Contains(run, "-v persona-pgdata:/var/lib/postgresql") ||
		!strings.Contains(run, "-p 127.0.0.1:18661:5432") || !strings.HasSuffix(run, "postgres:18-alpine") || strings.Contains(run, "POSTGRES_PASSWORD=") {
		t.Fatalf("docker run: %q", run)
	}
	if len(cmds.envs) != 3 || !strings.HasPrefix(cmds.envs[0], "POSTGRES_PASSWORD=") {
		t.Fatalf("password env: %v", cmds.envs)
	}
	// The built-in manager: its own service, bound by the manager's program with the code in its environment only.
	if cfg.Assistant.Mode != AssistantModeService {
		t.Fatalf("assistant.mode %q:\n%s", cfg.Assistant.Mode, b)
	}
	install := filepath.Join(e.root, "bin", "personad") + " manager install --builtin --join http://127.0.0.1:" + strconv.Itoa(port)
	if !cmds.called(install) || strings.Join(cmds.envs[1:], " ") != "PERSONA_HOME="+e.root+" PERSONA_MANAGER_CODE=mi_setup" {
		t.Fatalf("manager install: %v %v", cmds.calls, cmds.envs)
	}
	for _, c := range cmds.calls {
		if strings.Contains(c, "mi_setup") {
			t.Fatalf("the code on a command line: %q", c)
		}
	}
	// hub-systemd.sh's units; the TLS address is bound, so FreeBind.
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	socket, _ := os.ReadFile(filepath.Join(unitDir, "personad.socket"))
	if !strings.Contains(string(socket), "ListenStream="+strconv.Itoa(port)+"\nListenStream="+lan+":18662\n") || !strings.Contains(string(socket), "FreeBind=true") {
		t.Fatalf("socket unit:\n%s", socket)
	}
	for _, c := range []string{"systemctl --user daemon-reload", "systemctl --user enable personad", "systemctl --user stop personad.socket personad", "systemctl --user enable personad.socket", "systemctl --user start personad.socket", "systemctl --user start personad"} {
		if !cmds.called(c) {
			t.Fatalf("no %q in %v", c, cmds.calls)
		}
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var steps []string
	for _, l := range lines[:len(lines)-1] {
		var s struct {
			Step   string `json:"step"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
			Next   string `json:"next"`
		}
		if err := json.Unmarshal([]byte(l), &s); err != nil || s.Step == "" || !s.OK {
			t.Fatalf("step line %q", l)
		}
		steps = append(steps, s.Step)
		if s.Step == "service" && !strings.Contains(s.Next, "sudo loginctl enable-linger someone") {
			t.Fatalf("linger not offered: %q", l)
		}
	}
	if strings.Join(steps, ",") != "config,database,service,health,manager,doctor" {
		t.Fatalf("steps %v", steps)
	}
	var done map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &done); err != nil || done["done"] != true ||
		!strings.HasPrefix(done["pair_url"].(string), "persona://pair?") ||
		done["admin_url"] != "http://"+lan+":"+strconv.Itoa(port)+"/admin#setup=ABCD-EFGH" || done["setup_code"] != "ABCD-EFGH" || done["expires_at"] == "" {
		t.Fatalf("done line %q", lines[len(lines)-1])
	}
	// Again: nothing changes, nothing restarts.
	before, _ := os.ReadFile(filepath.Join(e.root, "hub.json"))
	cmds.calls = nil
	cmds.replies["systemctl --user is-active"] = fakeReply{}
	cmds.replies["loginctl show-user"] = fakeReply{out: "yes\n"}
	out.Reset()
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("again: %v\n%s", err, out)
	}
	if after, _ := os.ReadFile(filepath.Join(e.root, "hub.json")); string(after) != string(before) {
		t.Fatalf("second run changed hub.json:\n%s\n%s", before, after)
	}
	if cmds.called("systemctl --user restart") || cmds.called("systemctl --user stop") || cmds.called("docker run") {
		t.Fatalf("second run restarted things: %v", cmds.calls)
	}
	// An upgrade (install.sh --hub replaced bin/personad): the units are the same, the running hub is the old one.
	if err := os.WriteFile(filepath.Join(e.root, "bin", "personad"), []byte("#!/bin/sh\n# new\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmds.calls = nil
	out.Reset()
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	if !cmds.called("systemctl --user restart personad") || cmds.called("systemctl --user stop") {
		t.Fatalf("upgrade did not restart the hub: %v", cmds.calls)
	}
}

// A hub.json a hub already ran with (a token, no "public") keeps every byte: no TLS, no LAN setup code.
func TestSetupKeepsAnExistingHubJSON(t *testing.T) {
	home := setupTestHome(t)
	token := "handwritten-token"
	port := fakeHub(t, &token, true)
	cmds := &fakeCommands{replies: map[string]fakeReply{"loginctl show-user": {out: "yes\n"}}}
	e, out := testSetupEnv(t, home, setupOptions{listen: ":18660", tls: ":18662", listenSet: true}, cmds, "systemctl", "loginctl")
	file := `{"token":"handwritten-token","listen":":` + strconv.Itoa(port) + `","public_url":"http://hub.example.com:1","database_url":"postgres://x"}`
	if err := os.WriteFile(filepath.Join(e.root, "hub.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "hub.json")); string(b) != file {
		t.Fatalf("hub.json changed: %s", b)
	}
	socket, _ := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "personad.socket"))
	if strings.Contains(string(socket), "18662") || strings.Contains(string(socket), "FreeBind") {
		t.Fatalf("socket unit:\n%s", socket)
	}
	text := out.String()
	for _, want := range []string{"已有的值不改", "--listen、--tls 只用于新装", "管理员密码已经设过了", "http://" + ip4(192, 168, 1, 20) + ":" + strconv.Itoa(port) + "/admin\n", "persona://pair?"} {
		if !strings.Contains(text, want) {
			t.Fatalf("no %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "#setup=") {
		t.Fatalf("a setup code with the password set:\n%s", text)
	}
}

// A hub installed before claude / herdr (#4083): rerunning setup once they are there adds this machine while hub.json
// has "machines": [], and restarts the running hub so it sees it; never in a container, with PERSONA_LOCAL_NODE=0, over
// machines already there, or while a manager keeps any machine (their entries left hub.json for its own list). When
// who keeps what does not read, it adds nothing and says why. It reads that after the database step, which starts a
// stopped database.
func TestSetupAddsTheLocalMachineLater(t *testing.T) {
	for _, c := range []struct {
		name, prog, machines  string
		localNode0, container bool
		kept                  map[string]string
		keptErr, dbStopped    bool
		want                  string // the one machine's kind afterwards, "" for none
	}{
		{name: "empty list, claude", prog: "claude", machines: `,"machines":[]`, want: "local"},
		{name: "empty list, herdr", prog: "herdr", machines: `,"machines":[]`, want: "local"},
		{name: "PERSONA_LOCAL_NODE=0", prog: "claude", machines: `,"machines":[]`, localNode0: true},
		{name: "in a container", prog: "claude", machines: `,"machines":[]`, container: true},
		{name: "machines listed", prog: "claude", machines: `,"machines":[{"id":"remote1","name":"remote1","kind":"ssh"}]`, want: "ssh"},
		{name: "a manager keeps this machine", prog: "claude", machines: `,"machines":[]`, kept: map[string]string{"host1": "assistant"}},
		{name: "a manager keeps another machine", prog: "claude", machines: `,"machines":[]`, kept: map[string]string{"remote1": "mgr-b"}},
		{name: "who keeps what does not read", prog: "claude", machines: `,"machines":[]`, keptErr: true},
		{name: "the database was stopped", prog: "claude", machines: `,"machines":[]`, dbStopped: true, want: "local"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := setupTestHome(t)
			token := "existing-token"
			port := fakeHub(t, &token, true)
			cmds := &fakeCommands{replies: map[string]fakeReply{"loginctl show-user": {out: "yes\n"}}}
			e, out := testSetupEnv(t, home, setupOptions{listen: ":18660", tls: ":18662"}, cmds, "systemctl", "loginctl", c.prog)
			e.getenv = func(k string) string {
				if k == "PERSONA_LOCAL_NODE" && c.localNode0 {
					return "0"
				}
				return ""
			}
			e.isContainer = func() bool { return c.container }
			if c.dbStopped {
				cmds.replies["docker ps -a"] = fakeReply{out: setupDBContainer + "\n"}
				e.pingDB = func(context.Context, string) error {
					if !cmds.called("docker start " + setupDBContainer) {
						return errors.New("connection refused")
					}
					return nil
				}
			}
			reads := 0
			e.keptMachines = func(_ context.Context, cfg *Config) (map[string]string, error) {
				reads++
				if cfg.DatabaseURL != "postgres://x" || (c.dbStopped && !cmds.called("docker start "+setupDBContainer)) {
					t.Errorf("who keeps what read before the database step: %q, calls %v", cfg.DatabaseURL, cmds.calls)
				}
				if c.keptErr {
					return nil, errors.New("connection refused")
				}
				return c.kept, nil
			}
			write := func(machines string) {
				file := `{"token":"existing-token","listen":":` + strconv.Itoa(port) + `","public_url":"http://hub.example.com:1","database_url":"postgres://x"` + machines + `}`
				if err := os.WriteFile(filepath.Join(e.root, "hub.json"), []byte(file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// A first run installs the service, so the second has nothing but the machine to restart the hub for.
			write(`,"machines":[{"id":"remote1","name":"remote1","kind":"ssh"}]`)
			if err := e.setup(context.Background()); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if reads != 0 {
				t.Fatalf("who keeps what read %d times over a hub.json that lists machines", reads)
			}
			write(c.machines)
			cmds.calls = nil
			cmds.replies["systemctl --user is-active"] = fakeReply{}
			out.Reset()
			if err := e.setup(context.Background()); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			var cfg Config
			if b, err := os.ReadFile(filepath.Join(e.root, "hub.json")); err != nil || json.Unmarshal(b, &cfg) != nil {
				t.Fatalf("hub.json: %v", err)
			}
			if len(cfg.Machines) > 1 || (len(cfg.Machines) == 1 && cfg.Machines[0].Kind != c.want) || (len(cfg.Machines) == 0) != (c.want == "") {
				t.Fatalf("machines %+v, want one %q", cfg.Machines, c.want)
			}
			added := c.want == "local"
			if strings.Contains(out.String(), "补上本机") != added || strings.Contains(out.String(), "hub.json 补了本机") != added ||
				cmds.called("systemctl --user restart personad") != added {
				t.Fatalf("added=%v: calls %v, output:\n%s", added, cmds.calls, out)
			}
			if wantReads := c.machines == `,"machines":[]` && !c.localNode0 && !c.container; (reads == 1) != wantReads || reads > 1 {
				t.Fatalf("who keeps what read %d times, want it read: %v", reads, wantReads)
			}
			if said := strings.Contains(out.String(), "有问题｜配置：hub.json 里没有电脑，但读不了哪些电脑归管理者管（connection refused）"); said != c.keptErr {
				t.Fatalf("read failure said=%v, want %v:\n%s", said, c.keptErr, out)
			}
		})
	}
}

// Setup reads who keeps what as the hub does, from the hub's own settings and migrating nothing: a machine whose manager
// is gone does not count, none recorded reads as none, and so does a database without the hub's settings (a hub from
// before them); any other failure is one, naming the setting.
func TestSetupReadsTheKeptMachines(t *testing.T) {
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set (scripts/test/test-server.sh runs this on the hub machine)")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, url, db.HubSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	var before json.RawMessage
	if _, err := database.Setting(ctx, machinesKeptKey, &before); err != nil {
		t.Fatal(err)
	}
	put := func(v any) {
		if err := database.PutSetting(ctx, machinesKeptKey, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { put(before) })
	cfg := &Config{DatabaseURL: url}
	put(nil)
	if kept, err := keptMachines(ctx, cfg); err != nil || len(kept) != 0 {
		t.Fatalf("none recorded: %v %v", kept, err)
	}
	put(map[string]string{"host1": assistantwire.AssistantID, "remote1": "gone-" + randomHex(6)})
	if kept, err := keptMachines(ctx, cfg); err != nil || !maps.Equal(kept, map[string]string{"host1": assistantwire.AssistantID}) {
		t.Fatalf("kept %v %v, want host1 by the built-in manager only", kept, err)
	}
	put("not a map")
	if kept, err := keptMachines(ctx, cfg); err == nil || !strings.Contains(err.Error(), machinesKeptKey) {
		t.Fatalf("a value that does not read: %v %v, want an error naming %s", kept, err, machinesKeptKey)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	schema := "setup_kept_" + randomHex(6)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	u, err := neturl.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	if kept, err := keptMachines(ctx, &Config{DatabaseURL: u.String()}); err != nil || len(kept) != 0 {
		t.Fatalf("no hub_settings: %v %v, want nothing kept", kept, err)
	}
	var tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1`, schema).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("%d tables in %s afterwards (%v), want none: setup migrates nothing", tables, schema, err)
	}
}

// A hub listening on loopback only (#4484): the admin URL is the loopback one that opens, said to work on this
// machine only; the same --listen / --tls again says nothing about not changing them.
func TestSetupLoopbackHubAdminURL(t *testing.T) {
	home := setupTestHome(t)
	token := "handwritten-token"
	port := fakeHub(t, &token, false)
	listen := "127.0.0.1:" + strconv.Itoa(port)
	cmds := &fakeCommands{replies: map[string]fakeReply{"loginctl show-user": {out: "yes\n"}}}
	o := setupOptions{listen: listen, tls: "127.0.0.1:18662", listenSet: true, tlsSet: true}
	e, out := testSetupEnv(t, home, o, cmds, "systemctl", "loginctl")
	file := `{"token":"handwritten-token","listen":"` + listen + `","database_url":"postgres://x","public":{"tls_listen":"127.0.0.1:18662"}}`
	if err := os.WriteFile(filepath.Join(e.root, "hub.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	text := out.String()
	for _, want := range []string{"http://" + listen + "/admin#setup=ABCD-EFGH\n", "只能在这台电脑上打开", "在这台电脑的浏览器里打开"} {
		if !strings.Contains(text, want) {
			t.Fatalf("no %q in:\n%s", want, text)
		}
	}
	for _, not := range []string{ip4(192, 168, 1, 20), "只用于新装", "用手机浏览器"} {
		if strings.Contains(text, not) {
			t.Fatalf("%q in:\n%s", not, text)
		}
	}
	// --json says so too.
	e.o.json = true
	out.Reset()
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var done map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &done); err != nil || done["admin_local_only"] != true ||
		done["admin_url"] != "http://"+listen+"/admin#setup=ABCD-EFGH" {
		t.Fatalf("done line %q", lines[len(lines)-1])
	}
}

func TestSetupAdminURL(t *testing.T) {
	lan := ip4(192, 168, 1, 20)
	e := &setupEnv{lanIPv4: func() string { return lan }}
	for _, c := range []struct {
		listen, public, want string
		local                bool
	}{
		{":18660", "", "http://" + lan + ":18660/admin", false},
		{"0.0.0.0:18660", "", "http://" + lan + ":18660/admin", false},
		{"[::]:18660", "", "http://" + lan + ":18660/admin", false},
		{"127.0.0.1:18660", "", "http://127.0.0.1:18660/admin", true},
		{"localhost:18660", "", "http://127.0.0.1:18660/admin", true},
		{"[::1]:18660", "", "http://[::1]:18660/admin", true},
		{ip4(10, 0, 0, 5) + ":18660", "", "http://" + ip4(10, 0, 0, 5) + ":18660/admin", false},
	} {
		if got, local := e.adminURL(&Config{Listen: c.listen, PublicURL: c.public}); got != c.want || local != c.local {
			t.Errorf("adminURL(%q) = %q, %v; want %q, %v", c.listen, got, local, c.want, c.local)
		}
	}
	// No LAN address found: public_url's host, else a placeholder.
	e.lanIPv4 = func() string { return "" }
	if got, _ := e.adminURL(&Config{Listen: ":18660", PublicURL: "http://hub.example.com:18660"}); got != "http://hub.example.com:18660/admin" {
		t.Errorf("from public_url: %q", got)
	}
}

// Rerunning with --listen / --tls (#4484): only a value that differs from hub.json's is mentioned, with both sides.
func TestSetupKeptListenNote(t *testing.T) {
	lan := ip4(192, 168, 1, 20)
	file := func(s string) *jsonObject {
		o, err := parseJSONObject([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	fresh := file(`{"token":"x","listen":":18660","public":{"tls_listen":"` + lan + `:18662"}}`)
	for _, c := range []struct {
		name string
		o    setupOptions
		raw  *jsonObject
		want []string // "" when there is no note
	}{
		{"flags not given", setupOptions{listen: ":1", tls: ":2"}, fresh, nil},
		{"same as a new install wrote", setupOptions{listen: ":18660", tls: ":18662", listenSet: true, tlsSet: true}, fresh, nil},
		{"spelled differently", setupOptions{listen: "0.0.0.0:18660", tls: lan + ":18662", listenSet: true, tlsSet: true}, fresh, nil},
		{"no listen in the file", setupOptions{listen: ":18660", listenSet: true}, file(`{"token":"x"}`), nil},
		{"other listen", setupOptions{listen: ":18760", listenSet: true}, fresh, []string{"listen 仍是 :18660（这次给的 --listen :18760）", "只用于新装"}},
		{"tls off in the file", setupOptions{tls: ":18662", tlsSet: true}, file(`{"token":"x"}`), []string{"public.tls_listen 仍是 空（没开 TLS）（这次给的 --tls :18662）"}},
		{"other tls host", setupOptions{tls: "127.0.0.1:18662", tlsSet: true}, fresh, []string{"public.tls_listen 仍是 " + lan + ":18662"}},
	} {
		e := &setupEnv{o: c.o, lanIPv4: func() string { return lan }}
		got := e.keptListenNote(c.raw)
		if len(c.want) == 0 && got != "" {
			t.Errorf("%s: note %q", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: no %q in %q", c.name, w, got)
			}
		}
	}
}

// db.sh wrote only database_url (no hub has run): the new-install defaults fill in around it.
func TestSetupFillsAFileNoHubHasRead(t *testing.T) {
	home := setupTestHome(t)
	var token string
	port := fakeHub(t, &token, false)
	cmds := &fakeCommands{replies: map[string]fakeReply{"loginctl show-user": {out: "yes\n"}}}
	e, out := testSetupEnv(t, home, setupOptions{listen: ":" + strconv.Itoa(port), tls: ":18662"}, cmds, "systemctl", "loginctl")
	if err := os.WriteFile(filepath.Join(e.root, "hub.json"), []byte(`{"database_url":"postgres://dbsh","admin":{"access":"loopback"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.pingDB = func(_ context.Context, u string) error {
		cfg, _ := readHubJSON(e.root)
		raw, _ := cfg.Get("token")
		_ = json.Unmarshal(raw, &token)
		if u != "postgres://dbsh" {
			return errors.New("wrong database")
		}
		return nil
	}
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var cfg Config
	b, _ := os.ReadFile(filepath.Join(e.root, "hub.json"))
	_ = json.Unmarshal(b, &cfg)
	if cfg.DatabaseURL != "postgres://dbsh" || cfg.Admin.Access != "loopback" || cfg.Admin.FirstSetup != "code" || cfg.Public.TLSListen == "" || cfg.Token == "" {
		t.Fatalf("hub.json: %s", b)
	}
	if cmds.called("docker") {
		t.Fatalf("docker with a database_url: %v", cmds.calls)
	}
}

func TestSetupStopsWithoutADatabase(t *testing.T) {
	home := setupTestHome(t)
	for _, goos := range []string{"linux", "darwin"} {
		e, out := testSetupEnv(t, home, setupOptions{listen: ":18660", tls: ":18662"}, &fakeCommands{})
		e.goos = goos
		e.portFree = func(p int) bool { return p != 5432 || goos == "darwin" }
		_ = os.Remove(filepath.Join(e.root, "hub.json"))
		if err := e.setup(context.Background()); !errors.Is(err, ErrSetupStopped) {
			t.Fatalf("%s: %v", goos, err)
		}
		text := out.String()
		want := "装 Docker Desktop 或 OrbStack 后重跑这条命令"
		if goos == "linux" {
			want = "5432 端口上像是有 Postgres"
		}
		if !strings.Contains(text, want) || strings.Contains(text, "常驻") {
			t.Fatalf("%s:\n%s", goos, text)
		}
	}
}

func TestSetupDockerNotUsable(t *testing.T) {
	home := setupTestHome(t)
	cmds := &fakeCommands{replies: map[string]fakeReply{"docker info": {err: true, out: "permission denied while trying to connect to the Docker daemon socket"}}}
	e, out := testSetupEnv(t, home, setupOptions{listen: ":18660", tls: ":18662"}, cmds, "docker")
	if err := e.setup(context.Background()); !errors.Is(err, ErrSetupStopped) || !strings.Contains(out.String(), "usermod -aG docker someone") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The container is there (an earlier run, db.sh) but hub.json lost its address: the password comes from the container.
func TestSetupReusesTheContainer(t *testing.T) {
	home := setupTestHome(t)
	cmds := &fakeCommands{replies: map[string]fakeReply{
		"docker ps -a":   {out: "persona-postgres\n"},
		"docker inspect": {out: "PATH=/usr/bin\nPOSTGRES_PASSWORD=abc123\n"},
		"docker port":    {out: "127.0.0.1:18661\n"},
	}}
	e, out := testSetupEnv(t, home, setupOptions{listen: ":1", tls: ":18662", noService: true}, cmds, "docker")
	_ = e.setup(context.Background()) // stops at health: nothing runs
	b, _ := os.ReadFile(filepath.Join(e.root, "hub.json"))
	if !strings.Contains(string(b), `"database_url": "postgres://persona:abc123@127.0.0.1:18661/persona?sslmode=disable"`) || cmds.called("docker run") {
		t.Fatalf("%s\n%v\n%s", b, cmds.calls, out)
	}
	if !strings.Contains(out.String(), "personad hub run") {
		t.Fatalf("--no-service should say how to run it:\n%s", out)
	}
}

// Another program on 18660 / 18662: a new install moves along by 100.
func TestSetupMovesOffBusyPorts(t *testing.T) {
	home := setupTestHome(t)
	e, out := testSetupEnv(t, home, setupOptions{listen: ":18660", tls: ":18662"}, &fakeCommands{})
	e.portFree = func(p int) bool { return p != 18660 }
	if _, err := e.config(); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	b, _ := os.ReadFile(filepath.Join(e.root, "hub.json"))
	_ = json.Unmarshal(b, &cfg)
	if cfg.Listen != ":18760" || !strings.HasSuffix(cfg.Public.TLSListen, ":18762") || !strings.HasSuffix(cfg.PublicURL, ":18760") || !strings.Contains(out.String(), "改用 18760 / 18762") {
		t.Fatalf("%s\n%s", b, out)
	}
}

func TestSetupOutsideOpensTLSOnAnExistingFile(t *testing.T) {
	home := setupTestHome(t)
	e, _ := testSetupEnv(t, home, setupOptions{listen: ":18660", tls: ":18662", outside: true}, &fakeCommands{})
	_ = os.WriteFile(filepath.Join(e.root, "hub.json"), []byte(`{"token":"t","public":{"tls_listen":"`+ip4(192, 168, 1, 20)+`:18672","advertise":["x"]}}`), 0o600)
	if _, err := e.config(); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	b, _ := os.ReadFile(filepath.Join(e.root, "hub.json"))
	_ = json.Unmarshal(b, &cfg)
	if cfg.Public.TLSListen != ":18672" || len(cfg.Public.Advertise) != 1 || cfg.Token != "t" {
		t.Fatalf("%s", b)
	}
}

func TestSetupMacLaunchAgent(t *testing.T) {
	home := setupTestHome(t)
	var token string
	port := fakeHub(t, &token, false)
	cmds := &fakeCommands{replies: map[string]fakeReply{"launchctl print": {err: true}}}
	e, out := testSetupEnv(t, home, setupOptions{listen: ":" + strconv.Itoa(port), tls: ":18662"}, cmds)
	e.goos, e.uid = "darwin", 501
	e.run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "launchctl" && len(args) > 0 && args[0] == "bootstrap" { // the hub comes up with the agent
			cfg, _ := readHubJSON(e.root)
			raw, _ := cfg.Get("token")
			_ = json.Unmarshal(raw, &token)
		}
		return cmds.run(ctx, env, name, args...)
	}
	_ = os.WriteFile(filepath.Join(e.root, "hub.json"), []byte(`{"database_url":"postgres://x"}`), 0o600)
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	plist, _ := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "com.persona.hub.plist"))
	bin := realPath(t, filepath.Join(e.root, "bin", "personad"))
	if !strings.Contains(string(plist), "<string>"+bin+"</string><string>hub</string>") || !strings.Contains(string(plist), "<key>PERSONA_HOME</key><string>"+e.root+"</string>") {
		t.Fatalf("plist:\n%s", plist)
	}
	if !cmds.called("launchctl bootstrap gui/501 " + filepath.Join(home, "Library", "LaunchAgents", "com.persona.hub.plist")) {
		t.Fatalf("calls %v", cmds.calls)
	}
	if !strings.Contains(out.String(), "▀") && !strings.Contains(out.String(), "█") {
		t.Fatalf("no QR code:\n%s", out)
	}
}

// The socket unit is hub-systemd.sh's (keep the two in step); FreeBind only with an address-bound listener.
func TestHubUnitsMatchHubSystemd(t *testing.T) {
	want := "[Unit]\nDescription=Persona hub's listening sockets (docs/operations/hub-restart.md)\n\n[Socket]\nListenStream=18660\nListenStream=18662\n" +
		"Backlog=4096\nNoDelay=true\nService=personad.service\n\n[Install]\nWantedBy=sockets.target\n"
	if got := hubSocketUnit([]string{":18660", ":18662"}); got != want {
		t.Fatalf("socket:\n%s", got)
	}
	svc := hubServiceUnit("/home/someone/.persona", false, true, false)
	for _, line := range []string{"Description=Persona hub (personad)", `ExecStart="/home/someone/.persona/bin/personad" hub`, "Wants=personad.socket", `Environment="PERSONA_HOME=/home/someone/.persona"`, "Restart=always"} {
		if !strings.Contains(svc, line+"\n") {
			t.Fatalf("no %q in:\n%s", line, svc)
		}
	}
	// hub-systemd.sh's for a unit that runs persona-hub.
	svc = hubServiceUnit("/home/someone/.persona", true, true, false)
	for _, line := range []string{"Description=Persona hub (persona-hub)", `ExecStart="/home/someone/.persona/bin/persona-hub" run`, "Wants=personad.socket"} {
		if !strings.Contains(svc, line+"\n") {
			t.Fatalf("no %q in:\n%s", line, svc)
		}
	}
}

// admin.first_setup "code" (new installs): a LAN source needs the setup code too; old files keep loopback only.
func TestAdminFirstSetupCodeFromTheLAN(t *testing.T) {
	h, _, mux := adminTestHub(t)
	lan := &adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 20) + ":1"}
	if _, st, _ := lan.do("GET", "/api/v1/admin/state", ""); st["setup_code_required"] != false {
		t.Fatalf("old file: %v", st)
	}
	h.cfg.Admin.FirstSetup = "code"
	if _, st, _ := lan.do("GET", "/api/v1/admin/state", ""); st["setup_code_required"] != true || st["can_setup"] != true {
		t.Fatalf("first_setup code: %v", st)
	}
	if code, out, _ := lan.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`); code != 403 || out["code"] != "setup_code_required" {
		t.Fatalf("no code: %d %v", code, out)
	}
	setup, _ := h.newSetupCode()
	if code, st, _ := lan.do("POST", "/api/v1/admin/setup", `{"password":"a good password","setup_code":"`+setup+`"}`); code != 200 || st["logged_in"] != true {
		t.Fatalf("with the code: %d %v", code, st)
	}
}

func TestFillMissing(t *testing.T) {
	dst, _ := parseJSONObject([]byte(`{"a":1,"admin":{"access":"lan"},"machines":[{"id":"x"}],"n":null}`))
	src, _ := parseJSONObject([]byte(`{"a":2,"admin":{"access":"any","first_setup":"code"},"machines":[],"b":3,"n":{"x":1}}`))
	if err := fillMissing(dst, src); err != nil {
		t.Fatal(err)
	}
	got, _ := dst.Indented()
	var v map[string]any
	_ = json.Unmarshal(got, &v)
	admin := v["admin"].(map[string]any)
	if v["a"] != float64(1) || v["b"] != float64(3) || admin["access"] != "lan" || admin["first_setup"] != "code" || len(v["machines"].([]any)) != 1 || v["n"] != nil {
		t.Fatalf("%s", got)
	}
}

// A TLS address this machine does not have (the LAN address moved): the hub starts without TLS instead of not at all.
func TestTLSAddressGone(t *testing.T) {
	addr := net.JoinHostPort(ip4(192, 0, 2, 77), "0") // TEST-NET-1: never this machine's
	_, err := net.Listen("tcp", addr)
	if err == nil {
		t.Skip("this machine has the address")
	}
	if !tlsAddressGone(addr, err) {
		t.Fatalf("%v not taken for a vanished address", err)
	}
	if _, err := net.Listen("tcp", "bad:addr:x"); tlsAddressGone(":18662", err) {
		t.Fatal("no host: not a vanished address")
	}
}

// Setup finds a launch agent through a user-configured .persona symlink and does
// not install a second agent or restart an unchanged binary twice.
func TestSetupMacFindsAnAgentThroughASymlink(t *testing.T) {
	home := setupTestHome(t)
	token := "t"
	port := fakeHub(t, &token, true)
	cmds := &fakeCommands{replies: map[string]fakeReply{}}
	e, out := testSetupEnv(t, home, setupOptions{listen: ":" + strconv.Itoa(port), tls: ":18662"}, cmds)
	e.goos, e.uid = "darwin", 501
	if err := os.Rename(e.root, filepath.Join(home, "state")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("state", filepath.Join(home, ".persona")); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.root, "hub.json"), []byte(`{"token":"t","listen":":`+strconv.Itoa(port)+`","database_url":"postgres://x"}`), 0o600)
	agents := filepath.Join(home, "Library", "LaunchAgents")
	_ = os.MkdirAll(agents, 0o755)
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>Label</key><string>com.example.myhub</string>
  <key>ProgramArguments</key><array><string>~/.persona/bin/personad</string><string>hub</string></array>
  <key>KeepAlive</key><true/>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(agents, "myhub.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	e.portFree = func(int) bool { return false } // the old hub holds the port
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(agents, "com.persona.hub.plist")); err == nil || cmds.called("launchctl bootstrap") {
		t.Fatalf("a second agent: %v", cmds.calls)
	}
	if !cmds.called("launchctl kickstart -k gui/501/com.example.myhub") {
		t.Fatalf("not restarted for the new binary: %v", cmds.calls)
	}
	cmds.calls = nil
	if err := e.setup(context.Background()); err != nil || cmds.called("launchctl kickstart") {
		t.Fatalf("again: %v %v", err, cmds.calls)
	}
}

// A hub running some other way (a terminal) on a Mac: no agent that could not get the port and restart forever.
func TestSetupMacHubRunningElsewhere(t *testing.T) {
	home := setupTestHome(t)
	token := "t"
	port := fakeHub(t, &token, true)
	cmds := &fakeCommands{replies: map[string]fakeReply{"launchctl print": {err: true}}}
	e, out := testSetupEnv(t, home, setupOptions{listen: ":" + strconv.Itoa(port), tls: ":18662"}, cmds)
	e.goos, e.uid = "darwin", 501
	_ = os.WriteFile(filepath.Join(e.root, "hub.json"), []byte(`{"token":"t","listen":":`+strconv.Itoa(port)+`","database_url":"postgres://x"}`), 0o600)
	if err := e.setup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if cmds.called("launchctl bootstrap") || !strings.Contains(out.String(), "不是 launchd 服务起的") {
		t.Fatalf("calls %v\n%s", cmds.calls, out)
	}
}

func TestLaunchdJob(t *testing.T) {
	job, ok := launchdJob([]byte(`<plist version="1.0"><dict><key>Label</key><string>a.b</string><key>Program</key><string>/x/personad</string><key>RunAtLoad</key><true/></dict></plist>`))
	if !ok || job.label != "a.b" || !slices.Equal(job.args, []string{"/x/personad"}) || job.disabled {
		t.Fatalf("%+v %v", job, ok)
	}
	// Program is what runs; ProgramArguments[0] only names it.
	job, ok = launchdJob([]byte(`<plist version="1.0"><dict><key>Program</key><string>/x/persona-hub</string><key>ProgramArguments</key><array><string>/x/personad</string><string>hub</string></array><key>Disabled</key><true/></dict></plist>`))
	if !ok || !slices.Equal(job.args, []string{"/x/persona-hub", "hub"}) || !job.disabled {
		t.Fatalf("%+v %v", job, ok)
	}
	if _, ok := launchdJob([]byte("bplist00")); ok {
		t.Fatal("binary plist parsed")
	}
}

// A container on the host's network listens on database_url's port inside, not 5432 (db.sh's PGPORT).
func TestStartContainerHostNetwork(t *testing.T) {
	home := setupTestHome(t)
	cmds := &fakeCommands{replies: map[string]fakeReply{"docker inspect -f {{.HostConfig.NetworkMode}}": {out: "host\n"}}}
	e, _ := testSetupEnv(t, home, setupOptions{}, cmds, "docker")
	if err := e.startContainer(context.Background(), "postgres://persona:p@127.0.0.1:5544/persona?sslmode=disable"); err != nil {
		t.Fatal(err)
	}
	if !cmds.called("docker exec persona-postgres pg_isready -h 127.0.0.1 -p 5544 ") {
		t.Fatalf("%v", cmds.calls)
	}
	cmds.replies["docker inspect -f {{.HostConfig.NetworkMode}}"] = fakeReply{out: "bridge\n"}
	cmds.calls = nil
	_ = e.startContainer(context.Background(), "postgres://persona:p@127.0.0.1:5544/persona")
	if !cmds.called("docker exec persona-postgres pg_isready -h 127.0.0.1 -p 5432 ") {
		t.Fatalf("%v", cmds.calls)
	}
}

// personad.socket binds a moved LAN address anyway (FreeBind): the hub says so instead of TLS going quiet.
func TestAddressNotLocal(t *testing.T) {
	lan := ip4(192, 168, 1, 20)
	addrs := func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP(lan), Mask: net.CIDRMask(24, 32)}}, nil
	}
	if _, gone := addressNotLocal(lan+":18662", addrs); gone {
		t.Fatal("own address taken for gone")
	}
	if host, gone := addressNotLocal(ip4(192, 168, 1, 21)+":18662", addrs); !gone || host != ip4(192, 168, 1, 21) {
		t.Fatal("moved address not noticed")
	}
	if _, gone := addressNotLocal(":18662", addrs); gone {
		t.Fatal("every address taken for gone")
	}
}
