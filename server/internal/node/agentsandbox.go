package node

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The agent sandbox at Persona's own agent starts (docs/server/agent-sandbox.md, #5357): the assistant's Claude on the CLI and
// on the Agent SDK (started by the node), a managed launcher (`personad launch`, run by a pane's shell) and a Codex
// started from the phone (`personad codex-start`, the same). Off unless turned on; on, a start whose agent has a
// sandbox of its own that would clash, or whose sandbox cannot be told, is not wrapped and says why. A start that
// cannot be wrapped runs as before.

// agentSandboxEnv turns the agent sandbox on or off for the process that reads it, over the switch file.
const agentSandboxEnv = "PERSONA_AGENT_SANDBOX"

// agentSandboxStatusFile keeps the last status of each kind of start, for the doctor.
const agentSandboxStatusFile = "agent-sandbox-status.json"

// agentSandboxProbe and agentSandboxCheck are what the sandbox package uses to find out what this machine can do
// (tests replace them).
var (
	agentSandboxProbe = sandbox.AgentProbe
	agentSandboxCheck func(profile string) error // nil: sandbox-exec tries the profile
)

// agentSandboxOn says whether the agent sandbox is on for this process and why: PERSONA_AGENT_SANDBOX (on / off, of
// this process: launchd's for the node, the pane's shell for `personad launch` and codex-start), else the switch file
// in the data directory (the same for every process on the machine).
func agentSandboxOn(dir string) (bool, string) {
	if v, ok := identity.LookupEnv(agentSandboxEnv); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "on", "true", "yes":
			return true, agentSandboxEnv + "=" + v
		case "0", "off", "false", "no":
			return false, agentSandboxEnv + "=" + v
		}
	}
	if _, err := os.Stat(filepath.Join(dir, sandbox.AgentOnFile)); err == nil {
		return true, filepath.Join(dir, sandbox.AgentOnFile)
	}
	return false, ""
}

// agentDataDirs includes the node's root, the configured root and the canonical default, each once.
// The sandbox package resolves existing custom symlinks to real paths; it creates no directory aliases.
func agentDataDirs(home, dir string) []string {
	var dirs []string
	for _, path := range []string{dir, identity.DataDir(home), filepath.Join(home, ".persona")} {
		if !slices.Contains(dirs, path) {
			dirs = append(dirs, path)
		}
	}
	return dirs
}

// wrapAgent is sandbox.WrapAgent for one start (where: what started it, for the log and the doctor) when the switch is
// on; inner says why the agent's own sandbox would clash ("" none), asked only then. A start that is not wrapped
// is logged with why.
func wrapAgent(where, home, dir, cwd string, settings []string, inner func() string, argv, env []string, logf func(string, ...any)) ([]string, []string, sandbox.AgentStatus) {
	on, _ := agentSandboxOn(dir)
	if !on {
		return argv, env, sandbox.AgentStatus{State: sandbox.AgentOff}
	}
	plan := sandbox.AgentPlan{DataDirs: agentDataDirs(home, dir), Home: home, Cwd: cwd, LauncherSettings: settings,
		Probe: agentSandboxProbe, Check: agentSandboxCheck}
	if envValue(env, sandbox.AgentEnv) != "1" && inner != nil {
		plan.Inner = inner()
	}
	argv, env, st := sandbox.WrapAgent(plan, argv, env)
	if st.State != sandbox.AgentActive && logf != nil {
		logf("agent sandbox: %s not wrapped (%s): %s", where, st.State, st.Why)
	}
	recordAgentSandbox(dir, where, st)
	return argv, env, st
}

// paneStartWhere is the doctor's name for an agent the node starts in a herdr or Orca pane by a launcher other than
// `personad launch`.
const paneStartWhere = "pane"

// notePaneStart records, while the sandbox is on, that an agent the node starts in a pane with launcher runs outside
// it: there only a managed launcher (`personad launch`, which records its own start and clears this one) is wrapped, and herdr's resume
// repeats the same command (docs/server/agent-sandbox.md, "Starts this version does not wrap"). The doctor then warns
// instead of reading as if every agent were covered.
func (n *Node) notePaneStart(launcher string) {
	if _, managed := n.managedLauncher(launcher); managed {
		// Once pane starts go through a managed launcher, an earlier unwrapped one is no longer what the doctor warns of.
		forgetAgentSandbox(n.dir, paneStartWhere)
		return
	}
	if on, _ := agentSandboxOn(n.dir); !on {
		return
	}
	st := sandbox.AgentStatus{State: sandbox.AgentSkipped,
		Why: "在 herdr 或 Orca 里用「" + launcher + "」启动（不是托管启动器 personad launch），这一版不包，herdr 重启后接着跑的也一样"}
	log.Printf("agent sandbox: %s not wrapped (%s): %s", paneStartWhere, st.State, st.Why)
	recordAgentSandbox(n.dir, paneStartWhere, st)
}

// agentSandboxRecord is one kind of start's last status.
type agentSandboxRecord struct {
	At time.Time `json:"at"`
	sandbox.AgentStatus
}

// recordAgentSandbox keeps st as where's last status (best effort: two starts at once may lose one).
func recordAgentSandbox(dir, where string, st sandbox.AgentStatus) {
	all := readAgentSandboxStatus(dir)
	all[where] = agentSandboxRecord{At: time.Now().UTC(), AgentStatus: st}
	writeAgentSandboxStatus(dir, all)
}

// forgetAgentSandbox drops where's last status, when there is one.
func forgetAgentSandbox(dir, where string) {
	all := readAgentSandboxStatus(dir)
	if _, ok := all[where]; !ok {
		return
	}
	delete(all, where)
	writeAgentSandboxStatus(dir, all)
}

func writeAgentSandboxStatus(dir string, all map[string]agentSandboxRecord) {
	path := filepath.Join(dir, agentSandboxStatusFile)
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "."+agentSandboxStatusFile+"-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return
	}
	if tmp.Close() == nil {
		_ = os.Rename(tmp.Name(), path)
	}
}

func readAgentSandboxStatus(dir string) map[string]agentSandboxRecord {
	all := map[string]agentSandboxRecord{}
	if b, err := os.ReadFile(filepath.Join(dir, agentSandboxStatusFile)); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	if all == nil {
		all = map[string]agentSandboxRecord{}
	}
	return all
}

// settingsArgs are the files a command line passes with --settings (inline JSON left out).
func settingsArgs(argv []string) []string {
	var out []string
	for i, a := range argv {
		v, ok := "", false
		if a == "--settings" && i+1 < len(argv) {
			v, ok = argv[i+1], true
		} else if s, cut := strings.CutPrefix(a, "--settings="); cut {
			v, ok = s, true
		}
		if ok && !strings.HasPrefix(strings.TrimSpace(v), "{") {
			out = append(out, v)
		}
	}
	return out
}

// claudeManagedSettings are where an administrator's settings for Claude Code live on macOS.
var claudeManagedSettings = []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}

// claudeManagedSettingsDirs hold more of them, one file each.
var claudeManagedSettingsDirs = []string{"/Library/Application Support/ClaudeCode/managed-settings.d"}

// claudeInner says why a Claude started with argv (its arguments, the program first or not) in cwd may run its
// own sandbox (Claude Code's /sandbox, settings `sandbox.enabled`), "" when none of its settings turn it on: the
// user's (settings.json in env's CLAUDE_CONFIG_DIR or ~/.claude, and in each of dirs: a shell launcher may set its own
// CLAUDE_CONFIG_DIR, so the node passes every config directory it knows), the project's (.claude/settings.json and
// settings.local.json in cwd and the folders above it), the administrator's (managed settings) and what argv passes
// with --settings. A file that is there but cannot be read counts as on: not knowing is not wrapping.
func claudeInner(argv, env []string, cwd, home string, dirs []string) string {
	cfg := envValue(env, "CLAUDE_CONFIG_DIR")
	if cfg == "" {
		cfg = filepath.Join(home, ".claude")
	}
	var files []string
	for _, d := range append([]string{cfg}, dirs...) {
		if f := filepath.Join(proto.ExpandHome(d, home), "settings.json"); !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	if cwd != "" {
		for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
			files = append(files, filepath.Join(d, ".claude", "settings.json"), filepath.Join(d, ".claude", "settings.local.json"))
			if d == "/" || d == filepath.Dir(d) {
				break
			}
		}
	}
	files = append(files, claudeManagedSettings...)
	for _, d := range claudeManagedSettingsDirs {
		if ents, err := os.ReadDir(d); err == nil {
			for _, e := range ents {
				if strings.HasSuffix(e.Name(), ".json") {
					files = append(files, filepath.Join(d, e.Name()))
				}
			}
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "读不了 Claude 的设置 " + f + "，判断不出它有没有开自带 sandbox"
		}
		if why := claudeSandboxIn(b, f); why != "" {
			return why
		}
	}
	for i, a := range argv {
		var v string
		if a == "--settings" && i+1 < len(argv) {
			v = argv[i+1]
		} else if s, ok := strings.CutPrefix(a, "--settings="); ok {
			v = s
		} else {
			continue
		}
		if t := strings.TrimSpace(v); strings.HasPrefix(t, "{") {
			if why := claudeSandboxIn([]byte(t), "--settings"); why != "" {
				return why
			}
			continue
		}
		b, err := os.ReadFile(proto.ExpandHome(v, home))
		if err != nil {
			return "读不了 --settings 的 " + v + "，判断不出 Claude 有没有开自带 sandbox"
		}
		if why := claudeSandboxIn(b, v); why != "" {
			return why
		}
	}
	return ""
}

// claudeSandboxIn says why settings (named where) may turn Claude's own sandbox on, "" when they do not.
func claudeSandboxIn(b []byte, where string) string {
	var s map[string]json.RawMessage
	if err := json.Unmarshal(b, &s); err != nil {
		return "Claude 的设置 " + where + " 不是合法的 JSON，判断不出它有没有开自带 sandbox"
	}
	raw, ok := s["sandbox"]
	if !ok || string(raw) == "null" || string(raw) == "false" {
		return ""
	}
	var sb struct {
		Enabled *bool `json:"enabled"`
	}
	if json.Unmarshal(raw, &sb) != nil {
		return "Claude 的设置 " + where + " 里的 sandbox 看不懂，判断不出它有没有开自带 sandbox"
	}
	if sb.Enabled != nil && !*sb.Enabled {
		return ""
	}
	if sb.Enabled == nil {
		return "" // a sandbox block that does not turn it on (only its options)
	}
	return "Claude 在 " + where + " 里开了自带 sandbox（sandbox.enabled），和外层沙箱不能同时用"
}

// codexManagedConfig are an administrator's Codex settings: they can force a sandbox mode over the user's.
var codexManagedConfig = []string{"/etc/codex/managed_config.toml", "/etc/codex/requirements.toml"}

// codexInner says why a Codex started with argv (codex and its arguments) in cwd may run its own sandbox or its tools
// elsewhere, "" only when it is sure to run them itself with none (danger-full-access in its own process):
//   - on an app-server of its own (--remote), or with no flag that keeps a 0.163 off the shared daemon
//     (codexOwnProcess), its tools run in a process Persona did not start: wrapping the TUI would not reach them;
//   - an administrator's config, or a project's .codex/config.toml, may say otherwise: not knowing is not wrapping;
//   - otherwise its mode is, first to last: --dangerously-bypass-approvals-and-sandbox / --yolo, -s / --sandbox /
//     --full-auto, the profile's sandbox_mode (-p, else config's profile), -c sandbox_mode, config's sandbox_mode,
//     read-only.
func codexInner(argv, env []string, cwd, home string) string {
	if codexOnRemote(argv) {
		return "Codex 接的是另外的 app-server（--remote）：它的工具不在这个进程里跑"
	}
	if codexOwnProcess(argv) == "" {
		return "plain Codex 可能跑在共享的 app-server 守护进程上，包住 TUI 挡不住它的工具；要包的话启动器里加 -c sandbox_mode=\"danger-full-access\""
	}
	for _, f := range codexManagedConfig {
		if _, err := os.Stat(f); err == nil {
			return "有 Codex 的托管配置 " + f + "，判断不出它的沙箱模式"
		}
	}
	codexHome := envValue(env, "CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	codexHome = proto.ExpandHome(codexHome, home)
	if cwd != "" {
		for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
			if p := filepath.Join(d, ".codex"); p != filepath.Clean(codexHome) {
				if _, err := os.Stat(filepath.Join(p, "config.toml")); err == nil {
					return "项目里有 " + filepath.Join(p, "config.toml") + "，判断不出 Codex 的沙箱模式"
				}
			}
			if d == "/" || d == filepath.Dir(d) {
				break
			}
		}
	}
	mode, profile, override := "", "", ""
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		next := func() string {
			if i+1 < len(argv) {
				i++
				return argv[i]
			}
			return ""
		}
		flag, val, hasVal := strings.Cut(a, "=")
		switch {
		case a == "--":
			i = len(argv)
		case a == "--dangerously-bypass-approvals-and-sandbox" || a == "--yolo":
			return ""
		case flag == "-s" || flag == "--sandbox":
			if !hasVal {
				val = next()
			}
			if mode == "" {
				mode = val
			}
		case a == "--full-auto":
			if mode == "" {
				mode = "workspace-write"
			}
		case flag == "-p" || flag == "--profile":
			if !hasVal {
				val = next()
			}
			profile = val
		case flag == "-c" || flag == "--config":
			kv := val
			if !hasVal {
				kv = next()
			}
			k, v, _ := strings.Cut(kv, "=")
			k = strings.TrimSpace(k)
			switch {
			case k == "sandbox_mode":
				override = tomlWord(v)
			case k == "profile":
				if profile == "" {
					profile = tomlWord(v)
				}
			case strings.HasPrefix(k, "profiles."):
				return "启动参数里用 -c 改了 profile（" + k + "），判断不出 Codex 的沙箱模式"
			}
		}
	}
	if mode == "" {
		b, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "读不了 Codex 的 config.toml，判断不出它的沙箱模式"
		}
		cfg, ok := codexSandboxConfig(string(b))
		if !ok {
			return "Codex 的 config.toml 里有看不懂的 profile 写法，判断不出它的沙箱模式"
		}
		if profile == "" {
			profile = cfg.profile
		}
		switch {
		case profile != "" && cfg.profiles[profile] != "":
			mode = cfg.profiles[profile]
		case override != "":
			mode = override
		default:
			mode = cfg.mode
		}
	}
	if mode == "danger-full-access" {
		return ""
	}
	if mode == "" {
		mode = "默认模式"
	}
	return "Codex 自带沙箱（" + mode + "）和外层沙箱冲突，只有 danger-full-access 才包"
}

// codexConfig is what a config.toml says about the sandbox.
type codexConfig struct {
	mode, profile string
	profiles      map[string]string // profile -> its sandbox_mode
}

var (
	tomlProfileTable = lazyre.New(`^\s*\[\s*profiles\.(?:"([^"]*)"|'([^']*)'|([A-Za-z0-9_-]+))\s*\]`)
	tomlDottedKey    = lazyre.New(`^\s*"?profiles"?\s*\.`)
)

// codexSandboxConfig reads the top-level sandbox_mode and profile and each [profiles.<name>]'s sandbox_mode; ok false
// for a profile written some other way (a dotted key, an inline table).
func codexSandboxConfig(text string) (codexConfig, bool) {
	c := codexConfig{profiles: map[string]string{}}
	table, top := "", true
	for _, line := range strings.Split(text, "\n") {
		if m := tomlProfileTable.FindStringSubmatch(line); m != nil {
			table, top = m[1]+m[2]+m[3], false
			continue
		}
		if tomlTable.MatchString(line) {
			table, top = "", false
			continue
		}
		if top && tomlDottedKey.MatchString(line) {
			return c, false
		}
		m := tomlKV.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, _ := tomlScalar(m[2])
		s, _ := v.(string)
		switch {
		case top && m[1] == "sandbox_mode":
			c.mode = s
		case top && m[1] == "profile":
			c.profile = s
		case top && m[1] == "profiles":
			return c, false
		case table != "" && m[1] == "sandbox_mode":
			c.profiles[table] = s
		}
	}
	return c, true
}

// tomlWord is a -c value: a TOML string, or the word as it is.
func tomlWord(v string) string {
	if s, ok := tomlScalar(v); ok {
		if str, ok := s.(string); ok {
			return str
		}
	}
	return strings.TrimSpace(v)
}
