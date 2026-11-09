package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the assistant's task_plan tool ----

// planTaskIn is a task as task_plan's arguments have it. Its todo_id is the todo to run on (PlanTask.ReuseTodo); the
// plan's own todo_id, the todo a started task became, is the hub's to set, and the shallower field wins in JSON.
type planTaskIn struct {
	PlanTask
	TodoID string `json:"todo_id"`
}

func (s *Service) toolTaskPlan(ctx context.Context, sessionID string, args json.RawMessage) (string, error) {
	var in struct {
		Tasks []planTaskIn `json:"tasks"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("tasks: %v", err)
	}
	var a struct{ Tasks []PlanTask }
	for _, t := range in.Tasks {
		t.PlanTask.ReuseTodo = strings.TrimSpace(t.TodoID)
		a.Tasks = append(a.Tasks, t.PlanTask)
	}
	if len(a.Tasks) == 0 {
		return "", fmt.Errorf("tasks is empty")
	}
	if len(a.Tasks) > 8 {
		return "", fmt.Errorf("at most 8 tasks in one plan")
	}
	reused := map[string]bool{}
	for i := range a.Tasks {
		t := &a.Tasks[i]
		t.normalizeReadonlyPermission()
		t.TodoID, t.Error, t.ErrorCode, t.ErrorArgs = "", "", "", nil
		// Reroute is hub-owned: drop any reroute a model passes in task_plan args, so healthyPlace seeds it only from a
		// real health detour and its first-detour guard keeps the true original machine (#2271).
		t.Reroute = nil
		t.Title, t.Prompt, t.Cwd = strings.TrimSpace(t.Title), strings.TrimSpace(t.Prompt), strings.TrimSpace(t.Cwd)
		t.Model = strings.TrimSpace(t.Model)
		t.Permission = strings.TrimSpace(t.Permission)
		if t.Permission == "" {
			t.Permission = s.settings(ctx).DefaultPermissionMode
			if t.Permission == "" {
				t.Permission = "default"
			}
		}
		if err := t.normalizeMachinePolicy(); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		t.DoneWhen, t.ExpectMinutes = cleanDoneWhen(t.DoneWhen), max(0, min(t.ExpectMinutes, 24*60))
		if t.Title == "" || t.Prompt == "" || t.Cwd == "" {
			return "", fmt.Errorf("task %d: title, prompt and cwd are required", i+1)
		}
		if t.Model != "" && !proto.ValidModelID(t.Model) { // it goes to the agent as --model (#1644)
			return "", fmt.Errorf("task %d: model %q is not a model id: %s", i+1, t.Model, proto.ModelIDRule)
		}
		if !s.placeTask(ctx, t) {
			return "", fmt.Errorf("task %d: unknown machine %q (machines: %s)", i+1, t.Machine, s.machineIDs(ctx))
		}
		// Validate the tier now without pinning its model: it is resolved again at dispatch.
		preview := *t
		if err := s.applyTaskTier(ctx, &preview); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		s.healthyPlace(ctx, &preview) // health follows the resolved launcher
		t.Machine, t.Cwd, t.Why = preview.Machine, preview.Cwd, preview.Why
		t.Reroute = preview.Reroute
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", fmt.Errorf("todos are unavailable: %v", err)
	}
	for i, t := range a.Tasks {
		if t.ReuseTodo == "" {
			continue
		}
		if reused[t.ReuseTodo] {
			return "", fmt.Errorf("task %d: todo_id %s is already the todo of another task in this plan", i+1, t.ReuseTodo)
		}
		reused[t.ReuseTodo] = true
		if err := s.reusableTodo(sessionID, t.ReuseTodo, false); err != nil {
			return "", fmt.Errorf("task %d: %v", i+1, err)
		}
	}
	dups := s.planDuplicates(ctx, a.Tasks) // before the plan's own todos exist
	now := time.Now().UTC()
	p := &TaskPlan{ID: "plan_" + randomHex(6), Conversation: sessionID, Tasks: a.Tasks, State: planOpen, CreatedAt: now, UpdatedAt: now}
	if err := s.savePlan(ctx, p); err != nil {
		return "", err
	}
	// The plan goes in the item's body too: a client that missed the task_plan event (or raced it) still draws the
	// card from the conversation itself.
	if _, err := s.appendFeed(ctx, sessionID, feedPlan, p.ID, map[string]any{"plan": p}); err != nil {
		s.logf("task plan %s: feed: %v", p.ID, err)
	}
	// A plan from the health space hands its prompts to agents outside it: it waits for the user's "Start" on its card,
	// whatever would start it at once, and its push names no task (health.go).
	health := s.inHealth(ctx, sessionID)
	if health || !s.planStartsAtOnce(ctx, p.Tasks) {
		s.event(ctx, map[string]any{"type": "task_plan", "plan": p})
		// A card below the answer is easy to miss: push it as well.
		titles := make([]string, 0, len(p.Tasks))
		for _, t := range p.Tasks {
			titles = append(titles, t.Title)
		}
		l := s.lang(ctx)
		title := i18n.T(l, "assistant-tasks.plan_waiting", i18n.Args{"n": len(p.Tasks)})
		if len(titles) == 1 {
			title = i18n.T(l, "assistant-tasks.plan_waiting_single")
		}
		here := s.settings(ctx).MachineID
		machineName := "Persona"
		if m, ok := s.device(ctx, here); ok {
			machineName = m.Name
		}
		body := strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep"))
		if health {
			title, body = healthNotice(l)
		}
		s.push(ctx, Notification{Subtitle: machineName, DeviceID: here, SessionID: sessionID, Title: title, Body: body})
		return fmt.Sprintf("Plan %s is on the user's screen as a card and pushed to their phone; nothing has started. "+
			"Don't wait for it: carry on. When they start it, the tasks show up in the context snapshot.", p.ID) + dups, nil
	}
	started, err := s.startPlan(ctx, p.ID, nil, false)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Queued %d task(s) for dispatch without asking (read-only, asked for by the user, or a kind the user trusts):\n", len(started.Tasks))
	for _, t := range started.Tasks {
		if t.Error != "" {
			fmt.Fprintf(&b, "- %s on %s: dispatch failed: %s\n", t.Title, t.Machine, t.Error)
		} else {
			fmt.Fprintf(&b, "- %s → todo %s on %s (dispatching)\n", t.Title, t.TodoID, t.Machine)
		}
	}
	b.WriteString("This is not confirmation that any agent started. Startup can still fail; the task cards and context snapshot show the outcome.")
	if s.isMain(ctx, sessionID) {
		b.WriteString(" You supervise them: a supervision message comes when one needs you (review it with task_review).")
	}
	b.WriteString(dups)
	return b.String(), nil
}

// applyPlanEdit takes what the user changed on the card; an empty field keeps the assistant's.
func applyPlanEdit(t *PlanTask, e PlanTask) {
	if s := strings.TrimSpace(e.Title); s != "" {
		t.Title = s
	}
	if s := strings.TrimSpace(e.Prompt); s != "" {
		t.Prompt = s
	}
	if s := strings.TrimSpace(e.Machine); s != "" {
		if s != t.Machine {
			t.MachinePolicy = machineFixed
			// The user's explicit retarget supersedes a plan-time health detour: drop the stale reroute so the receipt
			// names the machine the task will actually run on, not the old landing (#2271).
			t.Reroute = nil
		}
		t.Machine = s
	}
	if e.MachinePolicy != "" {
		t.MachinePolicy = e.MachinePolicy
	}
	if s := strings.TrimSpace(e.Cwd); s != "" {
		t.Cwd = s
	}
	if s := strings.TrimSpace(e.Launcher); s != "" {
		t.Launcher = s
	}
	if s := strings.TrimSpace(e.Model); s != "" {
		t.Model = s
	}
	if s := strings.TrimSpace(e.Tier); s != "" {
		t.Tier = s
	}
	if s := strings.TrimSpace(e.Effort); s != "" {
		t.Effort = s
	}
	if s := strings.TrimSpace(e.Permission); s != "" {
		t.Permission = s
	}
	if e.Ultracode != nil {
		t.Ultracode = e.Ultracode
	}
}

func planErr(w http.ResponseWriter, err error) {
	var invalid planInvalid
	switch {
	case errors.As(err, &invalid):
		apierr.Write(w, http.StatusBadRequest, invalid.error)
	case errors.Is(err, errPlanNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, errPlanNotOpen):
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusServiceUnavailable, err)
	}
}
