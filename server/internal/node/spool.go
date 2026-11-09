package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// While a resident node has no manager connection (the manager restarting, crashed, or not installed yet), it answers
// node.sock itself in standby: what hooks report and what `personad notify` sends goes into a bounded spool persisted
// under the data dir, and the next manager connection's Node replays it, in order, before it takes new hooks. Hooks
// that wait on the phone, and `personad ask`, are told to retry while the manager has been away less than standbyHold,
// so a manager restart does not drop their card to the terminal; past that they go to the terminal at once, as without
// a manager. Everything else on the socket (MCP, launchers, CLI calls) is answered "away", which clients take for "no
// node", exactly as before (docs/protocol/protocol.md "When the manager is away").

// spoolFile is the spool's name in the data dir: one JSON entry per line, oldest first.
const spoolFile = "node-spool.jsonl"

// Bounds of the spool: the oldest entries go first when either is reached. An entry older than spoolMaxAge is dropped
// at replay: a day-old "done" is no news.
var (
	spoolMost      = 500
	spoolBytesMost = 1 << 20
	spoolMaxAge    = 24 * time.Hour
)

// standbyAway is what standby says it cannot do.
const standbyAway = "the manager is not connected to this computer"

// spoolTriesMost: a notification refused (or not answered) this often is dropped: it would hold the spool forever.
// Only real refusals count, not "not now" (nodeproto.ErrSpoolBusy, ErrHubAway); with the retries on the same
// connection (replayRetryAfter) this is about 7 minutes of them.
const spoolTriesMost = 5

var (
	// replayNotifyTimeout bounds one replayed notification.
	replayNotifyTimeout = 5 * time.Second
	// replayGateMost is the longest a new hook / ask / notify waits for the replay.
	replayGateMost = 20 * time.Second
	// replayRetryAfter are the waits before a connection tries again what the manager did not take: the session state
	// (reloadState, node.go) and the notifications its replay kept (retryNotifications). The last one repeats.
	replayRetryAfter = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	// replayGoneGrace: after a notification timed out, how long the replay looks whether the connection went with it
	// (then no try is counted).
	replayGoneGrace = 250 * time.Millisecond
)

// standbyHold is how long after the manager went away hooks that wait on the phone are told to retry.
var standbyHold = 10 * time.Minute

// SpoolPath is where a resident node keeps what it took while its manager was away.
func SpoolPath() string { return filepath.Join(identity.HomeDir(), spoolFile) }

// spoolEntry is one thing taken in standby. ID goes with it to the manager (spool_id), which drops a repeat.
type spoolEntry struct {
	ID      string      `json:"id"`
	At      time.Time   `json:"at"`
	Kind    string      `json:"kind"` // "hook" or "notify"
	Request sockRequest `json:"request"`
	Tries   int         `json:"tries,omitempty"` // replays that failed
}

// spool is the file of entries. Only the resident node's process uses it (Attach): its standby writes it and its
// connections' Nodes replay it, one at a time (replayMu).
type spool struct {
	path     string
	replayMu sync.Mutex
}

var spoolMu sync.Mutex // one spool file per process

func openSpool(path string) *spool { return &spool{path: path} }

// load reads the entries; a line that does not parse is skipped.
func (s *spool) load() []spoolEntry {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	return s.loadLocked()
}

func (s *spool) loadLocked() []spoolEntry {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var out []spoolEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), spoolBytesMost+1)
	for sc.Scan() {
		var e spoolEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" {
			out = append(out, e)
		}
	}
	return out
}

// add appends e, dropping the oldest entries past the bounds. dropped is how many went.
func (s *spool) add(e spoolEntry) (dropped int, err error) {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	line, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	if len(line) >= spoolBytesMost {
		return 0, errors.New("too large to keep")
	}
	all := append(s.loadLocked(), e)
	lines := make([][]byte, 0, len(all))
	size := 0
	for _, x := range all {
		b, _ := json.Marshal(x)
		lines = append(lines, b)
		size += len(b) + 1
	}
	for len(lines) > spoolMost || size > spoolBytesMost {
		size -= len(lines[0]) + 1
		lines = lines[1:]
		dropped++
	}
	return dropped, s.writeLocked(lines)
}

// failed counts a failed replay of id and says whether it is to be dropped now.
func (s *spool) failed(id string) (drop bool, err error) {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	var lines [][]byte
	for _, x := range s.loadLocked() {
		if x.ID == id {
			if x.Tries++; x.Tries >= spoolTriesMost {
				drop = true
				continue
			}
		}
		b, _ := json.Marshal(x)
		lines = append(lines, b)
	}
	return drop, s.writeLocked(lines)
}

// remove takes the entry id out (it was delivered).
func (s *spool) remove(id string) error {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	var lines [][]byte
	for _, x := range s.loadLocked() {
		if x.ID != id {
			b, _ := json.Marshal(x)
			lines = append(lines, b)
		}
	}
	return s.writeLocked(lines)
}

// writeLocked replaces the file with lines (atomically: a crash leaves the old file or the new one); none removes it.
func (s *spool) writeLocked(lines [][]byte) error {
	if len(lines) == 0 {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	_ = os.Remove(tmp) // a leftover keeps its mode through O_TRUNC
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, l := range lines {
		_, _ = w.Write(l)
		_ = w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// holdsForPhone: the hook would wait on the phone if the manager were there (asksPhone), so in standby it is told to
// retry. A Bash PreToolUse waits only for the assistant's gated commands, which standby cannot tell: it goes on.
func holdsForPhone(in hookInput, req sockRequest) bool {
	if req.ReportOnly || req.Attempt == "" || !asksPhone(in) {
		return false
	}
	return !(in.HookEventName == "PreToolUse" && in.ToolName == "Bash")
}

// spooled says whether standby keeps a hook's event: what the node reports to the manager (notifyHookEvent). A
// resumed hook was reported by the node it first reached; a global report-only one only for the events it reports.
func spooled(in hookInput, req sockRequest) bool {
	if req.Resume || !validID(in.SessionID) {
		return false
	}
	return !req.ReportOnly || reportedEvent(in)
}

// standby answers node.sock while no manager connection is served (Attach). It listens only where nothing else
// answers: a `node --stdio` that serves by itself (no resident node reached in time) keeps the socket, and standby
// takes it once that one is gone.
type standby struct {
	sp   *spool
	logf func(string, ...any)
	now  func() time.Time
	// retry is how often standby looks whether it can (or still does) own node.sock while it is wanted.
	retry time.Duration
	// afterAsk, in tests only, runs between asking whether anything answers on node.sock and taking it.
	afterAsk func()

	mu      sync.Mutex
	wanted  bool
	ln      net.Listener
	lnFile  os.FileInfo // the socket file ln listens on: another one at the path means someone took it
	looping bool
	conns   sync.WaitGroup
	healthy time.Time // the manager was last there for a while (or the resident node started)
}

func newStandby(sp *spool, logf func(string, ...any)) *standby {
	return &standby{sp: sp, logf: logf, now: time.Now, retry: 5 * time.Second}
}

// want makes standby answer node.sock from now on (as soon as nothing else does).
func (s *standby) want() {
	s.mu.Lock()
	s.wanted = true
	if s.healthy.IsZero() {
		s.healthy = s.now() // the resident node starting: its manager is likely on its way
	}
	if !s.looping {
		s.looping = true
		go s.loop()
	}
	s.mu.Unlock()
	s.tryListen()
}

// managerSeen: a manager connection was served for a while, until at; the hold counts from there.
func (s *standby) managerSeen(at time.Time) {
	s.mu.Lock()
	s.healthy = at
	s.mu.Unlock()
}

// unwant stops standby (a manager connection's Node takes node.sock) and waits for the requests it took.
func (s *standby) unwant() {
	s.mu.Lock()
	s.wanted = false
	s.closeLocked()
	s.mu.Unlock()
	s.conns.Wait()
}

func (s *standby) loop() {
	t := time.NewTicker(s.retry)
	defer t.Stop()
	for range t.C {
		if !s.check() {
			return
		}
	}
}

// check is one look of loop: false once standby is no longer wanted (the loop ends).
func (s *standby) check() bool {
	s.mu.Lock()
	if !s.wanted {
		s.looping = false
		s.mu.Unlock()
		return false
	}
	if s.ln != nil && !s.ownsPathLocked() {
		// A node serving by itself took the path, or is taking it (it removes the file, then listens): let it be for
		// this round, and take it back once nothing answers there. Listening again now could beat that node's listen.
		s.closeLocked()
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	s.tryListen()
	return true
}

func (s *standby) closeLocked() {
	if s.ln != nil {
		s.ln.Close()
		s.ln, s.lnFile = nil, nil
	}
}

func (s *standby) ownsPathLocked() bool {
	fi, err := os.Stat(nodeclient.SocketPath())
	return err == nil && s.lnFile != nil && os.SameFile(fi, s.lnFile)
}

// tryListen listens on node.sock unless standby already does or something else answers there. It asks outside mu (a
// ping takes up to 1.3 s, and holding, managerSeen and unwant wait for mu) and looks again under it. It takes only
// what it asked about: the file that did not answer, or a path with nothing there. A node serving by itself may take
// the path meanwhile (remove, then listen); standby removing that node's new file kept the path for good (#1556).
func (s *standby) tryListen() {
	s.mu.Lock()
	skip := !s.wanted || s.ln != nil
	s.mu.Unlock()
	if skip {
		return
	}
	path := nodeclient.SocketPath()
	asked, _ := os.Lstat(path) // nil: nothing there
	if answers(path) {
		return // a live node serves it
	}
	if s.afterAsk != nil {
		s.afterAsk()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.wanted || s.ln != nil {
		return // unwanted meanwhile (a connection's Node takes the socket), or another try listened first
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.logf("standby: %v", err)
		return
	}
	if now, _ := os.Lstat(path); !sameEntry(asked, now) {
		return // a node is taking the path: ask again next time
	} else if now != nil {
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		s.logf("standby: %v", err)
		return
	}
	// Never unlink on close: the next Node listens on the same path, maybe before this one is closed.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = os.Chmod(path, 0o600)
	fi, err := os.Stat(path)
	if err != nil {
		ln.Close()
		s.logf("standby: %v", err)
		return
	}
	s.ln, s.lnFile = ln, fi
	// Ends when closeLocked closes ln; a transient Accept error does not end it (acceptloop.Run).
	go acceptloop.Run(context.Background(), ln, s.logf, func(c net.Conn) {
		s.mu.Lock()
		current := s.ln == ln
		if current {
			s.conns.Add(1) // under mu: unwant's Wait sees every request taken before it closed ln
		}
		s.mu.Unlock()
		if !current {
			c.Close() // ln was closed: the next Accept ends the loop
			return
		}
		go func() {
			defer s.conns.Done()
			s.serve(c)
		}()
	})
}

// sameEntry says whether a and b (from os.Lstat; nil: nothing there) are the same file, or both nothing.
func sameEntry(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b)
}

// answers says whether a node serves path: it answers a ping (a listener that is closing, or a stopped process whose
// backlog still accepts, does not).
func answers(path string) bool {
	c, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte(`{"kind":"ping"}` + "\n")); err != nil {
		return false
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	return err == nil && len(line) > 1
}

func (s *standby) listening() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ln != nil
}

// holding: the manager was there less than standbyHold ago, so a hook that would wait on the phone is told to retry
// (each one for at most nodeclient.ResumeWait). A manager that only flaps (connections shorter than a minute) does not
// renew it.
func (s *standby) holding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Sub(s.healthy) < standbyHold
}

func (s *standby) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second)) // a request is one short line; unwant waits for this
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return
	}
	var req sockRequest
	if json.Unmarshal(line, &req) != nil {
		return
	}
	b, _ := json.Marshal(s.answer(req))
	_, _ = c.Write(append(b, '\n'))
}

// answer is standby's reply to req (one line on the socket, what a Node would answer when it could).
func (s *standby) answer(req sockRequest) any {
	switch req.Kind {
	case "hook":
		var in hookInput
		if json.Unmarshal(req.Input, &in) != nil {
			return map[string]any{"output": nil}
		}
		if spooled(in, req) {
			s.keep("hook", req)
		}
		if holdsForPhone(in, req) && s.holding() {
			return map[string]any{"retry": true} // the hook asks again; the manager is likely back soon
		}
		return map[string]any{"output": nil} // the terminal decides, as with no manager at all
	case "hook.decided":
		return map[string]any{"ok": true, "standby": true}
	case "ping": // which program and build answer, as a Node's does: persona-link node switch waits for it
		return map[string]any{"ok": true, "standby": true, "program": residentProgram.Load(), "version": residentVersion.Load()}
	case "ask":
		if req.Attempt != "" && s.holding() {
			return nodeclient.AskReply{Retry: true}
		}
		return nodeclient.AskReply{Status: nodeclient.AskUnavailable, Error: standbyAway}
	case "notify":
		var nr nodeclient.NotifyRequest
		if json.Unmarshal(req.Input, &nr) != nil {
			return nodeclient.NotifyReply{Error: "bad request"}
		}
		if strings.TrimSpace(nr.Title) == "" && strings.TrimSpace(nr.Body) == "" {
			return nodeclient.NotifyReply{Error: "the notification is empty"}
		}
		if err := s.keep("notify", req); err != nil {
			return nodeclient.NotifyReply{Error: "could not keep it for the manager: " + err.Error()}
		}
		return nodeclient.NotifyReply{Queued: true}
	case "mcp.tools": // an older `personad mcp` (no away_ok): what a node says when it cannot reach its manager
		if !req.AwayOK {
			return map[string]any{"tools": []proto.Tool{}, "error": standbyAway}
		}
	case "mcp.call":
		if !req.AwayOK {
			return proto.TextResult("Persona hub error: "+standbyAway+"; the call was not run", true)
		}
	}
	return map[string]any{"away": true} // the client takes it for no node (nodeclient.dial)
}

func (s *standby) keep(kind string, req sockRequest) error {
	dropped, err := s.sp.add(spoolEntry{ID: newUUID(), At: s.now().UTC(), Kind: kind, Request: req})
	if err != nil {
		s.logf("standby: keep %s: %v", kind, err)
	} else if dropped > 0 {
		s.logf("standby: spool full, dropped the %d oldest", dropped)
	}
	return err
}

// replaySpool hands the manager what standby took, in order, then opens the gate for new hooks (n.replayed).
//
// It waits for the session state first (takesReport needs it: an empty state takes every session for one Persona does
// not report for, and a notification's pane for no session). When the first try of the state fails, the replay is held
// until a later try brings it (reloadState), and the gate opens at once: new hooks never wait for the hold. A replay
// that runs after new hooks went ahead of it leaves what they moved on alone (replayAhead).
//
// An entry goes from the file once it was sent; a hook event that could not be sent stops the replay (the connection is
// gone), and the rest stays for the next one. A notification the manager refused, did not answer, or could not take
// now stays too; retry says whether one is left for retryNotifications on this connection. One the manager got twice
// (the node stopped between sending and removing) it drops by spool_id.
func (n *Node) replaySpool(ctx context.Context, sp *spool) (retry bool) {
	var once sync.Once
	openGate := func() { once.Do(func() { close(n.replayed) }) }
	defer openGate()
	defer n.ahead.finish() // before the gate opens: hooks after it are not ahead
	if len(sp.load()) == 0 {
		return false
	}
	select {
	case <-n.stateReady:
	case <-n.stateMissed:
		n.logger.Printf("spool: the session state did not come from the manager; %d kept until it does", len(sp.load()))
		openGate()
		select {
		case <-n.stateReady:
		case <-ctx.Done():
			return false
		}
	case <-ctx.Done():
		return false
	}
	sp.replayMu.Lock() // the last connection's replay may still be on its last entry
	defer sp.replayMu.Unlock()
	sent, stale, ahead := 0, 0, 0
	later := false // a notification timed out, or the hub is away: the others wait for the retries, not the gate
	defer func() {
		if sent+stale+ahead > 0 {
			n.logger.Printf("spool: replayed %d taken while the manager was away (%d too old, %d behind new hooks: dropped)", sent, stale, ahead)
		}
	}()
	for _, e := range sp.load() {
		if ctx.Err() != nil {
			return false
		}
		switch {
		case time.Since(e.At) > spoolMaxAge:
			stale++
		case e.Kind == "hook" && n.ahead.behind(e):
			ahead++ // its session had a new hook since: older news than what the manager heard
		case e.Kind == "hook":
			if err := n.replayHook(e); err != nil {
				return false // a hook.event write fails only when the connection is gone: all kept for the next one
			}
			sent++
		case e.Kind == "notify":
			if later {
				retry = true // kept, untried, for the retries
				continue
			}
			switch r := n.replayNotify(ctx, sp, e); r {
			case notifyGone:
				return false // it and the rest stay for the next connection, no try counted
			case notifySent:
				sent++
			default:
				// Kept (the rest goes on, so the hook events after it keep their order), or dropped.
				retry = retry || r != notifyDropped
				later = later || r == notifyTimedOut || r == notifyHubAway
				continue
			}
		}
		if err := sp.remove(e.ID); err != nil {
			n.logger.Printf("spool: %v", err)
		}
	}
	return retry
}

// replayAhead is what live hooks did on a connection before its replay was done: hooks go first when the replay is
// held for the session state, or when one waited out replayGateMost. The replay then drops the kept events of a
// session that had a new hook (the manager heard newer news of it), and binds no pane a new hook bound to the session
// it had before.
type replayAhead struct {
	mu       sync.Mutex
	done     bool
	sessions map[string]bool
	panes    map[string]bool
}

// note records a live hook that passed the gate (serveSock), unless the replay is done. A resumed hook is not news: it
// asks again for an event reported already, or kept by standby (which told it to retry), whose kept copy the replay
// still has to hand over. A report-only hook binds no pane, so its pane is not taken.
func (a *replayAhead) note(req sockRequest) {
	var in hookInput
	if req.Resume || json.Unmarshal(req.Input, &in) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	if validID(in.SessionID) {
		if a.sessions == nil {
			a.sessions = map[string]bool{}
		}
		a.sessions[in.SessionID] = true
	}
	if req.Pane != "" && !req.ReportOnly {
		if a.panes == nil {
			a.panes = map[string]bool{}
		}
		a.panes[req.Pane] = true
	}
}

// behind: a kept hook event of a session that had a live hook since.
func (a *replayAhead) behind(e spoolEntry) bool {
	var in hookInput
	if json.Unmarshal(e.Request.Input, &in) != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[in.SessionID]
}

// paneTaken: a live hook bound pane since.
func (a *replayAhead) paneTaken(pane string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.panes[pane]
}

func (a *replayAhead) finish() {
	a.mu.Lock()
	a.done, a.sessions, a.panes = true, nil, nil
	a.mu.Unlock()
}

// What became of one replayed notification.
type notifyOutcome int

const (
	notifySent     notifyOutcome = iota // handed over: it leaves the spool
	notifyGone                          // the connection is gone: kept, no try counted
	notifyBusy                          // the manager still has an earlier try (nodeproto.ErrSpoolBusy): kept, no try counted
	notifyHubAway                       // the manager cannot reach the hub now (nodeproto.ErrHubAway): kept, no try counted
	notifyRefused                       // refused: kept, a try counted
	notifyTimedOut                      // not answered in replayNotifyTimeout: kept, a try counted
	notifyDropped                       // refused for good, or once too often: gone from the spool
)

// replayNotify hands the manager one kept notification. It counts a failed try (sp.failed) when it was refused or not
// answered, and drops one that can never be sent; it does not remove a sent one.
func (n *Node) replayNotify(ctx context.Context, sp *spool, e spoolEntry) notifyOutcome {
	cctx, cancel := context.WithTimeout(ctx, replayNotifyTimeout)
	req := e.Request
	req.Pane = n.ownPane(req.Pane) // kept as the client sent it (serveSock translates live ones)
	rep, err := n.notifyCall(cctx, req, e.ID)
	timedOut := cctx.Err() != nil && ctx.Err() == nil
	cancel()
	if err == nil && rep.Error != "" { // refused before the manager was asked (empty, not JSON): never sendable
		n.logger.Printf("spool: notification %s dropped: %s", e.ID, rep.Error)
		if rerr := sp.remove(e.ID); rerr != nil {
			n.logger.Printf("spool: %v", rerr)
		}
		return notifyDropped
	}
	var refused *rpc.Error
	var r notifyOutcome
	switch {
	case err == nil:
		return notifySent
	case errors.Is(err, rpc.ErrClosed):
		return notifyGone
	case errors.As(err, &refused) && refused.Code == nodeproto.ErrSpoolBusy:
		return notifyBusy
	case errors.As(err, &refused) && refused.Code == nodeproto.ErrHubAway:
		return notifyHubAway
	case errors.As(err, &refused):
		r = notifyRefused
	case timedOut && !n.peerGone(ctx):
		r = notifyTimedOut
	default:
		return notifyGone // the connection went (maybe right at the deadline)
	}
	drop, ferr := sp.failed(e.ID)
	n.logger.Printf("spool: notification %s not handed over (%v); dropped: %v %v", e.ID, err, drop, ferr)
	if drop {
		return notifyDropped
	}
	return r
}

// peerGone says whether the connection ended, waiting replayGoneGrace for a close that came with a timeout.
func (n *Node) peerGone(ctx context.Context) bool {
	t := time.NewTimer(replayGoneGrace)
	defer t.Stop()
	select {
	case <-n.peer.Done():
		return true
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

// retryWait is the wait before try i+2 of something this connection tries again (replayRetryAfter).
func retryWait(i int) time.Duration { return replayRetryAfter[min(i, len(replayRetryAfter)-1)] }

// sleepCtx waits d, false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// retryNotifications tries again, on this connection, the notifications its replay kept: after each wait of
// replayRetryAfter (the last one repeating), until none is left or the connection ends. Refusals count, so one goes
// after spoolTriesMost of them; "not now" answers (the manager still sending it, or away from the hub) do not, and
// only the age limit (spoolMaxAge, from when standby took it) bounds those. New hooks do not wait for this: the gate
// opened with the replay.
func (n *Node) retryNotifications(ctx context.Context, sp *spool) {
	for i := 0; ; i++ {
		if !sleepCtx(ctx, retryWait(i)) || !n.retryNotificationsOnce(ctx, sp) {
			return
		}
	}
}

// retryNotificationsOnce is one round of retryNotifications; it says whether one is left to try again.
func (n *Node) retryNotificationsOnce(ctx context.Context, sp *spool) (again bool) {
	sp.replayMu.Lock()
	defer sp.replayMu.Unlock()
	for _, e := range sp.load() {
		if e.Kind != "notify" {
			continue // a hook entry left by a connection that dropped: the next connection's replay has it
		}
		if ctx.Err() != nil {
			return false
		}
		if time.Since(e.At) > spoolMaxAge {
			n.logger.Printf("spool: notification %s too old, dropped unsent", e.ID)
			if err := sp.remove(e.ID); err != nil {
				n.logger.Printf("spool: %v", err)
			}
			continue
		}
		switch n.replayNotify(ctx, sp, e) {
		case notifyGone:
			return false
		case notifySent:
			n.logger.Printf("spool: notification %s handed over on a retry", e.ID)
			if err := sp.remove(e.ID); err != nil {
				n.logger.Printf("spool: %v", err)
			}
		case notifyBusy, notifyRefused:
			again = true
		case notifyTimedOut, notifyHubAway:
			return true // the manager cannot take any now: the rest waits for the next round, untried
		}
	}
	return again
}

// replayHook is what handleHook does with an event before it asks the phone (bookkeeping and the report), for a hook
// that standby answered: no card, it is long over or asks again by itself.
func (n *Node) replayHook(e spoolEntry) error {
	req := e.Request
	req.Pane = n.ownPane(req.Pane)
	var in hookInput
	if json.Unmarshal(req.Input, &in) != nil {
		return nil
	}
	if req.ReportOnly && !n.takesReport(in) {
		return nil
	}
	stopSig := n.recordHookTurn(req, in, e.At)
	if !req.ReportOnly {
		if paneKindOK(n, req.Pane) && !n.ahead.paneTaken(req.Pane) {
			n.bindPane(req.Pane, in.SessionID)
		}
	}
	n.rememberTranscript(in)
	p := hookEventParams(in, stopSig, req.ReportOnly)
	p["spool_id"], p["spooled_at"] = e.ID, e.At
	return n.peer.Notify("hook.event", p)
}
