package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// pi (docs/integrations/pi.md). A pi in a herdr pane is a herdr session, as a Claude there is: the pane, its space and tab, the
// terminal and the keys are herdr's. Its conversation and its control (send, interrupt, cards, live text) come from
// the Persona extension in that same pi process, a runtime (runtimes.go) that lists the session with the pane it runs
// in; the node joins the two into one row. pi run elsewhere — pi-web, a terminal outside herdr — is only the runtime's.
// A pi session that ran in herdr and has stopped is resumed in herdr (`pi --session`, the runtime's resume_argv),
// never in pi-web.

const piAgent = "pi"

// piResumeWait is how long a resume in the engine waits for the pi it started to list the session (its extension).
var piResumeWait = 30 * time.Second

var paneIDRe = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// validPane: a pane id a runtime may name (herdr's "w1:p1", a named session's "@<session>:w1:p1": herdr.QualifyID),
// nothing that could mean something else.
func validPane(p string) bool {
	_, bare := herdr.SplitID(p)
	return paneIDRe.MatchString(bare)
}

// piSession is the session the pi in pane a runs: what herdr's pi integration reported (its session file), else what
// the extension in it said (the pane in its list), else the pane a resume of ours started it in.
func (n *Node) piSession(a host.Agent) string {
	if id := a.SessionID(); id != "" && runtimeIDRe.MatchString(id) {
		return id
	}
	if id := n.runtimes.sessionInPane(a.PaneID, n.ownPane); id != "" {
		return id
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.panes[a.PaneID]
}

// mergePiRow completes a herdr pi row with what the extension serving it lists: the title, the last thing said, the
// model, a revision that moves with the conversation, and "needs you" while it waits for the phone. Without one the
// row says what is missing.
func (n *Node) mergePiRow(s *proto.Session) {
	r, _, ok := n.runtimes.rowOf(s.SessionID)
	if !ok {
		s.ReadOnlyReason = piNoExtension
		return
	}
	if r.Title != "" {
		s.Title = r.Title
	}
	s.LastText, s.Model, s.UpdatedAt, s.Rev = r.LastText, r.Model, r.UpdatedAt, r.Rev
	if r.Status == proto.StatusBlocked {
		s.Status = proto.StatusBlocked
	}
}

const piNoExtension = "这个 pi 没有装 Persona 的扩展：手机上只能看终端。装上（pi install <Persona>/integrations/pi）再重开 pi，就能看对话、发消息"

// resumeInEngine resumes a session its runtime lists offline with a resume_argv (a pi that ran in herdr) in a new tab
// of the engine, waits for the pi it starts to list the session, and sends the phone's message there. Without an engine
// that can start it: refused, nothing sent (it is not handed to pi-web).
func (n *Node) resumeInEngine(ctx context.Context, h *runtimeConn, id string, argv []string, params json.RawMessage) (any, error) {
	if n.defaultKind() == proto.EngineOrca { // it would start in the default host: Orca cannot reopen one
		return nil, refusal("orca_resume", "Orca 会话请在电脑上恢复：这次没有启动，也没有发送。", nil)
	}
	row, _ := h.row(id)
	cwd := row.Cwd
	if st, err := os.Stat(cwd); cwd == "" || err != nil || !st.IsDir() {
		return nil, refusal("not_running", "这个会话的目录不在了，没法在终端里接着它，这次没有发送。", nil)
	}
	launch, err := hostCap[host.Launcher](n)
	if err != nil {
		name := n.defaultEngineName()
		return nil, refusal(engineMissing, fmt.Sprintf("这个会话是在 %s 里跑的，要在 %s 里接着它，可这台电脑上的 %s 没在运行（或没装），这次没有发送。", name, name, name), nil)
	}
	ws, err := n.workspaceFor(ctx, launch, cwd)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(n.dir, "tab-start-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "start.json")
	b, _ := json.Marshal(codexStartFile{Argv: argv})
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return nil, err
	}
	var created *host.Created
	if ws == "" {
		if created, err = launch.CreateSpace(ctx, cwd, filepath.Base(cwd)); err == nil && created.SlotID != "" {
			_ = renameSlot(ctx, launch, created.SlotID, tabLabel("", nil))
		}
	} else {
		created, err = launch.CreateSlot(ctx, ws, cwd, tabLabel("", n.tabLabels(ctx, launch, ws)))
	}
	if err != nil {
		return nil, fmt.Errorf("创建终端标签页失败：%w", err)
	}
	pane := created.Pane.String()
	n.bindPane(pane, id)               // the pane is this session's before herdr or the extension says so
	time.Sleep(300 * time.Millisecond) // the fresh shell loads ~/.zshrc; typed input is buffered either way
	if err := launch.Run(ctx, created.Pane, n.agentCommand(shellQuote(n.bin)+" tab-start "+shellQuote(file), shellQuote, paneShells(ctx)...)); err != nil {
		return nil, fmt.Errorf("run %s: %w", argv[0], err)
	}
	deadline := time.Now().Add(piResumeWait)
	for {
		if r, owner, ok := n.runtimes.rowOf(id); ok && owner != h && r.Status != proto.StatusOffline {
			_ = n.refreshHosts(ctx)
			res, err, _ := n.runtimeHandle(ctx, owner, "session.send", params)
			return res, err
		}
		if time.Now().After(deadline) {
			screen, _ := n.readPane(ctx, pane, "recent-unwrapped", 30)
			return nil, refusal("not_running", fmt.Sprintf("在新标签里接着这个会话，%s 秒内没等到它起来，这次没有发送。\n%s",
				fmt.Sprint(int(piResumeWait.Seconds())), lastLines(screen, 12)), nil)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
