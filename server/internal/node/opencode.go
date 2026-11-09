package node

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// opencode (docs/integrations/opencode.md). An opencode in a herdr pane is a herdr session, as a pi there is (pi.go): the pane, its
// space and tab, the terminal and the keys are herdr's, and so are the state and the session id (herdr's opencode
// plugin reports both from the TUI). Its conversation and its control (send, interrupt, cards, live text) come from
// the Persona plugin in the opencode server process, a runtime (runtimes.go) that lists the session by the same id;
// the node joins the two rows into one. Without the plugin the row is terminal-only and says what is missing.
//
// Every other agent herdr reports that Persona has no reader for (otherAgent) is listed the same way, with nothing
// but its terminal.

const opencodeAgent = "opencode"

// opencodeTitlePrefix is what herdr's opencode plugin puts before the session title in the terminal title.
const opencodeTitlePrefix = "OC |"

// opencodeTitle is the session title in herdr's terminal title ("OC | <title>"), "" when there is none yet.
func opencodeTitle(terminalTitle string) string {
	t := strings.TrimSpace(terminalTitle)
	if len(t) >= len(opencodeTitlePrefix) && strings.EqualFold(t[:len(opencodeTitlePrefix)], opencodeTitlePrefix) {
		t = strings.TrimSpace(t[len(opencodeTitlePrefix):])
	}
	if strings.EqualFold(t, "opencode") || strings.EqualFold(t, "OC") {
		return "" // the TUI's own name before a title is generated
	}
	return t
}

// opencodeSession is the session the opencode in pane a runs: what herdr's plugin reported (the opencode session id),
// else what the plugin in the server said (the pane in its list, when it knew it).
func (n *Node) opencodeSession(a host.Agent) string {
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

// mergeOpencodeRow completes a herdr opencode row with what the plugin lists for the same session: the full title
// (herdr's is cut), the last thing said, the model, the context use, a revision that moves with the conversation,
// and "needs you" while a card waits on the phone. Without a plugin the row says what is missing.
func (n *Node) mergeOpencodeRow(s *proto.Session) {
	r, _, ok := n.runtimes.rowOf(s.SessionID)
	if !ok {
		s.ReadOnlyReason = opencodeNoPlugin
		return
	}
	if r.Title != "" {
		s.Title = r.Title
	}
	s.LastText, s.Model, s.UpdatedAt, s.Rev = r.LastText, r.Model, r.UpdatedAt, r.Rev
	s.ContextTokens, s.ContextWindow = r.ContextTokens, r.ContextWindow
	if r.Status == proto.StatusBlocked {
		s.Status = proto.StatusBlocked
	}
}

const opencodeNoPlugin = "这个 opencode 没有装 Persona 的插件：手机上只能看终端。把插件装进 opencode（见 docs/integrations/opencode.md）并重启 opencode 服务，就能看对话、发消息"

// otherNoReader is what a pane of an agent Persona cannot read says.
func otherNoReader(agent string) string {
	return fmt.Sprintf("Persona 还不能读 %s 的对话：手机上只能看终端、按键盘", agent)
}

// otherAgent: an agent herdr reports that no branch of setAgents reads (gemini, amp…): listed with its terminal only.
func otherAgent(a host.Agent) bool {
	switch a.Agent {
	case "", "claude", "codex", traexAgent, piAgent, opencodeAgent:
		return false
	}
	return host.ParsePane(a.PaneID).Kind != host.Botmux
}

// otherSession is the id of the terminal-only row of the pane a runs: the agent and its pane, which is what the row
// is (the agent's own session id is not one Persona can use).
func otherSession(a host.Agent) string {
	var b strings.Builder
	for _, r := range strings.ToLower(a.Agent + "-" + a.PaneID) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	id := b.String()
	if len(id) > 128 {
		id = id[:128]
	}
	return strings.TrimLeft(id, "-")
}

// terminalOnly: the agent of session id has no reader here (a pi or opencode without its extension, or another agent);
// ok false for sessions that have one.
func (n *Node) terminalOnly(id string) (reader terminalReader, ok bool) {
	a, found := n.agent(id)
	if !found {
		return terminalReader{}, false
	}
	switch {
	case a.Agent == piAgent:
		return terminalReader{kind: piAgent, notice: piNoExtension, code: "pi_no_extension"}, true
	case a.Agent == opencodeAgent:
		return terminalReader{kind: opencodeAgent, notice: opencodeNoPlugin, code: "opencode_no_extension"}, true
	case otherAgent(a):
		return terminalReader{kind: a.Agent, notice: otherNoReader(a.Agent), code: "terminal_only"}, true
	}
	return terminalReader{}, false
}

// terminalReader is what a terminal-only pane answers with: the agent, the notice and the code a refused send carries.
type terminalReader struct{ kind, notice, code string }

// terminalOnlyCalls answers the calls only a reader of the agent's conversation could, for a pane that has none: an
// empty conversation saying so, no sending (typing into a TUI blind is not safe), Esc for interrupt. Terminal calls
// are herdr's as usual (handled false).
func (n *Node) terminalOnlyCalls(ctx context.Context, method, id string, r terminalReader) (any, error, bool) {
	switch method {
	case "session.items":
		return map[string]any{"items": []proto.Item{{ID: r.kind + "#0", Seq: 1, Role: "system", Kind: "notice", Text: r.notice,
			TS: time.Now().UTC()}}, "has_more": false, "history": r.kind + "-pane:" + id, "to_seq": 1}, nil, true
	case "session.item", "session.image":
		return nil, fmt.Errorf("no item"), true
	case "session.watch", "session.unwatch":
		return okResult(nil), nil, true
	case "session.send":
		return nil, refusal(r.code, r.notice+"。这次没有发送。", nil), true
	case "session.interrupt":
		a, _ := n.agent(id)
		return okResult(n.sendKeys(ctx, a.PaneID, "escape")), nil, true
	case "session.modes", "session.queue.edit", "session.queue.send_now":
		return nil, refusal("unsupported", r.kind+" 没有这个操作", nil), true
	}
	return nil, nil, false
}
