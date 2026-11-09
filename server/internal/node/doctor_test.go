package node

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// doctorEnv is a node directory, an executable standing in for personad, and the sockets and Claude settings the
// doctor looks at, all under one temp directory.
type doctorEnv struct {
	opts DoctorOptions
	n    *Node
}

func newDoctorEnv(t *testing.T) *doctorEnv {
	t.Helper()
	root, err := os.MkdirTemp("", "doc") // short: unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	bin := filepath.Join(root, "personad")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := mkdir(t, filepath.Join(root, "node"))
	return &doctorEnv{
		n: &Node{home: root, dir: dir, bin: bin},
		opts: DoctorOptions{
			Dir: dir, Bin: bin,
			NodeSock: filepath.Join(root, "n.sock"), RuntimeSock: filepath.Join(root, "r.sock"), HerdrSock: filepath.Join(root, "h.sock"),
			ClaudeConfigDir: mkdir(t, filepath.Join(root, "claude")),
			ClaudeVersion:   func(context.Context) string { return "2.1.5" },
		},
	}
}

// listen serves the node's socket protocol on path (the real serveSock, so the ping is the node's own).
func (e *doctorEnv) listenNode(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go e.n.serveSock(context.Background(), c)
		}
	}()
}

// listenPlain accepts connections and drops them: runtime.sock and herdr's socket only need to be dialed.
func listenPlain(t *testing.T, path string, dial func(string) (net.Listener, error)) {
	t.Helper()
	ln, err := dial(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
}

func (e *doctorEnv) healthy(t *testing.T) {
	t.Helper()
	if err := e.n.writeClaudeConfig(); err != nil {
		t.Fatal(err)
	}
	e.listenNode(t, e.opts.NodeSock)
	listenPlain(t, e.opts.RuntimeSock, func(p string) (net.Listener, error) { return net.Listen("unix", p) })
	listenPlain(t, e.opts.HerdrSock, herdr.Listen)
}

func doctorCheckNamed(t *testing.T, rep DoctorReport, name string) DoctorCheck {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", name, rep.Checks)
	return DoctorCheck{}
}

func wantStatus(t *testing.T, rep DoctorReport, name, status, state string) DoctorCheck {
	t.Helper()
	c := doctorCheckNamed(t, rep, name)
	if c.Status != status || c.State != state {
		t.Errorf("%s: status %s state %q (%s), want %s %q", name, c.Status, c.State, c.Detail, status, state)
	}
	return c
}

func TestDoctorHealthyNode(t *testing.T) {
	e := newDoctorEnv(t)
	e.healthy(t)
	rep := RunDoctor(context.Background(), e.opts)
	if !rep.OK {
		t.Errorf("not ok: %+v", rep.Checks)
	}
	for _, name := range []string{"node.sock", "runtime.sock", "command-paths", "claude-version", "herdr", "global-permission-hooks"} {
		wantStatus(t, rep, name, DoctorOK, "")
	}
	// What the node wrote at startup is what the doctor expects: the two share the generator.
	for _, name := range append(slices.Clone(generatedGroups), agentPluginGroup) {
		wantStatus(t, rep, name, DoctorOK, "current")
	}
}

func TestDoctorStaleAndMissingFiles(t *testing.T) {
	e := newDoctorEnv(t)
	e.healthy(t)
	// Another binary wrote the files (a redeploy to another path): every file naming the program differs.
	other := filepath.Join(filepath.Dir(e.opts.Bin), "old-personad")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeGeneratedConfig(e.opts.Dir, other); err != nil {
		t.Fatal(err)
	}
	rep := RunDoctor(context.Background(), e.opts)
	if !rep.OK {
		t.Errorf("stale files alone must not fail: %+v", rep.Checks)
	}
	for _, name := range generatedGroups {
		wantStatus(t, rep, name, DoctorWarn, "stale")
	}
	wantStatus(t, rep, agentPluginGroup, DoctorOK, "current") // does not name the program

	// A file gone, part of a plugin gone, and the skills plugin gone.
	os.Remove(filepath.Join(e.opts.Dir, "claude-settings.json"))
	os.Remove(filepath.Join(e.opts.Dir, "assistant-hooks", "hooks", "hooks.json"))
	os.RemoveAll(filepath.Join(e.opts.Dir, agentPluginGroup))
	rep = RunDoctor(context.Background(), e.opts)
	if rep.OK {
		t.Errorf("a missing settings file must fail: %+v", rep.Checks)
	}
	wantStatus(t, rep, "claude-settings.json", DoctorFail, "missing")
	c := wantStatus(t, rep, "assistant-hooks", DoctorWarn, "stale")
	if !slices.Contains(c.Items, "missing assistant-hooks/hooks/hooks.json") {
		t.Errorf("items %v", c.Items)
	}
	wantStatus(t, rep, agentPluginGroup, DoctorWarn, "missing") // only the office skill is lost: not a failure
}

func TestDoctorFixRewritesOnlyGeneratedFiles(t *testing.T) {
	e := newDoctorEnv(t)
	e.healthy(t)
	os.RemoveAll(filepath.Join(e.opts.Dir, "claude-hooks"))
	os.Remove(filepath.Join(e.opts.Dir, "mcp.json"))
	// The user's own Claude Code settings, and a file of the user's beside Persona's.
	global := filepath.Join(e.opts.ClaudeConfigDir, "settings.json")
	if err := os.WriteFile(global, []byte(`{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"other-tool gate"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(e.opts.Dir, "notes.txt")
	if err := os.WriteFile(mine, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(mustRead(t, global))

	// Without --fix nothing is written.
	rep := RunDoctor(context.Background(), e.opts)
	if len(rep.Fixed) != 0 || rep.OK {
		t.Fatalf("read-only run: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(e.opts.Dir, "mcp.json")); err == nil {
		t.Fatal("the doctor without --fix wrote mcp.json")
	}

	e.opts.Fix = true
	rep = RunDoctor(context.Background(), e.opts)
	if !rep.OK {
		t.Errorf("after fix: %+v", rep.Checks)
	}
	if !slices.Equal(rep.Fixed, []string{"claude-hooks", "mcp.json"}) {
		t.Errorf("fixed %v", rep.Fixed)
	}
	for _, name := range generatedGroups {
		wantStatus(t, rep, name, DoctorOK, "current")
	}
	if sha256.Sum256(mustRead(t, global)) != before {
		t.Error("the user's global settings changed")
	}
	if string(mustRead(t, mine)) != "keep" {
		t.Error("a file the doctor does not own changed")
	}
	// Nothing to fix: nothing reported.
	if rep = RunDoctor(context.Background(), e.opts); len(rep.Fixed) != 0 {
		t.Errorf("fixed again: %v", rep.Fixed)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDoctorSockets(t *testing.T) {
	e := newDoctorEnv(t)
	e.healthy(t)
	// node.sock deleted under a running node (the redeploy that started this): fail.
	os.Remove(e.opts.NodeSock)
	rep := RunDoctor(context.Background(), e.opts)
	if rep.OK {
		t.Error("a missing node.sock must fail")
	}
	wantStatus(t, rep, "node.sock", DoctorFail, "")

	// A socket file nobody listens on: fail, not "exists".
	ln, err := net.Listen("unix", e.opts.NodeSock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	os.Remove(e.opts.RuntimeSock)
	rep = RunDoctor(context.Background(), e.opts)
	wantStatus(t, rep, "node.sock", DoctorFail, "")
	if !strings.Contains(doctorCheckNamed(t, rep, "node.sock").Detail, "连不上") {
		t.Errorf("detail %q", doctorCheckNamed(t, rep, "node.sock").Detail)
	}
	wantStatus(t, rep, "runtime.sock", DoctorWarn, "") // optional: no runtime may be wanted
}

// ping changes nothing and says which program and build answer (persona-link node switch waits for persona-node).
func TestNodeAnswersPing(t *testing.T) {
	e := newDoctorEnv(t)
	e.n.Program, e.n.Version = "persona-node", "v9"
	e.listenNode(t, e.opts.NodeSock)
	line, err := nodeclient.DialAt(context.Background(), e.opts.NodeSock, sockRequest{Kind: "ping"})
	if err != nil || strings.TrimSpace(string(line)) != `{"ok":true,"program":"persona-node","version":"v9"}` {
		t.Errorf("ping: %q, %v", line, err)
	}
}

// Standby (no manager connected) answers ping as the resident node it stands in for.
func TestStandbyAnswersPingAsTheResident(t *testing.T) {
	SetResident("persona-node", "v9")
	t.Cleanup(func() { SetResident("", "") })
	b, _ := json.Marshal((&standby{}).answer(sockRequest{Kind: "ping"}))
	if string(b) != `{"ok":true,"program":"persona-node","standby":true,"version":"v9"}` {
		t.Errorf("standby ping: %s", b)
	}
}

func TestDoctorCommandPaths(t *testing.T) {
	e := newDoctorEnv(t)
	e.healthy(t)
	if err := os.Remove(e.opts.Bin); err != nil {
		t.Fatal(err)
	}
	rep := RunDoctor(context.Background(), e.opts)
	c := wantStatus(t, rep, "command-paths", DoctorFail, "")
	if len(c.Items) != 1 || !strings.Contains(c.Items[0], "not found") {
		t.Errorf("items %v", c.Items)
	}
	// Present but not executable.
	if err := os.WriteFile(e.opts.Bin, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c = doctorCheckNamed(t, RunDoctor(context.Background(), e.opts), "command-paths")
	if c.Status != DoctorFail || !strings.Contains(strings.Join(c.Items, ";"), "not executable") {
		t.Errorf("not executable: %+v", c)
	}
	// Nothing generated yet: nothing to check, said so.
	empty := newDoctorEnv(t)
	wantStatus(t, RunDoctor(context.Background(), empty.opts), "command-paths", DoctorWarn, "")
}

func TestShellFirstWord(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/personad hook":       "/usr/bin/personad",
		"'/opt/my dir/personad' hook":  "/opt/my dir/personad",
		`'/opt/it'\''s/personad' hook`: "/opt/it's/personad",
		"  /bin/x":                     "/bin/x",
		"":                             "",
		"'/unterminated hook":          "",
		// The node's directory in front (rootAssign) is not the program.
		"PERSONA_HOME=/h/.persona /usr/bin/personad hook":          "/usr/bin/personad",
		"PERSONA_HOME='/h/my dir' A=1 '/opt/my dir/personad' hook": "/opt/my dir/personad",
		"PERSONA_HOME=/h/.persona":                                 "",
		"'/opt/a=b/personad' hook":                                 "/opt/a=b/personad",
		"/opt/a=b/personad hook":                                   "/opt/a=b/personad",
		`PERSONA_HOME='/it'\''s' '/opt/it'\''s/personad' hook`:     "/opt/it's/personad",
	} {
		if got := shellFirstWord(in); got != want {
			t.Errorf("shellFirstWord(%q) = %q, want %q", in, got, want)
		}
	}
	// Whatever shellQuote writes reads back the same, with or without a directory in front.
	for _, p := range []string{"/a/b", "/a b/c", "/it's/x", "/q\"x/y"} {
		if got := shellFirstWord(shellQuote(p) + " hook"); got != p {
			t.Errorf("round trip of %q: %q", p, got)
		}
		if got := shellFirstWord(rootAssign(p+"/.persona", shellQuote) + shellQuote(p) + " hook"); got != p {
			t.Errorf("round trip of %q after its directory: %q", p, got)
		}
	}
}

// A shell-form hook's program is its command line's first word; an exec-form one's ("args" given) is its command as it
// is, spaces included.
func TestConfigCommandsBothForms(t *testing.T) {
	b := []byte(`{"hooks": {
		"Stop": [{"hooks": [{"type": "command", "command": "'/opt/my dir/personad' hook"}]}],
		"MessageDisplay": [{"hooks": [{"type": "command", "command": "/opt/my dir/persona-node", "args": ["hook"]}]}],
		"Notification": [{"hooks": [{"type": "command", "command": "/opt/no args", "args": []}]}]
	}}`)
	got := configCommands(b)
	slices.Sort(got)
	if want := []string{"/opt/my dir/persona-node", "/opt/my dir/personad", "/opt/no args"}; !slices.Equal(got, want) {
		t.Fatalf("configCommands = %q, want %q", got, want)
	}
}

func TestDoctorClaudeVersion(t *testing.T) {
	e := newDoctorEnv(t)
	for v, status := range map[string]string{"": DoctorWarn, "2.0.9": DoctorWarn, "2.1.0": DoctorOK, "3.0.1-beta": DoctorOK} {
		e.opts.ClaudeVersion = func(context.Context) string { return v }
		if c := doctorCheckNamed(t, RunDoctor(context.Background(), e.opts), "claude-version"); c.Status != status {
			t.Errorf("version %q: %s (%s), want %s", v, c.Status, c.Detail, status)
		}
	}
}

func TestDoctorHerdr(t *testing.T) {
	e := newDoctorEnv(t)
	wantStatus(t, RunDoctor(context.Background(), e.opts), "herdr", DoctorWarn, "") // not running: only a warning
	listenPlain(t, e.opts.HerdrSock, herdr.Listen)
	wantStatus(t, RunDoctor(context.Background(), e.opts), "herdr", DoctorOK, "")
}

func TestDoctorGlobalPermissionHooks(t *testing.T) {
	e := newDoctorEnv(t)
	global := filepath.Join(e.opts.ClaudeConfigDir, "settings.json")
	wantStatus(t, RunDoctor(context.Background(), e.opts), "global-permission-hooks", DoctorOK, "")

	long := "other-tool " + strings.Repeat("x", 200)
	body := `{"hooks":{"PermissionRequest":[
		{"matcher":"*","hooks":[{"type":"command","command":"tool-a gate"},{"type":"command","command":"` + long + `"}]},
		{"hooks":[{"type":"command","command":"tool-b log","async":true}]}],
		"Stop":[{"hooks":[{"type":"command","command":"tool-c"}]}]}}`
	if err := os.WriteFile(global, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := RunDoctor(context.Background(), e.opts)
	c := wantStatus(t, rep, "global-permission-hooks", DoctorWarn, "")
	if len(c.Items) != 2 || c.Items[0] != "tool-a gate" || len([]rune(c.Items[1])) > 82 || !strings.HasSuffix(c.Items[1], "…") {
		t.Errorf("only the 2 synchronous PermissionRequest hooks, cut short: %q", c.Items)
	}
	if !strings.Contains(c.Detail, "2 个") {
		t.Errorf("detail %q", c.Detail)
	}
	if string(mustRead(t, global)) != body {
		t.Error("the doctor changed the user's settings")
	}
	// A warning never fails the run.
	e.healthy(t)
	if rep = RunDoctor(context.Background(), e.opts); !rep.OK {
		t.Errorf("not ok: %+v", rep.Checks)
	}
	if err := os.WriteFile(global, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, RunDoctor(context.Background(), e.opts), "global-permission-hooks", DoctorWarn, "")
}

// The JSON is what scripts read: the same checks in the same order, every field there, no null lists.
func TestDoctorJSONIsStable(t *testing.T) {
	e := newDoctorEnv(t)
	b, err := json.Marshal(RunDoctor(context.Background(), e.opts))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("null in %s", b)
	}
	var rep struct {
		OK     bool `json:"ok"`
		Checks []map[string]any
		Fixed  []string
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range rep.Checks {
		for _, k := range []string{"name", "status", "state", "detail", "hint", "items"} {
			if _, ok := c[k]; !ok {
				t.Errorf("check %v has no %q", c["name"], k)
			}
		}
		names = append(names, c["name"].(string))
	}
	want := []string{"node.sock", "runtime.sock", "claude-settings.json", "claude-hooks", "assistant-settings.json",
		"assistant-hooks", "mcp.json", "agent-plugin", "command-paths", "claude-version", "herdr", "global-permission-hooks", "agent-sandbox"}
	if !slices.Equal(names, want) {
		t.Errorf("checks %v, want %v", names, want)
	}
}

func TestDoctorCommandArgs(t *testing.T) {
	var out strings.Builder
	if _, err := DoctorCommand(context.Background(), []string{"extra"}, &out); err == nil {
		t.Error("a positional argument is refused")
	}
	if _, err := DoctorCommand(context.Background(), []string{"--nope"}, &out); err == nil {
		t.Error("an unknown flag is refused")
	}
}

func TestPrintDoctorOneLinePerCheck(t *testing.T) {
	e := newDoctorEnv(t)
	rep := RunDoctor(context.Background(), e.opts)
	var out strings.Builder
	PrintDoctor(&out, rep)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(rep.Checks)+2 { // header, one per check, summary
		t.Errorf("%d lines for %d checks:\n%s", len(lines), len(rep.Checks), out.String())
	}
	for _, l := range lines[1 : len(lines)-1] {
		label, _, ok := strings.Cut(l, "｜")
		if !ok || !slices.Contains([]string{"正常", "注意", "有问题"}, label) {
			t.Errorf("line %q", l)
		}
	}
}

// The doctor says whether the sandbox is on, whether this machine can run it, and why a start was not wrapped.
func TestCheckAgentSandbox(t *testing.T) {
	cases := []struct {
		name   string
		sup    sandbox.Support
		on     bool
		record *sandbox.AgentStatus
		status string
		words  []string
	}{
		{"off", macSandbox, false, nil, DoctorOK, []string{"未开启", "本机可以开启"}},
		{"off on linux", sandbox.Support{OS: "linux"}, false, nil, DoctorOK, []string{"未开启", "不支持"}},
		{"on", macSandbox, true, &sandbox.AgentStatus{State: sandbox.AgentActive}, DoctorOK, []string{"已开启", "已在沙箱里", "docker", "sudo", "herdr", "personad launch"}},
		{"on on linux", sandbox.Support{OS: "linux"}, true, nil, DoctorWarn, []string{"不支持"}},
		{"on without sandbox-exec", sandbox.Support{OS: "darwin", Why: "机器上没有 sandbox-exec"}, true, nil, DoctorWarn, []string{"工具不可用", "sandbox-exec"}},
		{"a start skipped", macSandbox, true, &sandbox.AgentStatus{State: sandbox.AgentSkipped, Why: "Claude 开了自带 sandbox"}, DoctorWarn, []string{"跳过", "Claude 开了自带 sandbox"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agentSandboxFakes(t, c.sup)
			n := agentNode(t, c.on)
			if c.record != nil {
				recordAgentSandbox(n.dir, "launch claude", *c.record)
			}
			got := checkAgentSandbox(n.dir)
			if got.Name != "agent-sandbox" || got.Status != c.status {
				t.Errorf("%+v", got)
			}
			for _, w := range c.words {
				if !strings.Contains(got.Detail, w) {
					t.Errorf("%q not in %q", w, got.Detail)
				}
			}
		})
	}
}
