package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// ---- the session lists ----

// setSessions takes a node's session list: the turns it shows starting and ending (the finished push, the
// assistant's own business), then the list the apps get.
func (s *Service) setSessions(machine string, list []proto.Session) {
	before := map[string]proto.Session{}
	for _, sess := range s.fleetPrevious(machine) {
		before[sess.SessionID] = sess
	}
	now := time.Now()
	for i := range list {
		list[i].MachineID = machine
		// A node from before titles were stripped first sends a clipped injected tag as the title.
		list[i].Title = transcript.Title(list[i].Title)
	}
	for _, sess := range list {
		// Any agent (herdr or headless, started by Persona or not) finishing a turn may notify the phone.
		was, key := before[sess.SessionID], machine+"/"+sess.SessionID
		prev := was.Status
		// The turn stopped on background work and its report woke it again within one of the node's list ticks: the
		// stop never showed, only the woken flag turning on. It is held work all the same (#4187, #4419). By the
		// background seen during the turn, not only in the last list: the list's background and its woken flag are
		// not always read together.
		if prev == proto.StatusWorking && sess.WokeByTask && !was.WokeByTask {
			s.turnsBook().holdIfBackground(key)
		}
		switch {
		case sess.Status == proto.StatusWorking && prev != proto.StatusWorking:
			s.takeTaskAnswerRetry(machine, sess.SessionID)
			s.turnsBook().working(key, now)
			s.replyWorked(machine, sess.SessionID) // answered: a question it asks next is a new one (agents_reply.go)
			ref := SessionRef{MachineID: machine, SessionID: sess.SessionID}
			// A first list or an offline-to-working transition can be a
			// reconnect to the same turn. Only a known stopped turn proves a
			// new one and resets its persisted loop state.
			newTurn := prev == proto.StatusDone || prev == proto.StatusIdle || prev == proto.StatusInterrupted
			s.inOrder(func() { s.turnStartedObserved(context.Background(), ref, newTurn) }) // after the items that came before it
		case prev == proto.StatusWorking && (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle):
			go s.notifyFinished(machine, sess.SessionID, s.turnsBook().ended(key, now, backgroundRunning(sess)))
			// A new assistant conversation is named after its first turn; a task from one may be done. After the
			// items of the turn (a channel's answer is read from them).
			ref := SessionRef{MachineID: machine, SessionID: sess.SessionID}
			s.inOrder(func() { go s.TurnEnded(context.Background(), ref) })
		case (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle) &&
			(sess.Status != prev || sess.Rev != was.Rev || sess.LastText != was.LastText || !sess.UpdatedAt.Equal(was.UpdatedAt)) &&
			s.takeTaskAnswerRetry(machine, sess.SessionID):
			ref := SessionRef{MachineID: machine, SessionID: sess.SessionID}
			s.inOrder(func() { go s.taskTurnEnded(context.Background(), ref.MachineID, ref.SessionID) })
		}
		if sess.Status == proto.StatusWorking && backgroundRunning(sess) {
			s.turnsBook().sawBackground(key, now)
		}
	}
	s.fleetKeepPrevious(machine, list)
	s.sessionsChanged(machine)
	// Messages from other conversations, for those no longer running; the todos' links and herdr marks.
	var connected time.Time
	if s.devs != nil {
		connected = s.devs.ConnectedAt(machine)
	}
	d := DeviceSessions{MachineID: machine, Sessions: list, Connected: connected}
	s.inOrder(func() { s.SessionsChanged(context.Background(), d) })
}

// fleetPrevious is the list setSessions saw last for the machine (the turns' starts and ends are against it).
func (s *Service) fleetPrevious(machine string) []proto.Session {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.prev[machine]
}

func (s *Service) fleetKeepPrevious(machine string, list []proto.Session) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.prev == nil {
		s.fleet.prev = map[string][]proto.Session{}
	}
	s.fleet.prev[machine] = list
}

func (s *Service) turnsBook() *turnTracker {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.turns == nil {
		s.fleet.turns = newTurnTracker()
	}
	return s.fleet.turns
}

// sessionsChanged: what the apps get of the machine's sessions may have changed: the hub is told, the screen and
// plan cards follow.
func (s *Service) sessionsChanged(machine string) {
	if _, ok := s.fleetMachine(machine); !ok {
		return
	}
	list := s.publishList(machine)
	s.syncScreenInteractions(machine, list)
	s.syncPlanInteractions(machine)
}

// publishSessions tells the hub the machine's session list as the apps get it now.
func (s *Service) publishSessions(machine string) {
	if _, ok := s.fleetMachine(machine); !ok {
		return
	}
	s.publishList(machine)
}

// publishList tells the hub the machine's list as the apps get it now, when it is not what it was told last
// (fleet.sessions); the fleet follows. It returns the list.
//
// The list is decorated and told under sentMu: two publishers (FleetUp's lists, a snapshot's changed machines) would
// otherwise each decorate, then tell in either order, and an older list could land last on the hub and stay there.
func (s *Service) publishList(machine string) []proto.Session {
	f := &s.fleet
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	list := s.decorated(machine)
	// A review's reviewer is no agent of the user's (supervise_autoreview.go): its result is on the task it reviewed.
	if hide := s.reviewerSessions(); len(hide) > 0 {
		kept := make([]proto.Session, 0, len(list))
		for _, sess := range list {
			if !hide[[2]string{machine, sess.SessionID}] {
				kept = append(kept, sess)
			}
		}
		list = kept
	}
	msg := assistantwire.FleetSessions{MachineID: machine, Sessions: list, Muted: s.mutedSessions(machine, list)}
	// json.Marshal's bytes, compared with the list told last as they are written: a list that is the same (most of them)
	// is not kept, and one that differs is copied once.
	w := changedWriter{old: f.sent[machine]}
	var b []byte // nil when the list does not encode, as json.Marshal gives
	if jsonv2.MarshalWrite(&w, msg, json.DefaultOptionsV1()) == nil {
		if w.same() {
			return list
		}
		b = w.written()
	} else if len(w.old) == 0 {
		return list
	}
	if f.sent == nil {
		f.sent = map[string][]byte{}
	}
	f.sent[machine] = b // kept and told: never written again
	f.shownMu.Lock()
	if f.shown == nil {
		f.shown = map[string][]proto.Session{}
	}
	f.shown[machine] = list
	f.shownMu.Unlock()
	s.hub.Tell(assistantwire.EventFleetSessions, json.RawMessage(b))
	s.liveChanged(false)
	return list
}

// changedWriter takes an encoding and compares it with old as it comes, copying it out only once it differs.
type changedWriter struct {
	old []byte
	n   int    // how many bytes came, while they are old's
	out []byte // all of them, once they differ from old
}

func (w *changedWriter) Write(p []byte) (int, error) {
	if w.out == nil {
		if len(p) <= len(w.old)-w.n && bytes.Equal(p, w.old[w.n:w.n+len(p)]) {
			w.n += len(p)
			return len(p), nil
		}
		w.out = append(make([]byte, 0, max(len(w.old), w.n+len(p))), w.old[:w.n]...)
	}
	w.out = append(w.out, p...)
	return len(p), nil
}

// same: what came is old.
func (w *changedWriter) same() bool { return w.out == nil && w.n == len(w.old) }

// written is everything that came, when it was not old.
func (w *changedWriter) written() []byte {
	if w.out == nil {
		return slices.Clone(w.old[:w.n]) // a prefix of old: never handed on as old's own bytes
	}
	return w.out
}

// decorated returns a machine's sessions as the apps get them: the unified states, the pending interaction counts,
// what the assistant says of its conversations, the work and todos.
func (s *Service) decoratedUnarchived(machine string) []proto.Session {
	return s.decorateInventory(machine, s.nodeSessions(machine), true) // a copy already: the devices' list stays as it is
}

// decorateInventory owns list. A subset gets the same live fields as the full
// inventory, but must not forget state marks belonging to rows it did not read.
func (s *Service) decorateInventory(machine string, list []proto.Session, complete bool) []proto.Session {
	m, _ := s.fleetMachine(machine)
	s.fleet.mu.Lock()
	counts := map[string]int{}
	asking := map[string]proto.Interaction{} // a session's card: its own before the manager's
	for _, p := range s.fleet.interactions {
		if p.ix.MachineID == machine {
			counts[p.ix.SessionID]++
			if cur, ok := asking[p.ix.SessionID]; !ok || cur.ToolName == assistantwire.WorkCardTool {
				asking[p.ix.SessionID] = p.ix
			}
		}
	}
	seen := make(map[string]time.Time, min(len(list), len(s.fleet.seen)))
	for _, sess := range list {
		if t, ok := s.fleet.seen[[2]string{machine, sess.SessionID}]; ok {
			seen[sess.SessionID] = t
		}
	}
	s.fleet.mu.Unlock()
	r := &s.fleet.rep
	r.mu.RLock()
	shown, via := r.sessions, r.marks
	r.mu.RUnlock()
	lang := s.lang(context.Background())
	dir := m.assistantCwd() // once, not a path.Join for each row
	asks := s.fleet.asks.reader(machine, complete)
	defer asks.done()
	for i := range list {
		list[i].MachineID = machine
		list[i].PendingInteractions = counts[list[i].SessionID]
		list[i].Assistant = isAssistantSession(&list[i], dir, m.manager)
		if list[i].Assistant {
			list[i].Space = identity.SpaceOf(list[i].Cwd)
		}
		if t := shown[list[i].SessionID].Title; t != "" && list[i].Assistant {
			list[i].Title = t // "MMDD | type | topic", the user's own, "Persona" for the main one (titles.go)
		}
		if ch := via[[2]string{machine, list[i].SessionID}]; len(ch) > 0 {
			list[i].Channels = ch // where its messages came from (docs/extensions/channels.md "Source marks")
		}
		if list[i].Assistant {
			list[i].Archived = shown[list[i].SessionID].Archived
			list[i].Origin, list[i].OriginRef = shown[list[i].SessionID].Origin, shown[list[i].SessionID].OriginRef // origins.go
			// Messages from other conversations wait in the assistant while it runs: shown like typed-ahead input.
			if q := shown[list[i].SessionID].Queued; len(q) > 0 {
				list[i].Queued = append(slices.Clip(list[i].Queued), q...) // the node's slice is shared: never write into it
			}
		}
		list[i].SeenAt = seen[list[i].SessionID]
		localizeSession(lang, &list[i]) // the node's Chinese activity line… (fleet_text.go)
		if list[i].StartErrorCode != "" {
			list[i].StartError = startErrorText(lang, cmp.Or(m.Name, machine), list[i])
		}
		list[i].State, list[i].BackgroundWorking = stateWithBackground(agentState(list[i], list[i].SeenAt), list[i])
		list[i].BackgroundWaiting = backgroundWaiting(list[i])
		list[i].TurnError, list[i].TurnErrorKind = turnError(list[i])
		decorateLimit(&list[i]) // a subscription's usage limit and when it resets (usage_limit.go)
		list[i].TurnErrorCode = sessionTurnErrorCode(list[i])
		if list[i].TurnError != "" {
			if list[i].TurnErrorCode == proto.TurnErrorModelFallbackUnavailable {
				list[i].TurnErrorKind = proto.TurnErrorUser
			}
			// Not worth a "Continue": the user signs in, fixes the key or the model. Only where agents_work would not retry it
			// anyway: a gateway's 5xx that mentions the model stays transient.
			if list[i].TurnErrorKind == "" && userTurnError(list[i].TurnErrorCode) && !transientError(list[i].TurnError) {
				list[i].TurnErrorKind = proto.TurnErrorUser
			}
		}
		s.decorateWork(machine, &list[i], asking)
		decorateAwaits(&list[i], asks)
	}
	s.decorateTodos(machine, list)
	for i := range list {
		// The todos' alone, stripped before push to client (#762).
		list[i].SpawnFrom, list[i].Chat, list[i].Topic, list[i].Dispatched = "", "", "", nil
	}
	s.fillStateMarks(machine, list, time.Now(), complete)
	return list
}

// decoratedSession is one of the machine's sessions as the apps get it.
func (s *Service) decoratedSession(machine, sid string) (proto.Session, bool) {
	for _, sess := range s.decorated(machine) {
		if sess.SessionID == sid {
			return sess, true
		}
	}
	return proto.Session{}, false
}

// decorateTodos puts on machine mid's sessions the todo each works for and whether it is quiet.
func (s *Service) decorateTodos(mid string, list []proto.Session) {
	r := &s.fleet.rep
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range list {
		key := [2]string{mid, list[i].SessionID}
		if l, ok := r.links[key]; ok {
			list[i].Todo = &proto.TodoRef{ID: l.TodoID, Title: l.Title}
		}
		list[i].Quiet = r.quiet[key]
	}
}
