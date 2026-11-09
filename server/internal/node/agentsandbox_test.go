package node

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/proto"
)

var macSandbox = sandbox.Support{OS: "darwin", SandboxExec: true}

// agentSandboxFakes makes the agent sandbox's look at this machine predictable: the probe finds sup, sandbox-exec
// takes every profile, there are no administrator's settings for Claude or Codex, and the environment says nothing
// about the switch, the data directory or Claude's config directory.
func agentSandboxFakes(t *testing.T, sup sandbox.Support) {
	t.Helper()
	probe, check := agentSandboxProbe, agentSandboxCheck
	claude, claudeDirs, codex := claudeManagedSettings, claudeManagedSettingsDirs, codexManagedConfig
	t.Cleanup(func() {
		agentSandboxProbe, agentSandboxCheck = probe, check
		claudeManagedSettings, claudeManagedSettingsDirs, codexManagedConfig = claude, claudeDirs, codex
	})
	agentSandboxProbe = func() sandbox.Support { return sup }
	agentSandboxCheck = func(string) error { return nil }
	none := filepath.Join(t.TempDir(), "none")
	claudeManagedSettings, claudeManagedSettingsDirs, codexManagedConfig = []string{none}, []string{none}, []string{none}
	for _, k := range []string{"PERSONA_AGENT_SANDBOX", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "PERSONA_HOME"} {
		t.Setenv(k, "")
	}
}

func TestAgentDataDirsCanonical(t *testing.T) {
	home := t.TempDir()
	canonical := filepath.Join(home, ".persona")
	t.Setenv("PERSONA_HOME", "")
	if got := agentDataDirs(home, canonical); !slices.Equal(got, []string{canonical}) {
		t.Fatalf("canonical root must occur once: %q", got)
	}
	custom := filepath.Join(home, "custom-state")
	t.Setenv("PERSONA_HOME", custom)
	if got := agentDataDirs(home, custom); !slices.Equal(got, []string{custom, canonical}) {
		t.Fatalf("configured root and default: %q", got)
	}
	nodeRoot := filepath.Join(home, "node-state")
	if got := agentDataDirs(home, nodeRoot); !slices.Equal(got, []string{nodeRoot, custom, canonical}) {
		t.Fatalf("distinct configured roots: %q", got)
	}
}

// agentNode is a test node with its data directory (~/.persona), the switch file there when on.
func agentNode(t *testing.T, on bool) *Node {
	t.Helper()
	n, home := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.dir = filepath.Join(home, ".persona")
	for _, d := range []string{"assistant", "launcher-settings"} {
		if err := os.MkdirAll(filepath.Join(n.dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if on {
		writeFile(t, filepath.Join(n.dir, sandbox.AgentOnFile), "")
	}
	return n
}

func inSandbox(args []string) bool {
	return len(args) > 3 && args[0] == "/usr/bin/sandbox-exec" && args[1] == "-p"
}

func realOf(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The assistant's claude on the CLI runs as before with the switch off (the default); on, sandbox-exec runs the same
// command, its launcher settings readable, and the environment can turn it off again.
func TestAssistantCLIInAgentSandbox(t *testing.T) {
	agentSandboxFakes(t, macSandbox)
	env := []string{"PATH=/usr/bin:/bin"}
	plain := func(args []string) []string {
		return append([]string{"/bin/zsh", "-ic", `claude "$@"`, "persona"}, args...)
	}

	n := agentNode(t, false)
	settings := filepath.Join(n.dir, "launcher-settings", "claude-claude-settings.json")
	writeFile(t, settings, "{}")
	args := []string{"-p", "--settings", settings}
	cmd := n.assistantCLICommand("/bin/zsh", "claude", filepath.Join(n.dir, "assistant"), args, env)
	if !slices.Equal(cmd.Args, plain(args)) || !slices.Equal(cmd.Env, env) {
		t.Fatalf("off: %q %q", cmd.Args, cmd.Env)
	}
	if _, err := os.Stat(filepath.Join(n.dir, agentSandboxStatusFile)); err == nil {
		t.Error("off: a status was recorded")
	}

	n = agentNode(t, true)
	settings = filepath.Join(n.dir, "launcher-settings", "claude-claude-settings.json")
	writeFile(t, settings, "{}")
	args = []string{"-p", "--settings", settings}
	cmd = n.assistantCLICommand("/bin/zsh", "claude", filepath.Join(n.dir, "assistant"), args, env)
	if !inSandbox(cmd.Args) || !slices.Equal(cmd.Args[3:], plain(args)) {
		t.Fatalf("on: %q", cmd.Args)
	}
	if !slices.Contains(cmd.Env, sandbox.AgentEnv+"=1") || !slices.Contains(cmd.Env, env[0]) {
		t.Errorf("on: env %q", cmd.Env)
	}
	if want := `(allow file-read* (literal "` + realOf(t, settings) + `"))`; !strings.Contains(cmd.Args[2], want) {
		t.Errorf("its launcher settings are not readable:\n%s", cmd.Args[2])
	}
	if st := readAgentSandboxStatus(n.dir)["assistant"]; st.State != sandbox.AgentActive || st.At.IsZero() {
		t.Errorf("status %+v", st)
	}

	// A managed launcher's shell runs `personad launch` outside the sandbox: it fetches the launcher's settings, then
	// wraps the agent it execs itself (TestLaunchExec).
	n.setManaged([]proto.ManagedLauncher{{Name: "work"}})
	if cmd := n.assistantCLICommand("/bin/zsh", "work", filepath.Join(n.dir, "assistant"), args, env); inSandbox(cmd.Args) || cmd.Args[0] != "/bin/zsh" {
		t.Errorf("managed launcher: the shell was wrapped: %q", cmd.Args)
	}

	t.Setenv("PERSONA_AGENT_SANDBOX", "off")
	if cmd := n.assistantCLICommand("/bin/zsh", "claude", filepath.Join(n.dir, "assistant"), args, env); inSandbox(cmd.Args) {
		t.Errorf("PERSONA_AGENT_SANDBOX=off over the file: %q", cmd.Args)
	}
	t.Setenv("PERSONA_AGENT_SANDBOX", "on")
	n = agentNode(t, false)
	if cmd := n.assistantCLICommand("/bin/zsh", "claude", filepath.Join(n.dir, "assistant"), nil, env); !inSandbox(cmd.Args) {
		t.Errorf("PERSONA_AGENT_SANDBOX=on without the file: %q", cmd.Args)
	}
}

// On but not wrapped: the agent starts as before, and the status says why.
func TestAgentSandboxFallsBack(t *testing.T) {
	enabled := `{"sandbox": {"enabled": true}}`
	cases := []struct {
		name  string
		sup   sandbox.Support
		prep  func(t *testing.T, n *Node, cwd string) []string // more claude args
		state string
	}{
		{"no sandbox-exec", sandbox.Support{OS: "darwin", Why: "机器上没有 sandbox-exec"}, nil, sandbox.AgentUnavailable},
		{"linux", sandbox.Support{OS: "linux", Bwrap: "/usr/bin/bwrap"}, nil, sandbox.AgentUnsupported},
		{"claude's own sandbox in the user's settings", macSandbox, func(t *testing.T, n *Node, cwd string) []string {
			writeFile(t, filepath.Join(n.home, ".claude", "settings.json"), enabled)
			return nil
		}, sandbox.AgentSkipped},
		{"in the project's local settings", macSandbox, func(t *testing.T, n *Node, cwd string) []string {
			writeFile(t, filepath.Join(cwd, ".claude", "settings.local.json"), enabled)
			return nil
		}, sandbox.AgentSkipped},
		{"in the administrator's settings", macSandbox, func(t *testing.T, n *Node, cwd string) []string {
			f := filepath.Join(t.TempDir(), "managed-settings.json")
			writeFile(t, f, enabled)
			claudeManagedSettings = []string{f}
			return nil
		}, sandbox.AgentSkipped},
		{"in --settings", macSandbox, func(t *testing.T, n *Node, cwd string) []string {
			return []string{"--settings", enabled}
		}, sandbox.AgentSkipped},
		{"settings that cannot be read", macSandbox, func(t *testing.T, n *Node, cwd string) []string {
			writeFile(t, filepath.Join(n.home, ".claude", "settings.json"), "{")
			return nil
		}, sandbox.AgentSkipped},
		{"a --settings file that is not there", macSandbox, func(t *testing.T, n *Node, cwd string) []string {
			return []string{"--settings", filepath.Join(n.home, "gone.json")}
		}, sandbox.AgentSkipped},
		{"sandbox-exec refuses the rules", sandbox.Support{OS: "darwin", SandboxExec: true}, func(t *testing.T, n *Node, cwd string) []string {
			agentSandboxCheck = func(string) error { return os.ErrInvalid }
			return nil
		}, sandbox.AgentUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agentSandboxFakes(t, c.sup)
			n := agentNode(t, true)
			cwd := filepath.Join(n.dir, "assistant")
			args := []string{"-p"}
			if c.prep != nil {
				args = append(args, c.prep(t, n, cwd)...)
			}
			env := []string{"PATH=/usr/bin"}
			cmd := n.assistantCLICommand("/bin/zsh", "claude", cwd, args, env)
			if inSandbox(cmd.Args) || cmd.Args[0] != "/bin/zsh" || !slices.Equal(cmd.Env, env) {
				t.Fatalf("wrapped: %q %q", cmd.Args, cmd.Env)
			}
			st := readAgentSandboxStatus(n.dir)["assistant"]
			if st.State != c.state || st.Why == "" {
				t.Errorf("status %+v, want %s with why", st, c.state)
			}
		})
	}
	t.Run("off says nothing", func(t *testing.T) {
		agentSandboxFakes(t, macSandbox)
		n := agentNode(t, true)
		writeFile(t, filepath.Join(n.home, ".claude", "settings.json"), `{"sandbox": {"enabled": false, "autoAllowBashIfSandboxed": true}}`)
		if cmd := n.assistantCLICommand("/bin/zsh", "claude", filepath.Join(n.dir, "assistant"), nil, nil); !inSandbox(cmd.Args) {
			t.Errorf("sandbox.enabled false: not wrapped %q", cmd.Args)
		}
	})
}

// A Codex is wrapped only when it surely runs its tools itself with no sandbox of its own: danger-full-access, in its
// own process (not on the shared app-server daemon or another --remote one), with nothing it cannot read saying
// otherwise.
func TestCodexInner(t *testing.T) {
	agentSandboxFakes(t, macSandbox)
	cases := []struct {
		name   string
		argv   []string
		config string
		wrap   bool
	}{
		{"plain codex: the shared daemon runs its tools", []string{"codex"}, `sandbox_mode = "danger-full-access"`, false},
		{"--yolo on the shared daemon", []string{"codex", "--yolo"}, "", false},
		{"its own app-server", []string{"codex", "-c", `sandbox_mode="danger-full-access"`, "--remote", "ws://127.0.0.1:1"}, "", false},
		{"-c sandbox_mode", []string{"codex", "-c", `sandbox_mode="danger-full-access"`}, "", true},
		{"-c and --dangerously-bypass-approvals-and-sandbox", []string{"codex", "-c", "model=m", "--dangerously-bypass-approvals-and-sandbox"}, "", true},
		{"config.toml", []string{"codex", "-c", "model=m"}, `sandbox_mode = "danger-full-access"`, true},
		{"config.toml workspace-write", []string{"codex", "-c", "model=m"}, `sandbox_mode = "workspace-write"`, false},
		{"nothing set", []string{"codex", "-c", "model=m"}, "", false},
		{"the profile wins over the top level", []string{"codex", "-c", "model=m"},
			"sandbox_mode = \"danger-full-access\"\nprofile = \"safe\"\n[profiles.safe]\nsandbox_mode = \"read-only\"\n", false},
		{"-p picks a profile", []string{"codex", "-p", "yolo"}, "[profiles.yolo]\nsandbox_mode = \"danger-full-access\"\n", true},
		{"-s wins over -c", []string{"codex", "-c", `sandbox_mode="danger-full-access"`, "-s", "read-only"}, "", false},
		{"--full-auto", []string{"codex", "-c", `sandbox_mode="danger-full-access"`, "--full-auto"}, "", false},
		{"a profile as a dotted key", []string{"codex", "-c", "model=m"}, "sandbox_mode = \"danger-full-access\"\nprofiles.x.sandbox_mode = \"read-only\"\n", false},
		{"-c on a profile", []string{"codex", "-c", `profiles.x.sandbox_mode="read-only"`, "-c", `sandbox_mode="danger-full-access"`}, "", false},
		{"the prompt is not flags", []string{"codex", "-c", "model=m", "--", "--yolo"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			if c.config != "" {
				writeFile(t, filepath.Join(home, ".codex", "config.toml"), c.config)
			}
			why := codexInner(c.argv, nil, filepath.Join(home, "proj"), home)
			if (why == "") != c.wrap {
				t.Errorf("wrap %v, why %q", c.wrap, why)
			}
		})
	}
	t.Run("a project's config", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, "proj", ".codex", "config.toml"), `sandbox_mode = "read-only"`)
		if why := codexInner([]string{"codex", "-c", `sandbox_mode="danger-full-access"`}, nil, filepath.Join(home, "proj", "src"), home); why == "" {
			t.Error("wrapped with a project config it did not read")
		}
	})
	t.Run("an administrator's config", func(t *testing.T) {
		home := t.TempDir()
		f := filepath.Join(home, "managed_config.toml")
		writeFile(t, f, "")
		old := codexManagedConfig
		t.Cleanup(func() { codexManagedConfig = old })
		codexManagedConfig = []string{f}
		if why := codexInner([]string{"codex", "-c", `sandbox_mode="danger-full-access"`}, nil, home, home); why == "" {
			t.Error("wrapped with an administrator's config")
		}
	})
	t.Run("CODEX_HOME", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, "codex2", "config.toml"), `sandbox_mode = "danger-full-access"`)
		if why := codexInner([]string{"codex", "-c", "model=m"}, []string{"CODEX_HOME=" + filepath.Join(home, "codex2")}, home, home); why != "" {
			t.Error(why)
		}
	})
}

// codex-start wraps a Codex that runs danger-full-access in its own process, leaves a managed launcher to `personad
// launch` and other words (pi) alone, and says why when it does not wrap a Codex.
func TestCodexStartExec(t *testing.T) {
	agentSandboxFakes(t, macSandbox)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".persona")
	writeFile(t, filepath.Join(dir, sandbox.AgentOnFile), "")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)
	for _, words := range [][]string{{"/x/personad", "launch", "codex-work"}, {"pi", "--session", "s1"}} {
		path := "/bin/" + filepath.Base(words[0])
		if p, argv, _, st := codexStartExec(path, words, nil); p != path || !slices.Equal(argv, words) || st.State != sandbox.AgentOff {
			t.Errorf("%v: %s %q %+v", words, p, argv, st)
		}
	}
	if len(readAgentSandboxStatus(dir)) != 0 {
		t.Errorf("recorded %v", readAgentSandboxStatus(dir))
	}
	words := []string{"codex", "-c", `sandbox_mode="danger-full-access"`, "--", "hello"}
	p, argv, env, st := codexStartExec("/opt/bin/codex", words, []string{"A=1"})
	if st.State != sandbox.AgentActive || p != argv[0] || !inSandbox(argv) || argv[3] != "/opt/bin/codex" || !slices.Equal(argv[4:], words[1:]) || !slices.Contains(env, sandbox.AgentEnv+"=1") {
		t.Errorf("danger-full-access: %s %q %q %+v", p, argv, env, st)
	}
	words = []string{"codex", "--", "hello"}
	if p, argv, _, st := codexStartExec("/opt/bin/codex", words, nil); p != "/opt/bin/codex" || !slices.Equal(argv, words) || st.State != sandbox.AgentSkipped || st.Why == "" {
		t.Errorf("plain codex: %s %q %+v", p, argv, st)
	}
	if st := readAgentSandboxStatus(dir)["codex-start"]; st.State != sandbox.AgentSkipped {
		t.Errorf("recorded %+v", st)
	}
}

// `personad launch` wraps a managed Claude, says why it does not wrap an agent whose own sandbox is not known, and
// inside the assistant's sandbox runs as it is.
func TestLaunchExec(t *testing.T) {
	agentSandboxFakes(t, macSandbox)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".persona")
	writeFile(t, filepath.Join(dir, sandbox.AgentOnFile), "")
	t.Chdir(home)
	argv := []string{"claude", "--model", "m"}
	exe, got, env, st := launchExec("claude", "/opt/bin/claude", argv, []string{"K=v"})
	if st.State != sandbox.AgentActive || exe != got[0] || !inSandbox(got) || got[3] != "/opt/bin/claude" || !slices.Equal(got[4:], argv[1:]) || !slices.Contains(env, sandbox.AgentEnv+"=1") {
		t.Errorf("claude: %s %q %q %+v", exe, got, env, st)
	}
	if exe, got, _, st := launchExec("agy", "/opt/bin/agy", []string{"agy"}, nil); exe != "/opt/bin/agy" || !slices.Equal(got, []string{"agy"}) || st.State != sandbox.AgentSkipped || st.Why == "" {
		t.Errorf("agy: %s %q %+v", exe, got, st)
	}
	exe, got, _, st = launchExec("claude", "/opt/bin/claude", argv, []string{sandbox.AgentEnv + "=1"})
	if exe != "/opt/bin/claude" || !slices.Equal(got, argv) || st.State != sandbox.AgentActive {
		t.Errorf("inside the assistant's sandbox: %s %q %+v", exe, got, st)
	}
	all := readAgentSandboxStatus(dir)
	if all["launch claude"].State != sandbox.AgentActive || all["launch agy"].State != sandbox.AgentSkipped {
		t.Errorf("recorded %v", all)
	}
}

// The Agent SDK's bridge runs in the sandbox when it is on (the claude it starts with it), as it was when off.
func TestSDKBridgeInAgentSandbox(t *testing.T) {
	agentSandboxFakes(t, macSandbox)
	b := sdkBridge{js: jsRuntime{Path: "/opt/bin/node"}, script: "/x/bridge.mjs", claude: "/opt/bin/claude"}
	for _, on := range []bool{false, true} {
		n := agentNode(t, on)
		s := sdkStart{id: "s1", cwd: filepath.Join(n.dir, "assistant"), mcp: map[string]any{}}
		cmd, _, err := n.sdkCommand(b, "claude", s, []string{"PATH=/usr/bin"})
		if err != nil {
			t.Fatal(err)
		}
		bridge := []string{"/opt/bin/node", "/x/bridge.mjs"}
		if on && (!inSandbox(cmd.Args) || !slices.Equal(cmd.Args[3:], bridge)) || !on && !slices.Equal(cmd.Args, bridge) {
			t.Errorf("on %v: %q", on, cmd.Args)
		}
	}
}

// Everything the path guard calls a secret of the data directory, the sandbox closes too: they share the list
// (identity/secrets.go). The sandbox closes a few more things the path guard leaves (docs/server/agent-sandbox.md "Which
// files"), and neither touches a project's files below the data directory.
func TestAgentSandboxCoversPathGuard(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	profile, err := sandbox.DarwinAgent(sandbox.AgentPlan{DataDirs: []string{dir}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	real := realOf(t, dir)
	var res []*regexp.Regexp
	var subs []string
	block, _, _ := strings.Cut(profile, "(deny network-outbound")
	_, block, _ = strings.Cut(block, "(deny file-read* file-write*")
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), ")")
		if r, ok := strings.CutPrefix(line, `(regex #"`); ok {
			res = append(res, regexp.MustCompile(strings.TrimSuffix(r, `"`)))
		} else if s, ok := strings.CutPrefix(line, "(subpath "); ok {
			p, err := strconv.Unquote(s)
			if err != nil {
				t.Fatal(line)
			}
			subs = append(subs, p)
		}
	}
	if len(res) == 0 || len(subs) == 0 {
		t.Fatalf("no rules in\n%s", profile)
	}
	closed := func(rel string) bool {
		p := real + "/" + rel
		for _, re := range res {
			if re.MatchString(p) {
				return true
			}
		}
		for _, s := range subs {
			if p == s || strings.HasPrefix(p, s+"/") {
				return true
			}
		}
		return false
	}
	both := []string{"secret.key", "secret.key.bak", "handles.key~", "launch.key", "hub.key-old", "hub.json", "hub.json.bak", "link.json",
		"tailcat.json", "tailcat-hub.json", "daemons.json", "manager.json", "apns.json", "apns.p8", "relay.json", "relay.secret",
		"devices.json", "mail_accounts.json.1", "tls/hub.key", "tls/hub.crt", "tls/.tls-1", "link-tls/id.key",
		"launcher-settings/claude-x.json", "managers/m1/manager.json", "managers/m1/secret.key", "managers/m1/tls/hub.key",
		"extensions/data/notes/token", "extensions/data/notes/sub/session.json", "extensions/staging/uninstall-x-1/0/token",
		"plugins/notes/manifest.json", "plugins/data/notes/token", "node-plugins/notes/data/session.json"}
	neither := []string{"mcp.json", "claude-settings.json", "assistant/notes.md", "data/proj/link.json", "data/certs/dev.pem",
		"extensions/notes/manifest.json", "data/extensions/data/x",
		"data/talk.key", "bin/personad", "uploads/a.png", "runtime-packages/x/node_modules/y/index.js", "node.sock"}
	sandboxOnly := []string{".hub.json.12345", "managers/m1/manager.log", "extensions/run/notes/proxy.sock", "run/hub-edge.sock"}
	for _, rel := range both {
		if !dataSecret(rel) || !closed(rel) {
			t.Errorf("%s: path guard %v, sandbox %v; want both", rel, dataSecret(rel), closed(rel))
		}
	}
	for _, rel := range neither {
		if dataSecret(rel) || closed(rel) {
			t.Errorf("%s: path guard %v, sandbox %v; want neither", rel, dataSecret(rel), closed(rel))
		}
	}
	for _, rel := range sandboxOnly {
		if dataSecret(rel) || !closed(rel) {
			t.Errorf("%s: path guard %v, sandbox %v; want the sandbox only", rel, dataSecret(rel), closed(rel))
		}
	}
}

// An agent the node starts in a herdr or Orca pane runs outside the sandbox unless its launcher is a managed one
// (`personad launch` records its own start): with the switch on, the start is recorded as skipped, so the doctor's
// line warns instead of reading as if every agent were covered.
func TestPaneStartOutsideTheSandboxIsRecorded(t *testing.T) {
	agentSandboxFakes(t, macSandbox)
	for _, on := range []bool{true, false} {
		withPaneShell(t, "/bin/zsh")
		n, f, home := herdrNode(t)
		if on {
			writeFile(t, filepath.Join(n.dir, sandbox.AgentOnFile), "")
		}
		f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
		f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
		ctx, cancel := context.WithCancel(context.Background())
		f.after(func(c herdrCall) {
			if c.Method == "pane.send_input" {
				cancel() // started: the record is written before the command is typed
			}
		})
		_, _ = n.start(ctx, home, "claude", "", "", "w1", "", "", "", startID, "")
		cancel()
		got, ok := readAgentSandboxStatus(n.dir)[paneStartWhere]
		if !on {
			if ok {
				t.Errorf("off: recorded %+v", got)
			}
			continue
		}
		if !ok || got.State != sandbox.AgentSkipped || !strings.Contains(got.Why, "「claude」") || !strings.Contains(got.Why, "personad launch") {
			t.Fatalf("on: %+v %v", got, ok)
		}
		if c := checkAgentSandbox(n.dir); c.Status != DoctorWarn || !strings.Contains(c.Detail, paneStartWhere+"：跳过") {
			t.Errorf("doctor: %+v", c)
		}
	}

	// A managed launcher records its own start, and clears an earlier unwrapped one: the user did what the doctor said.
	n := agentNode(t, true)
	n.managed.byID = map[string]proto.ManagedLauncher{"work": {Name: "work"}}
	n.notePaneStart("claude")
	recordAgentSandbox(n.dir, "launch claude", sandbox.AgentStatus{State: sandbox.AgentActive})
	n.notePaneStart("work")
	all := readAgentSandboxStatus(n.dir)
	if _, ok := all[paneStartWhere]; ok || all["launch claude"].State != sandbox.AgentActive {
		t.Errorf("after a managed start: %+v", all)
	}
	if c := checkAgentSandbox(n.dir); c.Status != DoctorOK {
		t.Errorf("doctor after a managed start: %+v", c)
	}
}
