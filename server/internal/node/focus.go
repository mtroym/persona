package node

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Focus sync for the web's full herdr view (docs/integrations/terminal.md "Focus sync"). herdr's focus (workspace, tab, pane) is one
// state shared by all its clients, and its events do not say which client moved it. The node
//
//   - reports every change to the hub (herdr.focus.changed) with where it came from: the focus Persona asked for itself
//     ("persona", the requesting view's client id), a full client of this node that had just been typed or clicked into
//     ("client", that client's id), or anything else ("other": the computer's own window, another tool);
//   - moves the focus when a view asks (herdr.client.focus), for the page's sidebar and palette.
//
// The attribution is a heuristic (a client with real input within focusWindow before the change), because herdr gives
// nothing better: herdr 0.9.5's focus events carry only the ids that became focused.

const (
	// focusSettle: a focus change sends three events in a burst; the snapshot is read once after they have arrived.
	focusSettle = 30 * time.Millisecond
	// focusWindow: input to a full client this recent is taken for the cause of a focus change.
	focusWindow = 1500 * time.Millisecond
	// focusSelfTTL: how long a focus Persona asked for is remembered, to tell its echo from a change made elsewhere.
	focusSelfTTL = 3 * time.Second
)

// focusState is the node's bookkeeping for focus sync (n.focus; the zero value is ready).
type focusState struct {
	mu    sync.Mutex
	last  host.FocusState
	known bool                // last has been read once; the first read is the baseline, not a change
	self  map[string]selfMark // pane -> the focus Persona asked for there
	queue map[string]string   // client id -> the session or pane address it asked for last (latest wins)
	wake  chan struct{}
	run   sync.Once
}

type selfMark struct {
	client string
	until  time.Time
}

// inputNoise is what a full client's input carries that no person did: focus in / out of the page, and the answers
// xterm gives to herdr's questions (colours, scheme, kitty keyboard, device attributes, sizes). mouseReport is one SGR
// mouse report, whose motion with no button down (herdr turns on any-motion reporting) is noise too. Only the rest
// counts as the user acting.
var (
	inputNoise  = lazyre.New(`\x1b\[[IO]|\x1b\[\?[\d;]*(?:\$y|[nuc])|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[\d+(?:;\d+)*t`)
	mouseReport = lazyre.New(`\x1b\[<(\d+);\d+;\d+M`)
)

// inputIsActivity reports whether bytes typed into a full client are the user doing something (keys, a click, a drag,
// the wheel), not the mouse passing over it or the page answering herdr.
func inputIsActivity(b []byte) bool {
	b = inputNoise.ReplaceAll(b, nil)
	b = mouseReport.ReplaceAllFunc(b, func(m []byte) []byte {
		code, _ := strconv.Atoi(string(mouseReport.FindSubmatch(m)[1]))
		if code&32 != 0 && code&3 == 3 && code&64 == 0 { // motion, no button
			return nil
		}
		return m
	})
	return len(b) > 0
}

// markActive notes that the user acted in this client.
func (f *fullClient) markActive(b []byte) {
	if !inputIsActivity(b) {
		return
	}
	f.mu.Lock()
	f.activeAt = time.Now()
	f.mu.Unlock()
}

// focusSource says where a focus change to pane came from (see above); client is the full client's id for "persona"
// (who asked) and "client" (who acted), else empty.
func (n *Node) focusSource(pane string, now time.Time) (source, client string) {
	n.focus.mu.Lock()
	m, ok := n.focus.self[pane]
	if ok {
		delete(n.focus.self, pane)
	}
	n.focus.mu.Unlock()
	if ok && now.Before(m.until) {
		return "persona", m.client
	}
	n.mu.Lock()
	fulls := make([]*fullClient, 0, len(n.fulls))
	for _, f := range n.fulls {
		fulls = append(fulls, f)
	}
	n.mu.Unlock()
	var best *fullClient
	var bestAt time.Time
	for _, f := range fulls {
		f.mu.Lock()
		at := f.activeAt
		f.mu.Unlock()
		if !at.IsZero() && now.Sub(at) <= focusWindow && at.After(bestAt) {
			best, bestAt = f, at
		}
	}
	if best != nil {
		return "client", best.id
	}
	return "other", ""
}

// watchFocus keeps the focus stream of the host that has one open, reopening it with a backoff, and reports each
// change to the hub. Only a host that is a host.Focuser has it (herdr).
func (n *Node) watchFocus(ctx context.Context) {
	f, ok := n.hosts.Get(host.Herdr).(host.Focuser)
	if !ok {
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		n.focusStream(ctx, f)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// focusStream is one subscription: a change (or the stream starting, to cover the gap before it) is read once the
// burst of events is over.
func (n *Node) focusStream(ctx context.Context, f host.Focuser) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	kick := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-sctx.Done():
				return
			case <-kick:
			}
			select {
			case <-sctx.Done():
				return
			case <-time.After(focusSettle):
			}
			select { // what came meanwhile is in the same read
			case <-kick:
			default:
			}
			n.reportFocus(sctx, f)
		}
	}()
	_ = f.WatchFocus(sctx, func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	})
}

// reportFocus reads the focus and tells the hub when it is not the one last reported. The first read only sets the
// baseline: nothing changed yet.
func (n *Node) reportFocus(ctx context.Context, f host.Focuser) {
	st, err := f.Focused(ctx)
	if err != nil || st.Pane == "" {
		return
	}
	n.focus.mu.Lock()
	same := n.focus.known && n.focus.last == st
	first := !n.focus.known
	n.focus.last, n.focus.known = st, true
	n.focus.mu.Unlock()
	if same || first || n.peer == nil {
		return
	}
	source, client := n.focusSource(st.Pane, time.Now())
	p := map[string]any{"workspace_id": st.Space, "tab_id": st.Slot, "pane_id": st.Pane, "source": source}
	if client != "" {
		p["client_id"] = client
	}
	if sid := n.sessionOfPane(st.Pane); sid != "" {
		p["session_id"] = sid
	}
	_ = n.peer.Notify("herdr.focus.changed", p)
}

// onFocusRequest queues a view's wish to show a session or pane (the hub's herdr.client.focus). Requests of one view
// replace each other: when the user clicks through the sidebar quickly only the last one is worth focusing.
func (n *Node) onFocusRequest(params json.RawMessage) {
	var p struct {
		ClientID  string `json:"client_id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(params, &p) != nil || p.SessionID == "" || len(p.SessionID) > 256 || n.fullOf(p.ClientID) == nil {
		return
	}
	q := &n.focus
	q.mu.Lock()
	if q.queue == nil {
		q.queue, q.wake = map[string]string{}, make(chan struct{}, 1)
	}
	q.queue[p.ClientID] = p.SessionID
	q.mu.Unlock()
	q.run.Do(func() { go n.focusWorker() })
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// focusWorker moves the focus for the queued requests, one at a time and in the order they came.
func (n *Node) focusWorker() {
	q := &n.focus
	for range q.wake {
		q.mu.Lock()
		todo := q.queue
		q.queue = map[string]string{}
		q.mu.Unlock()
		for client, id := range todo {
			if n.fullOf(client) != nil { // a view that ended meanwhile asks for nothing
				n.focusSession(client, id)
			}
		}
	}
}

// focusSession focuses the pane of a session (or "pane:" address) for a view. The focus is remembered as Persona's own
// until its echo has come back, so the hub tells the page that this is not a change to follow.
func (n *Node) focusSession(client, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pane, err := n.terminalPane(ctx, id)
	if err != nil || strings.TrimSpace(pane) == "" {
		return
	}
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return
	}
	f, ok := h.(host.Focuser)
	if !ok {
		return
	}
	if fc := n.fullOf(client); fc != nil && ref.Kind == host.Herdr {
		// A view's client shows one herdr server (its session's): a pane of another cannot be shown there, and what
		// the user types would go to a pane they do not see. The view ends; opening it again attaches to the pane's.
		if session, _ := herdr.SplitID(ref.ID); session != fc.session {
			go fc.stop("this pane is in another herdr session: open the full view again to show it", true)
			return
		}
	}
	// Focus comes back from the own herdr server only (herdrHost.WatchFocus): a pane of a named session (#2196) gets
	// no echo to mark.
	if session, _ := herdr.SplitID(ref.ID); session != "" && ref.Kind == host.Herdr {
		_ = f.FocusPane(ctx, ref)
		return
	}
	n.focus.mu.Lock()
	if n.focus.self == nil {
		n.focus.self = map[string]selfMark{}
	}
	n.focus.self[ref.ID] = selfMark{client: client, until: time.Now().Add(focusSelfTTL)}
	n.focus.mu.Unlock()
	if err := f.FocusPane(ctx, ref); err != nil {
		n.focus.mu.Lock()
		delete(n.focus.self, ref.ID)
		n.focus.mu.Unlock()
	}
}
