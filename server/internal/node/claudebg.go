package node

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Claude sessions that run where Persona has no terminal pane: a background session Claude's own daemon runs (/fork,
// /background: kind "bg"), a session open in Claude's desktop app (entrypoint claude-desktop…), claude in a terminal
// that herdr, Orca or botmux do not run (entrypoint cli). Each writes sessions/<pid>.json in its config directory with
// the session it has open, its status (busy / idle / waiting) and, since Claude Code 2.1.29x, its peer inbox
// (claudepeer.go). They listed as offline while they worked, and a message from the phone was refused (a second
// claude --resume would write the same transcript, openelsewhere.go). The node lists every live one as running, with
// its status from the file and its transcript as for any session; a message goes through its inbox when the node can
// use it (send) and is refused, with why, when it cannot — never by starting a second claude.
//
// What the phone cannot do there: answer a question or a permission prompt (those are on the computer: no hooks of
// Persona's run in such a claude, and its screen is not Persona's to read), stop a turn (nothing to send Esc to), or
// open a terminal. A claude in a pane of Persona's engines lists there as before (its pane comes first: seen). One
// whose pane variable names a pane no engine in use lists with a claude in it (Orca's terminals on a node without
// Orca, a nested tmux, a claude the engine's process scan missed) lists here as running, after a short grace that
// lets an engine report a claude just started in its pane first (claudeInPane).
//
// An interactive claude that sent its own session to the background keeps that session in its sessions/<pid>.json
// with parkedJobId set, and shows Claude's agents view in its pane until it comes back. herdr still names the pane
// after the session its SessionStart hook reported and reads its state off the terminal title — a background
// session's — so the parked session looked busy with another one's work, and a message to it went into the agents
// view. As in Claude Code (a parked claude does not hold its session), such a pane is not taken for that session.
//
// The agents view opens the job the session went to (Claude's autoOpenJobId), and the pane then is that job's: its
// conversation, its prompt, its name as the terminal title. Such a pane is the background session's (the job's
// sessions/<pid>.json has jobId = the parkedJobId, and its name is the title), so it lists in its herdr tab and takes
// what is typed (2026-11-04: tab 4 of a herdr workspace was missing on the phone, its session listed apart, read-only).
//
// On the agents list (the job ended or stopped, or another one is picked) the pane is still the parked session's tab:
// it lists there, idle (herdr's state is a background session's title), with its terminal: the phone picks a session
// from the list as on the computer (arrows, enter), and the pane is then that one's. A message is not typed into it —
// the list's prompt starts a new background session. Before, such a pane listed nowhere (2026-11-05: tabs 2 and 4
// missing on the phone after their jobs ended).

const sourceClaudeBg = proto.SourceClaudeBg

const claudeBgReadOnly = proto.ReadOnlyClaudeBg

const claudeAgentsList = "这个标签停在 Claude Code 的 agents 列表上：打开终端，用方向键选中一个会话、回车进去后再发消息"

// claudeBgStatus maps a claude's sessions/<pid>.json status to the node's: waiting is a question or dialog of its
// own, answered on the computer. Anything else is unknown.
var claudeBgStatus = map[string]string{"busy": proto.StatusWorking, "idle": proto.StatusIdle, "waiting": proto.StatusBlocked}

// parkedClaudeSessions are the sessions only parked claudes have open, each with the background job it went to.
func parkedClaudeSessions(procs []claudeProc) map[string]parkedJob {
	parked, held := map[string]parkedJob{}, map[string]bool{}
	jobs := map[string]claudeProc{}
	for _, c := range procs {
		if c.Kind == "bg" && c.JobID != "" {
			jobs[c.JobID] = c
		}
	}
	for _, c := range procs {
		if c.ParkedJobID != "" {
			j := jobs[c.ParkedJobID]
			parked[c.SessionID] = parkedJob{SessionID: j.SessionID, Name: j.Name}
		} else {
			held[c.SessionID] = true
		}
	}
	for id := range held {
		delete(parked, id)
	}
	return parked
}

// parkedJob is the live background session a parked session went to: its id and name ("" when it is not running).
type parkedJob struct{ SessionID, Name string }

// shownIn is the job's session when the pane shows it (its name is the pane's title), else "".
func (j parkedJob) shownIn(a host.Agent) string {
	if j.SessionID == "" || !validID(j.SessionID) || j.Name == "" || a.Title != j.Name {
		return ""
	}
	return j.SessionID
}

// onAgentsList is a parked session's pane on the agents list, idle: the state herdr reads off the title is not its.
func onAgentsList(a host.Agent) host.Agent {
	a.AgentStatus = proto.StatusIdle
	return a
}

// agentsListRefusal refuses a message to a parked session whose pane is on the agents list, nil for any other.
func (n *Node) agentsListRefusal(id string) error {
	n.mu.Lock()
	listed := n.onAgentsList[id]
	n.mu.Unlock()
	if !listed {
		return nil
	}
	return refusal(openElsewhere, claudeAgentsList+"。这次没有发送。", nil)
}

// outsideSource is the source a live claude outside Persona's panes lists under, "" for one the node leaves alone: a
// warm background process no session has claimed, a parked one (its session is the job's now), one that runs from an
// SDK or another program (entrypoint sdk-…: its own host drives it), one inside a terminal of Persona's engines.
func (n *Node) outsideSource(c claudeProc) string {
	switch {
	case c.Spare || c.ParkedJobID != "" || !validID(c.SessionID):
		return ""
	case c.Kind == "bg":
		return sourceClaudeBg
	case c.Kind != "" && c.Kind != "interactive":
		return ""
	case desktopEntrypoint(c.Entrypoint):
		return proto.SourceClaudeDesktop
	case c.Entrypoint == "cli" && !n.claudeInPane(c):
		return proto.SourceClaudeTerminal
	}
	return ""
}

// claudeSessions lists the live claudes outside Persona's panes that are not in seen and have a transcript: the
// background ones, the desktop app's, the ones in other terminals.
func (n *Node) claudeSessions(procs []claudeProc, seen map[string]bool) []proto.Session {
	var out []proto.Session
	listed := map[string]bool{}
	for _, c := range procs {
		id := c.SessionID
		if seen[id] || listed[id] {
			continue // a pane lists it (and a cli claude's environment is not read for nothing)
		}
		source := n.outsideSource(c)
		if source == "" {
			continue
		}
		p := n.transcriptPath(id)
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		listed[id] = true
		m := n.meta(p, st)
		status, ok := claudeBgStatus[c.Status]
		if !ok {
			status = proto.StatusUnknown
		}
		s := proto.Session{
			SessionID: id, Cwd: c.Cwd, Status: status, Agent: "claude", Title: m.title, SourceKind: source,
			Model: m.model, LastText: snippet(m.lastText), UpdatedAt: lastUsed(m, st), Rev: st.Size(), AgentSurface: m.surface,
		}
		s.Capabilities, s.ReadOnlyReason = outsideCapabilities(c)
		if status == proto.StatusBlocked {
			s.AttentionReason = proto.AttentionClaudeComputer
		}
		if s.Cwd == "" {
			s.Cwd = m.cwd
		}
		if s.Title == "" {
			s.Title = c.Name
		}
		untitled(&s, m)
		setContext(&s, m)
		s.ModelFallback = m.modelFallback
		s.Activity, s.ActivityAt = m.activityOf()
		s.PermissionMode, s.Effort = n.currentModes(id, m)
		m.turnErr.set(&s)
		if status == proto.StatusWorking || status == proto.StatusBlocked {
			s.Queued = m.queue
		}
		s.WokeByTask = m.wake
		n.setBackground(&s, m) // as for a pane's session
		s.BackgroundWaiting = n.backgroundWaits(&s)
		s.Loop, s.Goal, s.Plan = m.loop, m.goal, m.plan
		s.Subagents = n.subagents(id)
		n.decorate(&s)
		out = append(out, s)
	}
	return out
}

// outsideCapabilities: read always; send when its peer inbox is one the node can use, with a reason when not.
func outsideCapabilities(c claudeProc) (*proto.SessionCapabilities, string) {
	p, has := peerOf(c)
	switch {
	case has && peerReachable(p):
		return &proto.SessionCapabilities{Read: true, Send: true}, ""
	case c.Kind == "bg":
		return &proto.SessionCapabilities{Read: true}, claudeBgReadOnly
	case !has:
		return &proto.SessionCapabilities{Read: true}, proto.ReadOnlyClaudeNoInbox
	}
	return &proto.SessionCapabilities{Read: true}, proto.ReadOnlyClaudeInbox
}

// outsideClaude is the live claude outside Persona's panes that has session id open (outsideSource), ok false when
// none has.
func (n *Node) outsideClaude(id string) (claudeProc, bool) {
	if !validID(id) {
		return claudeProc{}, false
	}
	for _, c := range n.liveClaudes() {
		if c.SessionID == id && n.outsideSource(c) != "" {
			return c, true
		}
	}
	return claudeProc{}, false
}

// sendOutside sends text to a session a claude outside Persona's panes has open, through its peer inbox; handled
// false when no such claude has it, or it has no inbox (an older Claude Code): the caller goes on as before, and
// start refuses it as open elsewhere (openElsewhereRefusal). Nothing else is tried: a second claude --resume would
// write the same transcript. A send counts once the session wrote that it took the message (peerTaken); one it did
// not take in time — held for the person at the computer, dropped as a duplicate — is reported as not confirmed
// (delivered unknown), so the app neither shows it as sent nor resends it by itself.
func (n *Node) sendOutside(ctx context.Context, id, text string, images []proto.Image) (handled bool, err error) {
	c, ok := n.outsideClaude(id)
	if !ok {
		return false, nil
	}
	p, has := peerOf(c)
	if !has {
		return false, nil
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		// The inbox runs no commands (Claude Code takes a peer's message with skipSlashCommands): /exit, /model or
		// /clear would reach the model as words. Refused, so nothing looks done that was not.
		return true, refusal(peerCommand, peerCommandMsg, nil)
	}
	if len(images) > 0 {
		paths, err := n.saveImages(images)
		if err != nil {
			return true, err
		}
		text = attachImages(text, paths)
	}
	var offset int64
	if st, err := os.Stat(n.transcriptPath(id)); err == nil {
		offset = st.Size()
	}
	err = peerSend(ctx, p, id, text)
	var pe *errPeer
	if asPeer(err, &pe) {
		log.Printf("send %s: claude %d's inbox refused: %v", id, c.pid, err)
		return true, refusal(pe.code, pe.msg, map[string]any{"pid": c.pid})
	}
	if peerTaken(ctx, func() string { return n.transcriptPath(id) }, offset, text, peerConfirmWait) {
		log.Printf("send %s: claude %d (%s) took it through its inbox", id, c.pid, c.Entrypoint)
		return true, nil
	}
	if err != nil {
		log.Printf("send %s: claude %d's inbox: %v", id, c.pid, err)
		return true, uncertain(err)
	}
	log.Printf("send %s: written to claude %d's inbox, not taken within %s (held or dropped there)", id, c.pid, peerConfirmWait)
	return true, unconfirmed()
}

// unconfirmed is the outcome of a message the inbox read but the session has not taken: delivery unknown.
func unconfirmed() error {
	b, _ := json.Marshal(map[string]any{"delivered": deliveredUnknown})
	return &rpc.Error{Code: peerUnconfirmed, Message: peerUnconfirmedMsg, Data: b}
}

// asPeer: err is a refusal of the inbox's (*errPeer).
func asPeer(err error, target **errPeer) bool { return errors.As(err, target) }

// claudeInPane: the claude process runs in a pane that one of the engines in use lists with a claude in it, so that
// engine lists it, not this. A pane variable alone is not enough: an engine the machine does not enable (Orca's
// terminals while the node uses herdr alone), or one whose process scan missed the claude (a nested pty, a launcher
// that renamed it), lists nothing there, and a running claude must not fall through to the offline list. A claude
// the node first saw less than outsidePaneGrace ago still counts as its pane's: an engine reports a claude typed into
// its pane a moment after it starts, and until then it should not flip to an outside row and back.
func (n *Node) claudeInPane(c claudeProc) bool {
	if paneEnvOfPID == nil {
		return false
	}
	refs := n.enginePaneRefs(paneEnvOfPID(c.pid))
	if len(refs) == 0 {
		return false
	}
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, agents := range n.hostAgents {
		for _, a := range agents {
			if a.Agent == "claude" && slices.Contains(refs, a.PaneID) {
				return true
			}
		}
	}
	if n.paneClaudes == nil {
		n.paneClaudes = map[int]paneClaudeSeen{}
	}
	for pid, e := range n.paneClaudes { // gone (the list is rebuilt every second): forget it
		if now.Sub(e.last) > paneClaudeForget {
			delete(n.paneClaudes, pid)
		}
	}
	e, ok := n.paneClaudes[c.pid]
	if !ok || e.start != c.ProcStart { // first seen, or the pid now belongs to another process
		e = paneClaudeSeen{start: c.ProcStart, first: now}
	}
	e.last = now
	n.paneClaudes[c.pid] = e
	return now.Sub(e.first) < outsidePaneGrace
}

// paneClaudeSeen is when claudeInPane first and last saw a claude process (start: its procStart, telling a reused pid
// apart).
type paneClaudeSeen struct {
	start       string
	first, last time.Time
}

// outsidePaneGrace: how long a claude in an engine pane that no engine lists yet is left to its engine (a var: tests
// set it to 0). paneClaudeForget: an entry not looked at for this long is dropped.
var outsidePaneGrace = 5 * time.Second

const paneClaudeForget = time.Minute

// enginePaneRefs are the panes a process's engine pane variables (enginePaneEnvs) name, as the hosts write PaneIDs.
func (n *Node) enginePaneRefs(env map[string]string) []string {
	var out []string
	add := func(kind, id string) {
		if id != "" {
			out = append(out, host.PaneRef{Kind: kind, ID: id}.String())
		}
	}
	if p := n.envPane(func(name string) string { return env[name] }); p != "" {
		out = append(out, p) // herdr's pane ids are stored as they are, a named session's qualified (#2196)
	}
	add(host.HerdrCore, env[herdrCorePaneEnv])
	add(host.Orca, env[orcaPaneEnv])
	add(host.Orca, env[orcaLegacyPaneEnv])
	add(host.Botmux, env[botmuxPaneEnv])
	return out
}

// enginePaneEnvs are the variables Persona's engines set in their panes' processes.
// herdrSessionEnv and herdrSocketEnv come along to tell which herdr server's pane HERDR_PANE_ID is.
var enginePaneEnvs = []string{herdrPaneEnv, herdrSessionEnv, herdrSocketEnv, herdrCorePaneEnv, orcaPaneEnv, orcaLegacyPaneEnv,
	botmuxPaneEnv}
