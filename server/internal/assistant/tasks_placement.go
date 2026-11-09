package assistant

import (
	"context"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	machineFixed    = "fixed"
	machineFlexible = "flexible"
)

// Missing policies on older saved plans are fixed too: inferred intent cannot
// authorize moving work (and its local directory) to a different computer.
func (t *PlanTask) normalizeMachinePolicy() error {
	if t.MachinePolicy == "" {
		t.MachinePolicy = machineFixed
	}
	if t.MachinePolicy != machineFixed && t.MachinePolicy != machineFlexible {
		return fmt.Errorf("unknown machine_policy %q; use fixed or flexible", t.MachinePolicy)
	}
	return nil
}

// placeTask names t's machine by its id and makes ~ in its directory that machine's home; false: no such machine.
func (s *Service) placeTask(ctx context.Context, t *PlanTask) bool {
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return false
	}
	t.Machine = m.ID
	t.Cwd = proto.ExpandHome(t.Cwd, m.Home)
	return true
}

// healthyPlace moves t off a machine where its launcher failed its health check, to one where it works
// (launcher_health_check.go); its why says so. False: it stays.
func (s *Service) healthyPlace(ctx context.Context, t *PlanTask) bool {
	if t.MachinePolicy != machineFlexible {
		return false
	}
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return false
	}
	alt, note, moved := s.healthDetour(ctx, m, s.startLauncher(ctx, t.Launcher))
	if note != "" && !strings.Contains(t.Why, note) {
		t.Why = strings.TrimSpace(t.Why + " (" + note + ")")
	}
	if moved {
		// Keep the machine the task was first placed on across repeated moves; each move refreshes the landing and the
		// health-check reason. Names, not ids, go on the card/receipt (#2271).
		if t.Reroute == nil {
			t.Reroute = &PlanReroute{FromMachine: firstNonEmpty(m.Name, m.ID)}
		}
		t.Reroute.ToMachine = firstNonEmpty(alt.Name, alt.ID)
		t.Reroute.Reason = note
		t.Machine, t.Cwd = alt.ID, rehome(t.Cwd, m, alt)
	}
	return moved
}

// startTaskAgent starts the agent for t on its machine (another when its launcher does not work there: t says which)
// and answers the machine's id and the session's: the machine it reached is the one the agent is linked to (looking it
// up again after the start could find another, or none). A failed start counts against the launcher there.
func (s *Service) startTaskAgent(ctx context.Context, t *PlanTask, retry bool) (mid, sid string, err error) {
	t.normalizeReadonlyPermission()
	if err := t.normalizeMachinePolicy(); err != nil {
		return "", "", err
	}
	params := map[string]any{"launcher": t.Launcher, "model": t.Model, "effort": t.Effort, "tier": t.Tier}
	if err := s.applyStartTier(ctx, params); err != nil {
		return "", "", err
	}
	if err := s.applyModel(ctx, "agent.new", params); err != nil {
		return "", "", err
	}
	t.Launcher, _ = params["launcher"].(string)
	t.Model, _ = params["model"].(string)
	t.Effort, _ = params["effort"].(string)
	s.healthyPlace(ctx, t)
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return "", "", fmt.Errorf("unknown machine %q", t.Machine)
	}
	if !m.Reachable {
		return "", "", machineOffline(m.Name)
	}
	if t.MachinePolicy == machineFixed {
		// Only an actual terminal start can recheck a repaired terminal shell.
		// An explicit retry keeps that existing recovery path on the fixed target;
		// login-shell checks and authentication gates are not bypassed.
		if r, bad := s.launcherUnusable(ctx, m.ID, s.startLauncher(ctx, t.Launcher)); bad && !(retry && r.Kind == errKindNotInTerminal) {
			return "", "", i18n.Err("assistant-tasks.fixed_launcher_unhealthy", i18n.Args{
				"machine": m.ID, "launcher": t.Launcher, "reason": r.Error,
			})
		}
	}
	prompt := t.Prompt
	if len(t.DoneWhen) > 0 { // what the supervisor will check (supervise.go)
		prompt += "\n\n" + prompts.RenderTaskDoneWhen(t.DoneWhen)
	}
	if !t.Readonly && s.doneNeedsDelivery(ctx) { // what done means: live and checked (delivery.go)
		prompt += "\n\n" + prompts.RenderTaskDelivery()
	}
	// The project background, and the todo's decisions (a task started again may have some), go before the prompt
	// (#2820, #1618); the tab is named from the title.
	prompt = s.withProjectContext(ctx, m.ID, t.Cwd, s.withTodoDecisions(ctx, t.TodoID, prompt))
	params["cwd"], params["prompt"], params["tab_name"] = t.Cwd, prompt, s.tabName(ctx, t.Title)
	if t.Model != "" {
		params["model"] = t.Model
	}
	mode := t.Permission
	if mode == "" {
		mode = s.settings(ctx).DefaultPermissionMode
		if mode == "" {
			mode = "default"
		}
	}
	params["permission_mode"] = mode
	if t.Ultracode != nil && *t.Ultracode {
		params["ultracode"] = true
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	picked, _ := params["launcher"].(string)
	if err := s.call(ctx, m.ID, DeviceSessionStart, params, &res); err != nil {
		s.recordStartFailure(ctx, m.ID, s.startLauncher(ctx, picked), err)
		return "", "", err
	}
	if res.Session.SessionID == "" || res.Session.MachineID != "" && res.Session.MachineID != m.ID {
		return "", "", &rpc.Error{Code: "identity_conflict", Message: i18n.T(s.lang(ctx), "assistant-tasks.start_identity_conflict")}
	}
	if res.Session.StartError != "" || res.Session.Starting {
		observed := res.Session
		observed.Launcher = s.startLauncher(ctx, picked)
		s.followStarts(ctx, DeviceSessions{MachineID: m.ID, Sessions: []proto.Session{observed}})
		if observed.StartError != "" {
			return "", "", sessionStartErr(observed)
		}
	}
	if !res.Session.Starting {
		s.recordStartSuccess(ctx, m.ID, s.startLauncher(ctx, picked))
	}
	return m.ID, res.Session.SessionID, nil
}

// updatePlanTask keeps the machine, directory and why task i of started plan planID runs with now.
func (s *Service) updatePlanTask(ctx context.Context, planID string, i int, t PlanTask) {
	p, err := s.loadPlan(ctx, planID)
	if err != nil || i >= len(p.Tasks) {
		return
	}
	p.Tasks[i].Machine, p.Tasks[i].Cwd, p.Tasks[i].Why = t.Machine, t.Cwd, t.Why
	p.Tasks[i].Reroute = t.Reroute
	p.Tasks[i].Launcher, p.Tasks[i].Model, p.Tasks[i].Effort = t.Launcher, t.Model, t.Effort
	if _, err := s.movePlan(ctx, p, planStarted); err != nil {
		s.logf("plan %s: %v", planID, err)
		return
	}
	s.event(ctx, map[string]any{"type": "task_plan", "plan": p})
}
