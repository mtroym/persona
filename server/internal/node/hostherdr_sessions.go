package node

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// The account's other herdr sessions (#2196, herdr/sessions.go). A node on the default herdr server drives every
// running named session too (`herdr --session <name>`): the herdr host lists their agents next to its own server's
// and sends each call to the session its id names. Their ids are "@<session>:<id>" (herdr.QualifyID), the own server's
// stay as they always were, so nothing stored or sent before changes. Left alone: stopped sessions, the sessions of
// other nodes' data directories (persona-<hash>, session.go), and every other session on a node that runs on a session
// of its own (it is not the user's everyday node). Pane-less work (new workspaces) stays on the own server.
//
// A named session is asked alongside the own server, never in its way: what it has not answered within namedWait is
// taken from its last answer, and it is not asked again while a call to it is still out, so a server that hangs costs
// one call at a time and never holds up the own server's list.

// sessionsEvery is how long the list of running sessions is used before herdr is asked again.
const sessionsEvery = 5 * time.Second

// namedWait is how long a listing waits for the named sessions, from its start: for their list (the first time) and
// their answers.
var namedWait = 2 * time.Second

// lookupWait is how long a call on an id of a session not known waits for herdr's list of sessions (`herdr session
// list` gives up after 5 s).
const lookupWait = 6 * time.Second

// namedCallTimeout bounds one call to a named session that goes on after the listing stopped waiting for it.
const namedCallTimeout = 15 * time.Second

// herdrSessions are the running named sessions a herdr host drives besides its own server.
type herdrSessions struct {
	own  *herdr.Client
	list func(ctx context.Context) ([]herdr.SessionInfo, error) // herdr session list
	logf func(format string, args ...any)                       // nil: silent

	mu       sync.Mutex
	at       time.Time     // the last time herdr answered or failed to (zero: never)
	listing  chan struct{} // closed when the ask now out ends; nil: none out
	sessions map[string]*herdrSession
	skipped  map[string]bool // names logged once as left alone
	fails    herdr.LogBackoff
}

// herdrSession is one named session: its client and its last answers.
type herdrSession struct {
	name string
	c    *herdr.Client
	busy atomic.Bool // a call to it is out

	mu     sync.Mutex
	layout *host.Layout // its last snapshot, ids qualified; nil: none yet
	spaces []host.Space // its last workspace list
}

// newHerdrSessions are the named sessions next to own: nil when own is not the default server at its usual socket (a
// node on a session of its own, or pointed elsewhere by HERDR_SOCKET_PATH) or on Windows (herdr's named-pipe layout
// there is not checked).
func newHerdrSessions(own *herdr.Client, logf func(format string, args ...any)) *herdrSessions {
	if runtime.GOOS == "windows" || !own.OnDefaultServer() {
		return nil
	}
	return &herdrSessions{own: own, list: own.ListSessions, logf: logf}
}

func (s *herdrSessions) log(format string, args ...any) {
	if s.logf != nil {
		s.logf(format, args...)
	}
}

// running are the running named sessions, by name. A list sessionsEvery old is asked for again in the background
// (callers get it as it was meanwhile); only the very first call waits for herdr's answer, until until at most.
func (s *herdrSessions) running(ctx context.Context, until time.Time) []*herdrSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	first := s.at.IsZero()
	var done chan struct{}
	if first || time.Since(s.at) >= sessionsEvery {
		done = s.listLocked(ctx)
	}
	s.mu.Unlock()
	if first {
		waitUntil(ctx, done, until)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

// lookup is the running named session called name. One not known is looked for again, at most once a second (it may
// have just started), and waited for while an ask is out (its answer may have it).
func (s *herdrSessions) lookup(ctx context.Context, name string) *herdrSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	ns := s.sessions[name]
	var done chan struct{}
	if ns == nil && (s.listing != nil || time.Since(s.at) >= time.Second) {
		done = s.listLocked(ctx)
	}
	s.mu.Unlock()
	if done == nil {
		return ns
	}
	waitUntil(ctx, done, time.Now().Add(lookupWait))
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[name]
}

// listLocked asks herdr for its sessions in the background unless an ask is out already, and returns the channel
// closed when the ask out ends. s.mu is held.
func (s *herdrSessions) listLocked(ctx context.Context) chan struct{} {
	if s.listing != nil {
		return s.listing
	}
	done := make(chan struct{})
	s.listing = done
	go func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), namedCallTimeout)
		defer cancel()
		s.refresh(cctx)
		s.mu.Lock()
		s.listing = nil
		s.mu.Unlock()
		close(done)
	}()
	return done
}

// waitUntil waits for done, at most until until (and while ctx lasts).
func waitUntil(ctx context.Context, done <-chan struct{}, until time.Time) {
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
}

// refresh asks herdr for its sessions and keeps the running named ones it may drive; a failed ask keeps the ones it
// had.
func (s *herdrSessions) refresh(ctx context.Context) {
	infos, err := s.list(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at = time.Now()
	if err != nil {
		if s.fails.Due(time.Now()) {
			s.log("herdr sessions: %v (the named sessions stay as they were)", err)
		}
		return
	}
	s.fails.Reset()
	next := map[string]*herdrSession{}
	for _, info := range infos {
		if info.Default || !info.Running || info.Name == "" {
			continue
		}
		why := ""
		switch {
		case herdr.IsOwnSession(info.Name):
			continue // another node's data directory: that node drives it
		case !herdr.ValidSessionName(info.Name):
			why = "its name cannot stand in an id"
		case info.Socket != "" && sameSocket(info.Socket, s.own.Socket):
			continue
		}
		if why != "" {
			if !s.skipped[info.Name] {
				if s.skipped == nil {
					s.skipped = map[string]bool{}
				}
				s.skipped[info.Name] = true
				s.log("herdr sessions: %q left alone: %s", info.Name, why)
			}
			continue
		}
		if ns := s.sessions[info.Name]; ns != nil && (info.Socket == "" || sameSocket(ns.c.Socket, info.Socket)) {
			next[info.Name] = ns
			continue
		}
		c := s.own.ForSession(info.Name, info.Socket)
		if s.logf != nil {
			name, logf := info.Name, s.logf
			c.Log = func(format string, args ...any) { logf("[herdr session %s] "+format, append([]any{name}, args...)...) }
		}
		next[info.Name] = &herdrSession{name: info.Name, c: c}
	}
	for name := range next {
		if s.sessions[name] == nil {
			s.log("herdr sessions: driving session %q too", name)
		}
	}
	for name := range s.sessions {
		if next[name] == nil {
			s.log("herdr sessions: session %q is gone", name)
		}
	}
	s.sessions = next
}

func (s *herdrSessions) sortedLocked() []*herdrSession {
	out := make([]*herdrSession, 0, len(s.sessions))
	for _, name := range slices.Sorted(maps.Keys(s.sessions)) {
		out = append(out, s.sessions[name])
	}
	return out
}

func sameSocket(a, b string) bool {
	return a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b)
}

func (ns *herdrSession) lastLayout() (*host.Layout, bool) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	return ns.layout, ns.layout != nil
}

func (ns *herdrSession) lastSpaces() ([]host.Space, bool) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	if ns.spaces == nil && ns.layout != nil {
		return ns.layout.Spaces, true
	}
	return ns.spaces, ns.spaces != nil
}

// fromNamed asks every named session at once (get) and gives each one's answer as it comes by until, else its last
// one (last; nothing for a session that never answered). A session with a call still out is not asked again.
func fromNamed[T any](ctx context.Context, until time.Time, sessions []*herdrSession, get func(context.Context, *herdrSession) (T, error), last func(*herdrSession) (T, bool)) []T {
	type answer struct {
		i  int
		v  T
		ok bool
	}
	answers := make(chan answer, len(sessions))
	asked := 0
	for i, ns := range sessions {
		if !ns.busy.CompareAndSwap(false, true) {
			continue
		}
		asked++
		go func() {
			defer ns.busy.Store(false)
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), namedCallTimeout)
			defer cancel()
			v, err := get(cctx, ns)
			answers <- answer{i: i, v: v, ok: err == nil}
		}()
	}
	got := make([]*T, len(sessions))
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
wait:
	for ; asked > 0; asked-- {
		select {
		case a := <-answers:
			if a.ok {
				got[a.i] = &a.v
			}
		case <-timer.C:
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	out := make([]T, 0, len(sessions))
	for i, ns := range sessions {
		if got[i] != nil {
			out = append(out, *got[i])
		} else if v, ok := last(ns); ok {
			out = append(out, v)
		}
	}
	return out
}

// herdrRoute is where an id of the herdr host lives: the client of its session, the session ("" the own server) and
// herdr's own id there.
type herdrRoute struct {
	c       *herdr.Client
	session string
	id      string
}

// qualify writes an id herdr gave in the target's session the way the host hands ids out.
func (t herdrRoute) qualify(id string) string { return herdr.QualifyID(t.session, id) }

// errNoSuchSession: an id names a herdr session this node does not drive (stopped, or never one of its own).
var errNoSuchSession = errors.New("herdr session not running here")

// target is where id lives: a plain id is the own server's; "@<session>:<id>" one of a running named session.
func (h *herdrHost) target(ctx context.Context, id string) (herdrRoute, error) {
	session, bare := herdr.SplitID(id)
	if session == "" {
		return herdrRoute{c: h.c, id: id}, nil
	}
	ns := h.named.lookup(ctx, session)
	if ns == nil {
		return herdrRoute{}, fmt.Errorf("%w: %q (%s)", errNoSuchSession, session, id)
	}
	return herdrRoute{c: ns.c, session: session, id: bare}, nil
}

// targets is target for a pane and the tab / workspace ids that go with it, which must be of the same session.
func (h *herdrHost) targets(ctx context.Context, pane string, ids ...string) (herdrRoute, []string, error) {
	t, err := h.target(ctx, pane)
	if err != nil {
		return t, nil, err
	}
	bare := make([]string, len(ids))
	for i, id := range ids {
		session, b := herdr.SplitID(id)
		if id != "" && session != t.session {
			return t, nil, fmt.Errorf("herdr: %s and %s are in different sessions", pane, id)
		}
		bare[i] = b
	}
	return t, bare, nil
}

// ownPane is a pane a hook, a CLI or a runtime sent — written as the default server sees it: its own panes bare,
// another session's qualified (nodeclient.HookPane) — as this node's herdr host writes it. That is the same on a node
// on the default server; a node whose own server is a named session takes that session off its own panes and
// qualifies the default server's ("@default:…", which it does not drive).
func (n *Node) ownPane(pane string) string {
	h, ok := n.hosts.Get(host.Herdr).(*herdrHost)
	if !ok || pane == "" || host.ParsePane(pane).Kind != host.Herdr {
		return pane
	}
	own := h.ownSession()
	if own == "" {
		return pane
	}
	if bare, ok := strings.CutPrefix(pane, "@"+own+":"); ok && bare != "" { // whatever the name: HERDR_SESSION is not checked
		return bare
	}
	if strings.HasPrefix(pane, "@") { // another session's
		return pane
	}
	return herdr.PaneOf(pane, "", own)
}

// envPane is the herdr pane a process's environment names (HERDR_PANE_ID, its session from HERDR_SESSION or
// HERDR_SOCKET_PATH: herdr.EnvSession) as this node's herdr host writes it; "" when it names none.
func (n *Node) envPane(getenv func(string) string) string {
	return n.herdrPaneIn(getenv(herdrPaneEnv), herdr.EnvSession(getenv))
}

// herdrPaneIn is herdr's pane id pane of session ("" the default server) as this node's herdr host writes it.
func (n *Node) herdrPaneIn(pane, session string) string {
	own := ""
	if h, ok := n.hosts.Get(host.Herdr).(*herdrHost); ok {
		own = h.ownSession()
	}
	return herdr.PaneOf(pane, session, own)
}

// ownSession is the session of the host's own server ("" the default one): what a process's HERDR_SESSION is in its
// panes, which the host writes without a session.
func (h *herdrHost) ownSession() string { return h.c.OwnName() }

// sessionLabel is a named session's workspace label as the host shows it: "<session> · <label>", so the apps say
// which session a space is in; the own server's as it is.
func sessionLabel(session, label string) string {
	if session == "" {
		return label
	}
	if label == "" {
		return session
	}
	return session + sessionLabelSep + label
}

const sessionLabelSep = " · "
