package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/compat"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The todos (docs/assistant/todos.md): something to do, handed to one or more agents; a task the assistant hands out is a todo
// whose origin is the conversation (tasks.go). They are the assistant's: it keeps them (the hub's Postgres tables
// todos, todo_spaces, todo_agents, todo_decisions, as they were) and answers the apps' /api/v1/todos… routes, which the
// hub relays. The hub shows what the assistant reports of them (TodoReport, in the snapshot: the todo each agent works
// for, the quiet agents). What follows from the machines' session lists — agents joining the todo of their workspace or
// of the agent that spawned them, the herdr marks, the agents' titles — is todo_sessions.go's, on sessions.changed.

type TodoSpace struct {
	MachineID string    `json:"machine_id"`
	Space     string    `json:"space"`
	LinkedAt  time.Time `json:"linked_at"`
}

type TodoAgent struct {
	MachineID   string     `json:"machine_id"`
	SessionID   string     `json:"session_id"`
	LinkedAt    time.Time  `json:"linked_at"`
	ReleasedAt  *time.Time `json:"released_at,omitempty"`
	Title       string     `json:"title"`
	Cwd         string     `json:"cwd"`
	Agent       string     `json:"agent"`
	StartError  string     `json:"start_error,omitempty"`
	Source      string     `json:"source"`
	Via         string     `json:"via,omitempty"`
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
}

type Todo struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Notes     string     `json:"notes"`
	DoneAt    *time.Time `json:"done_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Origin: the assistant conversation that opened it as a task; PlanID: the plan it came from (#930,
	// docs/assistant/unified-entry.md). Empty for a todo made by hand.
	Origin string `json:"origin,omitempty"`
	PlanID string `json:"plan_id,omitempty"`
	// StartError: a task whose agent did not start ends done with why (#1077); reopening clears it.
	StartError string `json:"start_error,omitempty"`
	// DoneBy is "auto" for the hub's completion judgment, "review" for supervisor acceptance, or "cancelled" for
	// cancellation, with DoneReason; ReopenedAt: when it was last reopened after it was done.
	DoneBy     string     `json:"done_by,omitempty"`
	DoneReason string     `json:"done_reason,omitempty"`
	ReopenedAt *time.Time `json:"reopened_at,omitempty"`
	// CancelledAt: when the task was cancelled; CancelledReason: why it was cancelled (#2272).
	CancelledAt     *time.Time  `json:"cancelled_at,omitempty"`
	CancelledReason string      `json:"cancelled_reason,omitempty"`
	Spaces          []TodoSpace `json:"spaces"`
	Agents          []TodoAgent `json:"agents"`
	// Quiet: a task from the main conversation while assistant.main_tasks_quiet was on (#1731,
	// docs/assistant/unified-entry.md "Quiet tasks"): no pushes, Live Activity or task badges for its agents; the main
	// conversation is told instead (quiet.go). Set when it is made, never changed.
	Quiet bool `json:"quiet"`
	// Supervision: the task's ledger when the main conversation handed it out and follows it up (#3124,
	// supervise.go); nil for any other todo. Clients that do not know it show the todo as ever.
	Supervision *Supervision `json:"supervision,omitempty"`
	// Stage is how far its work got (plan, develop, review, merge, deploy, verify; "" not known) and PRURL the PR it
	// named, read off its agents' replies (reports.go, #2768); the apps draw the stage bar from them.
	Stage string `json:"stage,omitempty"`
	PRURL string `json:"pr_url,omitempty"`
	// Delivery: how far its finished work went out, and while short of live where it is held and who can unhold it
	// (delivery.go, #3306); nil until judged.
	Delivery *TodoDelivery `json:"delivery,omitempty"`
	// Thread: the chat thread the todo is bound to (#5279, todo_thread.go); nil while it has none.
	Thread *TodoThread `json:"thread,omitempty"`
	// Source: where the todo was asked for when that is another chat (#6146, todo_source.go): the outcome is told
	// there once; nil for any other todo.
	Source *TodoSource `json:"source,omitempty"`
	// When it is due (DueAt a moment, DueDate a day "2006-01-02" in the user's time zone; at most one), when the phone
	// is reminded of it and whether it was, and its priority ("" normal, high, low): #1683, todo_plan.go. Clients that do
	// not know them show the todo as ever.
	DueAt      *time.Time `json:"due_at,omitempty"`
	DueDate    string     `json:"due_date,omitempty"`
	RemindAt   *time.Time `json:"remind_at,omitempty"`
	RemindedAt *time.Time `json:"reminded_at,omitempty"`
	Priority   string     `json:"priority,omitempty"`
}

// todoStore is what the todos keep in the database (internal/db). A Store without it (the unit tests' memStore) keeps
// them in memory only, as with no store at all.
type todoStore interface {
	ListTodos(ctx context.Context) ([]db.TodoRow, error)
	ListAllTodoSpaces(ctx context.Context) ([]db.TodoSpaceRow, error)
	ListAllTodoAgents(ctx context.Context) ([]db.TodoAgentRow, error)
	CreateTodo(ctx context.Context, t db.TodoRow) error
	UpdateTodo(ctx context.Context, id string, title *string, notes *string, done *bool, updatedAt time.Time) (*db.TodoRow, error)
	EditTodo(ctx context.Context, id string, e db.TodoEdit, updatedAt time.Time) (*db.TodoRow, error)
	ClaimTodoReminder(ctx context.Context, id string, remindAt, now time.Time) (bool, error)
	RetitleTodo(ctx context.Context, id, from, to string, at time.Time) (*db.TodoRow, bool, error)
	FailTodo(ctx context.Context, id, startError string, at time.Time) (*db.TodoRow, error)
	AutoDoneTodo(ctx context.Context, id, reason string, at, workSince time.Time) (*db.TodoRow, bool, error)
	RetryTodo(ctx context.Context, id string, at time.Time) (*db.TodoRow, bool, error)
	DeleteTodo(ctx context.Context, id string) error
	LinkTodoSpace(ctx context.Context, todoID, machineID, space string, linkedAt time.Time) (string, error)
	UnlinkTodoSpace(ctx context.Context, todoID, machineID, space string) error
	RenameTodoSpace(ctx context.Context, machineID, oldSpace, newSpace string) (bool, error)
	LinkTodoAgentWithSource(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error)
	LinkTodoAgentOfDoneTask(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error)
	AutoLinkUnlinkedTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) (bool, []string, error)
	UnlinkTodoAgent(ctx context.Context, todoID, machineID, sessionID string) error
	UpdateTodoAgentSnapshot(ctx context.Context, machineID, sessionID, title, startError string) error
	ListTodoDecisions(ctx context.Context, todoID string, all bool) ([]db.TodoDecisionRow, error)
	InsertTodoDecision(ctx context.Context, r db.TodoDecisionRow) (bool, error)
	SetTodoDecisionSupersedes(ctx context.Context, todoID, id, supersedesID string, at time.Time) (*db.TodoDecisionRow, error)
	ListTodoThreads(ctx context.Context) (map[string]db.TodoThread, error)
	SetTodoThread(ctx context.Context, todoID string, th db.TodoThread, at time.Time) error
	ClearTodoThread(ctx context.Context, todoID string, at time.Time) (bool, error)
	ListTodoSources(ctx context.Context) (map[string]db.TodoSource, error)
	SetTodoSource(ctx context.Context, todoID string, src db.TodoSource) error
	StopTodoSourceReport(ctx context.Context, todoID string, at time.Time) (bool, error)
	PlanTodoSourceReport(ctx context.Context, todoID, text string, due time.Time) (bool, error)
	ClaimTodoSourceReport(ctx context.Context, todoID string, now time.Time, maxAttempts int) (int, string, bool, error)
	ReleaseTodoSourceReport(ctx context.Context, todoID string) error
	ClearTodoSourcePlan(ctx context.Context, todoID string) (bool, error)
}

// The database keeps them: a Store that is it but lacked one of these would keep them in memory only.
var _ todoStore = (*db.DB)(nil)

// todoDB is the store the todos are in; nil: they live in memory only.
func (s *Service) todoDB() todoStore {
	st, _ := s.store().(todoStore)
	return st
}

// todoBook is the todos as the assistant has them: loaded from the store once, then kept as it writes them.
type todoBook struct {
	mu sync.RWMutex
	// writing is held across a todo's store write and putting the row it returns into memory (PATCH, reopening,
	// linking and unlinking an agent by hand, closing it on a judgment or a review, a failed start, a retitle), so
	// memory takes the rows in the order the store committed them: two devices writing the same todo at once
	// otherwise leave memory with whichever finished last, not the newer row. Every writer that puts a stored row
	// into memory takes it. Workspace ownership writes and deletion also take it through publication; fanout
	// rechecks ownership under it after session lookup. Automatic links take it for memory changes and again for background persistence, so a
	// removed workspace cannot gain a late stored agent. Agent snapshots do not take it. Taken before mu, never
	// while mu is held; node calls and change notifications run after it is released.
	writing         sync.Mutex
	loaded          bool
	todos           map[string]*Todo
	activeBySession map[[2]string]string  // (machine_id, session_id) -> the open todo it works for
	snapshotSyncing map[[2]string]bool    // (machine_id, session_id) whose title is being written
	spacesByKey     map[string]string     // "<machine_id>/<space>" -> todo_id
	botmux          map[string]botmuxSeen // by machine: its botmux sessions' chats and dispatches (todo_sessions.go)

	tokensMu sync.Mutex
	tokens   map[string]*tokenSync // by machine (todo_sessions.go)
	linked   map[[2]string]string  // the links the marks were last synced for: todo id and title, by agent

	titling sync.WaitGroup // the model naming tasks' todos (todo_titles.go)

	// reporting are the todos whose report back to their source is waiting for the outcome (todo_source.go).
	reportMu  sync.Mutex
	reporting map[string]bool
	runMu     sync.Mutex // one report step at a time (sourceReportNow)

	// remindWake wakes remindLoop when the todos change (todo_plan.go).
	remindOnce sync.Once
	remindWake chan struct{}
}

// spaceKey computes the unified space key string ("machineID/space").
func spaceKey(machineID, space string) string {
	if machineID == "" || space == "" {
		return ""
	}
	return machineID + "/" + space
}

// sessionSpaceKey computes the unified space key string for a session.
func sessionSpaceKey(machineID string, c proto.Session) string {
	space := c.SpaceName
	if space == "" {
		space = c.SpaceID
	}
	return spaceKey(machineID, space)
}

// ensureTodosLoaded reads the todos from the store the first time; an error says why they can't be read.
func (s *Service) ensureTodosLoaded(ctx context.Context) error {
	b := &s.todos
	b.mu.RLock()
	if b.loaded {
		b.mu.RUnlock()
		return nil
	}
	b.mu.RUnlock()

	b.mu.Lock()
	if b.loaded {
		b.mu.Unlock()
		return nil
	}
	err := s.loadTodosLocked(ctx)
	loaded := b.loaded
	b.mu.Unlock()
	if loaded {
		// The links as read are the ones the marks go by from now on: one that goes away later syncs its machine
		// again (syncLinkedTodoTokens), even before any of the links changed.
		s.syncLinkedTodoTokens()
		s.sweepSourceReports() // a todo that closed while the assistant was away still tells where it came from
	}
	return err
}

func (s *Service) loadTodosLocked(ctx context.Context) error {
	b := &s.todos
	if b.snapshotSyncing == nil {
		b.snapshotSyncing = make(map[[2]string]bool)
	}
	st := s.todoDB()
	if st == nil {
		if b.todos == nil {
			b.todos = make(map[string]*Todo)
			b.activeBySession = make(map[[2]string]string)
			b.spacesByKey = make(map[string]string)
		}
		b.loaded = true
		s.todoMarksBaseline(b.todos)
		return nil
	}

	tlist, err := st.ListTodos(ctx)
	if err != nil {
		return err
	}
	slist, err := st.ListAllTodoSpaces(ctx)
	if err != nil {
		return err
	}
	alist, err := st.ListAllTodoAgents(ctx)
	if err != nil {
		return err
	}

	tmap := make(map[string]*Todo, len(tlist))
	active := make(map[[2]string]string)
	spaces := make(map[string]string)

	for _, tr := range tlist {
		tmap[tr.ID] = &Todo{
			ID:         tr.ID,
			Title:      tr.Title,
			Notes:      tr.Notes,
			DoneAt:     tr.DoneAt,
			CreatedAt:  tr.CreatedAt,
			UpdatedAt:  tr.UpdatedAt,
			Origin:     tr.Origin,
			PlanID:     tr.PlanID,
			StartError: tr.StartError,
			DoneBy:     tr.DoneBy,
			DoneReason: tr.DoneReason,
			ReopenedAt: tr.ReopenedAt,
			Quiet:      tr.Quiet,
			Spaces:     make([]TodoSpace, 0),
			Agents:     make([]TodoAgent, 0),
		}
		tmap[tr.ID].setPlan(tr)
	}

	for _, sr := range slist {
		if t, ok := tmap[sr.TodoID]; ok {
			t.Spaces = append(t.Spaces, TodoSpace{
				MachineID: sr.MachineID,
				Space:     sr.Space,
				LinkedAt:  sr.LinkedAt,
			})
			spaces[spaceKey(sr.MachineID, sr.Space)] = sr.TodoID
		}
	}

	for _, ar := range alist {
		t, ok := tmap[ar.TodoID]
		if !ok {
			continue
		}
		t.Agents = append(t.Agents, TodoAgent{
			MachineID:   ar.MachineID,
			SessionID:   ar.SessionID,
			LinkedAt:    ar.LinkedAt,
			ReleasedAt:  ar.ReleasedAt,
			Title:       ar.Title,
			Cwd:         ar.Cwd,
			Agent:       ar.Agent,
			StartError:  ar.StartError,
			Source:      ar.Source,
			Via:         ar.Via,
			DismissedAt: ar.DismissedAt,
		})
		if ar.ReleasedAt == nil && ar.DismissedAt == nil && t.DoneAt == nil {
			active[[2]string{ar.MachineID, ar.SessionID}] = t.ID
		}
	}

	if sst, ok := st.(supervisionStore); ok {
		rows, err := sst.ListTodoSupervision(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if t, ok := tmap[r.TodoID]; ok {
				t.Supervision = supervisionFromRow(r)
			}
		}
	}
	threads, err := st.ListTodoThreads(ctx)
	if err != nil {
		return err
	}
	for id, th := range threads {
		if t, ok := tmap[id]; ok {
			t.Thread = &th
		}
	}
	sources, err := st.ListTodoSources(ctx)
	if err != nil {
		return err
	}
	for id, src := range sources {
		if t, ok := tmap[id]; ok {
			t.Source = &src
		}
	}
	for id, st := range s.todoStages(ctx) {
		if t, ok := tmap[id]; ok {
			t.Stage, t.PRURL, t.Delivery = st.Stage, st.PR, st.Delivery
		}
	}

	b.todos = tmap
	b.activeBySession = active
	b.spacesByKey = spaces
	b.loaded = true
	s.todoMarksBaseline(b.todos)
	return nil
}

// todosLoaded: the todos were read (the session-list work waits for that rather than reading them on every list).
func (s *Service) todosLoaded() bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	return s.todos.loaded
}

// todosChanged tells the apps to read the todos again ({"type":"todos"}).
func (s *Service) todosChanged(ctx context.Context) {
	s.event(ctx, map[string]any{"type": "todos"})
	s.todoChangedSoon()
	s.remindSoon()   // a reminder set, moved or done (todo_plan.go)
	s.boardChanged() // a to-do closed, reopened or gone leaves "Please verify"
}

// linksChanged: which todo an agent works for changed (or a todo's title, or whether it is done): the hub is told
// (the snapshot's TodoReport: it pushes the session lists that look different now) and the herdr marks follow.
func (s *Service) linksChanged(ctx context.Context) {
	s.publish(context.WithoutCancel(ctx))
	s.syncLinkedTodoTokens()
}

// quietTask: a task opened now from conversation origin is quiet (#1731): it comes from the main conversation while
// the user wants those told only there (its own settings: the main conversation, main_tasks_quiet), or from a
// reporting conversation (#3192, reporters.go). The one place this is decided.
func (s *Service) quietTask(ctx context.Context, origin string) bool {
	if origin == "" {
		return false
	}
	if _, ok := s.reporterOf(ctx, origin); ok {
		return true
	}
	return s.settings(ctx).Main == origin && s.mainTasksQuiet(ctx)
}

// removeTodo deletes todo id from the store and from memory (its agents' and spaces' links with it) and tells the
// apps; db.ErrTodoNotFound when it is not there. The DELETE route and the code that must take back a todo it just
// made (a topic that turned out to be taken, feishu_thread_open.go) both go through it.
func (s *Service) removeTodo(ctx context.Context, id string) error {
	s.todos.writing.Lock()
	if st := s.todoDB(); st != nil {
		if err := st.DeleteTodo(ctx, id); err != nil {
			s.todos.writing.Unlock()
			return err
		}
	}

	s.todos.mu.Lock()
	if cur, ok := s.todos.todos[id]; ok {
		for _, a := range cur.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil {
				delete(s.todos.activeBySession, [2]string{a.MachineID, a.SessionID})
			}
		}
		for _, sp := range cur.Spaces {
			delete(s.todos.spacesByKey, sp.MachineID+"/"+sp.Space)
		}
		delete(s.todos.todos, id)
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()

	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return nil
}

// createTodo makes a todo (origin and planID set when a plan from an assistant conversation opens it as a task) and
// tells the apps.
func (s *Service) createTodo(ctx context.Context, title, notes, origin, planID string) (Todo, error) {
	return s.newTodo(ctx, title, notes, origin, planID, s.quietTask(ctx, origin))
}

// newTodo is createTodo, quiet or not as given (a launcher repair's has no conversation and is quiet,
// launcher_repair.go).
func (s *Service) newTodo(ctx context.Context, title, notes, origin, planID string, quiet bool) (Todo, error) {
	return s.newTodoAs(ctx, "todo_"+randomHex(8), title, notes, origin, planID, quiet)
}

// newTodoAs is newTodo with the id given (a question's to-do has its card's, ask_todo.go).
func (s *Service) newTodoAs(ctx context.Context, id, title, notes, origin, planID string, quiet bool) (Todo, error) {
	now := time.Now().UTC()
	return s.insertTodo(ctx, db.TodoRow{ID: id, Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now, Origin: origin, PlanID: planID,
		Quiet: quiet})
}

// insertTodo stores todo row, puts it in memory and tells the apps.
func (s *Service) insertTodo(ctx context.Context, row db.TodoRow) (Todo, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, err
	}
	if st := s.todoDB(); st != nil {
		if err := st.CreateTodo(ctx, row); err != nil {
			return Todo{}, err
		}
	}
	id := row.ID
	tObj := &Todo{ID: id, Title: row.Title, Notes: row.Notes, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Origin: row.Origin,
		PlanID: row.PlanID, Quiet: row.Quiet, Spaces: make([]TodoSpace, 0), Agents: make([]TodoAgent, 0)}
	tObj.setPlan(row)
	s.todos.mu.Lock()
	s.todos.todos[id] = tObj
	out := formatTodo(copyTodoLocked(tObj))
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	return out, nil
}

func copyTodoLocked(t *Todo) Todo {
	out := *t
	if t.DoneAt != nil {
		da := *t.DoneAt
		out.DoneAt = &da
	}
	if t.ReopenedAt != nil {
		ra := *t.ReopenedAt
		out.ReopenedAt = &ra
	}
	if t.CancelledAt != nil {
		ca := *t.CancelledAt
		out.CancelledAt = &ca
	}
	out.Spaces = make([]TodoSpace, len(t.Spaces))
	copy(out.Spaces, t.Spaces)
	out.Agents = make([]TodoAgent, len(t.Agents))
	for i, a := range t.Agents {
		out.Agents[i] = a
		if a.ReleasedAt != nil {
			ra := *a.ReleasedAt
			out.Agents[i].ReleasedAt = &ra
		}
		if a.DismissedAt != nil {
			da := *a.DismissedAt
			out.Agents[i].DismissedAt = &da
		}
	}
	out.Supervision = t.Supervision.clone()
	if t.Thread != nil {
		th := *t.Thread
		out.Thread = &th
	}
	if t.Source != nil {
		src := *t.Source
		out.Source = &src
	}
	return out
}

// formatTodo is a todo as the apps get it: no dismissed agents, the active ones first (newest link first), then those
// that worked for it (latest released first).
func formatTodo(t Todo) Todo {
	out := t
	// Existing clients use review for cancellation labels and reasons. Keep that wire value;
	// the stored marker distinguishes cancellation even for tasks without a supervision ledger.
	if out.DoneBy == db.DoneByCancelled {
		out.DoneBy = db.DoneByReview
	}
	if out.Spaces == nil {
		out.Spaces = []TodoSpace{}
	}
	out.Agents = make([]TodoAgent, 0, len(t.Agents))
	var curr, past []TodoAgent
	for _, a := range t.Agents {
		// Dismissed rows are not returned by the API (#762)
		if a.DismissedAt != nil {
			continue
		}
		if a.ReleasedAt == nil {
			curr = append(curr, a)
		} else {
			past = append(past, a)
		}
	}
	sort.Slice(curr, func(i, j int) bool {
		return curr[i].LinkedAt.After(curr[j].LinkedAt)
	})
	sort.Slice(past, func(i, j int) bool {
		if past[i].ReleasedAt != nil && past[j].ReleasedAt != nil {
			return past[i].ReleasedAt.After(*past[j].ReleasedAt)
		}
		return false
	})
	out.Agents = append(out.Agents, curr...)
	out.Agents = append(out.Agents, past...)
	return out
}

// ---- reading them ----

// todo is todo id; an error when the todos can't be read.
func (s *Service) todo(ctx context.Context, id string) (Todo, bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, false, err
	}
	t, ok := s.todoNow(id)
	return t, ok, nil
}

// todoNow is a copy of todo id as it is now.
func (s *Service) todoNow(id string) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t, ok := s.todos.todos[id]
	if !ok {
		return Todo{}, false
	}
	return copyTodoLocked(t), true
}

// todosOf are the todos opened from conversation ("*": from any) as tasks; none when they can't be read. A question's
// to-do (ask_todo.go) is no task: it has its own line in the snapshot and its own row in the briefing.
func (s *Service) todosOf(ctx context.Context, conversation string) []Todo {
	if s.ensureTodosLoaded(ctx) != nil {
		return nil
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	list := []Todo{}
	for _, t := range s.todos.todos {
		if isAskTodo(t.ID) {
			continue
		}
		if conversation == "*" && t.Origin != "" || conversation != "*" && t.Origin == conversation {
			list = append(list, copyTodoLocked(t))
		}
	}
	return list
}

// todoView is todo id as the apps get it.
func (s *Service) todoView(ctx context.Context, id string) (json.RawMessage, error) {
	t, ok, err := s.todo(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, db.ErrTodoNotFound
	}
	return json.Marshal(formatTodo(t))
}

// taskOf is the task (a todo opened from an assistant conversation, still open) the agent mid/sid works for.
// Cancelled reports whether the task is cancelled (by review, cancellation marker, or interaction).
func (t Todo) Cancelled() bool {
	if t.CancelledAt != nil {
		return true
	}
	if t.DoneBy == db.DoneByCancelled {
		return true
	}
	if t.Supervision != nil && t.Supervision.state(t) == reviewCancelled {
		return true
	}
	return false
}

// AgentAborted reports whether the agent (machineID, sessionID) was let go, replaced or reassigned.
func (t Todo) AgentAborted(machineID, sessionID string) bool {
	for _, a := range t.Agents {
		if a.MachineID == machineID && a.SessionID == sessionID {
			return a.ReleasedAt != nil || a.DismissedAt != nil
		}
	}
	return false
}

func (s *Service) taskOf(ctx context.Context, mid, sid string) (Todo, bool) {
	if s.ensureTodosLoaded(ctx) != nil {
		return Todo{}, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	id, ok := s.todos.activeBySession[[2]string{mid, sid}]
	if !ok {
		return Todo{}, false
	}
	t := s.todos.todos[id]
	if t == nil || t.Origin == "" || t.DoneAt != nil {
		return Todo{}, false
	}
	return copyTodoLocked(t), true
}

// completedTodo reports whether mid/sid still belongs to a completed todo and no open one. Unlike closedTaskOf,
// manual todos and user completions count too: a new turn alone does not authorize automatic follow-ups.
func (s *Service) completedTodo(ctx context.Context, mid, sid string) (bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false, err
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	if _, open := s.todos.activeBySession[[2]string{mid, sid}]; open {
		return false, nil
	}
	for _, t := range s.todos.todos {
		if t.DoneAt == nil {
			continue
		}
		for _, a := range t.Agents {
			if a.MachineID == mid && a.SessionID == sid && a.ReleasedAt == nil && a.DismissedAt == nil {
				return true, nil
			}
		}
	}
	return false, nil
}

// closedTaskOf is the task the hub closed (judged done, or accepted by its supervisor) the agent mid/sid still belongs
// to: linked, neither released nor dismissed, and working for no open todo since; the one closed last when there are
// several (#5892). Cancelled tasks and ones the user closed stay theirs to reopen, even when an agent starts work.
func (s *Service) closedTaskOf(ctx context.Context, mid, sid string) (Todo, bool) {
	if s.ensureTodosLoaded(ctx) != nil {
		return Todo{}, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	if _, open := s.todos.activeBySession[[2]string{mid, sid}]; open {
		return Todo{}, false
	}
	var found *Todo
	for _, t := range s.todos.todos {
		if t.Origin == "" || t.DoneAt == nil || t.StartError != "" || t.DoneBy != db.DoneByAuto && t.DoneBy != db.DoneByReview ||
			found != nil && !t.DoneAt.After(*found.DoneAt) {
			continue
		}
		// Older cancellations used done_by=review; the ledger preserves their verdict.
		if t.Supervision.state(*t) == reviewCancelled {
			continue
		}
		for _, a := range t.Agents {
			if a.MachineID == mid && a.SessionID == sid && a.ReleasedAt == nil && a.DismissedAt == nil {
				found = t
				break
			}
		}
	}
	if found == nil {
		return Todo{}, false
	}
	return copyTodoLocked(found), true
}

// ---- HTTP ----

var errTodosUnavailable = errors.New("todos service unavailable")

func (s *Service) todoRoutes() {
	s.todoDecisionRoutes()
	s.todoThreadRoutes()
	s.todoRetitleRoute()
	// GET /api/v1/todos: list all todos (newest created_at first), agents sorted (active first, then released); a todo
	// waiting for the owner carries its ask (todo_ask.go)
	s.handle("GET /api/v1/todos", func(w http.ResponseWriter, r *http.Request) {
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		s.todos.mu.RLock()
		list := make([]listedTodo, 0, len(s.todos.todos))
		for _, t := range s.todos.todos {
			list = append(list, listedTodo{Todo: formatTodo(copyTodoLocked(t))})
		}
		s.todos.mu.RUnlock()
		asks := s.todoAsks(r.Context()) // once the todos are let go: it reads them itself
		for i := range list {
			if a, ok := asks[list[i].ID]; ok {
				list[i].Ask = &a
			}
		}

		sort.Slice(list, func(i, j int) bool {
			return list[i].CreatedAt.After(list[j].CreatedAt)
		})
		apierr.WriteJSON(w, map[string]any{"todos": list})
	})

	// POST /api/v1/todos: create todo {title, notes, due_at | due_date, remind_at, priority}
	s.handle("POST /api/v1/todos", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		var in struct {
			Title string `json:"title"`
			Notes string `json:"notes"`
			todoPlanIn
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		title := strings.TrimSpace(in.Title)
		if title == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("title cannot be empty"))
			return
		}
		var plan db.TodoEdit
		if err := in.todoPlanIn.edit(&plan); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		resTodo, err := s.createPlainTodo(r.Context(), title, in.Notes, plan)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"todo": resTodo})
	})

	// PATCH /api/v1/todos/{id}: update title, notes, done, due_at | due_date, remind_at, priority
	s.handle("PATCH /api/v1/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		id := r.PathValue("id")
		if _, ok := s.todoNow(id); !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}

		var in struct {
			Title *string `json:"title"`
			Notes *string `json:"notes"`
			Done  *bool   `json:"done"`
			todoPlanIn
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		e := db.TodoEdit{Title: in.Title, Notes: in.Notes, Done: in.Done}
		if in.Title != nil {
			t := strings.TrimSpace(*in.Title)
			if t == "" {
				apierr.Write(w, http.StatusBadRequest, errors.New("title cannot be empty"))
				return
			}
			e.Title = &t
		}
		if err := in.todoPlanIn.edit(&e); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		resTodo, err := s.editTodo(r.Context(), id, e)
		if err != nil {
			if errors.Is(err, db.ErrTodoNotFound) {
				apierr.Write(w, http.StatusNotFound, err)
			} else {
				apierr.Write(w, http.StatusInternalServerError, err)
			}
			return
		}
		apierr.WriteJSON(w, map[string]any{"todo": resTodo})
	})

	// DELETE /api/v1/todos/{id}: delete todo
	s.handle("DELETE /api/v1/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		id := r.PathValue("id")
		if _, ok := s.todoNow(id); !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if err := s.deleteTodo(r.Context(), id); err != nil {
			if errors.Is(err, db.ErrTodoNotFound) {
				apierr.Write(w, http.StatusNotFound, err)
			} else {
				apierr.Write(w, http.StatusInternalServerError, err)
			}
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})

	// PUT /api/v1/todos/{id}/spaces/{mid}/{space...}: link whole space to todo
	s.handle("PUT /api/v1/todos/{id}/spaces/{mid}/{space...}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := r.PathValue("id")
		mid := r.PathValue("mid")
		space := r.PathValue("space")
		if space == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("space is required"))
			return
		}

		tInfo, ok := s.todoNow(todoID)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if tInfo.DoneAt != nil {
			apierr.Write(w, http.StatusConflict, errors.New("todo is already completed"))
			return
		}

		linkedCount, err := s.linkTodoSpace(r.Context(), todoID, mid, space)
		if err != nil {
			todoErr(w, err)
			return
		}

		apierr.WriteJSON(w, map[string]any{"ok": true, "linked": linkedCount})
	})

	// DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}: unlink space from todo
	s.handle("DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := r.PathValue("id")
		if _, ok := s.todoNow(todoID); !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if err := s.unlinkTodoSpace(r.Context(), todoID, r.PathValue("mid"), r.PathValue("space")); err != nil {
			todoErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})

	// PUT /api/v1/todos/{id}/agents/{mid}/{sid}: link existing agent
	s.handle("PUT /api/v1/todos/{id}/agents/{mid}/{sid}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := r.PathValue("id")
		mid := r.PathValue("mid")
		sid := r.PathValue("sid")

		tInfo, ok := s.todoNow(todoID)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if tInfo.DoneAt != nil {
			apierr.Write(w, http.StatusConflict, errors.New("todo is already completed"))
			return
		}

		var title, cwd, agent string
		if c, ok := s.session(r.Context(), mid, sid); ok {
			title, cwd, agent = c.Title, c.Cwd, c.Agent
		}
		if err := s.linkTodoAgent(r.Context(), todoID, mid, sid, title, cwd, agent); err != nil {
			todoErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})

	// DELETE /api/v1/todos/{id}/agents/{mid}/{sid}: unlink agent (soft unlink: dismissed_at + released_at #762)
	s.handle("DELETE /api/v1/todos/{id}/agents/{mid}/{sid}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := r.PathValue("id")
		mid := r.PathValue("mid")
		sid := r.PathValue("sid")

		target, ok := s.todoNow(todoID)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		agentFound := false
		for _, a := range target.Agents {
			if a.MachineID == mid && a.SessionID == sid {
				agentFound = true
				break
			}
		}
		if !agentFound {
			apierr.Write(w, http.StatusNotFound, errors.New("agent link not found"))
			return
		}

		s.todos.writing.Lock()
		if st := s.todoDB(); st != nil {
			if err := st.UnlinkTodoAgent(r.Context(), todoID, mid, sid); err != nil {
				s.todos.writing.Unlock()
				todoErr(w, err)
				return
			}
		}

		now := time.Now().UTC()
		s.todos.mu.Lock()
		if target, ok := s.todos.todos[todoID]; ok {
			for i := range target.Agents {
				if target.Agents[i].MachineID == mid && target.Agents[i].SessionID == sid {
					target.Agents[i].ReleasedAt = &now
					target.Agents[i].DismissedAt = &now
					break
				}
			}
			target.UpdatedAt = now
		}
		if curTid, ok := s.todos.activeBySession[[2]string{mid, sid}]; ok && curTid == todoID {
			delete(s.todos.activeBySession, [2]string{mid, sid})
		}
		s.todos.mu.Unlock()
		s.todos.writing.Unlock()

		s.linksChanged(r.Context())
		s.todosChanged(r.Context())
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// todoErr answers a todo write that failed: no such todo 404, a done one 409, anything else 500.
func todoErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTodoNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, db.ErrTodoCompleted):
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

// ---- writing them ----

// linkTodoSpace links a space to todoID and attaches all current online agents in that space.
func (s *Service) linkTodoSpace(ctx context.Context, todoID, mid, space string) (int, error) {
	s.todos.writing.Lock()
	now := time.Now().UTC()
	spKey := spaceKey(mid, space)

	var oldTodoID string
	if st := s.todoDB(); st != nil {
		var err error
		oldTodoID, err = st.LinkTodoSpace(ctx, todoID, mid, space, now)
		if err != nil {
			s.todos.writing.Unlock()
			return 0, err
		}
	} else {
		s.todos.mu.RLock()
		t, ok := s.todos.todos[todoID]
		if ok {
			if oldID, has := s.todos.spacesByKey[spKey]; has && oldID != todoID {
				oldTodoID = oldID
			}
		}
		done := ok && t.DoneAt != nil
		s.todos.mu.RUnlock()
		if !ok {
			s.todos.writing.Unlock()
			return 0, db.ErrTodoNotFound
		}
		if done {
			s.todos.writing.Unlock()
			return 0, db.ErrTodoCompleted
		}
	}

	s.todos.mu.Lock()
	// Clean up from old todo if space belonged to another todo
	if oldTodoID != "" && oldTodoID != todoID {
		if oldT, ok := s.todos.todos[oldTodoID]; ok {
			var keptSpaces []TodoSpace
			for _, sp := range oldT.Spaces {
				if !(sp.MachineID == mid && sp.Space == space) {
					keptSpaces = append(keptSpaces, sp)
				}
			}
			oldT.Spaces = keptSpaces
			var keptAgents []TodoAgent
			for _, a := range oldT.Agents {
				if a.Source == "space" && a.Via == spKey {
					if s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}] == oldTodoID {
						delete(s.todos.activeBySession, [2]string{a.MachineID, a.SessionID})
					}
					continue
				}
				keptAgents = append(keptAgents, a)
			}
			oldT.Agents = keptAgents
			oldT.UpdatedAt = now
		}
	}

	// Add or update in target todo
	if target := s.todos.todos[todoID]; target != nil {
		spFound := false
		for i := range target.Spaces {
			if target.Spaces[i].MachineID == mid && target.Spaces[i].Space == space {
				target.Spaces[i].LinkedAt = now
				spFound = true
				break
			}
		}
		if !spFound {
			target.Spaces = append(target.Spaces, TodoSpace{MachineID: mid, Space: space, LinkedAt: now})
		}
		s.todos.spacesByKey[spKey] = todoID
		target.UpdatedAt = now
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()

	// Link all online agents currently in this space
	linkedCount := 0
	list, _ := s.sessions(ctx, mid)
	for _, c := range list {
		if sessionSpaceKey(mid, c) != spKey || c.Status == proto.StatusOffline {
			continue
		}
		if linked, err := s.linkTodoSpaceAgent(ctx, todoID, mid, spKey, now, c); err == nil && linked {
			linkedCount++
		}
	}

	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return linkedCount, nil
}

// applyTodoRow puts a todo's row as it now is in the store into memory and lets everything that follows from done
// happen as for the user's PATCH: its agents' links count again or no longer do (sessions lose or regain the todo,
// the nodes' persona_todo tokens follow), and the apps refetch. guard, when given, is checked under the lock first
// (false: nothing changes); false also when the todo is gone.
func (s *Service) applyTodoRow(ctx context.Context, id string, row db.TodoRow, guard func(*Todo) bool) (Todo, bool) {
	out, doneKeys, ok := s.setTodoRow(id, row, guard)
	if ok {
		s.todoRowSet(ctx, doneKeys)
	}
	return out, ok
}

// setTodoRow is applyTodoRow's change to memory, without telling anyone: the todo as it is now and the agents done
// releases (their pending work is retired); not ok when the guard refused or the todo is gone. A writer holding
// todoBook.writing calls it before letting go, and todoRowSet after.
func (s *Service) setTodoRow(id string, row db.TodoRow, guard func(*Todo) bool) (Todo, [][2]string, bool) {
	s.todos.mu.Lock()
	t, ok := s.todos.todos[id]
	if !ok || guard != nil && !guard(t) {
		s.todos.mu.Unlock()
		return Todo{}, nil, false
	}
	t.Title = row.Title
	t.Notes = row.Notes
	becameDone := t.DoneAt == nil && row.DoneAt != nil
	reopened := t.DoneAt != nil && row.DoneAt == nil
	t.DoneAt = row.DoneAt
	t.StartError = row.StartError
	t.DoneBy, t.DoneReason, t.ReopenedAt = row.DoneBy, row.DoneReason, row.ReopenedAt
	t.setPlan(row)
	t.UpdatedAt = row.UpdatedAt
	var doneKeys [][2]string
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			key := [2]string{a.MachineID, a.SessionID}
			if t.DoneAt != nil {
				if becameDone && s.todos.activeBySession[key] == t.ID {
					doneKeys = append(doneKeys, key) // its pending wake and work card are withdrawn with the todo
				}
				delete(s.todos.activeBySession, key)
			} else {
				s.todos.activeBySession[key] = t.ID
			}
		}
	}
	out := formatTodo(copyTodoLocked(t))
	closing := threadClosing(becameDone, t.StartError, t.DoneReason)
	s.todos.mu.Unlock()
	if reopened {
		s.dropSourcePlan(id) // what was planned to be told may not be what it comes to now (todo_source.go)
	}
	if closing != nil {
		s.threadPost(id, *closing) // after the lock: it looks the todo up again
		s.sourceReportSoon(id, sourceReportWait)
	}
	return out, doneKeys, true
}

// todoRowSet tells everyone what setTodoRow changed: the hub and the herdr marks, the apps, and the done agents'
// pending work withdrawn.
func (s *Service) todoRowSet(ctx context.Context, doneKeys [][2]string) {
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	if len(doneKeys) > 0 {
		s.retireTodoWork(doneKeys...)
	}
}

// unlinkTodoSpace unlinks a space from todoID and physically deletes source=space agents linked via this space.
func (s *Service) unlinkTodoSpace(ctx context.Context, todoID, mid, space string) error {
	s.todos.writing.Lock()
	spKey := spaceKey(mid, space)

	s.todos.mu.RLock()
	t, ok := s.todos.todos[todoID]
	spFound := false
	if ok {
		for _, sp := range t.Spaces {
			if sp.MachineID == mid && sp.Space == space {
				spFound = true
				break
			}
		}
	}
	s.todos.mu.RUnlock()
	if !spFound {
		s.todos.writing.Unlock()
		return db.ErrTodoNotFound
	}

	if st := s.todoDB(); st != nil {
		if err := st.UnlinkTodoSpace(ctx, todoID, mid, space); err != nil {
			s.todos.writing.Unlock()
			return err
		}
	}

	now := time.Now().UTC()
	s.todos.mu.Lock()
	if cur, ok := s.todos.todos[todoID]; ok {
		var keptSpaces []TodoSpace
		for _, sp := range cur.Spaces {
			if !(sp.MachineID == mid && sp.Space == space) {
				keptSpaces = append(keptSpaces, sp)
			}
		}
		cur.Spaces = keptSpaces
		delete(s.todos.spacesByKey, spKey)

		var keptAgents []TodoAgent
		for _, a := range cur.Agents {
			if a.Source == "space" && a.Via == spKey {
				if actTid, ok := s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}]; ok && actTid == todoID {
					delete(s.todos.activeBySession, [2]string{a.MachineID, a.SessionID})
				}
				continue
			}
			keptAgents = append(keptAgents, a)
		}
		cur.Agents = keptAgents
		cur.UpdatedAt = now
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()

	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return nil
}

// TodoSpaceRenamed: the hub renamed a workspace (todos.space_renamed): todo_spaces and the agents' via follow; when
// the new name is already linked to another todo, the old link is void (#873).
func (s *Service) TodoSpaceRenamed(ctx context.Context, r TodoSpaceRenamed) {
	if r.Old == "" || r.New == "" || r.Old == r.New {
		return
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("rename space: todos not loaded: %v", err)
		return
	}
	s.todos.writing.Lock()
	s.todos.mu.RLock()
	_, voided := s.todos.spacesByKey[spaceKey(r.MachineID, r.New)]
	s.todos.mu.RUnlock()
	if st := s.todoDB(); st != nil {
		var err error
		if voided, err = st.RenameTodoSpace(ctx, r.MachineID, r.Old, r.New); err != nil {
			s.todos.writing.Unlock()
			s.logf("rename space: migrate todo spaces in db from %s/%s to %s failed: %v", r.MachineID, r.Old, r.New, err)
			return
		}
	}
	if voided {
		s.logf("rename space: target space %s/%s already linked to another todo; voiding old link for %s", r.MachineID, r.New, r.Old)
		s.voidTodoSpaceInMemory(r.MachineID, r.Old)
	} else {
		s.renameTodoSpaceInMemory(r.MachineID, r.Old, r.New)
	}
	s.todos.writing.Unlock()
	if voided {
		s.linksChanged(ctx)
	}
	s.todosChanged(ctx)
}

// renameTodoSpaceInMemory carries todo_spaces and agent via over when space is renamed.
func (s *Service) renameTodoSpaceInMemory(machineID, oldSpace, newSpace string) {
	oldKey := spaceKey(machineID, oldSpace)
	newKey := spaceKey(machineID, newSpace)
	now := time.Now().UTC()

	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()

	if tid, ok := s.todos.spacesByKey[oldKey]; ok {
		delete(s.todos.spacesByKey, oldKey)
		s.todos.spacesByKey[newKey] = tid
		if t, ok := s.todos.todos[tid]; ok {
			for i := range t.Spaces {
				if t.Spaces[i].MachineID == machineID && t.Spaces[i].Space == oldSpace {
					t.Spaces[i].Space = newSpace
					break
				}
			}
			for i := range t.Agents {
				if t.Agents[i].Source == "space" && t.Agents[i].Via == oldKey {
					t.Agents[i].Via = newKey
				}
			}
			t.UpdatedAt = now
		}
	}
}

// voidTodoSpaceInMemory cleans up a space link and its space-derived agents from memory when voided due to unique conflict (#873).
func (s *Service) voidTodoSpaceInMemory(machineID, oldSpace string) {
	oldKey := spaceKey(machineID, oldSpace)
	now := time.Now().UTC()

	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()

	if tid, ok := s.todos.spacesByKey[oldKey]; ok {
		delete(s.todos.spacesByKey, oldKey)
		if t, ok := s.todos.todos[tid]; ok {
			var keptSpaces []TodoSpace
			for _, sp := range t.Spaces {
				if !(sp.MachineID == machineID && sp.Space == oldSpace) {
					keptSpaces = append(keptSpaces, sp)
				}
			}
			t.Spaces = keptSpaces
			var keptAgents []TodoAgent
			for _, a := range t.Agents {
				if a.Source == "space" && a.Via == oldKey {
					if actTid, ok := s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}]; ok && actTid == tid {
						delete(s.todos.activeBySession, [2]string{a.MachineID, a.SessionID})
					}
					continue
				}
				keptAgents = append(keptAgents, a)
			}
			t.Agents = keptAgents
			t.UpdatedAt = now
		}
	}
}

// linkTodoAgent links or reactivates an agent by hand on todoID (the user's link, a task's started agent, a hand-off).
func (s *Service) linkTodoAgent(ctx context.Context, todoID, mid, sid, title, cwd, agent string) error {
	return s.linkTodoAgentWithSource(ctx, todoID, mid, sid, title, cwd, agent, "", "manual", "")
}

// linkTodoAgentOfDoneTask links the agent of a task that started while the user finished it, to that done todo: the
// task keeps the agent (and shows it) instead of the start reading as failed, and the close loop (tasks_close.go)
// retires it. Unlike linkTodoAgent, a finished todo is not refused.
func (s *Service) linkTodoAgentOfDoneTask(ctx context.Context, todoID, mid, sid, title, cwd, agent string) error {
	return s.linkTodoAgentWithSourceAllowingDone(ctx, todoID, mid, sid, title, cwd, agent, "", "manual", "", true)
}

// linkTodoAgentWithSource links an agent with a specific source ("manual", "space", "spawned") and via.
func (s *Service) linkTodoAgentWithSource(ctx context.Context, todoID, mid, sid, title, cwd, agent, startError, source, via string) error {
	return s.linkTodoAgentWithSourceAllowingDone(ctx, todoID, mid, sid, title, cwd, agent, startError, source, via, false)
}

// linkTodoAgentWithSourceAllowingDone links the agent, refusing a todo that is done (db.ErrTodoCompleted) unless
// allowDone: agents of done tasks stay linked (tasks_close.go retires them), new ones are not attached by hand.
func (s *Service) linkTodoAgentWithSourceAllowingDone(ctx context.Context, todoID, mid, sid, title, cwd, agent, startError, source, via string, allowDone bool) error {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	s.todos.writing.Lock()
	err := s.writeTodoAgentLink(ctx, todoID, mid, sid, title, cwd, agent, startError, source, via, allowDone)
	s.todos.writing.Unlock()
	if err != nil {
		return err
	}
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return nil
}

// linkTodoSpaceAgent retires fanout when a newer move/unlink published while the
// session lookup was in flight. Check and write share the ownership write boundary.
func (s *Service) linkTodoSpaceAgent(ctx context.Context, todoID, mid, key string, linkedAt time.Time, c proto.Session) (bool, error) {
	s.todos.writing.Lock()
	s.todos.mu.RLock()
	current := false
	if target := s.todos.todos[todoID]; target != nil && s.todos.spacesByKey[key] == todoID {
		for _, sp := range target.Spaces {
			if spaceKey(sp.MachineID, sp.Space) == key && sp.LinkedAt.Equal(linkedAt) {
				current = true
				break
			}
		}
	}
	alreadyLinked := s.todos.activeBySession[[2]string{mid, c.SessionID}] == todoID
	s.todos.mu.RUnlock()
	if !current || alreadyLinked {
		s.todos.writing.Unlock()
		return false, nil
	}
	err := s.writeTodoAgentLink(ctx, todoID, mid, c.SessionID, c.Title, c.Cwd, c.Agent, c.StartError, "space", key, false)
	s.todos.writing.Unlock()
	return err == nil, err
}

// writeTodoAgentLink writes and publishes a membership with todoBook.writing held.
// Callers release it before notifying the hub or contacting nodes.
func (s *Service) writeTodoAgentLink(ctx context.Context, todoID, mid, sid, title, cwd, agent, startError, source, via string, allowDone bool) error {
	now := time.Now().UTC()

	if st := s.todoDB(); st != nil {
		link := st.LinkTodoAgentWithSource
		if allowDone {
			link = st.LinkTodoAgentOfDoneTask
		}
		if _, err := link(ctx, todoID, mid, sid, title, cwd, agent, startError, source, via, now); err != nil {
			return err
		}
	} else {
		t, ok := s.todoNow(todoID)
		if !ok {
			return db.ErrTodoNotFound
		}
		if t.DoneAt != nil && !allowDone {
			return db.ErrTodoCompleted
		}
	}

	s.todos.mu.Lock()
	// mark active rows in all other todos (including completed) as released
	for oid, other := range s.todos.todos {
		if oid != todoID {
			for i := range other.Agents {
				if other.Agents[i].MachineID == mid && other.Agents[i].SessionID == sid && other.Agents[i].ReleasedAt == nil {
					other.Agents[i].ReleasedAt = &now
					other.UpdatedAt = now
				}
			}
		}
	}

	if target, ok := s.todos.todos[todoID]; ok {
		found := false
		for i := range target.Agents {
			if target.Agents[i].MachineID == mid && target.Agents[i].SessionID == sid {
				target.Agents[i].ReleasedAt = nil
				target.Agents[i].DismissedAt = nil
				target.Agents[i].Source = source
				target.Agents[i].Via = via
				target.Agents[i].LinkedAt = now
				if title != "" {
					target.Agents[i].Title = title
				}
				if cwd != "" {
					target.Agents[i].Cwd = cwd
				}
				if agent != "" {
					target.Agents[i].Agent = agent
				}
				if startError != "" {
					target.Agents[i].StartError = startError
				}
				found = true
				break
			}
		}
		if !found {
			target.Agents = append(target.Agents, TodoAgent{MachineID: mid, SessionID: sid, LinkedAt: now, Title: title, Cwd: cwd,
				Agent: agent, StartError: startError, Source: source, Via: via})
		}
		target.UpdatedAt = now
		if target.DoneAt == nil { // as loadTodosLocked has it: only an open todo's agents work for it
			s.todos.activeBySession[[2]string{mid, sid}] = todoID
		}
	}
	s.todos.mu.Unlock()

	return nil
}

// failTodo ends a todo whose agent did not start: done, with why, and its agents no longer count for it.
func (s *Service) failTodo(ctx context.Context, id, reason string) error {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	s.todos.writing.Lock() // memory takes the rows in the order the store committed them (todoBook.writing)
	now := time.Now().UTC()
	doneAt := now
	if st := s.todoDB(); st != nil {
		row, err := st.FailTodo(ctx, id, reason, now)
		if err != nil {
			s.todos.writing.Unlock()
			return err
		}
		if row.DoneAt != nil {
			doneAt = *row.DoneAt
		}
	}
	s.todos.mu.Lock()
	t := s.todos.todos[id]
	var failKeys [][2]string
	var closing *threadEvent
	if t != nil {
		closing = threadClosing(t.DoneAt == nil, reason, "")
		if t.DoneAt == nil {
			t.DoneAt = &doneAt
		}
		t.StartError = reason
		t.UpdatedAt = now
		for _, a := range t.Agents {
			key := [2]string{a.MachineID, a.SessionID}
			if s.todos.activeBySession[key] == id {
				failKeys = append(failKeys, key) // its pending wake and work card are withdrawn with the todo
				delete(s.todos.activeBySession, key)
			}
		}
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()
	if t == nil {
		return db.ErrTodoNotFound
	}
	if closing != nil {
		s.threadPost(id, *closing)
		s.sourceReportSoon(id, threadPostSettle) // no outcome is coming for a start that failed
	}
	if len(failKeys) > 0 {
		s.retireTodoWork(failKeys...)
	}
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return nil
}

// autoDoneTodo marks todo id done by the assistant's judge when it is still open and was not reopened after work
// began; false when the user got there first (done it, reopened it) or it is gone.
func (s *Service) autoDoneTodo(ctx context.Context, id, reason string, work time.Time) (bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false, err
	}
	s.todos.writing.Lock() // no other store write lands between the store's close and memory's
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			s.todos.writing.Unlock()
		}
	}
	defer unlock()
	now := time.Now().UTC()
	var row db.TodoRow
	st := s.todoDB()
	if st != nil {
		r, ok, err := st.AutoDoneTodo(ctx, id, reason, now, work)
		if errors.Is(err, db.ErrTodoNotFound) {
			return false, nil
		}
		if err != nil || !ok {
			return false, err
		}
		row = *r
	} else {
		t, ok := s.todoNow(id)
		if !ok {
			return false, nil
		}
		row = db.TodoRow{ID: t.ID, Title: t.Title, Notes: t.Notes, CreatedAt: t.CreatedAt, Origin: t.Origin, PlanID: t.PlanID,
			ReopenedAt: t.ReopenedAt}
		row.DoneAt, row.DoneBy, row.DoneReason, row.UpdatedAt = &now, db.DoneByAuto, reason, now
		t.keepPlan(&row)
	}
	var before Todo
	_, doneKeys, ok := s.setTodoRow(id, row, func(t *Todo) bool {
		// With a store its conditional write decided it (open, not reopened since work began) and, writing held, no
		// writer that stores a row committed after it (all of them take writing): memory takes the stored row even
		// when an in-memory bump (an agent's title synced) moved updated_at past it. Memory alone has only its own
		// state to judge by.
		if st == nil && (t.UpdatedAt.After(row.UpdatedAt) || t.DoneAt != nil || t.ReopenedAt != nil && !t.ReopenedAt.Before(work)) {
			return false
		}
		before = copyTodoLocked(t) // its agents as they worked on it, before done releases them
		return true
	})
	unlock() // the notifications run without writing, as for the other writers
	if !ok {
		return false, nil
	}
	s.todoRowSet(ctx, doneKeys)
	if row.DoneAt != nil {
		s.goRecordTaskOutcome(before, *row.DoneAt) // what the task came to (todo_outcome.go, #2820)
	}
	return ok, nil
}

// reopenTodo reopens done todo id as the user's PATCH {done: false} does (reopened_at set: the stretch of work before
// does not close it again); a todo open by then is left as it is.
func (s *Service) reopenTodo(ctx context.Context, id string) error {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	cur, ok := s.todoNow(id)
	if !ok {
		return db.ErrTodoNotFound
	}
	if cur.DoneAt == nil {
		return nil
	}
	s.todos.writing.Lock()
	now := time.Now().UTC()
	row := &db.TodoRow{ID: cur.ID, Title: cur.Title, Notes: cur.Notes, CreatedAt: cur.CreatedAt, UpdatedAt: now,
		Origin: cur.Origin, PlanID: cur.PlanID, ReopenedAt: &now, Quiet: cur.Quiet}
	cur.keepPlan(row)
	if st := s.todoDB(); st != nil {
		open := false
		r, err := st.UpdateTodo(ctx, id, nil, nil, &open, now)
		if err != nil {
			s.todos.writing.Unlock()
			return err
		}
		row = r
	}
	_, doneKeys, applied := s.setTodoRow(id, *row, nil)
	s.todos.writing.Unlock()
	if applied {
		s.todoRowSet(ctx, doneKeys)
	}
	return nil
}

// reopenFailedTodo reopens a task whose agent did not start, for its start again (retryTask): its error cleared, the
// same todo; not ok when another assistant sharing the store reopened it first (the todo as it is then).
func (s *Service) reopenFailedTodo(ctx context.Context, id string) (Todo, bool, error) {
	cur, ok, err := s.todo(ctx, id)
	if err != nil {
		return Todo{}, false, err
	}
	if !ok {
		return Todo{}, false, db.ErrTodoNotFound
	}
	s.todos.writing.Lock()
	now := time.Now().UTC()
	row := db.TodoRow{ID: cur.ID, Title: cur.Title, Notes: cur.Notes, CreatedAt: cur.CreatedAt, UpdatedAt: now,
		Origin: cur.Origin, PlanID: cur.PlanID, ReopenedAt: &now}
	cur.keepPlan(&row)
	if st := s.todoDB(); st != nil {
		r, ok, err := st.RetryTodo(ctx, id, now)
		if err != nil {
			s.todos.writing.Unlock()
			return Todo{}, false, err
		}
		if !ok {
			s.todos.writing.Unlock()
			cur, _ := s.todoNow(id)
			return formatTodo(cur), false, nil
		}
		row = *r
	}
	out, doneKeys, applied := s.setTodoRow(id, row, nil)
	s.todos.writing.Unlock()
	if applied {
		s.todoRowSet(ctx, doneKeys)
	}
	return out, true, nil
}

// ---- what the hub asks about them ----

// TodoHandoff: an agent is handed work for todo h.TodoID (assistant.todo_handoff, POST /api/v1/sessions or
// …/messages with todo_id): the todo as it is, whether the hand-off may go (no such todo: 404, a done one: 409, the
// words the hub answers with), and the text with the todo's decisions in effect before it (#1618) and, for a new
// agent, its project's background before those (#2820); a blank text stays as it is.
func (s *Service) TodoHandoff(ctx context.Context, h TodoHandoff) (TodoHandoffResult, error) {
	t, ok, err := s.todo(ctx, h.TodoID)
	if err != nil {
		return TodoHandoffResult{}, err
	}
	if !ok {
		return TodoHandoffResult{Text: h.Text, Status: http.StatusNotFound, Error: "todo not found"}, nil
	}
	text := h.Text
	if strings.TrimSpace(text) != "" {
		text = s.withTodoDecisions(ctx, h.TodoID, text)
		if h.New {
			text = s.withProjectContext(ctx, h.MachineID, h.Cwd, text) // a new agent: its project's background first
		}
	}
	res := TodoHandoffResult{Found: true, Title: t.Title, DoneAt: t.DoneAt, Text: text}
	if t.DoneAt != nil {
		res.Status, res.Error = http.StatusConflict, "todo is already completed"
	}
	return res, nil
}

// TodoLink: the agent handed work for a todo works for it from now on (assistant.todo_link); the todo's title.
// Without a title the link's comes from the prompt the agent was started with.
func (s *Service) TodoLink(ctx context.Context, p TodoLinkParams) (TodoLinkResult, error) {
	title := p.Title
	if title == "" {
		title = cleanPromptTitle(p.Prompt)
	}
	if err := s.linkTodoAgent(ctx, p.TodoID, p.MachineID, p.SessionID, title, p.Cwd, p.Agent); err != nil {
		return TodoLinkResult{}, err
	}
	res := TodoLinkResult{Title: title}
	if t, ok, err := s.todo(ctx, p.TodoID); err == nil && ok {
		res.Title = t.Title
	}
	return res, nil
}

// contextLeadRe and contextCloseRe are where a prompt's context block starts and ends (contextOpenRe: channels_plugin.go).
var (
	contextLeadRe  = lazyre.New(`^<` + compat.ContextPattern() + `\b`)
	contextCloseRe = lazyre.New(`</` + compat.ContextPattern() + `>`)
)

// cleanPromptTitle is a title for an agent from the prompt it was started with: its first line past a
// context block (<persona-context>, as older ones were, #3182).
func cleanPromptTitle(prompt string) string {
	s := strings.TrimSpace(prompt)
	if contextLeadRe.MatchString(s) {
		if loc := contextCloseRe.FindStringIndex(s); loc != nil {
			s = s[loc[1]:]
		} else if endTagIdx := strings.Index(s, ">"); endTagIdx != -1 {
			s = s[endTagIdx+1:]
		} else {
			s = ""
		}
	}
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !contextOpenRe.MatchString(trimmed) {
			return trimmed
		}
	}
	return ""
}
