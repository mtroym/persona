package node

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// start launches the selected agent in a new herdr tab and waits for its session: `<launcher> --session-id …` (or
// --resume) for Claude Code, waiting until claude is ready; a Codex launcher of the login shell with the first message
// (codex_start.go); a compatible CLI, or a launcher the user says runs another agent, without Claude's flags (start_agent.go).
// workspaceID puts the tab into an existing space (e.g. "new tab next to this agent"); otherwise the workspace named
// after the directory is reused or created.
// tabName names the herdr tab (hub picks one from the first message when the user gave none); empty: agent-<n>.
// newID is the id a new session gets (startAsync picked it already); empty: a fresh one.
// agentType is the agent the user picked for the launcher (claude, codex, traex); empty: what the launcher runs.
func (n *Node) start(ctx context.Context, cwd, launcher, prompt, resume, workspaceID, mode, effort, tabName, newID, agentType string, options ...modelExecution) (*proto.Session, error) {
	execution := executionOption(options)
	if err := execution.check(); err != nil {
		return nil, err
	}
	if resume != "" && validID(resume) && n.codexPath(resume) != "" && !n.isAssistantSession(resume) {
		if _, live := n.agent(resume); !live { // a Codex thread nothing runs (codex_resume.go)
			return n.resumeCodexSession(ctx, resume, launcher, prompt, mode)
		} else if mode == "plan" {
			return nil, readonlyStartUnsupported() // CLI flags cannot constrain a process that is already running.
		}
	}
	if resume != "" && n.externalCodex(resume) {
		return nil, refusal("codex_read_only", "不能用启动器恢复外部 Codex 会话", nil)
	}
	cwd, launcher, err := n.checkStart(cwd, launcher, resume, mode, effort)
	if err != nil {
		return nil, err
	}
	kind := agentType
	if kind == "" {
		kind = launcherAgentType(launcher)
	}
	if kind != "claude" && kind != "codex" && kind != traexAgent {
		return nil, fmt.Errorf("unsupported agent type %q", kind)
	}
	argv, codex := n.codexLauncher(ctx, launcher)
	if mode == "plan" {
		if codex && kind != traexAgent {
			if argv, err = n.codexReadonlyArgs(launcher, argv, mode); err != nil {
				return nil, err
			}
		} else if kind != "claude" {
			// This launcher path has no verified read-only argument mapping.
			return nil, readonlyStartUnsupported()
		}
	}
	if err := n.checkUltracodeStart(ctx, execution.Ultracode, kind); err != nil {
		return nil, err
	}
	if execution.Model != "" {
		agent, err := n.launcherModelCheck(ctx, launcher, execution.Model)
		if err != nil {
			return nil, err
		}
		if agent != "claude" && agent != "codex" {
			return nil, errors.New("普通 Agent 的启动只支持 Claude Code 或 Codex 启动器")
		}
	}
	if resume != "" {
		if a, ok := n.agent(resume); ok {
			if mode == "plan" && a.Agent == "codex" {
				return nil, readonlyStartUnsupported() // A live thread without a local rollout cannot acquire CLI flags either.
			}
			return n.sessionFor(resume, a), nil // already running
		}
		// A claude still alive on the session (Claude's desktop app, another terminal, suspended there) refuses: two
		// writers would corrupt its transcript. Otherwise a Claude session resumes in the start's host, Orca like herdr: a
		// terminal opened in the session's worktree (#5548).
		if r := n.openElsewhereRefusal(resume); r != nil { // open in Claude's desktop app or a terminal (openelsewhere.go)
			return nil, r
		}
		if _, _, known := n.runtimes.belongs(resume); known { // an Orca native chat and the like: its runtime's, not a terminal's
			return nil, refusal("runtime_session", "这个会话属于另一个工具（例如 Orca 的原生对话），请在那里继续，这次没有启动。", nil)
		}
		if n.startKind(ctx) == proto.EngineOrca && kind != "claude" { // the start's host
			return nil, refusal("orca_resume", "Orca 上只能从手机继续 Claude 会话：其它 Agent 请在电脑上恢复，这次没有启动。", nil)
		}
	}

	// New sessions start in the host the start named, else the default host (herdr-core cannot start them).
	launch, err := hostCapFor[host.Launcher](n, n.startKind(ctx))
	if err != nil {
		return nil, err
	}
	ws := workspaceID
	if ws == "" {
		if ws, err = n.workspaceFor(ctx, launch, cwd); err != nil {
			return nil, err
		}
	}
	if codex && kind != traexAgent { // codex_start.go
		if resume != "" { // a Codex thread is resumed above: this id is not one
			return nil, refusal(codexResumeRefused, "找不到这个 Codex 会话的记录，没法接着它：请在电脑上恢复（codex resume）。", nil)
		}
		if execution.Model != "" {
			argv = append(withoutModelArgs(argv), "--model", execution.Model)
		}
		if execution.Effort != "" {
			return nil, errors.New("新建 Codex 的思考强度由其启动器管理，请将此用途的思考强度恢复继承")
		}
		return n.startCodex(ctx, launch, cwd, launcher, argv, prompt, ws, tabName, mode)
	}
	if kind != "claude" { // start_agent.go
		if mode != "" || effort != "" {
			return nil, fmt.Errorf("permission mode and effort are only supported for Claude starts")
		}
		return n.startOtherAgent(ctx, launch, cwd, launcher, kind, prompt, resume, ws, tabName)
	}
	if err := n.codexFunctionLauncher(ctx, launcher, true); err != nil {
		return nil, err
	}
	id := resume
	if id == "" {
		id = newID
	}
	if id == "" {
		id = newUUID()
	}
	created, err := n.newTab(ctx, launch, cwd, ws, tabName, id) // retire.go
	if err != nil {
		return nil, err
	}
	pane := created.Pane.String()
	if name := n.starts.tabCreated(id, created.SlotID); name != "" { // named while the tab was being made
		_ = renameSlot(ctx, launch, created.SlotID, name)
	}

	// A session whose first start failed (claude never came up) has no transcript to resume: start it under its id.
	args := "--session-id " + id
	if resume != "" && n.transcriptPath(id) != "" {
		args = "--resume " + id
	}
	// Persona's hooks: a plugin, or in --settings for a claude that loads none (hooksplugin.go).
	settings, hooks := n.claudeStartSettings(ctx, launcher, "claude", cwd)
	spec := claudeLaunch{Launcher: launcher, Settings: settings, Hooks: hooks, Display: n.liveRepliesPlugin(hooks), Model: execution.Model,
		Plugins: n.claudeLoadsPlugins(ctx)}
	// herdr told it is Claude (hostherdr.go): a launcher that wraps another binary is otherwise not seen as one. The
	// node's directory in front, the assistant's environment left out (agentenv.go).
	shells := paneShells(ctx)
	cmd := withHerdrAgent(typedHerdrAgent(launch, "claude", shells...), n.claudeCommandLine(spec, args+modeFlags(mode, effort), shellQuote))
	cmd = n.agentCommand(cmd, shellQuote, shells...)
	n.notePaneStart(launcher) // not in the agent sandbox unless a managed launcher (agentsandbox.go)
	st := sessionState{Pane: pane, Launcher: launcher, Cwd: cwd, StartedAt: time.Now().UTC(), Model: execution.Model, ModelFingerprint: execution.Fingerprint}
	if prev, ok := n.state.get(id); ok && resume != "" {
		if !prev.StartedAt.IsZero() { // a branch (fork.go) has none: it starts now
			st.StartedAt = prev.StartedAt
		}
		st.Ultracode = prev.Ultracode
	}
	n.state.put(id, st)
	n.noteModes(id, mode, effort) // shown before the first prompt writes them down
	// Give the fresh shell a moment to load ~/.zshrc; typed input is buffered either way.
	time.Sleep(300 * time.Millisecond)
	n.beginUltracodeProcess(id, resume != "", true, time.Now())
	done, err := n.runAgentCommand(ctx, launch, created, ws, id, cmd, shells...) // typedstart.go
	if err != nil {
		return nil, err
	}
	a, err := n.waitReady(ctx, pane, n.probeFor(ctx, id, launcher, shells))
	done()
	if err != nil {
		screen, _ := n.readPane(ctx, pane, "recent-unwrapped", 30)
		// Close the tab: a claude that comes up after we gave up would sit there unbound, and a retry opens another.
		n.closeNewTab(ctx, launch, created, ws)
		if rpcCode(err) != "" { // the tab's shell said why (shellmiss.go): its tail would only repeat the command
			return nil, err
		}
		return nil, fmt.Errorf("%v\n%s", err, lastLines(screen, 12))
	}
	n.bindPane(pane, id)
	_ = launch.NameAgent(ctx, created.Pane, "j-"+id[:6])
	n.holdResume(ctx, launch, pane, id, spec) // herdr's restore resumes it through the launcher (herdrresume.go)
	// Before the first prompt; never a failed start: Claude is up, and ultracode_note says why it is not on.
	if n.startUltracode(ctx, id, pane, execution.Ultracode) && prompt != "" {
		// The command may still sit in the input box: the first prompt would be typed after it and sent with it. Left
		// to the user as one held back by a dialog is (firstPrompt).
		if err := n.ultracodeBoxClear(ctx, pane); err != nil {
			why := lostUltracode
			if rpcCode(err) == screen.DialogOpen {
				why = lostDialog
			}
			log.Printf("start %s: first message held back after ultracode: %v", id, err)
			n.lostFirst.set(id, why, time.Now())
			prompt = ""
		}
	}
	if prompt != "" {
		// Checked against the transcript and typed again when it was dropped; one that never goes in leaves the
		// session needing the user, with why, not idle (firstprompt.go).
		if err := n.firstPrompt(ctx, pane, id, prompt, a); err != nil {
			return nil, err
		}
	}
	return n.sessionFor(id, *a), nil
}

// checkStart validates a start's arguments and returns the directory (~ expanded, home when empty) and launcher.
func (n *Node) checkStart(cwd, launcher, resume, mode, effort string) (string, string, error) {
	if launcher == "" {
		launcher = "claude"
	}
	if !nameRe.MatchString(launcher) {
		return "", "", fmt.Errorf("invalid launcher %q", launcher)
	}
	if resume != "" && !validID(resume) {
		return "", "", fmt.Errorf("invalid session id %q", resume)
	}
	if err := validModes(mode, effort); err != nil {
		return "", "", err
	}
	cwd = proto.ExpandHome(cwd, n.home)
	if cwd == "" {
		cwd = n.home
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return "", "", fmt.Errorf("directory %s does not exist", cwd)
	}
	return cwd, launcher, nil
}

// tabLabels are the names of the workspace's tabs.
func (n *Node) tabLabels(ctx context.Context, launch host.Launcher, ws string) map[string]bool {
	taken := map[string]bool{}
	h, ok := launch.(host.Host)
	if !ok {
		return taken
	}
	if snap, err := h.Snapshot(ctx); err == nil {
		for _, t := range snap.Slots {
			if t.SpaceID == ws {
				taken[t.Label] = true
			}
		}
	}
	return taken
}

// tabLabel is the name a new tab gets: the given one (one line, at most 32 characters), else agent-<n>; a name the
// workspace already has gets -2, -3….
func tabLabel(name string, taken map[string]bool) string {
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > 32 {
		name = strings.TrimSpace(string(r[:32]))
	}
	if name == "" {
		for i := 1; ; i++ {
			if l := fmt.Sprintf("agent-%d", i); !taken[l] {
				return l
			}
		}
	}
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		if l := fmt.Sprintf("%s-%d", name, i); !taken[l] {
			return l
		}
	}
}

// workspaceFor reuses the workspace named after the project directory (the user keeps one workspace per project).
func (n *Node) workspaceFor(ctx context.Context, launch host.Launcher, cwd string) (string, error) {
	var startErr error
	if _, ok := launch.(*herdrHost); ok {
		startErr = n.ensureHerdr(ctx, "start or resume a session") // herdrstart.go
	}
	wss, err := launch.Spaces(ctx)
	if err != nil {
		// Nothing was started or typed: say so, and what to do, instead of herdr's own words (the app showed
		// "herdr workspace list: exec: \"herdr\": executable file not found").
		name := n.defaultEngineName()
		if k := n.startKind(ctx); proto.EngineNames[k] != "" && k != n.defaultKind() {
			name = proto.EngineNames[k] // a start that named its host
		}
		msg := fmt.Sprintf("这台电脑上的 %s 没在运行（或没装），不能在这里新开或接着会话，这次没有发送。"+
			"要从手机接着电脑上开着的会话，在那边继续；要从手机新开，先在这台电脑上装好并打开 %s。", name, name)
		if startErr != nil && !errors.Is(startErr, errHerdrNotInstalled) {
			msg = fmt.Sprintf("这台电脑上的 %s 没在运行，自动启动也没成功，不能在这里新开或接着会话，这次没有发送。"+
				"请在这台电脑上打开 %s 再试；原因见详情。", name, name)
		}
		return "", refusal(engineMissing, msg,
			map[string]any{"detail": herdrMissingDetail(err, startErr)})
	}
	base := filepath.Base(cwd)
	for _, w := range wss {
		if w.Label == base {
			return w.ID, nil
		}
	}
	return "", nil
}

func (n *Node) sessionFor(id string, a host.Agent) *proto.Session {
	s := &proto.Session{
		SessionID: id, Cwd: a.Cwd, Status: a.AgentStatus, PaneID: a.PaneID, TabID: a.TabID,
		WorkspaceID: a.WorkspaceID, AgentName: a.Name, Title: a.Title, Agent: a.Agent, UpdatedAt: time.Now().UTC(), // just started
	}
	_ = n.refreshHosts(context.Background())
	n.placeIn(s, a)
	n.decorate(s)
	s.PermissionMode, s.Effort = n.modesOf(id)
	if why := n.lostFirstPrompt(id, s.Status); why != "" { // firstprompt.go
		s.Status, s.AttentionReason = proto.StatusBlocked, why
	}
	return s
}

// close quits any Claude running in the machine's engine (started by Persona or by hand) and closes its pane: herdr
// closes its tab, or in a split tab only its own pane, so the other panes survive; herdr-core ends its terminal.
func (n *Node) close(ctx context.Context, id string) error {
	a, ok := n.agent(id)
	if !ok {
		if _, mine := n.state.get(id); mine {
			return nil // already gone
		}
		return fmt.Errorf("这个 Agent 不在这台电脑的终端里运行，没法从手机关闭")
	}
	org, ref, err := paneCap[host.Organizer](n, a.PaneID) // a host that cannot close it is not interrupted first
	if err != nil {
		return err
	}
	_ = n.sendKeys(ctx, a.PaneID, "ctrl+c")
	time.Sleep(200 * time.Millisecond)
	_ = n.sendKeys(ctx, a.PaneID, "ctrl+c")
	time.Sleep(500 * time.Millisecond)
	return org.Close(ctx, ref, a.WorkspaceID, a.TabID)
}

// exitCommand: the message is Claude's or Codex's own command to quit (/exit, /quit).
func exitCommand(text string) bool {
	switch strings.TrimSpace(text) {
	case "/exit", "/quit":
		return true
	}
	return false
}

// exitAgent is a message that only says to quit: typed in, the agent would exit and leave its tab holding a bare
// shell, and one already closed would be resumed in a new tab just to quit there. A running agent is closed as
// session.close does (its tab goes with it); one not running is left as it is.
func (n *Node) exitAgent(ctx context.Context, id string) error {
	if _, ok := n.agent(id); !ok {
		return nil
	}
	return n.close(ctx, id)
}
