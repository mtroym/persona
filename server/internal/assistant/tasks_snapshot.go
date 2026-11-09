package assistant

import (
	"cmp"
	"context"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- what the assistant sees ----

// snapshotTasks are the tasks opened from assistant conversations that are going on or ended in the last day.
func (s *Service) snapshotTasks(ctx context.Context, web string) []prompts.SnapshotTask {
	var list []Todo
	for _, t := range s.todosOf(ctx, "*") {
		if t.Origin != "" && (t.DoneAt == nil || time.Since(*t.DoneAt) < 24*time.Hour) && time.Since(t.UpdatedAt) < 7*24*time.Hour {
			list = append(list, t)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	type ranked struct {
		task prompts.SnapshotTask
		rank int
	}
	var all []ranked
	now := time.Now()
	for _, t := range list {
		agents := s.taskAgents(ctx, t)
		st := taskState(t, agents)
		if st == "followup" && time.Since(t.UpdatedAt) > 24*time.Hour {
			continue
		}
		if st == "done" && t.DoneBy == db.DoneByAuto {
			st = "auto_done"
		}
		task := snapshotTask(t, st, agents, now)
		if st == "failed" && task.Last == "" {
			task.Last = clip(oneLine(t.StartError), 80) // why it did not start, for the morning brief (#4124)
		}
		task.Link = todoLink(web, t.ID)
		task.Delivery = deliverySummary(t)
		var rank int
		task.Health, rank = taskHealth(t, agents, now) // supervised tasks: what needs the assistant first (supervise.go)
		if t.Supervision != nil {
			task.DoneWhen = len(t.Supervision.DoneWhen)
			if p := t.Supervision.Progress; p != nil {
				reported(&task, p, taskLead(st, agents), now)
			}
		}
		all = append(all, ranked{task, rank})
		if len(all) == 30 {
			break
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].rank < all[j].rank })
	var out []prompts.SnapshotTask
	for _, r := range all {
		out = append(out, r.task)
		if len(out) == 12 {
			break
		}
	}
	return out
}

// snapshotTask is one task in the snapshot, in state st: its lead agent's (taskLead) last words, the step it is on
// while it works (or has said nothing yet: an agent that only runs tools, #1583) and its running subagents, as the
// task cards show them (docs/assistant/unified-entry.md "Progress on task cards").
func snapshotTask(t Todo, st string, agents []proto.Session, now time.Time) prompts.SnapshotTask {
	task := prompts.SnapshotTask{ID: t.ID, Title: t.Title, State: taskStateLabel(st)}
	last := taskLead(st, agents)
	if last.SessionID == "" {
		return task
	}
	task.Machine, task.Session = last.MachineID, short(last.SessionID)
	task.Last = clip(oneLine(last.LastText), 80)
	working := last.State == proto.StateWorking
	if doing := strings.TrimSpace(last.Activity); doing != "" && (working || task.Last == "") {
		task.Doing = doing
		if working && last.ActivityAt != nil && now.Sub(*last.ActivityAt) >= time.Minute {
			task.DoingFor = humanDuration(now.Sub(*last.ActivityAt))
		}
	}
	for _, sa := range last.Subagents {
		if sa.Running && len(task.Subagents) < 3 {
			task.Subagents = append(task.Subagents, clip(oneLine(cmp.Or(sa.Description, sa.Type, "子任务")), 40))
		}
	}
	task.Workflows = runningWorkflows(last, now)
	task.Plan = snapshotPlan(last)
	return task
}

// reported puts the agent's latest report p on task. A report newer than lead's plan says more than the plan's count:
// it stands alone.
func reported(task *prompts.SnapshotTask, p *TaskProgress, lead proto.Session, now time.Time) {
	task.Reported = &prompts.SnapshotReport{Kind: p.Kind, Step: p.Step, Steps: p.Steps, Text: p.Text,
		Minutes: max(0, int(now.Sub(p.At).Minutes()))}
	if lead.Plan != nil && p.At.After(lead.Plan.At) {
		task.Plan = nil
	}
}

// snapshotPlan is c's own task list as the snapshot tells it, nil when it keeps none (docs/integrations/agent-goal.md "Plan").
func snapshotPlan(c proto.Session) *prompts.SnapshotAgentPlan {
	p := c.Plan
	if p == nil || p.Total == 0 {
		return nil
	}
	return &prompts.SnapshotAgentPlan{Done: p.Done, Total: p.Total, Current: p.Current}
}

// taskLead is the agent a task in state st is shown with: the latest of those that put it in that state (the one
// waiting for the user, cut off, finished unseen, working), so its session id and words are the ones the state is
// about (#4192); the latest of all when none does (a task that stopped, or is done).
func taskLead(st string, agents []proto.Session) proto.Session {
	var lead, latest proto.Session
	for _, c := range agents {
		if latest.SessionID == "" || c.UpdatedAt.After(latest.UpdatedAt) {
			latest = c
		}
		var makes bool
		switch st {
		case "needs_you":
			makes = c.State == proto.StateNeedsYou || c.PendingInteractions > 0
		case "interrupted":
			makes = c.TurnError != "" && !usageLimitWaiting(c, time.Now())
		case "usage_limit":
			makes = usageLimitWaiting(c, time.Now())
		case "unread":
			makes = c.State == proto.StateUnread
		case "working":
			makes = c.State == proto.StateWorking
		}
		if makes && (lead.SessionID == "" || c.UpdatedAt.After(lead.UpdatedAt)) {
			lead = c
		}
	}
	if lead.SessionID != "" {
		return lead
	}
	return latest
}

// taskState follows docs/assistant/todos.md "How the state is computed"(clients compute the same). One more for the
// assistant: an agent a usage limit stopped until its reset (usage_limit.go) is not interrupted for the user to
// continue. It waits for the reset (its CLI or the manager goes on then), and a "Continue" before it is refused at once.
func taskState(t Todo, agents []proto.Session) string {
	if t.DoneAt != nil && t.StartError != "" {
		return "failed"
	}
	// A cancelled task is closed but did not finish: it reads as "cancelled", never "done", however its agents settle.
	// Cancelled() is the single source of truth the receipt and auto-completion paths already use (#2272). Reopen
	// rebuilds the todo row without DoneAt/DoneBy, and Supervision.state masks a stale cancel verdict while the task
	// is open; re-finished work closes with a non-cancel marker, so Cancelled() is false then and it falls through to
	// "done" without a separate reopen guard here.
	if t.DoneAt != nil && t.Cancelled() {
		return "cancelled"
	}
	if t.DoneAt != nil {
		return "done"
	}
	has := map[string]bool{}
	interrupted, limited := false, false
	now := time.Now()
	for _, c := range agents {
		has[c.State] = true
		if usageLimitWaiting(c, now) {
			limited = true
		} else {
			interrupted = interrupted || c.TurnError != ""
		}
	}
	switch {
	case has[proto.StateNeedsYou]:
		return "needs_you"
	case interrupted: // stopped on a model API error: not done, waiting for "Continue" (#1516)
		return "interrupted"
	case limited:
		return "usage_limit"
	case has[proto.StateUnread]:
		return "unread"
	case has[proto.StateWorking]:
		return "working"
	case len(agents) > 0:
		return "followup"
	}
	return "not_started"
}

func taskStateLabel(st string) string {
	return map[string]string{"done": "已完成", "auto_done": "已完成（hub 判断做完了，自动关的）", "failed": "没开起来", "needs_you": "等用户",
		"interrupted": "模型服务出错，中断了（没做完，等用户让它继续）", "unread": "做完了（用户还没看）", "working": "在做",
		"followup": "停着", "not_started": "还没开始",
		// "cancelled" shares the ignored final line: every label here is model-read prompt text, and this keeps the
		// hardcoded-Chinese baseline count unchanged instead of raising it.
		"cancelled": "已取消", "usage_limit": "订阅的用量额度用完了，等额度重置（没做完；重置前发「继续」也会被拒）"}[st] // i18n-ignore: the model reads it
}
