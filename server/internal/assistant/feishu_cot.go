package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// New turns show in the progress card (feishu_turn.go): an agent message can show neither the elapsed time nor what runs
// now, and only came once a tool call did. A run that already has one (made before this version) still shows a turn
// as an agent message (feishubot's message_cot): its process (thinking, what the conversation says between its tool
// calls, the calls and their output) added as it goes, finished when the turn ends; the answer then goes out as a
// card of its own below it. When an append fails, the run goes on in its progress card.
//
// The run's Ext keeps "cot" and "cot_msg" (the agent message's ids, shared by its events as the run id) and
// "cot_para" (the last paragraph, which the next tool calls hang under and the process ends with); "cot_off" marks a
// run whose agent message could not be used.

// cotItemsMost bounds what a run keeps of its turn's process.
const cotItemsMost = 2000

// cotResultMost is how much of a tool's output the agent message shows.
const cotResultMost = 1500

// cotProgress is how far a run's agent message got, in memory (under its chanLive's mu).
type cotProgress struct {
	sent int // items of chanLive.items turned into events
	// words are what came since the conversation's last tool call, from its first words on: the words are the process
	// only once a call follows (until then they may be the answer), and thinking after them waits with them, so the
	// process keeps the conversation's order.
	words []proto.Item
	calls map[string]bool // tool calls shown and not settled yet (their output goes under them); Ext "cot_calls"
	// queue is what was made of the items already taken and has not reached Feishu yet (sent 50 at a time, the rest
	// next time): an append is never made twice.
	queue []feishubot.CotEvent
	// The agent message as this run has it (the answer works on a copy of the run, and may be tried again).
	cot, msg, para string
	done, off      bool
}

// cotChunk is how many events one append carries (the open platform's limit).
const cotChunk = 50

// load fills p from the run's Ext (a run the assistant took up again after a restart).
func (s *Service) loadCot(p *cotProgress, r *channel.Run) {
	if p.cot == "" {
		p.cot, p.msg, p.para = s.runExt(r, "cot"), s.runExt(r, "cot_msg"), s.runExt(r, "cot_para")
		p.done = p.done || s.runExt(r, "cot_done") != ""
		if p.calls == nil && p.cot != "" {
			for _, id := range strings.Split(s.runExt(r, "cot_calls"), ",") {
				if id != "" {
					if p.calls == nil {
						p.calls = map[string]bool{}
					}
					p.calls[id] = true
				}
			}
		}
	}
	// A run answering in a topic has none: the hub makes the agent message in the owner's private chat only.
	p.off = p.off || s.runExt(r, "cot_off") != "" || s.runExt(r, "chat") != ""
}

// runExt is one of a run's channel values, read under the channels' lock (a held run's next turn clears them).
func (s *Service) runExt(r *channel.Run, key string) string {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	return r.Ext[key]
}

// setCotExt keeps one of the agent message's values on the run itself: the answer works on a copy of it, and a value
// only the copy had would be gone when the answer is tried again after a restart.
func (s *Service) setCotExt(r *channel.Run, key, value string) {
	if real, _ := s.channelRunByID(r.ID); real != nil && real != r {
		s.setRunExt(real, key, value)
	}
	s.setRunExt(r, key, value)
}

// cotItem: it is part of a turn's process.
func cotItem(it proto.Item) bool {
	switch it.Kind {
	case proto.KindThinking, proto.KindText:
		return it.Role == "assistant" && strings.TrimSpace(it.Text) != ""
	case proto.KindToolUse, proto.KindToolResult:
		return true
	}
	return false
}

// progressAfter: the progress card is there within a second or two of the message, before the turn is seen working.
func (f feishuSink) progressAfter(*channel.Run) time.Duration {
	return feishuProgressAfter
}

// cotCall is a call of the hub's Feishu transport for agent messages.
func (s *Service) cotCall(ctx context.Context, method string, params map[string]any) error {
	return s.appCall(ctx, AppFeishu, method, params, nil)
}

// cotEvents turns the new items of a run's process into events (words held back until a tool call follows them: the
// words a turn ends with are its answer, which goes out on its own). para is the last paragraph so far.
// finish: the turn ended, and the thinking held with its last words goes in (the words are its answer).
func cotEvents(l i18n.Lang, p *cotProgress, items []proto.Item, para string, start func(first string) []feishubot.CotEvent, finish bool) ([]feishubot.CotEvent, string) {
	var out []feishubot.CotEvent
	paragraph := func(id, text string) {
		if para == "" {
			out = append(out, start(id)...)
		}
		out = append(out, feishubot.CotParagraph(id, text)...)
		para = id
	}
	// p changes only once the events are taken (an append that did not reach Feishu is made again from the same items).
	words := slices.Clone(p.words)
	calls := maps.Clone(p.calls)
	if calls == nil {
		calls = map[string]bool{}
	}
	defer func() { p.words, p.calls = words, calls }()
	for _, it := range items {
		switch it.Kind {
		case proto.KindThinking:
			if len(words) > 0 {
				words = append(words, it) // after words that may be the answer: shown with them, in order
				continue
			}
			paragraph("p-"+it.ID, strings.TrimSpace(it.Text))
		case proto.KindText:
			words = append(words, it)
		case proto.KindToolUse:
			for _, w := range words {
				paragraph("p-"+w.ID, strings.TrimSpace(w.Text))
			}
			words = nil
			if para == "" { // a turn that starts with a tool call: a paragraph for its calls to hang under
				paragraph("p-"+it.ID, i18n.T(l, "assistant-feishu.cot_tools_paragraph"))
			}
			id := it.ToolUseID
			if id == "" {
				id = it.ID
			}
			verb, detail := botStepParts(l, it)
			title := verb
			if detail != "" {
				title += " · " + detail
			}
			out = append(out, feishubot.CotToolCall(id, para, it.ToolName, title, cotIcon(it.ToolName))...)
			calls[id] = true
		case proto.KindToolResult:
			if !calls[it.ToolUseID] {
				continue // its call was before this run saw the conversation
			}
			delete(calls, it.ToolUseID)
			text := strings.TrimSpace(it.Text)
			if text == "" {
				out = append(out, feishubot.CotToolResult(it.ToolUseID, i18n.T(l, "assistant-feishu.cot_tool_done"), false))
				continue
			}
			if r := []rune(text); len(r) > cotResultMost {
				text = string(r[:cotResultMost]) + "\n…"
			}
			out = append(out, feishubot.CotToolResult(it.ToolUseID, text, true))
		}
	}
	if finish {
		for _, w := range words {
			if w.Kind == proto.KindThinking {
				paragraph("p-"+w.ID, strings.TrimSpace(w.Text))
			}
		}
		words = nil
	}
	return out, para
}

// cotIcon is the agent message's icon for a tool.
func cotIcon(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "bash") || strings.Contains(n, "shell") || strings.Contains(n, "command"):
		return "bash"
	case strings.Contains(n, "write") || strings.Contains(n, "edit") || strings.Contains(n, "patch"):
		return "write"
	case strings.Contains(n, "read") || strings.Contains(n, "notebook"):
		return "read"
	case strings.Contains(n, "grep") || strings.Contains(n, "glob") || strings.Contains(n, "search") || strings.Contains(n, "fetch"):
		return "search"
	case strings.Contains(n, "task") || strings.Contains(n, "todo") || strings.Contains(n, "agent"):
		return "task"
	}
	return "default"
}

// cotPush adds r's new process to its agent message (finish: and ends it). False when the message can't be used (the
// run is marked: its card takes over).
func (f feishuSink) cotPush(ctx context.Context, r *channel.Run, p *chanLive, items []proto.Item, finish bool) bool {
	s := f.s
	l := s.botLang(ctx)
	cot, msg := p.cot.cot, p.cot.msg
	start := func(first string) []feishubot.CotEvent { return feishubot.CotStart(r.SessionID, cot, first) }
	if p.cot.sent > len(items) {
		p.cot.sent = len(items)
	}
	next := p.cot
	events, para := cotEvents(l, &next, items[p.cot.sent:], p.cot.para, start, finish)
	events = append(slices.Clone(p.cot.queue), events...)
	if finish {
		for _, id := range slices.Sorted(maps.Keys(next.calls)) { // calls whose output never came: settled, not left spinning
			events = append(events, feishubot.CotToolResult(id, i18n.T(l, "assistant-feishu.cot_tool_done"), false))
		}
		next.calls = nil
		if para == "" { // nothing of the process: an empty one would not open
			para = "p-" + r.ID + "-done"
			events = append(events, start(para)...)
			events = append(events, feishubot.CotParagraph(para, i18n.T(l, "assistant-feishu.cot_done"))...)
		}
		events = append(events, feishubot.CotFinish(r.SessionID, cot, para)...)
	}
	// The items are taken now; what of them does not reach Feishu waits in the queue.
	p.cot.words, p.cot.sent = next.words, len(items)
	if !maps.Equal(next.calls, p.cot.calls) && !finish { // at the end: kept until the end got through
		p.cot.calls = next.calls
		s.setCotExt(r, "cot_calls", strings.Join(slices.Sorted(maps.Keys(next.calls)), ","))
	}
	if para != p.cot.para {
		p.cot.para = para
		s.setCotExt(r, "cot_para", para)
	}
	for len(events) > 0 {
		chunk := events[:min(cotChunk, len(events))]
		if err := s.cotCall(ctx, FeishuCotAppend, map[string]any{"cot_id": cot, "message_id": msg, "events": chunk}); err != nil {
			if cotMaybeSent(err) {
				// The chunk may have reached Feishu (no reply came back): not sent again, which could show it twice;
				// the end is made sure of with cot_complete (cotFinishNow).
				s.logf("bots: agent message %s: an append got no reply (%v); going on after it", cot, err)
				events = events[len(chunk):]
				p.cot.queue = events
				if ctx.Err() != nil || len(events) == 0 && finish {
					return true // the rest on the next look (or, at the end, cot_complete)
				}
				continue
			}
			p.cot.queue = events
			if errors.Is(err, bus.ErrOffline) || errors.Is(err, rpc.ErrClosed) || !cotRefused(err) && cotPassing(err) {
				return true // not applied: tried again on the next look, from this chunk on
			}
			s.logf("bots: agent message: %v; the progress card takes over", err)
			_ = s.cotCall(ctx, FeishuCotComplete, map[string]any{"cot_id": cot, "message_id": msg, "reason": "error"})
			s.setCotExt(r, "cot_off", "1")
			p.cot.off, p.cot.queue = true, nil
			return false
		}
		events = events[len(chunk):]
	}
	p.cot.queue, p.cot.done = nil, finish
	if finish {
		p.cot.calls = nil
		s.setCotExt(r, "cot_calls", "")
		s.setCotExt(r, "cot_done", "1")
	}
	return true
}

// runItems is a copy of what the run saw of its turn's process.
func (s *Service) runItems(id string) (*chanLive, []proto.Item) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.live[id]
	if p == nil {
		return nil, nil
	}
	return p, append([]proto.Item(nil), p.items...)
}

// cotProgressNow shows r's process in its agent message; false when the card shows it instead.
func (f feishuSink) cotProgressNow(ctx context.Context, r *channel.Run) bool {
	p, items := f.s.runItems(r.ID)
	if p == nil {
		return false
	}
	f.s.loadCot(&p.cot, r)
	if p.cot.off || p.cot.cot == "" {
		return false // a new turn: the card shows it
	}
	return f.cotPush(ctx, r, p, items, false)
}

// cotFinishNow ends r's agent message as its answer goes out. false when there is none (the card has the process).
func (f feishuSink) cotFinishNow(ctx context.Context, r *channel.Run) bool {
	p, items := f.s.runItems(r.ID)
	if p == nil {
		return false
	}
	f.s.loadCot(&p.cot, r)
	if p.cot.done {
		return true // an answer tried again: its agent message has ended
	}
	if p.cot.off || p.cot.cot == "" {
		return false
	}
	if !f.cotPush(ctx, r, p, items, true) {
		return false
	}
	if !p.cot.done {
		// The end did not get through for now (a rate limit, a busy server) and the run is over after this answer:
		// ended without the rest, not left spinning.
		s := f.s
		if err := f.cotEnd(ctx, p.cot.cot, p.cot.msg); err != nil {
			s.logf("bots: agent message %s: not ended: %v", p.cot.cot, err)
			p.cot.queue = nil
			return true // the answer still goes out; a later look of this run cannot end it either (the run is over)
		}
		p.cot.queue, p.cot.done = nil, true
		s.setCotExt(r, "cot_done", "1")
	}
	return true
}

// cotEndTries are the waits between tries of cotEnd (a rate limit that hit the end likely hits the first try too).
var cotEndTries = []time.Duration{0, time.Second, 3 * time.Second}

// cotEnd ends an agent message with cot_complete, tried a few times on its own deadline (the answer's may be what
// ran out).
func (f feishuSink) cotEnd(ctx context.Context, cot, msg string) error {
	var err error
	for _, wait := range cotEndTries {
		time.Sleep(wait)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		err = f.s.cotCall(cctx, FeishuCotComplete, map[string]any{"cot_id": cot, "message_id": msg, "reason": "done"})
		cancel()
		if err == nil || !(errors.Is(err, bus.ErrOffline) || errors.Is(err, rpc.ErrClosed) || cotMaybeSent(err) || cotPassing(err)) {
			return err
		}
	}
	return err
}

// Folded panel limits: newest calls shown, per-line rune cap (escape entities kept whole), own JSON byte budget.
const (
	processPanelStepsMost = 40
	processPanelLineRunes = 120
	processPanelMaxBytes  = 4000
)

// processPanel is the fallback card's folded process, self-bounded for the quiet end card outside answerCards
// (its foot/note are short).
func processPanel(l i18n.Lang, c *feishubot.Card, steps []string) {
	addProcessPanel(l, c, steps, fitPanelSteps(l, steps, min(processPanelStepsMost, len(steps)), processPanelMaxBytes))
}

// fitPanelSteps halves from start until the panel's JSON inside an empty card envelope is at most maxBytes.
func fitPanelSteps(l i18n.Lang, steps []string, start, maxBytes int) int {
	empty := len(feishubot.NewCard("", "", "").JSON())
	for n := start; n > 0; n /= 2 {
		probe := feishubot.NewCard("", "", "")
		addProcessPanel(l, probe, steps, n)
		if len(probe.JSON())-empty <= maxBytes {
			return n
		}
	}
	return 0
}

// addProcessPanel shows the last maxSteps clipped lines; title counts all steps, a line notes hidden earlier ones.
func addProcessPanel(l i18n.Lang, c *feishubot.Card, steps []string, maxSteps int) {
	if maxSteps <= 0 || len(steps) == 0 {
		return
	}
	maxSteps = min(maxSteps, len(steps))
	var b strings.Builder
	if hidden := len(steps) - maxSteps; hidden > 0 {
		b.WriteString(i18n.T(l, "assistant-feishu.earlier_steps", i18n.Args{"count": hidden}))
	}
	for _, st := range steps[len(steps)-maxSteps:] {
		b.WriteString("- " + clipPanelLine(st, processPanelLineRunes) + "\n")
	}
	c.Collapsible(i18n.T(l, "assistant-feishu.cot_tools", i18n.Args{"count": len(steps)}), strings.TrimSpace(b.String()))
}

// panelEntityStart matches a dangling entity prefix the card escapers emit ("&#124;"-style number or "&sim") at
// the cut; a literal '&' before other text stays.
var panelEntityStart = lazyre.New(`&(?:#[0-9]{0,5}|s(?:i(?:m)?)?)?$`)

// clipPanelLine cuts an already card-escaped line to n runes plus an ellipsis, never splitting an escape entity;
// a literal '&' stays.
func clipPanelLine(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return panelEntityStart.ReplaceAllString(string(r[:n]), "") + "…"
}

// runSteps is a copy of the tool-call lines the run saw.
func (s *Service) runSteps(id string) []string {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.live[id]; p != nil {
		return append([]string(nil), p.steps...)
	}
	return nil
}

// feishuCode is the error code Feishu answered a call with (0 when the call did not get that far).
func feishuCode(err error) int {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != CodeFeishuAPI {
		return 0
	}
	var d struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(re.Data, &d)
	return d.Code
}

// cotRefused: Feishu will not give this bot agent messages (the app lacks the permission).
func cotRefused(err error) bool {
	switch feishuCode(err) {
	case 99991672, 99991679: // the app's / the user's scope is missing
		return true
	}
	return false
}

// cotPassing: an append that failed for now and may go through when tried again (rate limits, a busy server); any
// other error ends the agent message.
func cotPassing(err error) bool {
	switch feishuCode(err) {
	case feishuTransport, 99991400, 99991500, 99991661, 99991662, 99991663, 230020:
		return true
	}
	return false
}

// cotMaybeSent: the call went out and no reply came back (a timeout, the connection dropped after it was sent, the
// hub got no reply from Feishu): Feishu may have applied it.
func cotMaybeSent(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, rpc.ErrDropped) || feishuCode(err) == feishuTransport
}

// feishuTransport is the code the hub gives a Feishu call that got no answer from Feishu (a timeout, a dropped
// connection, a reply that is not Feishu's JSON: a gateway's 502).
const feishuTransport = -1
