package assistant

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// A cancelled task must never read as "done" in the assistant's snapshot, whichever marker records the cancellation
// (#2272). Before the fix taskState only looked at DoneAt and reported "done" for every closed todo, so a cancelled
// task showed up to the model as finished. Cancelled() is the same source of truth the receipt and auto-completion
// paths use, so the snapshot label cannot drift from them.
func TestTaskStateDistinguishesCancelledFromDone(t *testing.T) {
	done := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

	// Every way a todo can be cancelled. All of them carry DoneAt (cancellation closes the task).
	cancelledCases := []struct {
		name string
		todo Todo
	}{
		{"cancelled completion marker", Todo{ID: "t_marker", DoneAt: &done, DoneBy: db.DoneByCancelled, Origin: "conv1"}},
		{"cancelled_at timestamp", Todo{ID: "t_stamp", DoneAt: &done, CancelledAt: &done, Origin: "conv1"}},
		// DoneBy is a review, so only the supervision verdict makes this one cancelled: exercises that operand alone.
		{"supervision cancel verdict", Todo{ID: "t_ledger", DoneAt: &done, DoneBy: db.DoneByReview,
			Supervision: &Supervision{State: reviewCancelled}, Origin: "conv1"}},
	}
	for _, c := range cancelledCases {
		if !c.todo.Cancelled() {
			t.Fatalf("%s: fixture must read as cancelled through Todo.Cancelled()", c.name)
		}
		if got := taskState(c.todo, nil); got != "cancelled" {
			t.Fatalf("%s: taskState reads %q, want cancelled", c.name, got)
		}
		if label := taskStateLabel("cancelled"); label == "" || label == taskStateLabel("done") {
			t.Fatalf("%s: cancelled label %q must differ from the done label %q", c.name, label, taskStateLabel("done"))
		}
	}

	// Reverse cases: work that really finished still reads as done, so the cancelled gate does not over-match.
	doneCases := []struct {
		name string
		todo Todo
	}{
		{"hub auto-completed", Todo{ID: "t_auto", DoneAt: &done, DoneBy: db.DoneByAuto, Origin: "conv1"}},
		{"closed by a review acceptance", Todo{ID: "t_review", DoneAt: &done, DoneBy: db.DoneByReview,
			Supervision: &Supervision{State: reviewAccepted}, Origin: "conv1"}},
		{"finished by the user by hand", Todo{ID: "t_user", DoneAt: &done, Origin: "conv1"}},
	}
	for _, c := range doneCases {
		if c.todo.Cancelled() {
			t.Fatalf("%s: fixture must not read as cancelled", c.name)
		}
		if got := taskState(c.todo, nil); got != "done" {
			t.Fatalf("%s: taskState reads %q, want done", c.name, got)
		}
	}
}

// Reopen then re-finish must read as "done". reopenTodo rebuilds the row without DoneAt/DoneBy (the raw supervision
// ledger is kept but masked by Supervision.state while DoneAt is nil), so once the work closes again with a
// non-cancel marker Todo.Cancelled() is false. The end-to-end reopen lifecycle lives in TestCancelledTaskStaysClosed;
// this pins the snapshot behaviour for the post-reopen shape that flow produces (#2272).
func TestReopenedAndRefinishedTaskReadsAsDone(t *testing.T) {
	done := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	// The shape reopenTodo leaves behind, then finished by hand: DoneAt set, no DoneBy/CancelledAt marker, and no
	// cancel verdict (a fresh supervised round would move it back to pending).
	refinished := Todo{ID: "t_reopen", Title: "Finish it after all", DoneAt: &done, Origin: "conv1",
		Supervision: &Supervision{State: ""}}
	if refinished.Cancelled() {
		t.Fatalf("a reopened todo carries no cancellation marker and must not read as cancelled: %+v", refinished)
	}
	if got := taskState(refinished, nil); got != "done" {
		t.Fatalf("a reopened task finished by hand reads as %q, want done", got)
	}

	// A task cancelled again after the reopen gets a fresh marker, which wins and reads as cancelled once more.
	recancelled := Todo{ID: "t_recancel", DoneAt: &done, DoneBy: db.DoneByCancelled,
		Supervision: &Supervision{State: reviewCancelled}, Origin: "conv1"}
	if got := taskState(recancelled, nil); got != "cancelled" {
		t.Fatalf("a re-cancelled task reads as %q, want cancelled", got)
	}
}
