package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// `personad node doctor` looks at what a node on this machine needs for the sessions Persona starts to reach the
// phone: the sockets hooks and MCP dial, the files claude loads (compared with what this binary would write now, the
// same bytes the node writes at startup) and the programs those files run. It only reads. --fix writes Persona's
// own generated files again (writeGeneratedConfig, the startup path) and, on a Mac and outside the node, the resident
// node's LaunchAgent's ProcessType (launchagent.go); nothing else: the user's own Claude Code settings are only ever
// looked at. The path-access check (preflight.go) reads what a claude start reads, in a child it kills when a read
// hangs. Background: a redeploy once deleted node.sock and every hook and MCP call fell back to the terminal without a
// word (docs/lessons/server.md).

// Check statuses.
const (
	DoctorOK   = "ok"
	DoctorWarn = "warn"
	DoctorFail = "fail"
)

// DoctorCheck is one line of the report; the names are stable for scripts.
type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// State is current / stale / missing for a generated file or directory, "" for the other checks.
	State  string   `json:"state"`
	Detail string   `json:"detail"`
	Hint   string   `json:"hint"`
	Items  []string `json:"items"` // what differs, which commands, ...; never null
}

// DoctorReport is what `node doctor` prints (--json: as it is).
type DoctorReport struct {
	OK     bool          `json:"ok"` // no check failed
	Dir    string        `json:"dir"`
	Checks []DoctorCheck `json:"checks"`
	// Fixed names what --fix put right: the generated files it wrote again; on a Mac launchd-process-type when it
	// wrote ProcessType into the LaunchAgent and launchd-reload when it loaded the agent again.
	Fixed []string `json:"fixed"`
}

// DoctorOptions says where to look; DefaultDoctorOptions fills them in for this machine (tests give their own).
type DoctorOptions struct {
	Dir string // the node's directory (~/.persona)
	Bin string // the program the generated files should run: this binary
	// Sockets: node.sock, runtime.sock and herdr's API socket.
	NodeSock, RuntimeSock, HerdrSock string
	// ClaudeConfigDir is where the user's own Claude Code settings are (read, never written).
	ClaudeConfigDir string
	// ClaudeVersion asks claude's version ("" when unknown).
	ClaudeVersion func(context.Context) string
	// LaunchAgent is the resident node's launchd agent on a Mac (NodeLaunchAgentPath; pickNodeLaunchdLabel says which
	// when a Mac keeps its previously installed label); "" elsewhere, and the launchd-process-type check is
	// left out.
	LaunchAgent string
	// LaunchdTarget is that agent as launchctl names it (gui/<uid>/com.persona.node, or the older label).
	LaunchdTarget string
	// Launchctl runs launchctl and returns its output; nil: launchd is not asked (the check reads the plist only).
	Launchctl func(ctx context.Context, args ...string) (string, error)
	// InsideNode: the doctor runs in the resident node (node.doctor) or under it. --fix then leaves the LaunchAgent
	// alone: loading it again stops the node, and with it the doctor, half way.
	InsideNode bool
	// RestartWait caps the wait, after --fix loaded the agent again, for the new node to answer (0: nodeRestartWait).
	RestartWait time.Duration
	// Preflight is the path-access check (preflight.go); nil leaves it out.
	Preflight func(context.Context) DoctorCheck
	// Fix writes the generated files again when they are stale or missing, and (outside the node) writes
	// ProcessType=Interactive into the LaunchAgent and loads it again.
	Fix bool
}

// DefaultDoctorOptions is this machine's: the same places the node itself uses, without creating or moving any.
func DefaultDoctorOptions() (DoctorOptions, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return DoctorOptions{}, err
	}
	bin, err := os.Executable()
	if err != nil {
		return DoctorOptions{}, err
	}
	if p, err := filepath.EvalSymlinks(bin); err == nil {
		bin = p
	}
	opts := DoctorOptions{
		Dir: identity.DataDir(home), Bin: bin,
		NodeSock: nodeclient.SocketPath(), RuntimeSock: RuntimeSocketPath(), HerdrSock: herdr.SocketPath(),
		ClaudeConfigDir: claudeDirOf(home), ClaudeVersion: claudeVersion,
	}
	opts.withLaunchd(home)
	// Under the node's agent (a claude the node started runs the doctor) a reload would stop the doctor too: under
	// either label, today's or the one a Mac set up before the rename keeps.
	opts.InsideNode = isNodeLaunchdLabel(os.Getenv("XPC_SERVICE_NAME"))
	if runtime.GOOS != "windows" {
		opts.Preflight = preflightFromNode(opts.NodeSock, home, opts.ClaudeConfigDir)
	}
	return opts, nil
}

// withLaunchd points the launchd-process-type check at the resident node's agent, on a Mac only.
func (o *DoctorOptions) withLaunchd(home string) {
	if runtime.GOOS != "darwin" {
		return
	}
	// launchctl is asked only when both labels have a plist; bounded, as everything else here.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o.withLaunchAgent(ctx, home, launchdDomain(), os.Getenv("XPC_SERVICE_NAME"), runLaunchctl)
}

// withLaunchAgent points the check at the agent pickNodeLaunchdLabel picks in domain, asking launchctl.
func (o *DoctorOptions) withLaunchAgent(ctx context.Context, home, domain, self string,
	launchctl func(context.Context, ...string) (string, error)) {
	label := pickNodeLaunchdLabel(ctx, home, domain, self, launchctl)
	o.LaunchAgent, o.LaunchdTarget, o.Launchctl = NodeLaunchAgentPath(home, label), domain+"/"+label, launchctl
}

// generatedFile is a file the node writes for the sessions it starts: Group is the unit the doctor reports on, Rel
// the path under the node's directory.
type generatedFile struct {
	Group   string
	Rel     string
	Content []byte
}

// generatedGroups are the groups of generatedFiles, then the skills plugin (agent-plugin, assistantskills).
var generatedGroups = []string{"claude-settings.json", "claude-hooks", "assistant-settings.json", "assistant-hooks", "mcp.json"}

const agentPluginGroup = "agent-plugin"

// hookSettings is the settings with Persona's hooks for a claude whose program is bin, of the node whose directory is
// dir: the agents' (assistant false) or the assistant's, which add the Feishu gate on Bash (larkgate.go). The hook
// names dir itself (rootAssign): Claude Code runs it with the claude's environment, which is its pane's (agentenv.go).
func hookSettings(bin, dir string, assistant bool) map[string]any {
	cmd := shellQuote(bin) + " hook"
	if dir != "" {
		cmd = rootAssign(dir, shellQuote) + cmd
	}
	blocking := func(matcher string) map[string]any {
		return map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 86400}}}
	}
	notify := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 10}}}
	pre := []any{blocking("AskUserQuestion")}
	if assistant {
		pre = append(pre, blocking("Bash"))
	}
	return map[string]any{"hooks": map[string]any{
		"PreToolUse":        pre,
		"PermissionRequest": []any{blocking("*")},
		"Elicitation":       []any{blocking("*")},
		"Notification":      []any{notify},
		"Stop":              []any{notify},
		"UserPromptSubmit":  []any{notify},
		"SessionStart":      []any{notify},
	}}
}

// displayHooks is the MessageDisplay hook of the Agents Persona starts, the live replies plugin's (hooksplugin.go,
// livereply.go, #1309), for the node whose directory is dir: Claude Code hands it each batch of whole lines of a reply
// as it shows them. It shows the lines only once the hook is done, whatever "async" says (2.1.319), so the hook just
// passes them to the node and prints nothing (nodeclient.Display, at most 150 ms after it started); the timeout
// (seconds, the shortest whole one) bounds one that hangs, after which Claude shows the lines as they are. It is in
// exec form ("command" the program, "args" its arguments): Claude Code starts it directly instead of through /bin/sh -c,
// which costs every batch 6–8 ms more on a Mac (docs/integrations/claude-integration.md "What the terminal pays").
// Every Claude Code with the event takes exec form (2.1.150 has exec form and not the event; 2.1.200 has both). With
// no shell to put PERSONA_HOME in front (hookSettings), the hook is told dir as an argument (nodeclient.HomeFlag).
// The headless assistant streams its replies itself and has none.
func displayHooks(bin, dir string) []any {
	args := []string{"hook"}
	if dir != "" {
		args = append(args, nodeclient.HomeFlag, dir)
	}
	return []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": bin, "args": args, "async": true, "timeout": 1}}}}
}

// generatedFiles is every file writeGeneratedConfig writes under the node's directory dir (but the skills plugin),
// computed without writing. Each set of hooks is there twice: as a plugin for a claude that loads plugins, in a
// settings file for one that does not (hooksplugin.go). The hooks and the MCP server are told dir, as PERSONA_HOME.
// The Agents' live replies plugin is part of their hooks (claude-hooks), written whatever the setting says: a start
// loads it or not (liveRepliesPlugin).
func generatedFiles(bin, dir string) ([]generatedFile, error) {
	var out []generatedFile
	for _, kind := range []string{"claude", "assistant"} {
		settings := hookSettings(bin, dir, kind == "assistant")
		b, err := marshalJSON(settings)
		if err != nil {
			return nil, err
		}
		out = append(out, generatedFile{Group: kind + "-settings.json", Rel: kind + "-settings.json", Content: b})
		files, err := hooksPluginFiles(settings["hooks"])
		if err != nil {
			return nil, err
		}
		for _, rel := range slices.Sorted(maps.Keys(files)) {
			out = append(out, generatedFile{Group: kind + "-hooks", Rel: filepath.Join(kind+"-hooks", rel), Content: files[rel]})
		}
		if kind != "claude" {
			continue
		}
		live, err := liveRepliesPluginFiles(bin, dir)
		if err != nil {
			return nil, err
		}
		for _, rel := range slices.Sorted(maps.Keys(live)) {
			out = append(out, generatedFile{Group: kind + "-hooks", Rel: filepath.Join(liveRepliesDir, rel), Content: live[rel]})
		}
	}
	server := map[string]any{"command": bin, "args": []string{"mcp"}}
	if dir != "" {
		server["env"] = map[string]string{"PERSONA_HOME": dir}
	}
	mcp, err := marshalJSON(map[string]any{"mcpServers": compat.AddMCPAliases(map[string]any{"persona": server})})
	if err != nil {
		return nil, err
	}
	return append(out, generatedFile{Group: "mcp.json", Rel: "mcp.json", Content: mcp}), nil
}

// writeGeneratedConfig writes the hooks, MCP config and skills plugin that sessions Persona starts load into dir,
// for a claude whose hook program is bin: what the node does at each start, and what `node doctor --fix` does.
func writeGeneratedConfig(dir, bin string) error {
	files, err := generatedFiles(bin, dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := writeFileAtomic(path, f.Content); err != nil {
			return err
		}
	}
	return assistantskills.WritePlugin(filepath.Join(dir, agentPluginGroup)) // the skills the agents Persona starts load (agentplugin.go)
}

// RunDoctor runs the checks. With opts.Fix it first puts right what it may (the generated files; outside the node, the
// LaunchAgent's ProcessType), then checks again.
func RunDoctor(ctx context.Context, opts DoctorOptions) DoctorReport {
	if pre := opts.Preflight; pre != nil { // read once: nothing a fix does changes what it reads
		var once sync.Once
		var c DoctorCheck
		opts.Preflight = func(ctx context.Context) DoctorCheck {
			once.Do(func() { c = pre(ctx) })
			return c
		}
	}
	rep := doctorChecks(ctx, opts)
	if !opts.Fix {
		return rep
	}
	var fixed []string
	var failed []DoctorCheck
	var stale []string
	for _, c := range rep.Checks {
		if c.State == "stale" || c.State == "missing" {
			stale = append(stale, c.Name)
		}
	}
	if len(stale) > 0 {
		if err := writeGeneratedConfig(opts.Dir, opts.Bin); err != nil {
			failed = append(failed, DoctorCheck{Name: "fix", Status: DoctorFail, Detail: "重新生成失败：" + err.Error(),
				Hint: "检查 " + opts.Dir + " 的权限和磁盘空间", Items: []string{}})
		} else {
			fixed = append(fixed, stale...)
		}
	}
	var notBack []string
	logPath := ""
	if opts.LaunchAgent != "" && !opts.InsideNode {
		a := inspectLaunchAgent(ctx, opts)
		wrote, reloaded, err := fixLaunchAgent(ctx, opts, a)
		if err != nil {
			failed = append(failed, DoctorCheck{Name: "fix", Status: DoctorFail, Detail: err.Error(),
				Hint: "也可以重新安装常驻 node 的服务（scripts/deploy/node-launchd.sh install）", Items: []string{opts.LaunchAgent}})
		}
		if wrote {
			fixed = append(fixed, launchdCheck)
		}
		if reloaded {
			fixed = append(fixed, launchdReloaded)
			// bootstrap returns before the new node listens: checked at once, its sockets would look dead.
			notBack, logPath = waitForRestart(ctx, opts, rep), a.logPath
		}
	}
	if len(fixed) == 0 && len(failed) == 0 {
		return rep
	}
	rep = doctorChecks(ctx, opts)
	notBackYet(&rep, notBack, opts.RestartWait, logPath)
	rep.Fixed = append(rep.Fixed, fixed...)
	if len(failed) > 0 {
		rep.Checks = append(rep.Checks, failed...)
		rep.OK = false
	}
	return rep
}

func doctorChecks(ctx context.Context, opts DoctorOptions) DoctorReport {
	rep := DoctorReport{Dir: opts.Dir, Fixed: []string{}}
	add := func(c DoctorCheck) {
		if c.Items == nil {
			c.Items = []string{}
		}
		rep.Checks = append(rep.Checks, c)
	}
	var pre chan DoctorCheck
	if opts.Preflight != nil { // reads in a child of its own, alongside the other checks
		pre = make(chan DoctorCheck, 1)
		go func() { pre <- opts.Preflight(ctx) }()
	}
	add(checkNodeSocket(ctx, opts.NodeSock))
	add(checkRuntimeSocket(ctx, opts.RuntimeSock))
	for _, c := range checkGenerated(opts) {
		add(c)
	}
	add(checkCommandPaths(opts.Dir))
	add(checkClaudeVersion(ctx, opts.ClaudeVersion))
	add(checkHerdr(ctx, opts.HerdrSock))
	add(checkGlobalPermissionHooks(opts.ClaudeConfigDir))
	add(checkAgentSandbox(opts.Dir))
	if opts.LaunchAgent != "" {
		add(checkLaunchAgent(inspectLaunchAgent(ctx, opts), opts))
	}
	if pre != nil {
		add(<-pre)
	}
	rep.OK = !slices.ContainsFunc(rep.Checks, func(c DoctorCheck) bool { return c.Status == DoctorFail })
	return rep
}

const doctorDialTimeout = 3 * time.Second

func checkNodeSocket(ctx context.Context, path string) DoctorCheck {
	c := DoctorCheck{Name: "node.sock"}
	if _, err := os.Stat(path); err != nil {
		c.Status, c.Detail = DoctorFail, "不存在"
		c.Hint = "没有 node 在监听：hook 和 MCP 会静默退回终端。确认 hub 已连上这台电脑；node 重启会重建"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	line, err := nodeclient.DialAt(ctx, path, sockRequest{Kind: "ping"})
	if err != nil {
		c.Status, c.Detail = DoctorFail, "文件在但连不上或没有应答（"+shorten(err.Error(), 80)+"）"
		c.Hint = "可能是旧 node 退出后留下的文件；重启这台电脑上的 node"
		return c
	}
	if !json.Valid(line) {
		c.Status, c.Detail = DoctorFail, "应答不是 JSON"
		c.Hint = "这个 socket 不是 node 的；重启 node"
		return c
	}
	c.Status, c.Detail = DoctorOK, "已应答"
	return c
}

func checkRuntimeSocket(ctx context.Context, path string) DoctorCheck {
	c := DoctorCheck{Name: "runtime.sock"}
	if _, err := os.Stat(path); err != nil {
		c.Status, c.Detail = DoctorWarn, "不存在"
		c.Hint = "没有 node 在监听：DSH、pi 这类 runtime 连不上；没用到可忽略"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		c.Status, c.Detail = DoctorFail, "文件在但连不上（"+shorten(err.Error(), 80)+"）"
		c.Hint = "可能是旧 node 退出后留下的文件；重启这台电脑上的 node"
		return c
	}
	conn.Close()
	c.Status, c.Detail = DoctorOK, "可连接"
	return c
}

// checkGenerated compares each generated file with what this binary would write: current, stale (differs, or part
// of a plugin is missing) or missing.
func checkGenerated(opts DoctorOptions) []DoctorCheck {
	files, err := generatedFiles(opts.Bin, opts.Dir)
	if err != nil {
		return []DoctorCheck{{Name: "generated-files", Status: DoctorFail, Detail: err.Error(), Items: []string{}}}
	}
	var out []DoctorCheck
	for _, g := range generatedGroups {
		var issues []string
		total := 0
		for _, f := range files {
			if f.Group != g {
				continue
			}
			total++
			got, err := os.ReadFile(filepath.Join(opts.Dir, f.Rel))
			switch {
			case err != nil:
				issues = append(issues, "missing "+filepath.ToSlash(f.Rel))
			case !bytes.Equal(got, f.Content):
				issues = append(issues, "differs "+filepath.ToSlash(f.Rel))
			}
		}
		allMissing := len(issues) == total && allPrefix(issues, "missing")
		out = append(out, generatedCheck(g, issues, allMissing, true))
	}
	issues := assistantskills.PluginIssues(filepath.Join(opts.Dir, agentPluginGroup))
	allMissing := len(issues) == 1 && issues[0] == "missing"
	return append(out, generatedCheck(agentPluginGroup, issues, allMissing, false))
}

// generatedCheck turns what differs in one generated file or directory into its check. critical: the sessions
// lose their hooks or MCP without it (missing fails), otherwise only the skills (missing warns).
func generatedCheck(name string, issues []string, allMissing, critical bool) DoctorCheck {
	c := DoctorCheck{Name: name, Items: issues}
	if c.Items == nil {
		c.Items = []string{}
	}
	switch {
	case len(issues) == 0:
		c.State, c.Status, c.Detail = "current", DoctorOK, "和当前二进制会写的一致"
	case allMissing:
		c.State, c.Status, c.Detail = "missing", DoctorWarn, "不存在"
		if critical {
			c.Status = DoctorFail
			c.Hint = "Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix"
		} else {
			c.Hint = "Persona 启动的 Agent 没有 persona-office 技能；重启 node 或运行 personad node doctor --fix"
		}
	default:
		c.State, c.Status = "stale", DoctorWarn
		c.Detail = fmt.Sprintf("过期：%d 处不同（%s）", len(issues), shorten(issues[0], 60))
		c.Hint = "重启 node 会重写；或运行 personad node doctor --fix"
	}
	return c
}

func allPrefix(items []string, prefix string) bool {
	return !slices.ContainsFunc(items, func(s string) bool { return !strings.HasPrefix(s, prefix) })
}

// configCommands are the programs a settings file or hooks plugin (top-level "hooks") runs: a shell-form hook's first
// word, an exec-form one's (with "args") command as it is.
func configCommands(b []byte) []string {
	var f struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"hooks"`
		} `json:"hooks"`
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	var out []string
	for _, matchers := range f.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				w := h.Command
				if h.Args == nil {
					w = shellFirstWord(h.Command)
				}
				if w != "" {
					out = append(out, w)
				}
			}
		}
	}
	for _, s := range f.MCPServers {
		if s.Command != "" {
			out = append(out, s.Command)
		}
	}
	return out
}

// shellFirstWord is the program a command line written with shellQuote runs: its first word past the VAR=value ones
// in front (rootAssign). A word is bare, or single-quoted with each embedded quote closed, backslash-escaped and
// reopened.
func shellFirstWord(cmd string) string {
	for rest := strings.TrimSpace(cmd); rest != ""; {
		var w strings.Builder
		i := 0
		for i < len(rest) && rest[i] != ' ' && rest[i] != '\t' {
			switch {
			case rest[i] == '\'':
				j := strings.IndexByte(rest[i+1:], '\'')
				if j < 0 {
					return "" // not a command line shellQuote writes
				}
				w.WriteString(rest[i+1 : i+1+j])
				i += j + 2
			case rest[i] == '\\' && i+1 < len(rest):
				w.WriteByte(rest[i+1])
				i += 2
			default:
				w.WriteByte(rest[i])
				i++
			}
		}
		if !assignmentRe.MatchString(rest[:i]) {
			return w.String()
		}
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	return ""
}

// assignmentRe is a shell word that sets a variable for the command after it.
var assignmentRe = lazyre.New(`^[A-Za-z_][A-Za-z0-9_]*=`)

// checkCommandPaths: the program each generated file runs exists and can be run. A file moved or removed under a
// running node (a redeploy to another path) leaves hooks that fail on every call.
func checkCommandPaths(dir string) DoctorCheck {
	c := DoctorCheck{Name: "command-paths", Items: []string{}}
	files := []string{"claude-settings.json", "assistant-settings.json", "mcp.json"}
	for _, kind := range []string{"claude", "assistant"} {
		files = append(files, filepath.Join(kind+"-hooks", "hooks", "hooks.json"))
	}
	files = append(files, filepath.Join(liveRepliesDir, "hooks", "hooks.json"))
	seen := map[string]bool{}
	var bad []string
	read := 0
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			continue
		}
		read++
		for _, p := range configCommands(b) {
			if seen[p] {
				continue
			}
			seen[p] = true
			if err := runnable(p); err != nil {
				bad = append(bad, fmt.Sprintf("%s: %v", p, err))
			}
		}
	}
	switch {
	case len(bad) > 0:
		slices.Sort(bad)
		c.Status, c.Items = DoctorFail, bad
		c.Detail = fmt.Sprintf("%d 个命令路径不可用（%s）", len(bad), shorten(bad[0], 80))
		c.Hint = "hook / MCP 会失败；用 node 现在的二进制运行 personad node doctor --fix 重写"
	case read == 0:
		c.Status, c.Detail = DoctorWarn, "没有可检查的生成文件"
		c.Hint = "先让 node 启动一次，或运行 personad node doctor --fix"
	default:
		c.Status, c.Detail = DoctorOK, fmt.Sprintf("%d 个命令路径都存在且可执行", len(seen))
	}
	return c
}

func runnable(path string) error {
	if !filepath.IsAbs(path) { // a bare name: found on PATH like claude would
		if _, err := exec.LookPath(path); err != nil {
			return errors.New("not on PATH")
		}
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return errors.New("not found")
	}
	if st.IsDir() || (runtime.GOOS != "windows" && st.Mode()&0o111 == 0) {
		return errors.New("not executable")
	}
	return nil
}

func checkClaudeVersion(ctx context.Context, ask func(context.Context) string) DoctorCheck {
	c := DoctorCheck{Name: "claude-version"}
	v := ""
	if ask != nil {
		v = ask(ctx)
	}
	switch {
	case v == "":
		c.Status, c.Detail = DoctorWarn, "读不到 claude 的版本"
		c.Hint = "没装 Claude Code，或登录 shell 里找不到它；读不到时 Persona 按不支持 --plugin-dir 处理，hooks 走 --settings"
	case !versionAtLeast(v, pluginMinVersion):
		c.Status, c.Detail = DoctorWarn, fmt.Sprintf("%s（低于 %d.%d.%d，不支持 --plugin-dir）", v, pluginMinVersion[0], pluginMinVersion[1], pluginMinVersion[2])
		c.Hint = "hooks 退回 --settings，persona-office 技能不可用；升级 Claude Code"
	default:
		c.Status, c.Detail = DoctorOK, v+"，支持 --plugin-dir"
	}
	return c
}

// checkHerdr dials herdr's API socket. The engine is chosen by the hub when it connects and is not kept on this
// machine, so a socket that is not there is a warning: it only matters when herdr is this machine's engine.
func checkHerdr(ctx context.Context, path string) DoctorCheck {
	c := DoctorCheck{Name: "herdr"}
	if path == "" {
		c.Status, c.Detail = DoctorWarn, "找不到 herdr 的 socket 位置"
		c.Hint = "设了 HERDR_SOCKET_PATH 的话确认它指向 herdr；这台电脑的引擎不是 herdr 时可忽略"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	conn, err := herdr.Dial(ctx, path)
	if err != nil {
		c.Status, c.Detail = DoctorWarn, "连不上 herdr 的 socket"
		c.Hint = "引擎是 herdr 时先启动 herdr；这台电脑用别的引擎时可忽略"
		return c
	}
	conn.Close()
	c.Status, c.Detail = DoctorOK, "herdr 在监听"
	return c
}

// checkAgentSandbox is the doctor's line for the agent sandbox: whether it is on and where from, whether this machine
// can run it, and each kind of start's last status.
func checkAgentSandbox(dir string) DoctorCheck {
	c := DoctorCheck{Name: "agent-sandbox", Status: DoctorOK, Items: []string{}}
	on, from := agentSandboxOn(dir)
	sup := sandbox.AgentSupport(agentSandboxProbe()) // on macOS sandbox-exec runs true once; elsewhere nothing runs
	last := readAgentSandboxStatus(dir)
	wheres := make([]string, 0, len(last))
	for w := range last {
		wheres = append(wheres, w)
	}
	slices.Sort(wheres)
	for _, w := range wheres {
		r := last[w]
		item := fmt.Sprintf("%s：%s（%s）", w, agentSandboxWords[r.State], r.At.Local().Format("01-02 15:04"))
		if r.Why != "" {
			item += "：" + r.Why
		}
		c.Items = append(c.Items, item)
	}
	switch {
	case !on:
		c.Detail = "未开启（默认关闭）"
		if from != "" {
			c.Detail = "未开启（" + from + "）"
		}
		switch sup.State {
		case sandbox.AgentActive:
			c.Detail += "；本机可以开启"
			c.Hint = "开启：touch " + filepath.Join(dir, sandbox.AgentOnFile) + "；见 docs/server/agent-sandbox.md"
		case sandbox.AgentUnsupported:
			c.Detail += "；不支持：" + sup.Why
		default:
			c.Detail += "；工具不可用：" + sup.Why
		}
	case sup.State == sandbox.AgentUnsupported:
		c.Status, c.Detail = DoctorWarn, "已开启，但不支持："+sup.Why
		c.Hint = "agent 照常启动，不进沙箱"
	case sup.State == sandbox.AgentUnavailable:
		c.Status, c.Detail = DoctorWarn, "已开启，但工具不可用："+sup.Why
		c.Hint = "agent 照常启动，不进沙箱"
	default:
		c.Detail = "已开启（" + from + "）；" + agentSandboxLimits
		c.Hint = "环境变量 " + agentSandboxEnv + " 只对设置它的进程生效（node 看 launchd 的环境，personad launch 看 pane 的 shell）"
		for _, w := range wheres {
			if s := last[w].State; s == sandbox.AgentSkipped || s == sandbox.AgentUnavailable {
				c.Status = DoctorWarn
				c.Detail += "；有启动被跳过，见下面的原因"
				c.Hint = "被跳过的启动照常运行、不在沙箱里"
				break
			}
		}
	}
	if len(c.Items) > 0 {
		c.Detail += "｜最近：" + strings.Join(c.Items, "；")
	}
	return c
}

// agentSandboxLimits is what the doctor reminds of while the sandbox is on (docs/server/agent-sandbox.md): what it does not
// stop, which agents it does not cover, and what stops working in it.
const agentSandboxLimits = "挡不住 docker；在 herdr 或 Orca 里不经托管启动器（personad launch）启动的 agent，" +
	"比如直接用 claude 或 ~/.zshrc 里的启动器，包括 herdr 重启后接着跑的，不在沙箱里；沙箱里 ps、top、sudo 这类 setuid 程序起不来"

// agentSandboxWords are the statuses in the doctor's words.
var agentSandboxWords = map[string]string{
	sandbox.AgentOff: "未开启", sandbox.AgentActive: "已在沙箱里", sandbox.AgentSkipped: "跳过",
	sandbox.AgentUnsupported: "不支持", sandbox.AgentUnavailable: "工具不可用",
}

// checkGlobalPermissionHooks looks at the user's own settings.json for synchronous PermissionRequest hooks (other
// tools put them there) and says how many; it never changes the file.
func checkGlobalPermissionHooks(configDir string) DoctorCheck {
	c := DoctorCheck{Name: "global-permission-hooks", Items: []string{}}
	b, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		c.Status, c.Detail = DoctorOK, "没有全局 settings.json"
		return c
	}
	if err != nil {
		c.Status, c.Detail = DoctorWarn, "读不了全局 settings.json（"+shorten(err.Error(), 60)+"）"
		return c
	}
	var f struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &f) != nil {
		c.Status, c.Detail = DoctorWarn, "全局 settings.json 不是合法 JSON"
		c.Hint = "Claude Code 自己也读不了它；Persona 不会改这个文件"
		return c
	}
	for _, m := range f.Hooks["PermissionRequest"] {
		for _, h := range m.Hooks {
			if !h.Async && (h.Type == "command" || h.Type == "") {
				c.Items = append(c.Items, shorten(h.Command, 80))
			}
		}
	}
	if len(c.Items) == 0 {
		c.Status, c.Detail = DoctorOK, "没有同步的 PermissionRequest 钩子"
		return c
	}
	c.Status = DoctorWarn
	c.Detail = fmt.Sprintf("%d 个同步的 PermissionRequest 钩子（%s）", len(c.Items), c.Items[0])
	c.Hint = "它们和 Persona 的手机审批同时拦着权限请求，可能互相影响；Persona 不会改这个文件，需要时自己检查"
	return c
}

// shorten cuts s to at most n runes, with … when it cut.
func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// DoctorCommand is `personad node doctor [--json] [--fix]`: it prints the report to out and says whether a check
// failed (the caller exits non-zero then).
func DoctorCommand(ctx context.Context, args []string, out io.Writer) (failed bool, err error) {
	if len(args) > 0 && args[0] == preflightChildArg { // the preflight's reads, in a child of the doctor's (preflight.go)
		preflightChild(args[1:], out)
		return false, nil
	}
	flags := flag.NewFlagSet("node doctor", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "以 JSON 输出")
	fix := flags.Bool("fix", false, "重新生成 Persona 自己生成的文件；Mac 上给常驻 node 的 LaunchAgent 写上 ProcessType=Interactive 并重新加载（不碰用户的全局配置）")
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if flags.NArg() != 0 {
		return false, errors.New("node doctor 不接受位置参数")
	}
	opts, err := DefaultDoctorOptions()
	if err != nil {
		return false, err
	}
	opts.Fix = *fix
	rep := RunDoctor(ctx, opts)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return !rep.OK, enc.Encode(rep)
	}
	PrintDoctor(out, rep)
	return !rep.OK, nil
}

// doctorLabels are the line prefixes of the text report, the same words `hub doctor` uses (hub/doctor.go).
var doctorLabels = map[string]string{DoctorOK: "正常", DoctorWarn: "注意", DoctorFail: "有问题"}

// PrintDoctor writes rep, one line per check: label | name: what was found; what to do about it (full-width separators).
func PrintDoctor(out io.Writer, rep DoctorReport) {
	fmt.Fprintf(out, "node 自检（%s）\n", rep.Dir)
	counts := map[string]int{}
	for _, c := range rep.Checks {
		counts[c.Status]++
		line := fmt.Sprintf("%s｜%s：%s", doctorLabels[c.Status], c.Name, c.Detail)
		if c.Hint != "" && c.Status != DoctorOK {
			line += "；" + c.Hint
		}
		fmt.Fprintln(out, line)
	}
	var regenerated []string
	for _, f := range rep.Fixed {
		switch f {
		case launchdCheck:
			fmt.Fprintln(out, "已给常驻 node 的 LaunchAgent 写上 ProcessType=Interactive")
		case launchdReloaded:
			fmt.Fprintln(out, "已重新加载常驻 node 的 LaunchAgent（node 已重启）")
		default:
			regenerated = append(regenerated, f)
		}
	}
	if len(regenerated) > 0 {
		fmt.Fprintf(out, "已重新生成：%s\n", strings.Join(regenerated, "、"))
	}
	fmt.Fprintf(out, "共 %d 项：%d 正常，%d 注意，%d 有问题\n", len(rep.Checks), counts[DoctorOK], counts[DoctorWarn], counts[DoctorFail])
}
