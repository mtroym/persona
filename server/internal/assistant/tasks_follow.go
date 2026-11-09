package assistant

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- following a task's agents ----

// taskAgents are the sessions of t's active agents, as the clients see them (with state).
func (s *Service) taskAgents(ctx context.Context, t Todo) []proto.Session {
	var out []proto.Session
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		list, err := s.sessions(ctx, a.MachineID)
		if err != nil {
			continue
		}
		for _, c := range list {
			if c.SessionID == a.SessionID {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// sessionsReached lists machine's sessions, retrying a dropped or offline link a bounded number of times within
// ctx. reached is false when the node could not be asked after the retries: the caller must not read that as
// "the machine has no sessions". ErrNoDevice is deliberately different: the hub itself answers that it has no
// such machine (it was removed from the hub's configuration), a definite answer rather than a flaky node — an
// agent registered on a machine the hub no longer knows cannot still be waiting, so it answers with an empty
// list and reached true. Retrying it would spend the budget and wrongly keep a needs notice for a machine gone
// for good.
func (s *Service) sessionsReached(ctx context.Context, machine string) ([]proto.Session, bool) {
	backoff := agentReachBackoff
	for attempt := 0; ; attempt++ {
		list, err := s.sessions(ctx, machine)
		if err == nil {
			return list, true
		}
		if errors.Is(err, ErrNoDevice) {
			return nil, true // definite: the hub no longer knows this machine
		}
		if attempt+1 >= agentReachAttempts || ctx.Err() != nil {
			return nil, false
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		backoff *= 2
	}
}

// taskAgentsReached is taskAgents plus reachability. Each distinct machine of t's active agents is asked once
// within one agentReachMax deadline so retries across machines cannot stack; reached is false when one stayed
// unreachable, so callers keep "still waits" rather than read an unreachable node as an answer (ErrNoDevice: none).
func (s *Service) taskAgentsReached(ctx context.Context, t Todo) (agents []proto.Session, reached bool) {
	ctx, cancel := context.WithTimeout(ctx, agentReachMax)
	defer cancel()
	reached = true
	lists, asked := map[string][]proto.Session{}, map[string]bool{}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil || asked[a.MachineID] {
			continue
		}
		asked[a.MachineID] = true
		list, ok := s.sessionsReached(ctx, a.MachineID)
		if !ok {
			reached = false
			continue
		}
		lists[a.MachineID] = list
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		for _, c := range lists[a.MachineID] {
			if c.SessionID == a.SessionID {
				agents = append(agents, c)
				break
			}
		}
	}
	return agents, reached
}

// taskNeedsRecheck freshly asks t's agents' machines whether one still waits for the user. known is false when an
// agent machine stayed unreachable after the bounded retries, so a caller can keep the notice rather than take
// an unreachable node for an answer.
func (s *Service) taskNeedsRecheck(ctx context.Context, t Todo) (waits, known bool) {
	agents, reached := s.taskAgentsReached(ctx, t)
	if !reached {
		return false, false
	}
	for _, c := range agents {
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			return true, true
		}
	}
	return false, true
}

// taskWaits: one of t's agents still waits for the user. A machine the recheck cannot reach is not an answer: it
// is taken as still waiting, so a needs notice is never dropped on a transient RPC failure.
func (s *Service) taskWaits(ctx context.Context, t Todo) bool {
	waits, known := s.taskNeedsRecheck(ctx, t)
	return waits || !known
}

// taskWorking: one of the task's agents started a turn. The agent of a task the hub closed meanwhile (judged done,
// accepted) works on it again — sent more by agent_send, which resumes a closed one, or by the user: the task is open
// again first, as a change asked for on its "Please verify" card reopens it (#3457), so this turn is followed
// (interrupted or done receipt, judgment) and the task shows in progress, not done (#5892).
func (s *Service) taskWorking(ctx context.Context, mid, sid string) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	t, ok := s.taskOf(ctx, mid, sid)
	if !ok {
		closed, found := s.closedTaskOf(ctx, mid, sid)
		if !found {
			return
		}
		if err := s.reopenTodo(ctx, closed.ID); err != nil {
			s.logf("task %s: reopen: %v", closed.ID, err)
			return
		}
		if t, ok = s.taskOf(ctx, mid, sid); !ok {
			return
		}
		s.logf("task %s: reopened: its agent works again", t.ID)
	}
	s.tasks.mu.Lock()
	s.tasks.working[t.ID] = time.Now()
	s.tasks.mu.Unlock()
	s.setTaskStage(ctx, t.ID, "develop", "")
	if t.Supervision != nil {
		s.supervisedWorking(ctx, t)
	}
}

type receiptBody struct {
	Title   string         `json:"title"`
	State   string         `json:"state"` // done, failed, interrupted
	Summary string         `json:"summary,omitempty"`
	PR      string         `json:"pr,omitempty"`
	Machine string         `json:"machine,omitempty"`
	Review  *receiptReview `json:"review,omitempty"`
	// Reroute names the machine a flexible task was moved off and where it landed, with the health-check reason;
	// absent when it ran where it was asked (#2271).
	Reroute *PlanReroute `json:"reroute,omitempty"`
	// Error: what the model service answered when the turn was cut off (interrupted), for the card's detail.
	Error string `json:"error,omitempty"`
}

// rerouteOfTask resolves the recorded machine reroute for the plan task that runs todoID: nil when the task has no
// plan, the plan no longer holds it, or it was never moved (#2271).
func (s *Service) rerouteOfTask(ctx context.Context, todoID, planID string) *PlanReroute {
	if planID == "" {
		return nil
	}
	p, err := s.loadPlan(ctx, planID)
	if err != nil || p == nil {
		return nil
	}
	for i := range p.Tasks {
		if p.Tasks[i].TodoID == todoID {
			return p.Tasks[i].Reroute
		}
	}
	return nil
}

type receiptReview struct {
	State    string `json:"state"` // checking: Persona follows up; awaiting: completion already judged
	Criteria int    `json:"criteria"`
}

// interruptedSummary is an interrupted task's receipt: the error itself is no conclusion (#1516).
const interruptedSummary = assistantwire.InterruptedSummary

// silentTurnErrorIn is the interrupted receipt's detail for a turn with no output at all (silentTurn, #2349), in l.
// Not kept in a package variable: that would read the catalogs whenever personad starts, a hook too (#1308).
func silentTurnErrorIn(l i18n.Lang) string { return i18n.T(l, "assistant-tasks.silent_turn") }

var prLink = lazyre.New(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)

// taskTurnEnded: one of the task's agents (sid on machine) ended a turn. Once none of them works or waits for the
// user any more, the conversation gets a receipt (once per stretch of work), and the todo is closed if the judge
// finds the work finished.
func (s *Service) taskTurnEnded(ctx context.Context, machine, sid string) {
	time.Sleep(s.settleDelay(ctx)) // a turn that ends to ask, or goes on at once, is not the end
	t, ok := s.taskOf(ctx, machine, sid)
	if !ok {
		return
	}
	if current, ok := s.todoNow(t.ID); ok && (current.Cancelled() || current.AgentAborted(machine, sid)) {
		s.logf("task %s: cancelled or agent %s/%s aborted: skipping turn end", t.ID, machine, sid)
		return
	}
	agents, reached := s.taskAgentsReached(ctx, t)
	if !reached || !taskAgentsSettled(t, agents) {
		return
	}
	var last, cut proto.Session
	for _, c := range agents {
		if c.State == proto.StateWorking || c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			return
		}
		if w := c.Work; w != nil && w.Phase == proto.WorkBlocked && w.On == proto.WaitTransient && w.WakeAt != nil {
			return // "Continue" goes by itself (agents_work.go): the receipt waits for how that ends
		}
		if now := time.Now(); limitTakenUp(c, now) {
			// A usage limit stopped it until the reset, and its CLI or the manager goes on then (usage_limit.go): the
			// receipt waits for how that ends. Looked at again once the wait is over, so a turn that did not go on after
			// all (the continue cancelled) still gets its receipt.
			s.taskTurnEndedAfter(ctx, machine, sid, limitLookAgain(c, now))
			return
		}
		if c.UpdatedAt.After(last.UpdatedAt) {
			last = c
		}
		// The node's word, whatever the state now; or, once it stopped, Claude Code's last words when they are the
		// error ("API Error: …", "Not logged in · …"): the node's raw list has no turn error for those (fleet.go
		// turnError reads them for the apps).
		text, kind := strings.TrimSpace(c.TurnError), c.TurnErrorKind
		if text == "" {
			text, kind = turnError(c)
		}
		if text != "" && (cut.SessionID == "" || c.UpdatedAt.After(cut.UpdatedAt)) {
			cut = c
			cut.TurnError, cut.TurnErrorKind = text, kind
		}
	}
	s.tasks.mu.Lock()
	since, worked := s.tasks.working[t.ID]
	if !worked {
		since = t.CreatedAt // a hub restarted since it began: count from the start
	}
	if r, ok := s.tasks.receipted[t.ID]; ok && !r.Before(since) {
		s.tasks.mu.Unlock()
		return
	}
	s.tasks.receipted[t.ID] = time.Now()
	s.tasks.mu.Unlock()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	name := machine
	if m, ok := s.device(cctx, machine); ok {
		name = m.Name
	}
	if !s.taskWorkCurrent(t.ID, since) || !s.taskRuntimeSettled(cctx, t) {
		return
	}
	if cut.SessionID != "" {
		// A turn cut off by the model service is no end of the work: an interrupted receipt (with "Continue" on the card),
		// and nothing to judge or close (#1516).
		s.logf("task %s: interrupted: %s", t.ID, clip(oneLine(cut.TurnError), 200))
		where := [2]string{cut.MachineID, cut.SessionID}
		body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(s.lang(cctx), "assistant-tasks.interrupted"), Machine: name,
			Error: clip(strings.TrimSpace(cut.TurnError), 300)}
		if _, err := s.appendFeed(cctx, t.Origin, feedReceipt, t.ID, body); err != nil {
			s.logf("task %s: receipt: %v", t.ID, err)
		}
		s.taskNotice(cctx, t, name, "interrupted", "", body.Error)
		s.taskReport(t, reportInterrupted, name, where, body.Error, nil)
		return
	}
	items, read := s.lastTurnItems(cctx, last.MachineID, last.SessionID)
	if !s.taskWorkCurrent(t.ID, since) || !s.taskRuntimeSettled(cctx, t) {
		return
	}
	if read && taskTurnAwaitingAnswer(items) && !silentTurn(items) {
		// Do not consume this stretch's receipt while the runtime is between tools and its answer.
		s.tasks.mu.Lock()
		if !s.tasks.working[t.ID].After(since) {
			delete(s.tasks.receipted, t.ID)
			if s.tasks.awaitingAnswer == nil {
				s.tasks.awaitingAnswer = make(map[SessionRef]bool)
			}
			s.tasks.awaitingAnswer[SessionRef{MachineID: machine, SessionID: sid}] = true
		}
		s.tasks.mu.Unlock()
		// The answer's update may have arrived during the transcript read, before
		// the retry was registered. Reconcile that update instead of waiting for another tick.
		if latest, ok := s.session(cctx, last.MachineID, last.SessionID); ok &&
			(latest.Status != last.Status || latest.Rev != last.Rev || latest.LastText != last.LastText || !latest.UpdatedAt.Equal(last.UpdatedAt)) &&
			s.takeTaskAnswerRetry(machine, sid) {
			go s.taskTurnEnded(context.WithoutCancel(ctx), machine, sid)
		}
		return
	}
	if read && silentTurn(items) {
		// No reply and no tool call, yet no error either (the model service answered 200 with nothing in it): no end
		// of the work. Interrupted, as a cut-off turn is, with nothing to judge or close (#2349).
		s.logf("task %s: interrupted: the turn had no output", t.ID)
		where := [2]string{last.MachineID, last.SessionID}
		l := s.lang(cctx)
		body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(l, "assistant-tasks.interrupted"), Machine: name,
			Error: silentTurnErrorIn(l)}
		if _, err := s.appendFeed(cctx, t.Origin, feedReceipt, t.ID, body); err != nil {
			s.logf("task %s: receipt: %v", t.ID, err)
		}
		s.taskNotice(cctx, t, name, "interrupted", "", body.Error)
		s.taskReport(t, reportInterrupted, name, where, body.Error, nil)
		return
	}
	// The whole last reply, as Markdown: the session list's last_text is one line cut at 200 characters.
	text := strings.TrimSpace(last.LastText)
	if _, answer := turnExchange(items); answer != "" {
		text = answer
	}
	if current, ok := s.todoNow(t.ID); !ok || current.Cancelled() || current.AgentAborted(machine, sid) {
		s.logf("task %s: cancelled or agent %s/%s aborted: not issuing done receipt", t.ID, machine, sid)
		return
	}
	body := receiptBody{Title: t.Title, State: "done", Summary: clipMarkdown(text, 600), Machine: name,
		Reroute: s.rerouteOfTask(cctx, t.ID, t.PlanID)}
	// Persona follows up on a supervised stop through sigReview or sigIdle after judgment; the receipt need not wait.
	// needs_user waits on the user instead. Read the latest todo in case it changed while reading the reply.
	if current, ok := s.todoNow(t.ID); ok && current.DoneAt == nil {
		if v := current.Supervision; v != nil && v.state(current) != reviewNeedsUser {
			state := "checking" // receipt stays ahead of the model; a stopped turn is not a completion verdict
			if v.state(current) == reviewPending {
				state = "awaiting"
			}
			body.Review = &receiptReview{State: state, Criteria: len(v.DoneWhen)}
		}
	}
	if body.Summary == "" {
		body.Summary = i18n.T(s.lang(cctx), "assistant-tasks.done")
	}
	if pr := prLink.FindAllString(text, -1); len(pr) > 0 {
		body.PR = pr[len(pr)-1]
	}
	if _, err := s.appendFeed(cctx, t.Origin, feedReceipt, t.ID, body); err != nil {
		s.logf("task %s: receipt: %v", t.ID, err)
	}
	s.taskNotice(cctx, t, name, "done", "", clipMarkdown(text, 1500))
	stage, pr := stageOf(text)
	s.setTaskStage(cctx, t.ID, stage, pr)
	if stageIndex(stage) >= stageIndex("deploy") {
		go s.askAcceptance(t, last.MachineID, last.SessionID, text) // the user tries it (acceptance.go)
	}
	s.taskReport(t, reportDone, name, [2]string{last.MachineID, last.SessionID}, text, nil)
	s.autoFinishTask(t, since, worked) // the work may be finished: then the todo is done (done.go)
}

// taskTurnEndedAfter has taskTurnEnded look at sid's task again at at. What happened meanwhile decides as ever: a turn
// that went on and ended had its own receipt, one that works again has none yet. A look already set for as early is
// enough (one still waiting then sets the next): the wait's wakes (a background task reporting, a /loop) end a turn
// each, and each comes here.
func (s *Service) taskTurnEndedAfter(ctx context.Context, machine, sid string, at time.Time) {
	ref := SessionRef{MachineID: machine, SessionID: sid}
	s.tasks.mu.Lock()
	if s.tasks.limitLooks == nil {
		s.tasks.limitLooks = map[SessionRef]time.Time{}
	}
	if pending, ok := s.tasks.limitLooks[ref]; ok && !pending.After(at) { // one as early is set: it looks again if need be
		s.tasks.mu.Unlock()
		return
	}
	s.tasks.limitLooks[ref] = at
	s.tasks.mu.Unlock()
	time.AfterFunc(time.Until(at), func() {
		s.tasks.mu.Lock()
		if s.tasks.limitLooks[ref].Equal(at) {
			delete(s.tasks.limitLooks, ref)
		}
		s.tasks.mu.Unlock()
		s.taskTurnEnded(ctx, machine, sid)
	})
}

// routeToTask sends text straight to the agent of task todoID that most needs it, and keeps it in the conversation as
// the user's (no from: a task card's Message, or a decision the owner made in Lark).
func (s *Service) routeToTask(ctx context.Context, conversation, todoID, text string) (FeedItem, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return FeedItem{}, err
	}
	t, ok, err := s.todo(ctx, todoID)
	if err != nil {
		return FeedItem{}, err
	}
	if !ok || t.Origin != conversation {
		return FeedItem{}, i18n.Err("assistant-tasks.route_no_task")
	}
	agents := s.taskAgents(ctx, t)
	if len(agents) == 0 {
		return FeedItem{}, i18n.Err("assistant-tasks.route_no_agent")
	}
	c := pickTaskAgent(agents)
	if _, ok := s.device(ctx, c.MachineID); !ok {
		return FeedItem{}, i18n.Err("assistant-tasks.route_offline")
	}
	// An agent closed after its task was done (tasks_close.go) is resumed first: that can take minutes, and goes
	// on when the phone stops waiting, as a send from the chat does.
	wait := 90 * time.Second
	if c.Status == proto.StatusOffline {
		wait = 5 * time.Minute
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
	defer cancel()
	if err := s.call(sctx, c.MachineID, DeviceSessionSend, map[string]any{"session_id": c.SessionID, "text": text}, nil); err != nil {
		return FeedItem{}, err
	}
	return s.appendFeed(sctx, conversation, feedRouted, todoID, map[string]string{"text": text})
}
