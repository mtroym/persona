package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// emptyHome points the home and state directories at a new, empty directory.
func emptyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	return home
}

// The commands that only talk to the hub running here, as hubcli runs them.
var runningHubCommands = []struct {
	name string
	run  func() error
}{
	{"pair", func() error { return Pair(context.Background(), false) }},
	{"pair --legacy", func() error { return Pair(context.Background(), true) }},
	{"pair --machine --add", func() error { return PairMachine(context.Background(), "study", true, "") }},
	{"pair --peer", func() error { return PairPeer(context.Background()) }},
	{"peer list", func() error {
		return PeerCommand(context.Background(), []string{"list"}, strings.NewReader(""), io.Discard)
	}},
	{"manager-code", func() error { return ManagerCode(context.Background(), io.Discard) }},
	{"assistant-token", func() error { return AssistantToken(context.Background(), io.Discard) }},
}

// None of them makes hub.json (or the state directory): one they made would make a later `persona-hub setup` take the
// machine for a hub that has already run, and `personad extension` stop saying there is no hub here.
func TestRunningHubCommandsNeedHubJSON(t *testing.T) {
	for _, c := range runningHubCommands {
		t.Run(c.name, func(t *testing.T) {
			home := emptyHome(t)
			err := c.run()
			if !errors.Is(err, errNoHubJSON) || !strings.Contains(err.Error(), "setup") {
				t.Fatalf("want the no-hub.json error, got %v", err)
			}
			if _, err := os.Lstat(filepath.Join(home, ".persona")); !os.IsNotExist(err) {
				t.Fatalf("the state directory was made: %v", err)
			}
		})
	}
}

// A hub.json no hub has started with yet (db.sh's) stays exactly as it is: they say so instead of adding a token.
func TestRunningHubCommandsLeaveATokenlessHubJSON(t *testing.T) {
	for _, c := range runningHubCommands {
		t.Run(c.name, func(t *testing.T) {
			home := emptyHome(t)
			path := writeHubJSON(t, home, `{"database_url":"postgres://x"}`, 0o644)
			err := c.run()
			if !errors.Is(err, errNoHubToken) || !strings.Contains(err.Error(), "setup") {
				t.Fatalf("want the no-token error, got %v", err)
			}
			b, _ := os.ReadFile(path)
			st, _ := os.Stat(path)
			if string(b) != `{"database_url":"postgres://x"}` || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o644) {
				t.Fatalf("hub.json changed: %s %v", b, st.Mode())
			}
		})
	}
}

// The hub's first start adds the token to a hand-written hub.json the way every other edit of it is made: the rest of
// the file as it was (key order, keys this build does not know), a new file renamed over the old one, mode 0600 (it now
// holds the master token next to the database password).
func TestLoadConfigAddsTheTokenAsAnEdit(t *testing.T) {
	home := emptyHome(t)
	path := writeHubJSON(t, home, `{"database_url":"postgres://x","from_a_newer_build":{"b":1,"a":2}}`, 0o644)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("hub.json mode: %v %v", st.Mode(), err)
		}
	}
	b, _ := os.ReadFile(path)
	o, err := parseJSONObject(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.Keys(), ",") != "database_url,from_a_newer_build,token" {
		t.Fatalf("keys %v", o.Keys())
	}
	var token string
	if raw, _ := o.Get("token"); json.Unmarshal(raw, &token) != nil || token != cfg.Token || cfg.DatabaseURL != "postgres://x" {
		t.Fatalf("hub.json %s, config token %q database %q", b, cfg.Token, cfg.DatabaseURL)
	}
	if raw, _ := o.Get("from_a_newer_build"); !bytes.Equal(compactedJSON(t, raw), []byte(`{"b":1,"a":2}`)) {
		t.Fatalf("an unknown key changed: %s", raw)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if e.Name() != "hub.json" && e.Name() != ".lock" { // .lock: managerconf.LockDir on Windows
			t.Fatalf("left behind: %s", e.Name())
		}
	}
	// The running hub's commands read the same token.
	if again, err := runningHubConfig(); err != nil || again.Token != cfg.Token {
		t.Fatalf("runningHubConfig: %v %v", again, err)
	}
}

// A hub.json that is there but empty gets only the token, as before: written out, the defaults would turn "no machines
// key" (the hub's default computer at every start) into a list of its own.
func TestLoadConfigAddsOnlyTheTokenToAnEmptyObject(t *testing.T) {
	home := emptyHome(t)
	path := writeHubJSON(t, home, `{}`, 0o600)
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if o, err := parseJSONObject(b); err != nil || strings.Join(o.Keys(), ",") != "token" {
		t.Fatalf("hub.json %s (%v)", b, err)
	}
}

// A token another process wrote first is kept, and the file is not written again.
func TestInitConfigFileKeepsAToken(t *testing.T) {
	home := emptyHome(t)
	path := writeHubJSON(t, home, `{"token":"t"}`, 0o644)
	if err := initConfigFile(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	st, _ := os.Stat(path)
	if string(b) != `{"token":"t"}` || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o644) {
		t.Fatalf("hub.json rewritten: %s %v", b, st.Mode())
	}
}

func writeHubJSON(t *testing.T, home, body string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hub.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // past the umask
		t.Fatal(err)
	}
	return path
}

func compactedJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
