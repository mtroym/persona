package node

import (
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// runtimeRegistry is the node's registry of connected runtimes.
type runtimeRegistry struct {
	mu     sync.Mutex
	serial int
	conns  map[int]*runtimeConn
	// owner: session id -> the runtime listing it (the newest connection wins when two list the same id).
	owner map[string]*runtimeConn
	// watching: sessions the hub watches -> the last seq pushed for each, so a runtime that reconnects is asked to
	// watch them again from where the phone is.
	watching map[string]int
	// known: session id -> the runtime that listed it or said it has it, kept after it leaves the list. A runtime
	// lists only its recent sessions (DSH: the latest 80 of thousands), but an older one is still its own: calls
	// about it go to that runtime, never to the terminal host as a Claude session to resume (that sent a DSH
	// session's message to herdr, on a machine without herdr).
	known map[string]knownSession
	// unclaimed: ids no connected runtime had when asked, and until when not to ask again.
	unclaimed map[string]time.Time
	// logf reports what an operator should fix (a launcher two runtimes offer); nil: nowhere. warned: said once each.
	logf   func(format string, args ...any)
	warned map[string]bool
}

// knownSession is the runtime a session belongs to, by the runtime's id (a reconnect is the same runtime).
type knownSession struct {
	runtime, label string
	at             time.Time
}

// maxKnown bounds the remembered sessions; the oldest go first.
const maxKnown = 8192

func newRuntimeRegistry() *runtimeRegistry {
	return &runtimeRegistry{conns: map[int]*runtimeConn{}, owner: map[string]*runtimeConn{}, watching: map[string]int{},
		known: map[string]knownSession{}, unclaimed: map[string]time.Time{}, warned: map[string]bool{}}
}

// warnOnce logs a message once per key. Callers hold b.mu.
func (b *runtimeRegistry) warnOnce(key, format string, args ...any) {
	if b.logf == nil || b.warned[key] {
		return
	}
	b.warned[key] = true
	b.logf(format, args...)
}

// besideShell drops the runtimes' launchers named like one of the login shell's (shell): a session.start with that
// name runs the shell's (runtimeStart), so the form lists it once. Logged once per name.
func (b *runtimeRegistry) besideShell(runtime, shell []proto.Launcher) []proto.Launcher {
	names := map[string]bool{}
	for _, l := range shell {
		names[l.Name] = true
	}
	out := make([]proto.Launcher, 0, len(runtime))
	for _, l := range runtime {
		if names[l.Name] {
			b.mu.Lock()
			b.warnOnce("shell launcher "+l.Name, "runtime %s: launcher %q has the name of a launcher in the login shell, which runs instead", l.Source, l.Name)
			b.mu.Unlock()
			continue
		}
		out = append(out, l)
	}
	return out
}

// shellLauncherNames are the login shell's launcher names as last listed (none before the first list; nothing is run
// to get them).
func shellLauncherNames() map[string]bool {
	launcherCache.Lock()
	defer launcherCache.Unlock()
	names := make(map[string]bool, len(launcherCache.list))
	for _, l := range launcherCache.list {
		names[l.Name] = true
	}
	return names
}

// hello records h's runtime.hello unless another process holds the same runtime id. A ready connection with that id
// and another instance is a second process configured alike: refused with runtime_conflict (one of them needs another
// id). One with the same instance is this process's earlier connection — it reconnected before the node saw the old
// socket close — returned as stale for the caller to close. A runtime that sends no instance (an older SDK) is let in
// as before: the newest connection answers for the id.
//
// The ids in daemons.json (reserved) are the daemons' (daemons.go): a runtime on the socket may not take one, and a
// daemon whose id a runtime on the socket already has stays out until that one leaves (it is dialed again). A daemon's
// link says hello with the config's id and an instance of its own, so its next connection is its own again.
func (b *runtimeRegistry) hello(h *runtimeConn, hello runtimeHello, reserved func(string) bool) (stale []*runtimeConn, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	daemon := h.daemon
	h.mu.Unlock()
	switch {
	case daemon != "" && hello.Runtime != daemon:
		return nil, &rpc.Error{Code: "runtime_conflict", Message: fmt.Sprintf("daemon %s said hello as %q", daemon, hello.Runtime)}
	case daemon == "" && reserved != nil && reserved(hello.Runtime):
		return nil, &rpc.Error{Code: "runtime_conflict",
			Message: fmt.Sprintf("runtime id %q is reserved by a daemon in daemons.json; give this runtime another id", hello.Runtime)}
	}
	for _, o := range b.conns {
		if o == h {
			continue
		}
		o.mu.Lock()
		same := o.ready && o.hello.Runtime == hello.Runtime
		instance := o.hello.Instance
		onSocket := o.daemon == ""
		o.mu.Unlock()
		if !same {
			continue
		}
		if daemon != "" && onSocket {
			return nil, &rpc.Error{Code: "runtime_conflict",
				Message: fmt.Sprintf("runtime id %q is in use by a runtime on this machine's socket", hello.Runtime)}
		}
		if instance == "" || hello.Instance == "" {
			continue
		}
		if instance != hello.Instance {
			return nil, &rpc.Error{Code: "runtime_conflict",
				Message: fmt.Sprintf("another process is connected to this node as runtime %q", hello.Runtime)}
		}
		stale = append(stale, o)
	}
	h.mu.Lock()
	h.hello, h.ready = hello, true
	h.mu.Unlock()
	return stale, nil
}

// remember records that id belongs to h's runtime. Callers hold b.mu and h.mu.
func (b *runtimeRegistry) remember(h *runtimeConn, id string, now time.Time) {
	label := h.hello.Name
	if label == "" {
		label = h.hello.Runtime
	}
	b.known[id] = knownSession{runtime: h.hello.Runtime, label: label, at: now}
	delete(b.unclaimed, id)
	if len(b.known) > maxKnown {
		oldest := make([]string, 0, len(b.known))
		for k := range b.known {
			oldest = append(oldest, k)
		}
		sort.Slice(oldest, func(i, j int) bool { return b.known[oldest[i]].at.Before(b.known[oldest[j]].at) })
		for _, k := range oldest[:len(oldest)-maxKnown*3/4] {
			delete(b.known, k)
		}
	}
}

// byRuntime is the newest ready connection of the runtime with this id, nil when it is not connected. Callers hold b.mu.
func (b *runtimeRegistry) byRuntime(rt string) *runtimeConn {
	var best *runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		ok := h.ready && h.hello.Runtime == rt
		h.mu.Unlock()
		if ok && (best == nil || h.serial > best.serial) {
			best = h
		}
	}
	return best
}

// belongs is the runtime a session belongs to: the connection listing it, else the connected runtime that listed it
// before; known: it belongs to a runtime (connected or not), with that runtime's name. A subagent of a runtime's
// session ("<parent>@<child>", splitRuntimeChild) belongs to the parent's runtime, which answers for it.
func (b *runtimeRegistry) belongs(id string) (h *runtimeConn, label string, known bool) {
	if b == nil || id == "" {
		return nil, "", false
	}
	if parent, _, ok := splitRuntimeChild(id); ok {
		id = parent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if o := b.owner[id]; o != nil {
		return o, "", true
	}
	k, ok := b.known[id]
	if !ok {
		return nil, "", false
	}
	return b.byRuntime(k.runtime), k.label, true
}

// owns: the session's calls and pushes are h's (it lists the session, or listed or claimed it before).
func (b *runtimeRegistry) owns(h *runtimeConn, id string) bool {
	got, _, _ := b.belongs(id)
	return got != nil && got == h
}

// claim records that h has the session (it answered for it).
func (b *runtimeRegistry) claim(h *runtimeConn, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	b.remember(h, id, time.Now())
	h.mu.Unlock()
}

// askable are the connections to ask about a session nobody claimed yet, newest first; none while the id was asked
// about lately.
func (b *runtimeRegistry) askable(id string) []*runtimeConn {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if until, ok := b.unclaimed[id]; ok && time.Now().Before(until) {
		return nil
	}
	var out []*runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		if h.ready {
			out = append(out, h)
		}
		h.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].serial > out[j].serial })
	return out
}

// unclaim: no runtime had the session; it is not asked about again for a minute.
func (b *runtimeRegistry) unclaim(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, until := range b.unclaimed {
		if time.Now().After(until) {
			delete(b.unclaimed, k)
		}
	}
	b.unclaimed[id] = time.Now().Add(time.Minute)
}

// ready are the connections that said hello.
func (b *runtimeRegistry) ready() []*runtimeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		if h.ready {
			out = append(out, h)
		}
		h.mu.Unlock()
	}
	return out
}

// resetWatches forgets what the hub watched (another hub connection, or none, from now on). Runtimes are not asked to
// stop: an unwatch could reach one after the next hub has watched the session again, and what they push meanwhile is
// harmless (the hub drops items nobody subscribed to).
func (b *runtimeRegistry) resetWatches() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.watching = map[string]int{}
	for _, h := range b.conns {
		h.mu.Lock()
		h.watched = map[string]bool{}
		h.mu.Unlock()
	}
}

// assistantIn is the connected runtime that runs assistant conversations (caps.assistant in its hello) whose process
// is one of pids, nil when none is.
func (b *runtimeRegistry) assistantIn(pids []int) *runtimeConn {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		h.mu.Lock()
		ok := h.ready && h.hello.Caps.Assistant && h.pid > 0 && slices.Contains(pids, h.pid)
		h.mu.Unlock()
		if ok {
			return h
		}
	}
	return nil
}

func (b *runtimeRegistry) add(peer *rpc.Peer) *runtimeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.serial++
	h := &runtimeConn{serial: b.serial, peer: peer, watched: map[string]bool{}}
	b.conns[h.serial] = h
	return h
}

func (b *runtimeRegistry) remove(h *runtimeConn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.conns, h.serial)
	b.recomputeOwners()
}

// recomputeOwners picks, for every listed session, the connection that answers for it. Two runtimes can list the
// same session: pi's web server lists sessions it can open from disk (status offline) while a pi in a terminal has
// one of them running. The one running it wins — a send must not reach a second writer of a file pi does not lock —
// and among equals the newest connection (a runtime that reconnected). Callers hold b.mu.
func (b *runtimeRegistry) recomputeOwners() {
	type claim struct {
		h    *runtimeConn
		live bool
	}
	best := map[string]claim{}
	for _, h := range b.conns {
		h.mu.Lock()
		for _, s := range h.sessions {
			c := claim{h: h, live: s.Status != proto.StatusOffline}
			o, seen := best[s.SessionID]
			if !seen || c.live && !o.live || c.live == o.live && h.serial > o.h.serial {
				best[s.SessionID] = c
			}
		}
		h.mu.Unlock()
	}
	b.owner = make(map[string]*runtimeConn, len(best))
	for id, c := range best {
		b.owner[id] = c.h
	}
}

// setSessions records a runtime's list and returns the watched sessions this connection has not been asked to watch
// yet (with the seq to start after).
func (b *runtimeRegistry) setSessions(h *runtimeConn, list []proto.Session) map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	h.sessions = list
	h.mu.Unlock()
	b.recomputeOwners()
	rewatch := map[string]int{}
	now := time.Now()
	h.mu.Lock()
	for _, s := range list {
		if b.owner[s.SessionID] != h {
			continue // another runtime has it running (recomputeOwners)
		}
		b.remember(h, s.SessionID, now)
		if since, ok := b.watching[s.SessionID]; ok && !h.watched[s.SessionID] {
			h.watched[s.SessionID] = true
			rewatch[s.SessionID] = since
		}
	}
	h.mu.Unlock()
	return rewatch
}

func (b *runtimeRegistry) ownerOf(id string) *runtimeConn {
	if b == nil || id == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.owner[id]
}

// sessions lists every connected runtime's sessions, each id once.
func (b *runtimeRegistry) sessions() []proto.Session {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	conns := make([]*runtimeConn, 0, len(b.conns))
	for _, h := range b.conns {
		conns = append(conns, h)
	}
	owner := make(map[string]*runtimeConn, len(b.owner))
	for id, h := range b.owner {
		owner[id] = h
	}
	b.mu.Unlock()
	sort.Slice(conns, func(i, j int) bool { return conns[i].serial < conns[j].serial })
	var out []proto.Session
	for _, h := range conns {
		h.mu.Lock()
		for _, s := range h.sessions {
			if owner[s.SessionID] == h {
				out = append(out, s)
			}
		}
		h.mu.Unlock()
	}
	return out
}

// launcherOwners maps each launcher name the runtimes offer to the one connection that answers for it: the one
// connected first, so the form and session.start agree and do not change while both stay connected. The others'
// launcher of that name is not offered (logged once). Callers hold b.mu.
func (b *runtimeRegistry) launcherOwners() map[string]*runtimeConn {
	conns := make([]*runtimeConn, 0, len(b.conns))
	for _, h := range b.conns {
		conns = append(conns, h)
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].serial < conns[j].serial })
	owners := map[string]*runtimeConn{}
	ownerIDs := map[string]string{}
	for _, h := range conns {
		h.mu.Lock()
		rt, ok := h.hello.Runtime, h.ready && h.hello.Caps.Start
		h.mu.Unlock()
		if !ok {
			continue
		}
		for _, l := range h.launcherList() {
			name := l.Name
			if owners[name] != nil {
				b.warnOnce(fmt.Sprintf("launcher %d %s", h.serial, name), "runtime %s #%d: launcher %q is offered by runtime %s already; not offered twice",
					rt, h.serial, name, ownerIDs[name])
				continue
			}
			owners[name], ownerIDs[name] = h, rt
		}
	}
	return owners
}

// launchers are the runtimes' entries for the new-session form.
func (b *runtimeRegistry) launchers() []proto.Launcher {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []proto.Launcher
	for name, h := range b.launcherOwners() {
		l, _ := h.launcherNamed(name)
		h.mu.Lock()
		// Agent, as for a Codex launcher (proto.Launcher.Agent): what it starts is not Claude Code, so the apps do
		// not offer it where only Claude Code or Codex can run (the Persona assistant).
		agent := l.Agent
		if agent == "" {
			agent = h.hello.Agent
		}
		if agent == "" {
			agent = h.hello.Runtime
		}
		if agent == "claude" {
			agent = ""
		}
		out = append(out, proto.Launcher{Name: name, Model: l.Model, Source: h.hello.Runtime, Agent: agent,
			Assistant: h.hello.Caps.Assistant && name == h.hello.Launcher})
		h.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// launcherRuntime is the runtime offering a launcher name, nil when none does.
func (b *runtimeRegistry) launcherRuntime(name string) *runtimeConn {
	if b == nil || name == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.launcherOwners()[name]
}

func (b *runtimeRegistry) watch(h *runtimeConn, id string, since int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.watching[id]; !ok || since > cur {
		b.watching[id] = since
	}
	h.mu.Lock()
	h.watched[id] = true
	h.mu.Unlock()
}

func (b *runtimeRegistry) unwatch(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.watching, id)
	for _, h := range b.conns {
		h.mu.Lock()
		delete(h.watched, id)
		h.mu.Unlock()
	}
}

// pushed moves a watched session's seq forward (the items a runtime pushed reached the hub).
// lastPushed is the last seq pushed for a watched session (0: not watched, or nothing yet).
func (b *runtimeRegistry) lastPushed(id string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.watching[id]
}

func (b *runtimeRegistry) pushed(id string, seq int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.watching[id]; ok && seq > cur {
		b.watching[id] = seq
	}
}

// sessionInPane is the session a runtime runs in the terminal pane (its list's pane_id, as own writes it: node ownPane
// takes the node's own herdr session off), "" when none says so. Only sessions it lists as running: a pane outlives
// the pi that told it.
func (b *runtimeRegistry) sessionInPane(pane string, own func(string) string) string {
	if b == nil || pane == "" {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		h.mu.Lock()
		for _, s := range h.sessions {
			// The hint as the node writes panes, or bare as an extension that does not name the session sends it.
			if p := h.panes[s.SessionID]; p != "" && (p == pane || own(p) == pane) && s.Status != proto.StatusOffline {
				h.mu.Unlock()
				return s.SessionID
			}
		}
		h.mu.Unlock()
	}
	return ""
}

// rowOf is the row of the runtime that answers for session id (ownerOf), with that runtime.
func (b *runtimeRegistry) rowOf(id string) (proto.Session, *runtimeConn, bool) {
	h := b.ownerOf(id)
	if h == nil {
		return proto.Session{}, nil, false
	}
	s, ok := h.row(id)
	return s, h, ok
}
