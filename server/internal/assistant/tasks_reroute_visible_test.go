package assistant

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// rerouteHub builds the launcher-health hub with machines whose display names differ from their ids, so a test can
// prove the reroute record carries display names, not raw ids.
func rerouteHub(t *testing.T) (*Service, *fakeHub) {
	s, f := launcherHealthHubOn(t, newStorelessService)
	f.devices = []Device{
		{ID: "server", Name: "Hub Server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "l4", Name: "Lab Mac L4", Home: "/home/me", SSHHost: "l4-ssh", Online: true, Reachable: true},
		{ID: "box", Name: "Build Box", Home: "/Users/me", Online: true, Reachable: true},
	}
	return s, f
}

// blockWorkLauncher marks machine's "work" launcher as one the terminal cannot run. That kind skips the on-demand
// probe (only a real terminal start clears it, #2018), so the detour is taken deterministically.
func blockWorkLauncher(s *Service, machine string) {
	s.recordHealth(context.Background(), machine, "work", healthResult{
		err: "launcher missing in the terminal", source: healthSourceStart, kind: errKindNotInTerminal,
	})
}

// failWorkLauncherCheck seeds a clearable check-sourced failure: unusable while fresh, but a later passing check
// record clears it (unlike a not_in_terminal block, which only a real start clears).
func failWorkLauncherCheck(s *Service, machine string) {
	s.recordHealth(context.Background(), machine, "work", healthResult{
		err: "90 秒内没有回答", source: healthSourceCheck,
	})
}

func planFirstTask(t *testing.T, s *Service, machine string) PlanTask {
	t.Helper()
	res, err := s.toolTaskPlan(context.Background(), "conv9", json.RawMessage(
		`{"tasks":[{"title":"修 CI","prompt":"修 CI","machine":"`+machine+`","cwd":"~/code/app","launcher":"work",
		"readonly":false,"user_requested":false,"machine_policy":"flexible","why":"代码在那"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`plan_[0-9a-f]+`).FindString(res)
	p, err := s.loadPlan(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Tasks[0]
}

// Before the fix a rerouted task only had the reason buried in Why: nothing named the original and landing machines.
// Now the plan/card body carries a structured reroute with both display names and the health-check reason (#2271).
func TestRerouteRecordedWithDisplayNames(t *testing.T) {
	s, _ := rerouteHub(t)
	blockWorkLauncher(s, "l4")         // l4 cannot run "work".
	failWorkLauncherCheck(s, "server") // leave box as the single healthy alternative.

	task := planFirstTask(t, s, "l4")
	if task.Machine != "box" {
		t.Fatalf("task was not rerouted to box: %+v", task)
	}
	r := task.Reroute
	if r == nil {
		t.Fatalf("rerouted task has no reroute record: %+v", task)
	}
	if r.FromMachine != "Lab Mac L4" || r.ToMachine != "Build Box" {
		t.Fatalf("reroute must use display names, got %+v", r)
	}
	if !strings.Contains(r.Reason, errKindNotInTerminal) {
		t.Fatalf("reroute reason lost the health-check note: %q", r.Reason)
	}
}

// Reverse case: a task planned on a healthy machine stays there and carries no reroute line at all (#2271).
func TestHealthyTaskHasNoReroute(t *testing.T) {
	s, _ := rerouteHub(t)

	task := planFirstTask(t, s, "box") // box is healthy.
	if task.Machine != "box" || task.Reroute != nil {
		t.Fatalf("a healthy task must stay put with no reroute, got %+v", task)
	}
}

// Repeated moves keep the machine the task was first placed on and always refresh the latest landing and reason.
func TestRerouteKeepsOriginalAndLatestLanding(t *testing.T) {
	s, _ := rerouteHub(t)
	ctx := context.Background()
	blockWorkLauncher(s, "l4")         // first move: l4 -> box.
	failWorkLauncherCheck(s, "server") // box is the only healthy machine at first.

	task := planFirstTask(t, s, "l4")
	if task.Machine != "box" || task.Reroute == nil {
		t.Fatalf("first move did not reroute: %+v", task)
	}

	blockWorkLauncher(s, "box") // box now fails too.
	// Repair server so it becomes the only healthy fallback; the second move lands there.
	if r := s.recordHealth(ctx, "server", "work", healthResult{ok: true, source: healthSourceCheck}); r.State != healthOK {
		t.Fatalf("server was not restored to healthy: %+v", r)
	}
	if moved := s.healthyPlace(ctx, &task); !moved {
		t.Fatal("the second healthyPlace did not move the task off box")
	}
	if task.Machine != "server" {
		t.Fatalf("second move landed on %q, want server", task.Machine)
	}
	r := task.Reroute
	if r.FromMachine != "Lab Mac L4" {
		t.Fatalf("original machine was lost after a second move: %+v", r)
	}
	if r.ToMachine != "Hub Server" {
		t.Fatalf("latest landing was not recorded: %+v", r)
	}
	if !strings.Contains(r.Reason, "box") {
		t.Fatalf("reason was not refreshed for the second move: %q", r.Reason)
	}
}

// The receipt path resolves a todo's reroute through its plan; tasks without a plan, an unmatched todo, or no reroute
// resolve to nothing (#2271).
func TestRerouteOfTaskLookup(t *testing.T) {
	s, _ := rerouteHub(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := &TaskPlan{ID: "plan_lookup", Conversation: "conv1", State: planOpen, CreatedAt: now, UpdatedAt: now,
		Tasks: []PlanTask{{Title: "t", Machine: "box", TodoID: "todo_1",
			Reroute: &PlanReroute{FromMachine: "Lab Mac L4", ToMachine: "Build Box", Reason: "not_in_terminal: down"}}}}
	if err := s.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := s.rerouteOfTask(ctx, "todo_1", "plan_lookup"); got == nil || got.ToMachine != "Build Box" {
		t.Fatalf("lookup by todo id returned %+v", got)
	}
	if got := s.rerouteOfTask(ctx, "todo_other", "plan_lookup"); got != nil {
		t.Fatalf("an unmatched todo must resolve to nil, got %+v", got)
	}
	if got := s.rerouteOfTask(ctx, "todo_1", ""); got != nil {
		t.Fatalf("an empty plan id must resolve to nil, got %+v", got)
	}

	p.Tasks[0].Reroute = nil
	if err := s.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := s.rerouteOfTask(ctx, "todo_1", "plan_lookup"); got != nil {
		t.Fatalf("a task that never moved must resolve to nil, got %+v", got)
	}
}

// lastDoneReceipt drives a started task's agent to a finished turn and reads the receipt it put in the conversation.
func lastDoneReceipt(t *testing.T, s *Service, f *fakeHub) receiptBody {
	t.Helper()
	ctx := context.Background()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "活干完了", 0)
	items, _, _ := s.feed(ctx, "conv1", time.Time{})
	if len(items) == 0 {
		t.Fatal("the finished turn put no receipt in the conversation")
	}
	var rb receiptBody
	_ = json.Unmarshal(items[len(items)-1].Body, &rb)
	if items[len(items)-1].Kind != feedReceipt || rb.State != "done" {
		t.Fatalf("last feed item is not a done receipt: kind=%s body=%s", items[len(items)-1].Kind, items[len(items)-1].Body)
	}
	return rb
}

// givePlanAReroute writes a reroute onto the plan task that runs todo, the shape a health detour leaves behind.
func givePlanAReroute(t *testing.T, s *Service, todo Todo) {
	t.Helper()
	ctx := context.Background()
	p, err := s.loadPlan(ctx, todo.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	i := -1
	for k := range p.Tasks {
		if p.Tasks[k].TodoID == todo.ID {
			i = k
			break
		}
	}
	if i < 0 {
		t.Fatalf("plan %s has no task for todo %s", todo.PlanID, todo.ID)
	}
	p.Tasks[i].Reroute = &PlanReroute{FromMachine: "Lab Mac L4", ToMachine: "Build Box", Reason: "not_in_terminal: down"}
	if err := s.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
}

// End to end: a rerouted task's done receipt carries the original machine, the landing machine and the reason.
func TestDoneReceiptShowsReroute(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	givePlanAReroute(t, s, todo)

	rb := lastDoneReceipt(t, s, f)
	if rb.Reroute == nil {
		t.Fatalf("the done receipt dropped the reroute: %+v", rb)
	}
	if rb.Reroute.FromMachine != "Lab Mac L4" || rb.Reroute.ToMachine != "Build Box" ||
		!strings.Contains(rb.Reroute.Reason, "not_in_terminal") {
		t.Fatalf("the done receipt's reroute lost a name or the reason: %+v", rb.Reroute)
	}
}

// Reverse case: a task that ran on the machine it was asked for gets a done receipt with no reroute line.
func TestDoneReceiptHasNoRerouteWhenHealthy(t *testing.T) {
	s, f := newTaskService(t)
	startedTask(t, s, f) // no detour recorded.

	if rb := lastDoneReceipt(t, s, f); rb.Reroute != nil {
		t.Fatalf("a healthy task's receipt must carry no reroute, got %+v", rb.Reroute)
	}
}

// Reroute is hub-owned: a reroute a model passes in task_plan args must be discarded, not persisted. On a healthy
// machine there is no real detour, so the saved task carries none. Without the reset the model could forge one and
// its presence would also suppress seeding of the true original on a later detour (#2271).
func TestModelSuppliedRerouteInPlanArgsIsDropped(t *testing.T) {
	s, _ := rerouteHub(t)
	res, err := s.toolTaskPlan(context.Background(), "conv9", json.RawMessage(
		`{"tasks":[{"title":"修 CI","prompt":"修 CI","machine":"box","cwd":"~/code/app","launcher":"work",
		"readonly":false,"user_requested":false,"machine_policy":"flexible","why":"代码在那",
		"reroute":{"from_machine":"Lab Mac L4","to_machine":"Build Box","reason":"forged"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`plan_[0-9a-f]+`).FindString(res)
	p, err := s.loadPlan(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tasks[0].Reroute != nil {
		t.Fatalf("a model-supplied reroute must be dropped on a healthy machine, got %+v", p.Tasks[0].Reroute)
	}
}

// A user's explicit machine edit supersedes a plan-time health detour: the stale reroute is cleared (and the task
// becomes fixed), so the receipt names the machine it actually runs on. An edit that keeps the same machine leaves
// the detour record alone (#2271).
func TestManualMachineEditClearsAStaleReroute(t *testing.T) {
	detoured := func() *PlanTask {
		return &PlanTask{Title: "t", Machine: "box", MachinePolicy: machineFlexible, Cwd: "~/code/app",
			Reroute: &PlanReroute{FromMachine: "Lab Mac L4", ToMachine: "Build Box", Reason: "not_in_terminal: down"}}
	}

	retargeted := detoured()
	applyPlanEdit(retargeted, PlanTask{Machine: "server"})
	if retargeted.Machine != "server" || retargeted.MachinePolicy != machineFixed {
		t.Fatalf("the manual retarget was not applied: %+v", retargeted)
	}
	if retargeted.Reroute != nil {
		t.Fatalf("a manual retarget must clear the stale reroute, got %+v", retargeted.Reroute)
	}

	sameMachine := detoured()
	applyPlanEdit(sameMachine, PlanTask{Machine: "box"})
	if sameMachine.Reroute == nil {
		t.Fatal("an edit that keeps the machine must preserve the detour record")
	}
}
