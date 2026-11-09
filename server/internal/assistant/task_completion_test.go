package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskCompletionOwnBackgroundWorkNeedsNoReport(t *testing.T) {
	for _, kind := range []string{"shell", "monitor", "agent", "workflow"} {
		t.Run(kind, func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login regression test passes"}, 0)
			s.taskWorking(ctx, "box", "s1")
			f.judge = `{"verdict":"done","reason":"The original login task and its tests are complete"}`
			f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: todo.Notes},
				proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Bash", ToolUseID: "login-tests", ToolInput: json.RawMessage(`{"command":"go test ./login"}`)})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Status: proto.StatusDone,
				Background: []proto.BackgroundTask{{ID: "login-work", Kind: kind}}})
			s.taskTurnEnded(ctx, "box", "s1")
			if todoNow(s, todo.ID).Supervision.State == reviewPending || strings.Contains(feedKinds(s, "conv1"), "receipt") {
				t.Fatal("running background work must not complete the task")
			}
			f.setItems(proto.Item{Kind: proto.KindNotice, Role: "system", Wake: true, Task: &proto.TaskNote{Kind: kind, TaskID: "login-work", ToolUseID: "login-tests", Status: "completed", Title: "Verify the login correction"}},
				proto.Item{Kind: proto.KindText, Role: "assistant", Text: "The original login task is complete; its regression test passes and the correction is delivered."})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Status: proto.StatusDone})
			s.taskTurnEnded(ctx, "box", "s1")
			if todoNow(s, todo.ID).Supervision.State != reviewPending {
				t.Fatal("own background work completion must not require report_to_supervisor")
			}
		})
	}
}

func TestTaskCompletionLimitContinueNeedsNoReport(t *testing.T) {
	for _, parsed := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw continuation", true: "injected metadata"}[parsed], func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login regression test passes"}, 0)
			s.taskWorking(ctx, "box", "s1")
			continuation := proto.Item{Kind: proto.KindText, Role: "user", Text: prompts.RenderAgentLimitContinue()}
			if parsed {
				continuation.Injected = prompts.ParseInjected(continuation.Text)
			}
			f.setItems(continuation, proto.Item{Kind: proto.KindText, Role: "assistant", Text: "The login correction is delivered and the regression test passes after resuming the original task."})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Status: proto.StatusDone})
			f.judge = `{"verdict":"done","reason":"The resumed original task is complete"}`
			s.taskTurnEnded(ctx, "box", "s1")
			if todoNow(s, todo.ID).Supervision.State != reviewPending {
				t.Fatal("limit continuation must not require report_to_supervisor")
			}
		})
	}
}

func TestTaskCompletionSideConversationGate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from     string
		ask      string
		injected *proto.Injected
		queued   bool
		wantDone bool
	}{
		{name: "peer", from: "peer-agent", ask: "Did this session fix the unrelated parser problem?"},
		{name: "queued peer", from: "peer-agent", ask: "Did this session fix the unrelated parser problem?", queued: true},
		{name: "loop inspection", ask: "/loop 5m inspect the other agents"},
		{name: "loop command without arguments", ask: "/loop"},
		{name: "not a loop command", ask: "/loopfix continues the approved login correction", wantDone: true},
		{name: "peer envelope", ask: `<cross-session-message from="peer-agent">Did you fix the parser?</cross-session-message>`},
		{name: "teammate envelope", ask: `<teammate-message teammate_id="peer-agent">Did you fix the parser?</teammate-message>`},
		{name: "source envelope", ask: `<persona-context kind="side-conversation">Source: another conversation.</persona-context>Did you fix the parser?`},
		{name: "injected patrol", ask: "Check another task's progress", injected: &proto.Injected{Kind: proto.InjectedSystem, State: "supervise"}},
		{name: "legacy patrol", ask: "📋 督办：你派出去的任务有 1 个要你处理\n这是 Persona 的督办消息"}, // i18n-ignore: synthetic legacy injected message
		{name: "actual task", ask: "iPad 横屏时登录页崩溃，修好并补测试", wantDone: true},      // i18n-ignore: matches synthetic task fixture
		{name: "Persona continuation", from: "Persona", ask: "Continue the login fix and hand over the verified result", wantDone: true},
		{name: "task supervisor", from: "conv1", ask: "Continue the login fix and hand over the verified result", wantDone: true},
		{name: "user continuation", ask: "Continue with the approved fix", wantDone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login regression test passes"}, 0)
			f.judge = `{"verdict":"done","reason":"The latest answer is complete"}`
			s.taskWorking(ctx, "box", "s1")
			answer := "已告知另一会话：本会话未处理该问题" // i18n-ignore: synthetic regression answer
			if tc.wantDone {
				answer = "The login fix is complete; the regression test passes and the result is ready for review."
			}
			f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: tc.ask, From: tc.from, Injected: tc.injected, Queued: tc.queued},
				proto.Item{Kind: proto.KindText, Role: "assistant", Text: answer})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: answer, UpdatedAt: time.Now()})
			s.taskTurnEnded(ctx, "box", "s1")
			current := todoNow(s, todo.ID)
			if got := current.Supervision.State == reviewPending; got != tc.wantDone || current.DoneAt != nil {
				t.Fatalf("pending review = %v, want %v: %+v", got, tc.wantDone, current)
			}
			items, _, _ := s.feed(ctx, "conv1", time.Time{})
			var receipt receiptBody
			for _, it := range items {
				if it.Kind == feedReceipt {
					_ = json.Unmarshal(it.Body, &receipt)
				}
			}
			if receipt.Review == nil || receipt.Review.State != "checking" {
				t.Fatalf("receipt must not imply acceptance before judgment: %+v", receipt)
			}
		})
	}
}

func TestTaskCompletionSideTurnNeedsFreshTaskReport(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stale      bool
		noEvidence bool
		failed     bool
		unrelated  bool
		wantReview bool
	}{
		{name: "fresh current-task done report", wantReview: true},
		{name: "old done report", stale: true},
		{name: "done report without evidence", noEvidence: true},
		{name: "failed done report", failed: true},
		{name: "unrelated report evidence", unrelated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login regression test passes"}, 0)
			s.taskWorking(ctx, "box", "s1")
			f.judge = `{"verdict":"done","reason":"The current task report is complete"}`
			at := time.Now()
			reportAt := at.Add(time.Second)
			if tc.stale {
				reportAt = todo.CreatedAt.Add(-time.Minute)
			}
			text := todo.Title + ": complete; Login regression test passes"
			evidence := []string{"Login regression test passes: go test ./login: PASS"}
			if tc.noEvidence {
				evidence = nil
			}
			if tc.unrelated {
				text = "Parser question answered"
				evidence = []string{"Another session received the message"}
			}
			args, _ := json.Marshal(map[string]any{"kind": "done", "text": text, "evidence": evidence})
			if !tc.failed {
				if _, err := s.reportToSupervisor(ctx, "box", "s1", args, reportAt); err != nil {
					t.Fatal(err)
				}
				if todoNow(s, todo.ID).Supervision.State == reviewPending {
					t.Fatal("report alone must not set pending review")
				}
			}
			f.setItems(proto.Item{Kind: proto.KindText, Role: "user", From: "peer-agent", Text: "Did you fix a separate parser problem?", TS: at},
				proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "mcp__persona__report_to_supervisor", ToolUseID: "done-report", ToolInput: args, TS: reportAt},
				proto.Item{Kind: proto.KindToolResult, Role: "user", ToolUseID: "done-report", Text: "Report delivered", IsError: tc.failed},
				proto.Item{Kind: proto.KindText, Role: "assistant", Text: "I told the other session that this session did not handle that problem."})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone})
			todo = todoNow(s, todo.ID)
			s.tasks.mu.Lock()
			work := s.tasks.working[todo.ID]
			s.tasks.mu.Unlock()
			s.autoFinishTask(todo, work, true)
			if got := todoNow(s, todo.ID).Supervision.State == reviewPending; got != tc.wantReview {
				t.Fatalf("pending review = %v, want %v", got, tc.wantReview)
			}
		})
	}
}

func TestTaskCompletionDoneReportFreshness(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"current", "missing tool time", "missing turn boundary", "before side request", "before reopen", "before criteria update", "progress only", "untrusted tool namespace", "report is not a verdict"} {
		t.Run(name, func(t *testing.T) {
			work := at.Add(-time.Second)
			todo := Todo{Title: "Fix login", CreatedAt: at.Add(-time.Hour), Supervision: &Supervision{
				DoneWhen: []string{"Login test passes"}, Progress: &TaskProgress{Kind: upReportDone, Text: "Fix login complete", At: at.Add(time.Second)},
			}}
			items := []proto.Item{
				{Kind: proto.KindText, Role: "user", From: "peer", TS: at},
				{Kind: proto.KindToolUse, Role: "assistant", ToolName: "report_to_supervisor", ToolUseID: "report", TS: at.Add(time.Second),
					ToolInput: json.RawMessage(`{"kind":"done","text":"Fix login complete","evidence":["Login test passes: PASS"]}`)},
				{Kind: proto.KindToolResult, Role: "user", ToolUseID: "report", Text: "Delivered"},
			}
			want := false
			switch name {
			case "current":
				want = true
			case "missing tool time":
				items[1].TS = time.Time{}
			case "missing turn boundary":
				work = time.Time{}
				items[0].TS = time.Time{}
			case "before side request":
				items[0].TS = at.Add(2 * time.Second)
			case "before reopen":
				reopened := at.Add(2 * time.Second)
				todo.ReopenedAt = &reopened
			case "before criteria update":
				todo.Supervision.UpdatedAt = at.Add(2 * time.Second)
			case "progress only":
				items = items[:1]
			case "untrusted tool namespace":
				items[1].ToolName = "mcp__extension__report_to_supervisor"
			case "report is not a verdict":
				todo.Supervision.Progress.Kind = upReportProgress
			}
			if _, _, got := freshTaskDoneReport(todo, items, work); got != want {
				t.Fatalf("eligible=%v, want %v", got, want)
			}
		})
	}
}

func TestTaskCompletionRejectsChangedAcceptanceCriteria(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login test passes"}, 0)
	s.taskWorking(ctx, "box", "s1")
	todo = todoNow(s, todo.ID)
	f.judge = `{"verdict":"done","reason":"Previous acceptance criteria met"}`
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Login test passes"})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone})
	f.hookReads(receiptHooks{after: func(int) {
		_, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
			v.DoneWhen = []string{"Login test passes", "Deployment verified"}
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
	}})
	s.tasks.mu.Lock()
	work := s.tasks.working[todo.ID]
	s.tasks.mu.Unlock()
	s.autoFinishTask(todo, work, true)
	if todoNow(s, todo.ID).Supervision.State == reviewPending {
		t.Fatal("old criteria yielded pending review")
	}
}

func TestTaskCompletionRequiresEveryAgentToSettle(t *testing.T) {
	todo := Todo{Agents: []TodoAgent{{MachineID: "computer", SessionID: "agent"}}}
	for _, tc := range []struct {
		name   string
		agents []proto.Session
		want   bool
	}{
		{"missing", nil, false},
		{"tool running with stale UI state", []proto.Session{{Status: proto.StatusWorking, State: proto.StateIdle}}, false},
		{"waiting", []proto.Session{{Status: proto.StatusBlocked}}, false},
		{"unknown", []proto.Session{{Status: proto.StatusUnknown}}, false},
		{"background", []proto.Session{{Status: proto.StatusDone, Background: []proto.BackgroundTask{{ID: "tool"}}}}, false},
		{"done", []proto.Session{{Status: proto.StatusDone}}, true},
		{"idle", []proto.Session{{Status: proto.StatusIdle}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskAgentsSettled(todo, tc.agents); got != tc.want {
				t.Fatalf("settled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTaskCompletionRejectsAResumedTurnDuringJudgment(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic close", true: "pending review"}[supervised], func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			if supervised {
				s.superviseTodo(ctx, todo.ID, "conv1", []string{"Verify delivery"}, 0)
				todo = todoNow(s, todo.ID)
			}
			f.judge = `{"verdict":"done","reason":"The previous answer claims success"}`
			s.taskWorking(ctx, "box", "s1")
			s.tasks.mu.Lock()
			work := s.tasks.working[todo.ID]
			s.tasks.mu.Unlock()
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: "Earlier work finished"})
			changed := false
			f.hookReads(receiptHooks{before: func(int) {
				if changed {
					return
				}
				changed = true
				s.taskWorking(ctx, "box", "s1")
				// Even a short resumed turn that already stopped invalidates the older judgment.
				f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: "More work remains"})
			}})
			s.autoFinishTask(todo, work, true)
			current := todoNow(s, todo.ID)
			if !changed || current.DoneAt != nil || current.Supervision != nil && current.Supervision.State == reviewPending {
				t.Fatalf("stale judgment applied: changed=%v todo=%+v", changed, current)
			}
			if ok, err := s.finishCurrentTask(ctx, todo.ID, "stale write", work); err != nil || ok {
				t.Fatalf("stale completion committed: %v %v", ok, err)
			}
		})
	}
}

func TestTaskReceiptDoesNotCompleteARunningToolOrMissingAgent(t *testing.T) {
	for _, missing := range []bool{false, true} {
		s, f := newTaskService(t)
		startedTask(t, s, f)
		f.judge = `{"verdict":"done","reason":"Progress looks finished"}`
		s.taskWorking(context.Background(), "box", "s1")
		if missing {
			f.setSessions("box")
		} else {
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateIdle,
				Activity: "python3", LastText: "The initial check passed", UpdatedAt: time.Now()})
		}
		s.taskTurnEnded(context.Background(), "box", "s1")
		if got := feedKinds(s, "conv1"); strings.Contains(got, "receipt") || onlyTodo(s).DoneAt != nil {
			t.Fatalf("receipt/completion while unresolved: missing=%v feed=%s", missing, got)
		}
	}
}

func TestTaskCompletionWaitsForTheAnswerAfterATool(t *testing.T) {
	for _, finalStatus := range []string{proto.StatusDone, proto.StatusIdle} {
		t.Run(finalStatus, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			f.judge = `{"verdict":"done","reason":"Delivery verified"}`
			ctx := context.Background()
			s.taskWorking(ctx, "box", "s1")
			idle := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle,
				LastText: "The first check passed", UpdatedAt: time.Now()}
			f.setSessions("box", idle)
			s.fleetKeepPrevious("box", []proto.Session{idle})
			f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "The first check passed"},
				proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Bash", ToolUseID: "command"},
				proto.Item{Kind: proto.KindToolResult, Role: "user", ToolUseID: "command", Text: "still verifying"})
			s.taskTurnEnded(ctx, "box", "s1")
			if strings.Contains(feedKinds(s, "conv1"), "receipt") || onlyTodo(s).DoneAt != nil || f.judged != 0 {
				t.Fatal("a temporary idle/tool result was treated as the completed task")
			}
			// A later session update must finish the held receipt without another working tick.
			f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Delivery verified; all acceptance checks passed"},
				proto.Item{Kind: "turn_duration", Role: "system"})
			final := idle
			final.Status, final.LastText, final.UpdatedAt = finalStatus, "Delivery verified", time.Now()
			f.setSessions("box", final)
			s.setSessions("box", []proto.Session{final})
			waitFor(t, "completion after the final session update", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
			f.mu.Lock()
			judged := f.judged
			f.mu.Unlock()
			if judged != 1 {
				t.Fatalf("completion judgments = %d, want 1", judged)
			}
		})
	}
}

func TestTaskCompletionReconcilesAnAnswerArrivingDuringTheRead(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	f.judge = `{"verdict":"done","reason":"Delivery verified"}`
	ctx := context.Background()
	s.taskWorking(ctx, "box", "s1")
	idle := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle, Rev: 1}
	f.setSessions("box", idle)
	s.fleetKeepPrevious("box", []proto.Session{idle})
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Checking"},
		proto.Item{Kind: proto.KindToolResult, Role: "user", ToolUseID: "command", Text: "Tool output"})
	f.hookReads(receiptHooks{after: func(read int) {
		if read != 1 {
			return
		}
		// The final event overtakes the old tool transcript response.
		f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Delivery verified"},
			proto.Item{Kind: "turn_duration", Role: "system"})
		final := idle
		final.Status, final.Rev = proto.StatusDone, 2
		f.setSessions("box", final)
		s.setSessions("box", []proto.Session{final})
	}})
	s.taskTurnEnded(ctx, "box", "s1")
	waitFor(t, "completion without another session update", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
}

func TestAutoDoneSkipRecognizesCancellation(t *testing.T) {
	now := time.Now()
	// Hand-made todo
	hand := Todo{}
	if got := autoDoneSkip(hand, now, true); got != "not_a_task" {
		t.Fatalf("hand-made todo: got %q, want not_a_task", got)
	}

	// Normal task
	task := Todo{Origin: "conv1"}
	if got := autoDoneSkip(task, now, true); got != "" {
		t.Fatalf("normal task: got %q, want empty", got)
	}

	// Cancelled by DoneBy
	cancelled := Todo{Origin: "conv1", DoneBy: db.DoneByCancelled}
	if got := autoDoneSkip(cancelled, now, true); got != "cancelled" {
		t.Fatalf("cancelled task by DoneBy: got %q, want cancelled", got)
	}

	// Cancelled by CancelledAt
	cancelledAt := Todo{Origin: "conv1", CancelledAt: &now}
	if got := autoDoneSkip(cancelledAt, now, true); got != "cancelled" {
		t.Fatalf("cancelled task by CancelledAt: got %q, want cancelled", got)
	}

	// Normal done task
	done := Todo{Origin: "conv1", DoneAt: &now, DoneBy: db.DoneByAuto}
	if got := autoDoneSkip(done, now, true); got != "done" {
		t.Fatalf("done task: got %q, want done", got)
	}
}

func TestCancelledTaskSettledAgentDoesNotIssueDoneReceipt(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	s.taskWorking(ctx, "box", "s1")
	f.judge = `{"verdict":"done","reason":"Delivery verified"}`

	// Cancel the task
	s.todos.writing.Lock()
	now := time.Now().UTC()
	row := db.TodoRow{
		ID: todo.ID, Title: todo.Title, Notes: todo.Notes, CreatedAt: todo.CreatedAt, Origin: todo.Origin,
		PlanID: todo.PlanID, DoneAt: &now, DoneBy: db.DoneByCancelled, DoneReason: "User cancelled", UpdatedAt: now,
	}
	s.setTodoRow(todo.ID, row, nil)
	s.todos.writing.Unlock()

	beforeFeed, beforeNotices := feedKinds(s, "conv1"), len(f.notices())

	// The agent finishes and settles
	idle := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone,
		LastText: "Done with work", UpdatedAt: time.Now()}
	f.setSessions("box", idle)
	s.fleetKeepPrevious("box", []proto.Session{idle})
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Done with work"},
		proto.Item{Kind: "turn_duration", Role: "system"})

	s.taskTurnEnded(ctx, "box", "s1")

	// Verify no receipt in feed and no notices
	if got := feedKinds(s, "conv1"); got != beforeFeed || len(f.notices()) != beforeNotices {
		t.Fatalf("cancelled task produced a receipt or notice: feed %q, notices %v", got, f.notices())
	}
	if current := todoNow(s, todo.ID); current.DoneBy != db.DoneByCancelled {
		t.Fatalf("task completion overwrote cancellation status: DoneBy=%s", current.DoneBy)
	}
}

// When a task is reassigned and its original agent is released, that agent settling must not mark the task done.
func TestReassignedAbortedAgentSettledDoesNotCompleteTask(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	s.taskWorking(ctx, "box", "s1")
	f.judge = `{"verdict":"done","reason":"Delivery verified"}`

	// Reassign task: link a new agent on box/s2, releasing box/s1
	now := time.Now().UTC()
	s.todos.writing.Lock()
	tptr := s.todos.todos[todo.ID]
	for i := range tptr.Agents {
		if tptr.Agents[i].MachineID == "box" && tptr.Agents[i].SessionID == "s1" {
			tptr.Agents[i].ReleasedAt = &now
		}
	}
	tptr.Agents = append(tptr.Agents, TodoAgent{
		MachineID: "box", SessionID: "s2", LinkedAt: now, Title: todo.Title,
	})
	s.todos.mu.Lock()
	delete(s.todos.activeBySession, [2]string{"box", "s1"})
	s.todos.activeBySession[[2]string{"box", "s2"}] = todo.ID
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()

	beforeFeed := feedKinds(s, "conv1")

	// The original released agent box/s1 finishes and settles
	idle1 := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone,
		LastText: "Old agent finished", UpdatedAt: time.Now()}
	f.setSessions("box", idle1)
	s.fleetKeepPrevious("box", []proto.Session{idle1})
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Old agent finished"},
		proto.Item{Kind: "turn_duration", Role: "system"})

	s.taskTurnEnded(ctx, "box", "s1")

	// Must NOT complete the task
	if current := todoNow(s, todo.ID); current.DoneAt != nil {
		t.Fatalf("aborted agent completing prematurely marked task done: %+v", current)
	}
	if got := feedKinds(s, "conv1"); got != beforeFeed {
		t.Fatalf("aborted agent settling issued receipt: %+v", got)
	}

	// Now the new active agent box/s2 finishes and settles
	s.taskWorking(ctx, "box", "s2")
	idle2 := proto.Session{MachineID: "box", SessionID: "s2", Status: proto.StatusDone,
		LastText: "New agent really finished", UpdatedAt: time.Now()}
	f.setSessions("box", idle2)
	s.fleetKeepPrevious("box", []proto.Session{idle2})
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "New agent really finished"},
		proto.Item{Kind: "turn_duration", Role: "system"})

	s.taskTurnEnded(ctx, "box", "s2")

	// Now the task SHOULD complete properly by the real agent!
	waitFor(t, "task completion by active agent", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
}
