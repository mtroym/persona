package herdr

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A named session's ids carry the session (#2196); the default server's stay as herdr gives them, so what was stored
// before reads the same.
func TestQualifyAndSplitID(t *testing.T) {
	for _, c := range []struct{ session, id, want string }{
		{"", "w4:p1", "w4:p1"},
		{"build", "w4:p1", "@build:w4:p1"},
		{"build", "w4:t1", "@build:w4:t1"},
		{"build", "w4", "@build:w4"},
		{"build", "", ""},
	} {
		got := QualifyID(c.session, c.id)
		if got != c.want {
			t.Errorf("QualifyID(%q, %q) = %q, want %q", c.session, c.id, got, c.want)
		}
		if c.id == "" {
			continue
		}
		if s, id := SplitID(got); s != c.session || id != c.id {
			t.Errorf("SplitID(%q) = %q, %q, want %q, %q", got, s, id, c.session, c.id)
		}
	}
	// Anything that is not a qualified id is herdr's own, as it is.
	for _, id := range []string{"w4:p1", "@:w4:p1", "@a b:w4:p1", "@build", "@build:", "@../x:w1:p1", "term_65c3"} {
		if s, bare := SplitID(id); s != "" || bare != id {
			t.Errorf("SplitID(%q) = %q, %q, want it as it is", id, s, bare)
		}
	}
}

func TestValidSessionName(t *testing.T) {
	for _, name := range []string{"build", "review_2", "a.b", "S1"} {
		if !ValidSessionName(name) {
			t.Errorf("%q should be valid", name)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", "a:b", "a b", "@x", strings.Repeat("x", 129)} {
		if ValidSessionName(name) {
			t.Errorf("%q should not be valid", name)
		}
	}
}

// What herdr puts in a pane's environment tells the server: HERDR_SESSION in a named session, else the session
// directory of HERDR_SOCKET_PATH; the default server's panes have neither (or name "default").
func TestEnvSession(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(name string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == name {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	for _, c := range []struct {
		env  func(string) string
		want string
	}{
		{env(), ""},
		{env("HERDR_SOCKET_PATH", "/home/u/.config/herdr/herdr.sock"), ""},
		{env("HERDR_SESSION", "default", "HERDR_SOCKET_PATH", "/home/u/.config/herdr/herdr.sock"), ""},
		{env("HERDR_SESSION", "build", "HERDR_SOCKET_PATH", "/home/u/.config/herdr/sessions/build/herdr.sock"), "build"},
		{env("HERDR_SOCKET_PATH", "/home/u/.config/herdr/sessions/review/herdr.sock"), "review"},
		{env("HERDR_SOCKET_PATH", "/tmp/other.sock"), ""},
	} {
		if got := c.env("HERDR_SESSION") + "|" + c.env("HERDR_SOCKET_PATH"); EnvSession(c.env) != c.want {
			t.Errorf("EnvSession(%s) = %q, want %q", got, EnvSession(c.env), c.want)
		}
	}
}

// A process's pane as a node writes it: its own server's bare, any other session's qualified — the default server's
// too, for a node on a session of its own.
func TestPaneOf(t *testing.T) {
	for _, c := range []struct{ pane, session, own, want string }{
		{"w4:p1", "", "", "w4:p1"},
		{"w4:p1", "build", "", "@build:w4:p1"},
		{"w4:p1", "persona-1a2b3c4d", "persona-1a2b3c4d", "w4:p1"},
		{"w4:p1", "", "persona-1a2b3c4d", "@default:w4:p1"},
		{"", "build", "", ""},
	} {
		if got := PaneOf(c.pane, c.session, c.own); got != c.want {
			t.Errorf("PaneOf(%q, %q, %q) = %q, want %q", c.pane, c.session, c.own, got, c.want)
		}
	}
}

func TestListSessionsParsesHerdr(t *testing.T) {
	out := `{"sessions":[{"default":true,"name":"default","running":true,"session_dir":"/c/herdr","socket_path":"/c/herdr/herdr.sock"},` +
		`{"default":false,"name":"build","running":true,"session_dir":"/c/herdr/sessions/build","socket_path":"/c/herdr/sessions/build/herdr.sock"},` +
		`{"default":false,"name":"old","running":false,"session_dir":"/c/herdr/sessions/old","socket_path":"/c/herdr/sessions/old/herdr.sock"}]}`
	bin, argsFile := cliScript(t, out)
	got, err := (&Client{Bin: bin}).ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []SessionInfo{
		{Name: "default", Default: true, Running: true, Socket: "/c/herdr/herdr.sock"},
		{Name: "build", Running: true, Socket: "/c/herdr/sessions/build/herdr.sock"},
		{Name: "old", Socket: "/c/herdr/sessions/old/herdr.sock"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if args, _ := os.ReadFile(argsFile); strings.TrimSpace(string(args)) != "session list --json" {
		t.Errorf("ran herdr %q", args)
	}
	if _, err := (&Client{Bin: filepath.Join(t.TempDir(), "none")}).ListSessions(context.Background()); err == nil {
		t.Error("no herdr: want an error")
	}
}

// A client of a named session sends its CLI calls to that session's server, whatever session or socket the node's own
// environment names (a node started in another session's pane inherits them).
func TestSessionClientEnvNamesItsServer(t *testing.T) {
	t.Setenv("HERDR_SESSION", "elsewhere")
	t.Setenv("HERDR_SOCKET_PATH", "/x/elsewhere.sock")
	c := (&Client{Bin: "herdr"}).ForSession("build", "/c/herdr/sessions/build/herdr.sock")
	env := c.env()
	var herdrVars []string // the two that pick a server (others, a pane's HERDR_PANE_ID, are not the client's business)
	for _, kv := range env {
		if strings.HasPrefix(kv, "HERDR_SESSION=") || strings.HasPrefix(kv, "HERDR_SOCKET_PATH=") {
			herdrVars = append(herdrVars, kv)
		}
	}
	want := []string{"HERDR_SESSION=build", "HERDR_SOCKET_PATH=/c/herdr/sessions/build/herdr.sock"}
	if !slices.Equal(herdrVars, want) {
		t.Errorf("HERDR_* = %v, want %v", herdrVars, want)
	}
	if (&Client{Bin: "herdr", Socket: "/s"}).env() != nil {
		t.Error("the default server's client inherits the environment as before")
	}
}

func TestOnDefaultServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the session layout is not checked on Windows")
	}
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	def := filepath.Join(cfg, "herdr", "herdr.sock")
	named := filepath.Join(cfg, "herdr", "sessions", "build", "herdr.sock")
	for _, c := range []struct {
		c    *Client
		want bool
		own  string
	}{
		{&Client{Socket: def}, true, ""},
		{&Client{Socket: named}, false, "build"},
		{&Client{Socket: named, Session: "build"}, false, "build"},
		{&Client{Socket: "/tmp/hf1/h.sock"}, false, ""},
		{&Client{}, false, ""},
	} {
		if got := c.c.OnDefaultServer(); got != c.want {
			t.Errorf("%+v: OnDefaultServer %v, want %v", c.c, got, c.want)
		}
		if got := c.c.OwnName(); got != c.own {
			t.Errorf("%+v: OwnName %q, want %q", c.c, got, c.own)
		}
	}
	if s := (&Client{Bin: "herdr"}).ForSession("build", ""); s.Socket != named || s.Session != "build" {
		t.Errorf("ForSession without a socket: %+v, want %s", s, named)
	}
}
