package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Supervised tasks (#3124, docs/assistant/unified-entry.md"Supervision"). A task the main conversation hands out (task_plan, or an
// agent_start that became a quiet task) has the main conversation as its supervisor: the task keeps a ledger (what
// done means, how long it should take, where its review stands) and the hub watches it without a model call. What
// needs the supervisor — the work looks finished and waits for review, an agent stopped short of it, waits for an
// answer too long, makes no progress, was cut off or did not start — becomes a signal; the signals of all tasks go to
// the main conversation as one supervision message (the quiet tasks' notices are among them), at most every few minutes, and
// the turn it starts is not pushed unless the assistant pushes (notify_user). The assistant answers with task_review:
// accepted closes the todo (its agents are closed later, tasks_close.go), rework sends the gaps back to the agent (at
// most maxReworkRounds times), needs_user waits for the user, cancelled ends it.
//
// A supervised todo is never closed by the hub's judge (done.go): a finished stretch of work waits for review instead.

const (
	reviewPending   = "pending_review"
	reviewAccepted  = "accepted"
	reviewRework    = "rework"
	reviewNeedsUser = "needs_user"
	reviewCancelled = "cancelled"
)

// maxReworkRounds is how many times a task is sent back; one more rework goes to the user instead.
const maxReworkRounds = 2

// Supervision is a supervised task's ledger, as the apps get it on the todo (supervision).
type Supervision struct {
	Supervisor    string       `json:"supervisor"` // the conversation that handed it out
	DoneWhen      []string     `json:"done_when"`
	ExpectMinutes int          `json:"expect_minutes,omitempty"`
	State         string       `json:"state"` // "" going on, or one of review*
	Round         int          `json:"round"` // reworks so far
	Reviews       []TaskReview `json:"reviews"`
	// AutoReview is the latest independent review of its change (supervise_autoreview.go, #2173); nil when none ran.
	AutoReview *AutoReview `json:"auto_review,omitempty"`
	UpdatedAt  time.Time   `json:"updated_at"`
	// Progress: what its agent last reported to the supervisor (report_to_supervisor, agent_control.go). A live
	// reading kept in memory only (the next report brings it back after a restart), and apart from UpdatedAt, which
	// names the review a task waits for.
	Progress *TaskProgress `json:"progress,omitempty"`
}

// TaskProgress is a supervised task's agent's latest report: its kind, the step it is on of how many (0: not said),
// what it said in a line, and when.
type TaskProgress struct {
	Kind  string    `json:"kind"`
	Step  int       `json:"step,omitempty"`
	Steps int       `json:"steps,omitempty"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
}

// TaskReview is one review: the assistant's verdict, with each criterion's evidence or gap.
type TaskReview struct {
	At      time.Time     `json:"at"`
	Verdict string        `json:"verdict"`
	By      string        `json:"by,omitempty"`    // "reviewer": the independent reviewer's rework (#2173); "" the supervisor's
	Round   int           `json:"round,omitempty"` // the rework round it started (rework)
	Checks  []ReviewCheck `json:"checks,omitempty"`
	Note    string        `json:"note,omitempty"`
}

type ReviewCheck struct {
	Criterion string `json:"criterion"`
	Met       bool   `json:"met"`
	Evidence  string `json:"evidence,omitempty"`
}

func (v *Supervision) clone() *Supervision {
	if v == nil {
		return nil
	}
	out := *v
	out.DoneWhen = slices.Clone(v.DoneWhen)
	out.Reviews = make([]TaskReview, len(v.Reviews))
	for i, r := range v.Reviews {
		out.Reviews[i] = r
		out.Reviews[i].Checks = slices.Clone(r.Checks)
	}
	if out.DoneWhen == nil {
		out.DoneWhen = []string{}
	}
	if v.Progress != nil {
		p := *v.Progress
		out.Progress = &p
	}
	if v.AutoReview != nil {
		a := *v.AutoReview
		out.AutoReview = &a
	}
	return &out
}

// state is the review state that holds now: an accepted or cancelled task the user reopened goes on again.
func (v *Supervision) state(t Todo) string {
	if v == nil {
		return ""
	}
	if t.DoneAt == nil && (v.State == reviewAccepted || v.State == reviewCancelled) {
		return ""
	}
	return v.State
}

// supervisionStore is what the ledgers keep in the database; a store without it keeps them in memory only.
type supervisionStore interface {
	ListTodoSupervision(ctx context.Context) ([]db.TodoSupervisionRow, error)
	PutTodoSupervision(ctx context.Context, r db.TodoSupervisionRow) error
	ReviewDoneTodo(ctx context.Context, id, reason string, accepted bool, at time.Time) (*db.TodoRow, bool, error)
	LinkTodoReviewer(ctx context.Context, todoID, machineID, sessionID, title, cwd, via string, at time.Time) error
}

var _ supervisionStore = (*db.DB)(nil)

func (s *Service) supervisionDB() supervisionStore {
	st, _ := s.store().(supervisionStore)
	return st
}

func supervisionFromRow(r db.TodoSupervisionRow) *Supervision {
	v := &Supervision{Supervisor: r.Supervisor, DoneWhen: r.DoneWhen, ExpectMinutes: r.ExpectMinutes, State: r.State, Round: r.Round,
		UpdatedAt: r.UpdatedAt}
	_ = json.Unmarshal(r.Reviews, &v.Reviews)
	if len(r.AutoReview) > 0 {
		var a AutoReview
		if json.Unmarshal(r.AutoReview, &a) == nil {
			v.AutoReview = &a
		}
	}
	return v.clone()
}

func (v *Supervision) row(todoID string) db.TodoSupervisionRow {
	raw, _ := json.Marshal(v.Reviews)
	var auto json.RawMessage
	if v.AutoReview != nil {
		auto, _ = json.Marshal(v.AutoReview)
	}
	return db.TodoSupervisionRow{TodoID: todoID, Supervisor: v.Supervisor, DoneWhen: v.DoneWhen, ExpectMinutes: v.ExpectMinutes,
		State: v.State, Round: v.Round, Reviews: raw, AutoReview: auto, UpdatedAt: v.UpdatedAt}
}

// cleanDoneWhen is a task's criteria as kept: trimmed, no empty or repeated ones, at most 6 of at most 200 characters.
func cleanDoneWhen(list []string) []string {
	out := []string{}
	for _, c := range list {
		c = clip(oneLine(c), 200)
		if c != "" && !slices.Contains(out, c) && len(out) < 6 {
			out = append(out, c)
		}
	}
	return out
}

// superviseTodo makes todo id a supervised task of conversation supervisor.
func (s *Service) superviseTodo(ctx context.Context, id, supervisor string, doneWhen []string, expect int) {
	_, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		v.Supervisor, v.DoneWhen, v.ExpectMinutes = supervisor, cleanDoneWhen(doneWhen), max(0, min(expect, 24*60))
		return true
	})
	if err != nil {
		s.logf("task %s: supervision: %v", id, err)
	}
}

// updateSupervision changes todo id's ledger (a new one when it has none) with change, which says whether it changed
// anything; the ledger as it is then. Writes are one at a time.
func (s *Service) updateSupervision(ctx context.Context, id string, change func(*Supervision) bool) (Supervision, bool, error) {
	b := &s.supervise
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	t, ok := s.todoNow(id)
	if !ok {
		return Supervision{}, false, db.ErrTodoNotFound
	}
	v := t.Supervision.clone()
	if v == nil {
		v = (&Supervision{}).clone()
	}
	if !change(v) {
		return *v, false, nil
	}
	v.UpdatedAt = time.Now().UTC()
	if st := s.supervisionDB(); st != nil && s.todoDB() != nil {
		if err := st.PutTodoSupervision(ctx, v.row(id)); err != nil {
			return Supervision{}, false, err
		}
	}
	s.todos.mu.Lock()
	if cur := s.todos.todos[id]; cur != nil {
		next := v.clone()
		if cur.Supervision != nil { // the progress is noteProgress's: a report that came meanwhile stays
			next.Progress = cur.Supervision.clone().Progress
		}
		cur.Supervision = next
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	return *v, true, nil
}

// noteProgress keeps p as supervised task id's progress (in memory, no ledger write: see Supervision.Progress) and
// tells the apps.
func (s *Service) noteProgress(ctx context.Context, id string, p TaskProgress) {
	s.todos.mu.Lock()
	cur := s.todos.todos[id]
	ok := cur != nil && cur.Supervision != nil
	if ok {
		v := cur.Supervision.clone()
		v.Progress = &p
		cur.Supervision = v
	}
	s.todos.mu.Unlock()
	if ok {
		s.todosChanged(ctx)
	}
}

// ---- the snapshot ----

// taskHealth is how supervised task t stands, for the snapshot: a label and a rank (lower: the more it needs the
// assistant). "" for a task that is not supervised.
func taskHealth(t Todo, agents []proto.Session, now time.Time) (string, int) {
	v := t.Supervision
	if v == nil {
		return "", 5
	}
	switch v.state(t) {
	case reviewNeedsUser:
		return "等你决定", 0
	case reviewPending:
		return "待验收", 1
	case reviewAccepted:
		return "已验收", 8
	case reviewCancelled:
		return "已取消", 8
	}
	if t.DoneAt != nil {
		return "", 8
	}
	for _, c := range agents {
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			if wait := now.Sub(cmp.Or(c.StateSince, c.UpdatedAt)); wait >= time.Minute {
				return "等回答 " + humanDuration(wait), 0
			}
			return "等回答", 0
		}
	}
	for _, c := range agents {
		if c.State != proto.StateWorking {
			continue
		}
		moved := c.UpdatedAt
		if c.ActivityAt != nil && c.ActivityAt.After(moved) {
			moved = *c.ActivityAt
		}
		if still := now.Sub(moved); !moved.IsZero() && still >= stallAfter(v.ExpectMinutes) {
			return "卡住 " + humanDuration(still), 2
		}
	}
	if v.state(t) == reviewRework {
		return fmt.Sprintf("返工第 %d 轮", v.Round), 3
	}
	return "正常", 5
}
