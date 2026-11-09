package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func standbyHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "sb") // short: sun_path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("JARVIS_HOME", "")
	return home
}

// listenAt serves node.sock as a live node would (a `node --stdio` serving by itself): every request gets {"live":true}.
func listenAt(t *testing.T) net.Listener {
	t.Helper()
	path := nodeclient.SocketPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte(`{"live":true}` + "\n"))
			}()
		}
	}()
	return ln
}

func deadAt(t *testing.T) net.Listener {
	t.Helper()
	path := nodeclient.SocketPath()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	return ln
}

func answeredBy(t *testing.T) string {
	t.Helper()
	line, err := nodeclient.Dial(context.Background(), sockRequest{Kind: "ping"})
	if err != nil {
		return "nobody"
	}
	var r map[string]any
	_ = json.Unmarshal(line, &r)
	switch {
	case r["live"] == true:
		return "live"
	case r["standby"] == true:
		return "standby"
	}
	return string(line)
}

// Standby leaves node.sock to a node that serves it, notices when one takes it over, and takes it back once it is gone.
func TestStandbyNeverTakesALiveSocket(t *testing.T) {
	standbyHome(t)
	live := listenAt(t)
	sb := newStandby(openSpool(SpoolPath()), t.Logf)
	sb.retry = 20 * time.Millisecond
	sb.want()
	defer sb.unwant()
	time.Sleep(100 * time.Millisecond)
	if sb.listening() || answeredBy(t) != "live" {
		t.Fatalf("standby took the socket of a live node: %s", answeredBy(t))
	}
	live.Close() // that node is gone (its file stays, nobody answers)
	waitFor(t, "standby to take node.sock", func() bool { return answeredBy(t) == "standby" })

	sb.unwant()
	dead := deadAt(t) // a listener that accepts and never answers (closing, or a stopped process)
	sb.want()
	waitFor(t, "standby to take a socket nobody answers on", func() bool { return answeredBy(t) == "standby" })
	dead.Close()

	other := listenAt(t) // a node serving by itself takes the path from standby
	defer other.Close()
	waitFor(t, "standby to let go of a path it no longer owns", func() bool { return !sb.listening() })
	if answeredBy(t) != "live" {
		t.Fatalf("answered by %s", answeredBy(t))
	}
	other.Close()
	waitFor(t, "standby to take it back", func() bool { return answeredBy(t) == "standby" })
	sb.unwant()
	time.Sleep(100 * time.Millisecond)
	if sb.listening() {
		t.Fatal("unwanted standby listens again")
	}
}

// A node serving by itself can take node.sock (remove, then listen) while standby asks whether anything answers there.
// Standby then takes nothing: it removed the node's fresh socket file and kept the path for good, the node listening on
// a file nobody could reach (#1556).
func TestStandbyDoesNotRemoveASocketTakenWhileItAsked(t *testing.T) {
	for _, before := range []string{"a file nobody answers on", "nothing"} {
		t.Run(before, func(t *testing.T) {
			standbyHome(t)
			_ = os.MkdirAll(filepath.Dir(nodeclient.SocketPath()), 0o700)
			if before != "nothing" {
				defer deadAt(t).Close()
			}
			sb := newStandby(openSpool(SpoolPath()), t.Logf)
			sb.retry = time.Hour // only the take below, no loop
			var other net.Listener
			sb.afterAsk = func() {
				if other == nil {
					other = listenAt(t)
				}
			}
			sb.want()
			defer sb.unwant()
			defer func() { other.Close() }()
			if sb.listening() {
				t.Fatal("standby took a path a node took while standby asked")
			}
			if got := answeredBy(t); got != "live" {
				t.Fatalf("answered by %s", got)
			}
		})
	}
}

// Standby that finds its socket file gone lets the path be for this round: a node serving by itself removes the file
// before it listens, and standby listening in between made that node's listen fail (#1556).
func TestStandbyLetsAPathGoForARoundWhenItsFileIsRemoved(t *testing.T) {
	standbyHome(t)
	sb := newStandby(openSpool(SpoolPath()), t.Logf)
	sb.retry = time.Hour
	sb.want()
	defer sb.unwant()
	if !sb.listening() {
		t.Fatal("standby did not take a free node.sock")
	}
	_ = os.Remove(nodeclient.SocketPath()) // a node taking the path, about to listen
	sb.check()
	if sb.listening() {
		t.Fatal("standby listened again on a path a node was taking")
	}
	other := listenAt(t)
	if got := answeredBy(t); got != "live" {
		t.Fatalf("answered by %s", got)
	}
	other.Close()
	sb.check() // that node is gone (its file stays, nobody answers): standby takes the path back
	if got := answeredBy(t); got != "standby" {
		t.Fatalf("answered by %s", got)
	}
}

// The hold counts from the last connection that held, not from each standby start: a flapping manager does not keep
// every permission hook waiting.
func TestStandbyHoldDoesNotRenewOnFlaps(t *testing.T) {
	standbyHome(t)
	now := time.Now()
	sb := newStandby(openSpool(SpoolPath()), t.Logf)
	sb.now = func() time.Time { return now }
	sb.want()
	defer sb.unwant()
	if !sb.holding() {
		t.Fatal("a resident node starting holds")
	}
	for range 5 { // short connections come and go
		now = now.Add(3 * time.Minute)
		sb.unwant()
		sb.want()
	}
	if sb.holding() {
		t.Fatal("flaps renewed the hold")
	}
	sb.managerSeen(now) // a connection that held
	if !sb.holding() {
		t.Fatal("a connection that held renews it")
	}
}

// A notification the manager could not take stays (with what follows), and is dropped after spoolTriesMost tries.
func TestReplayKeepsWhatCouldNotBeHandedOver(t *testing.T) {
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.panes = map[string]string{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	refuse := true // read by the hub's handler goroutine only after the writes below are done (rpc round trips)
	var mu sync.Mutex
	var events []string
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(events) }
	busy := false
	hub.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "notify" && busy {
			return nil, &rpc.Error{Code: nodeproto.ErrSpoolBusy, Message: "still being sent"}
		}
		if method == "notify" && refuse {
			return nil, &rpc.Error{Code: "busy", Message: "try later"}
		}
		return map[string]any{"sent": true}, nil
	}
	hub.OnEvent = func(event string, params json.RawMessage) {
		var p struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		events = append(events, p.Event)
		mu.Unlock()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { _ = n2hW.Close(); _ = h2nW.Close() })

	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	_, _ = sp.add(spoolEntry{ID: "n1", At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"t"}`)}})
	_, _ = sp.add(spoolEntry{ID: "h1", At: time.Now(), Kind: "hook", Request: hookReq("Stop", "", nil)})
	replay := func() bool {
		n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
		close(n.stateReady)
		return n.replaySpool(ctx, sp)
	}
	busy = true // the manager still has an earlier try: kept, not counted
	if !replay() {
		t.Fatal("a busy notification left nothing to retry")
	}
	if got := sp.load(); len(got) != 1 || got[0].ID != "n1" || got[0].Tries != 0 {
		t.Fatalf("after a busy answer: %+v, want the notify kept untried (the Stop after it went on)", got)
	}
	busy = false
	if !replay() {
		t.Fatal("a refused notification left nothing to retry")
	}
	if got := sp.load(); len(got) != 1 || got[0].ID != "n1" || got[0].Tries != 1 {
		t.Fatalf("after a refused notify: %+v, want it kept, tried once", got)
	}
	waitFor(t, "the Stop after it to go on (hook events keep their order)", func() bool { return count() == 1 })
	for range spoolTriesMost - 2 {
		replay()
	}
	if replay() { // the last refusal drops it
		t.Fatal("a dropped notification is retried")
	}
	if got := sp.load(); len(got) != 0 {
		t.Fatalf("left: %+v", got)
	}
	refuse = false
	_, _ = sp.add(spoolEntry{ID: "n2", At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"t"}`)}})
	cancel() // the connection is gone: nothing is counted against it
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	n.replaySpool(ctx, sp)
	if got := sp.load(); len(got) != 1 || got[0].Tries != 0 {
		t.Fatalf("a replay on a gone connection: %+v", got)
	}
}

// Replay waits for the session state (takesReport needs it), and only a resident node's connection replays at all.
func TestReplayWaitsForTheSessionState(t *testing.T) {
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	rec := newReplayHub(t, n)
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	_, _ = sp.add(spoolEntry{ID: "h1", At: time.Now(), Kind: "hook", Request: hookReq("Stop", "", func(r *sockRequest) { r.ReportOnly = true })})
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	n.state.Sessions[reportSession] = sessionState{} // Persona's own session: its report-only events are not taken
	go n.replaySpool(context.Background(), sp)
	time.Sleep(100 * time.Millisecond) // nothing but the state (or the connection ending) lets it go on
	select {
	case <-n.replayed:
		t.Fatal("replayed before the state came")
	default:
	}
	if got := sp.load(); len(got) != 1 {
		t.Fatalf("kept %+v before the state came", got)
	}
	close(n.stateReady)
	<-n.replayed
	if got := sp.load(); len(got) != 0 {
		t.Fatalf("left %+v: the state came, so the replay went through", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.got) != 0 {
		t.Fatalf("a report-only event of Persona's own session was reported: %v", rec.got)
	}
}

// A first try of the state that failed holds the replay: the gate for new hooks opens at once, everything stays
// untried, and the replay runs when a later try brings the state. Then it drops the kept events of a session that had a
// new hook meanwhile, and binds no pane a new hook bound back to an older session.
func TestReplayHoldsUntilTheStateComes(t *testing.T) {
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.panes = map[string]string{}
	rec := newReplayHub(t, n)
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	const moved, quiet, fresh = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000002", "aaaaaaaa-0000-4000-8000-000000000003"
	kept := func(id, session, event, pane string) {
		b, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": session, "cwd": "/work/project"})
		_, _ = sp.add(spoolEntry{ID: id, At: time.Now(), Kind: "hook", Request: sockRequest{Kind: "hook", Input: b, Pane: pane}})
	}
	kept("h1", moved, "Stop", "w1:p1")
	kept("h2", quiet, "Stop", "w1:p2")
	_, _ = sp.add(spoolEntry{ID: "n1", At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"t"}`)}})
	n.replayed, n.stateReady, n.stateMissed = make(chan struct{}), make(chan struct{}), make(chan struct{})
	close(n.stateMissed) // the first state.load failed
	done := make(chan bool)
	go func() { done <- n.replaySpool(context.Background(), sp) }()
	select {
	case <-n.replayed:
	case <-time.After(time.Second):
		t.Fatal("the gate for new hooks waits for a held replay")
	}
	if got := sp.load(); len(got) != 3 || got[2].Tries != 0 {
		t.Fatalf("held: kept %+v, want all three, untried", got)
	}
	// New hooks go ahead: one of the session that has kept events, on its pane, and one of another session that
	// took the second kept event's pane.
	live := func(session, pane string) sockRequest {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": session})
		return sockRequest{Kind: "hook", Input: b, Pane: pane}
	}
	n.ahead.note(live(moved, "w1:p1"))
	n.ahead.note(live(fresh, "w1:p2"))
	n.bindPane("w1:p2", fresh)
	reportOnly := live(fresh, "w1:p9")
	reportOnly.ReportOnly = true
	if n.ahead.note(reportOnly); n.ahead.paneTaken("w1:p9") {
		t.Fatal("a report-only hook binds no pane, so it takes none")
	}
	close(n.stateReady) // a later try brought it
	if retry := <-done; retry {
		t.Fatal("nothing was left to retry")
	}
	waitFor(t, "the replay", func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return len(rec.got) == 2 })
	rec.mu.Lock()
	got := strings.Join(rec.got, " ")
	rec.mu.Unlock()
	if got != "event:Stop notify:t" {
		t.Fatalf("replayed %q, want the quiet session's Stop and the notification (not the Stop of the session that moved on)", got)
	}
	n.mu.Lock()
	pane := n.panes["w1:p2"]
	n.mu.Unlock()
	if pane != fresh {
		t.Fatalf("pane w1:p2 is %s, want it left to the session a new hook bound", pane)
	}
	if left := sp.load(); len(left) != 0 {
		t.Fatalf("left %+v", left)
	}
	n.ahead.note(live(quiet, "w1:p3")) // after the replay: nothing is ahead any more
	if n.ahead.behind(spoolEntry{Request: live(quiet, "")}) {
		t.Fatal("a hook after the replay counted as ahead of it")
	}

	// The connection ending while the replay is held keeps everything.
	kept("h3", quiet, "Stop", "")
	n.replayed, n.stateReady, n.stateMissed = make(chan struct{}), make(chan struct{}), make(chan struct{})
	n.ahead = replayAhead{}
	close(n.stateMissed)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- n.replaySpool(ctx, sp) }()
	<-n.replayed
	cancel()
	<-done
	if got := sp.load(); len(got) != 1 || got[0].ID != "h3" {
		t.Fatalf("kept %+v after the connection ended", got)
	}
}

// What Serve runs (startState, with serveSock answering node.sock) against a manager whose state.load fails once:
//   - live hooks do not wait for the held replay, and the spool stays as it was;
//   - once the state loads on a later try, the replay goes through: the kept event of a session that had a live hook
//     meanwhile is dropped (serveSock noted it), while the kept permission whose own hook asked again (resumed, after
//     standby told it to retry) is handed over, since a resumed hook reports nothing itself;
//   - a notification the manager refuses once arrives on a retry on the same connection.
func TestStateRetriedOnTheConnectionReleasesTheReplay(t *testing.T) {
	defer func(d []time.Duration) { replayRetryAfter = d }(replayRetryAfter)
	replayRetryAfter = []time.Duration{10 * time.Millisecond}
	standbyHome(t)
	n, home := testNode(t)
	n.dir = home
	n.logger = log.New(io.Discard, "", 0)
	n.panes = map[string]string{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	var events, notes []string
	loads, notifies := 0, 0
	release := make(chan struct{})
	hub.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "state.load":
			loads++
			if loads == 1 {
				return nil, errors.New("database is restarting")
			}
			mu.Unlock()
			select { // the second try answers when the test says
			case <-release:
			case <-ctx.Done():
			}
			mu.Lock()
			return map[string]any{"sessions": map[string]any{}}, ctx.Err()
		case "notify":
			notifies++
			if notifies == 1 {
				return nil, &rpc.Error{Message: "try later"}
			}
			var p struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(params, &p)
			notes = append(notes, p.Title)
			return map[string]any{"sent": true}, nil
		case "interaction.create": // the phone allows at once
			return proto.Resolution{Allow: boolp(true)}, nil
		}
		return map[string]any{}, nil
	}
	hub.OnEvent = func(event string, params json.RawMessage) {
		if event != "hook.event" {
			return
		}
		var p struct {
			Event     string `json:"event"`
			SessionID string `json:"session_id"`
			SpoolID   string `json:"spool_id"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		events = append(events, p.Event+"/"+p.SessionID[:1]+"/"+map[bool]string{true: "kept", false: "live"}[p.SpoolID != ""])
		mu.Unlock()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { _ = n2hW.Close(); _ = h2nW.Close() })

	const moved, asked = "a0000000-0000-4000-8000-000000000001", "b0000000-0000-4000-8000-000000000002"
	hook := func(session, event, tool string, mod func(*sockRequest)) sockRequest {
		b, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": session, "tool_name": tool,
			"tool_input": map[string]any{"command": "make deploy"}, "cwd": "/work/project"})
		r := sockRequest{Kind: "hook", Input: b, Pane: "w1:p" + session[:1]}
		if mod != nil {
			mod(&r)
		}
		return r
	}
	n.spool = openSpool(SpoolPath())
	_, _ = n.spool.add(spoolEntry{ID: "h1", At: time.Now(), Kind: "hook", Request: hook(moved, "Stop", "", nil)})
	permission := func(r *sockRequest) { r.Attempt = "att-perm" }
	_, _ = n.spool.add(spoolEntry{ID: "h2", At: time.Now(), Kind: "hook", Request: hook(asked, "PermissionRequest", "Bash", permission)})
	_, _ = n.spool.add(spoolEntry{ID: "n1", At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"built"}`)}})
	sock, err := os.MkdirTemp("/tmp", "ss")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sock) })
	ln, err := net.Listen("unix", filepath.Join(sock, "s"))
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
			go n.serveSock(ctx, c)
		}
	}()
	live := func(req sockRequest) {
		t.Helper()
		start := time.Now()
		if _, err := nodeclient.DialAt(ctx, filepath.Join(sock, "s"), req); err != nil {
			t.Fatal(err)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("a live hook waited %s for a held replay", took)
		}
	}

	n.startState(ctx, nil)
	waitFor(t, "the state to be tried again", func() bool { mu.Lock(); defer mu.Unlock(); return loads >= 2 })
	live(hook(moved, "UserPromptSubmit", "", nil))                                                          // a new turn of a session with a kept event
	live(hook(asked, "PermissionRequest", "Bash", func(r *sockRequest) { permission(r); r.Resume = true })) // the kept permission asks again
	if got := n.spool.load(); len(got) != 3 || got[0].Tries != 0 || got[2].Tries != 0 {
		t.Fatalf("held: kept %+v", got)
	}
	close(release)
	waitFor(t, "the replay and the retried notification", func() bool { return len(n.spool.load()) == 0 })
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, " "); got != "UserPromptSubmit/a/live PermissionRequest/b/kept" {
		t.Fatalf("events %q: want the live prompt, then the kept permission (not the moved session's kept Stop)", got)
	}
	if strings.Join(notes, " ") != "built" || notifies != 2 {
		t.Fatalf("notified %v in %d tries, want it once, on the second", notes, notifies)
	}
}

// A notification the manager refused, or still had, is tried again on the same connection with backoff (not at the
// next reconnect), after the gate for new hooks opened, until it is taken; one refused every time is dropped after
// spoolTriesMost tries.
func TestReplayRetriesNotificationsOnTheSameConnection(t *testing.T) {
	defer func(d []time.Duration) { replayRetryAfter = d }(replayRetryAfter)
	replayRetryAfter = []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.panes = map[string]string{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	answers := map[string][]string{} // title → what the manager answers each try, in turn ("" accepts)
	tries := map[string]int{}
	sent := map[string]int{}
	hub.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		defer mu.Unlock()
		i := tries[p.Title]
		tries[p.Title]++
		a := ""
		if plan := answers[p.Title]; i < len(plan) {
			a = plan[i]
		} else if p.Title == "never" {
			a = "refuse"
		}
		switch a {
		case "busy":
			return nil, &rpc.Error{Code: nodeproto.ErrSpoolBusy, Message: "still being sent"}
		case "away":
			return nil, &rpc.Error{Code: nodeproto.ErrHubAway, Message: "the manager cannot reach the hub now"}
		case "refuse": // what the hub refuses with (hub/notify_api.go): no code
			return nil, &rpc.Error{Message: "notification is too large"}
		}
		sent[p.Title]++
		return map[string]any{"sent": true}, nil
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { _ = n2hW.Close(); _ = h2nW.Close() })
	answers["refused once"] = []string{"refuse"}
	answers["busy twice"] = []string{"busy", "busy"}
	answers["never"] = nil                   // refuses every time
	away := make([]string, spoolTriesMost+3) // the hub away for longer than a refused one would last: never counted
	for i := range away {
		away[i] = "away"
	}
	answers["hub away"] = away
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	for _, title := range []string{"refused once", "busy twice", "never", "hub away", ""} { // "": never sendable
		b, _ := json.Marshal(map[string]string{"title": title})
		_, _ = sp.add(spoolEntry{ID: newUUID(), At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: b}})
	}
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	done := make(chan struct{})
	go func() { // what Serve does with them
		defer close(done)
		if n.replaySpool(ctx, sp) {
			n.retryNotifications(ctx, sp)
		}
	}()
	select {
	case <-n.replayed: // before any retry
	case <-time.After(5 * time.Second):
		t.Fatal("the gate waited for the retries")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the retries did not end; left %+v", sp.load())
	}
	mu.Lock()
	defer mu.Unlock()
	if sent["refused once"] != 1 || sent["busy twice"] != 1 || sent["hub away"] != 1 || sent["never"] != 0 || tries[""] != 0 {
		t.Fatalf("sent %v (tries %v), want the refused, busy and hub-away ones once each, the others never", sent, tries)
	}
	if tries["never"] != spoolTriesMost {
		t.Fatalf("an always-refused notification was tried %d times, want %d", tries["never"], spoolTriesMost)
	}
	if left := sp.load(); len(left) != 0 {
		t.Fatalf("left in the spool: %+v", left)
	}
}

// A notification that times out just as its connection drops counts no try: the connection took it, not the manager.
func TestReplayTimeoutAtTheDropCountsNoTry(t *testing.T) {
	defer func(d, g time.Duration) { replayNotifyTimeout, replayGoneGrace = d, g }(replayNotifyTimeout, replayGoneGrace)
	replayNotifyTimeout, replayGoneGrace = 20*time.Millisecond, 3*time.Second // the drop comes well inside the grace
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	hub.OnRequest = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		<-ctx.Done() // never answers
		return nil, ctx.Err()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	e := spoolEntry{ID: "n1", At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"t"}`)}}
	_, _ = sp.add(e)
	time.AfterFunc(300*time.Millisecond, func() { _ = h2nW.Close(); _ = n2hW.Close() }) // after the timeout, inside the grace
	if r := n.replayNotify(ctx, sp, e); r != notifyGone {
		t.Fatalf("outcome %d, want notifyGone", r)
	}
	if got := sp.load(); len(got) != 1 || got[0].Tries != 0 {
		t.Fatalf("kept %+v, want it untried", got)
	}
}

// "The hub is away" (nodeproto.ErrHubAway) ends a replay's pass and a retry round at the first notification that gets
// it: the others are not asked (they would get the same), and no try is counted for any.
func TestHubAwayWaitsForTheNextRound(t *testing.T) {
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	var asked []string
	hub.OnRequest = func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var p struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		asked = append(asked, p.Title)
		mu.Unlock()
		return nil, &rpc.Error{Code: nodeproto.ErrHubAway, Message: "the manager cannot reach the hub now"}
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { _ = n2hW.Close(); _ = h2nW.Close() })
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	for _, title := range []string{"first", "second"} {
		b, _ := json.Marshal(map[string]string{"title": title})
		_, _ = sp.add(spoolEntry{ID: title, At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: b}})
	}
	check := func(what string, want string) {
		t.Helper()
		mu.Lock()
		got := strings.Join(asked, " ")
		asked = nil
		mu.Unlock()
		if got != want {
			t.Fatalf("%s asked %q, want %q", what, got, want)
		}
		if left := sp.load(); len(left) != 2 || left[0].Tries != 0 || left[1].Tries != 0 {
			t.Fatalf("%s: kept %+v, want both, untried", what, left)
		}
	}
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	if !n.replaySpool(ctx, sp) {
		t.Fatal("the replay left nothing to retry")
	}
	check("the replay", "first")
	if !n.retryNotificationsOnce(ctx, sp) {
		t.Fatal("the round left nothing to retry")
	}
	check("a retry round", "first")
}

// A manager that does not answer a notification in time: that one counts a try, the other notifications wait (untried)
// for the retries, and the hook events go on in order. A connection that drops counts no try.
func TestReplayTimeoutsAndDrops(t *testing.T) {
	defer func(d time.Duration) { replayNotifyTimeout = d }(replayNotifyTimeout)
	replayNotifyTimeout = 50 * time.Millisecond
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.panes = map[string]string{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	var events []string
	hub.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		<-ctx.Done() // never answers
		return nil, ctx.Err()
	}
	hub.OnEvent = func(event string, _ json.RawMessage) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	note := func(id string) spoolEntry {
		return spoolEntry{ID: id, At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"t"}`)}}
	}
	_, _ = sp.add(note("n1"))
	_, _ = sp.add(note("n2"))
	_, _ = sp.add(spoolEntry{ID: "h1", At: time.Now(), Kind: "hook", Request: hookReq("Stop", "", nil)})
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	start := time.Now()
	if !n.replaySpool(ctx, sp) {
		t.Fatal("notifications not answered left nothing to retry")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the replay took %s: one timeout should be all it waits", took)
	}
	got := sp.load()
	if len(got) != 2 || got[0].ID != "n1" || got[0].Tries != 1 || got[1].ID != "n2" || got[1].Tries != 0 {
		t.Fatalf("kept %+v: n1 tried once, n2 untried", got)
	}
	waitFor(t, "the Stop to go on", func() bool { mu.Lock(); defer mu.Unlock(); return len(events) == 1 })

	_ = h2nW.Close() // the connection drops: a call fails without ctx ending
	_ = n2hW.Close()
	n.replayed = make(chan struct{})
	n.replaySpool(ctx, sp)
	if got := sp.load(); len(got) != 2 || got[0].Tries != 1 || got[1].Tries != 0 {
		t.Fatalf("a dropped connection counted a try: %+v", got)
	}
}

// The retries end with the connection, in a wait or in a call the manager does not answer, and count no try.
func TestRetryNotificationsEndsWithTheConnection(t *testing.T) {
	defer func(d []time.Duration, to time.Duration) { replayRetryAfter, replayNotifyTimeout = d, to }(replayRetryAfter, replayNotifyTimeout)
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	asked := make(chan struct{}, 4)
	hub.OnRequest = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		asked <- struct{}{}
		<-ctx.Done() // never answers
		return nil, ctx.Err()
	}
	go func() { _ = hub.Serve(context.Background(), n2hR) }()
	go func() { _ = n.peer.Serve(context.Background(), h2nR) }()
	t.Cleanup(func() { _ = n2hW.Close(); _ = h2nW.Close() })
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	_, _ = sp.add(spoolEntry{ID: "n1", At: time.Now(), Kind: "notify", Request: sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"t"}`)}})
	run := func(ctx context.Context) chan struct{} {
		done := make(chan struct{})
		go func() { defer close(done); n.retryNotifications(ctx, sp) }()
		return done
	}

	replayRetryAfter = []time.Duration{time.Hour} // ends in the wait
	wctx, wcancel := context.WithCancel(ctx)
	done := run(wctx)
	wcancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the retries outlived their connection (in the wait)")
	}

	replayRetryAfter, replayNotifyTimeout = []time.Duration{time.Millisecond}, time.Hour // ends in the call
	cctx, ccancel := context.WithCancel(ctx)
	done = run(cctx)
	<-asked
	ccancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the retries outlived their connection (in a call)")
	}
	if got := sp.load(); len(got) != 1 || got[0].Tries != 0 {
		t.Fatalf("kept %+v, want it untried", got)
	}
}

// One round of retries: an entry past the age limit goes unsent, and a notification the manager does not answer
// ends the round (the rest untried) with one try counted against it.
func TestRetryRoundTimeoutAndAge(t *testing.T) {
	defer func(d time.Duration) { replayNotifyTimeout = d }(replayNotifyTimeout)
	replayNotifyTimeout = 50 * time.Millisecond
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	var asked []string
	hub.OnRequest = func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
		var p struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		asked = append(asked, p.Title)
		mu.Unlock()
		<-ctx.Done() // never answers
		return nil, ctx.Err()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { _ = n2hW.Close(); _ = h2nW.Close() })
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	note := func(id, title string, at time.Time) {
		b, _ := json.Marshal(map[string]string{"title": title})
		_, _ = sp.add(spoolEntry{ID: id, At: at, Kind: "notify", Request: sockRequest{Kind: "notify", Input: b}})
	}
	note("old", "old", time.Now().Add(-spoolMaxAge-time.Minute))
	note("n1", "first", time.Now())
	note("n2", "second", time.Now())
	if !n.retryNotificationsOnce(ctx, sp) {
		t.Fatal("a round that timed out says nothing is left")
	}
	got := sp.load()
	if len(got) != 2 || got[0].ID != "n1" || got[0].Tries != 1 || got[1].ID != "n2" || got[1].Tries != 0 {
		t.Fatalf("kept %+v: the old one gone, n1 tried once, n2 untried", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, " ") != "first" {
		t.Fatalf("asked the manager %v, want only the first (the old one is not sent, the round stops at the timeout)", asked)
	}
}
