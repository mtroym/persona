package node

import (
	"context"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Agents other than Claude Code that Persona starts without Claude's flags: a compatible CLI (on the PATH as traex), and a
// launcher the hub says runs Codex or a compatible CLI (hub.json machines[].launchers, a wrapper the login shell's list does not
// show as one). A Codex launcher the login shell lists starts through codex_start.go instead.

// agentLauncher is a launcher as launchers.list answers: with the agent it starts, for hubs that read agent_type.
type agentLauncher struct {
	proto.Launcher
	AgentType string `json:"agent_type,omitempty"`
}

// launcherAgentType is the agent a launcher starts when nothing else says: codex and traex by name, else claude.
func launcherAgentType(name string) string {
	if name == "codex" || name == traexAgent {
		return name
	}
	return "claude"
}

func typedLaunchers(list []proto.Launcher) []agentLauncher {
	out := make([]agentLauncher, 0, len(list))
	seen := map[string]bool{}
	for _, l := range list {
		if !seen[l.Name] {
			t := l.Agent
			if t == "" {
				t = launcherAgentType(l.Name)
			}
			out = append(out, agentLauncher{Launcher: l, AgentType: t})
			seen[l.Name] = true
		}
	}
	return out
}

func otherAgentCommand(launcher, resume, prompt string) string {
	cmd := launcher
	if resume != "" {
		cmd += " resume " + shellQuote(resume)
	}
	if prompt != "" {
		cmd += " " + shellQuote(prompt)
	}
	return cmd
}

// startOtherAgent opens a new tab in ws (a new space when ""), runs the launcher there (resuming a session when
// resume is set, with the first message as its argument) and waits until the host reports an agent of that kind in
// the tab with a session id that was not running before (or the one resumed).
func (n *Node) startOtherAgent(ctx context.Context, launch host.Launcher, cwd, launcher, kind, prompt, resume, ws, tabName string) (*proto.Session, error) {
	created, err := n.newTab(ctx, launch, cwd, ws, tabName, resume) // retire.go
	if err != nil {
		return nil, err
	}
	pane := created.Pane.String()
	// Exclude existing sessions when the normal scan has to infer a Codex rollout.
	n.mu.Lock()
	existing := make(map[string]bool, len(n.agents))
	for id := range n.agents {
		existing[id] = true
	}
	n.mu.Unlock()
	time.Sleep(300 * time.Millisecond) // the fresh shell loads ~/.zshrc; typed input is buffered either way
	// A Codex wrapper is told to herdr as Codex (hostherdr.go); a compatible CLI is herdr's to detect. The node's
	// directory in front, the assistant's environment left out (agentenv.go).
	shells := paneShells(ctx)
	cmd := withHerdrAgent(typedHerdrAgent(launch, kind, shells...), otherAgentCommand(launcher, resume, prompt))
	// Not in the agent sandbox (agentsandbox.go).
	n.notePaneStart(launcher)
	done, err := n.runAgentCommand(ctx, launch, created, ws, "", n.agentCommand(cmd, shellQuote, shells...), shells...) // typedstart.go
	if err != nil {
		return nil, err
	}
	defer done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			screen, _ := n.readPane(context.Background(), pane, "recent-unwrapped", 30)
			return nil, fmt.Errorf("wait for %s session: %w\n%s", kind, ctx.Err(), lastLines(screen, 12))
		case <-ticker.C:
		}
		if err := n.refreshHosts(ctx); err != nil {
			continue
		}
		n.mu.Lock()
		var id string
		var agent host.Agent
		for candidate, a := range n.agents {
			if a.PaneID == pane && a.Agent == kind && candidate != "" &&
				((resume == "" && !existing[candidate]) || candidate == resume) {
				id, agent = candidate, a
				break
			}
		}
		n.mu.Unlock()
		if id == "" {
			continue
		}
		n.state.put(id, sessionState{Pane: pane, Launcher: launcher, Cwd: cwd, StartedAt: time.Now().UTC()})
		n.bindPane(pane, id)
		session := n.sessionFor(id, agent)
		session.Agent = kind
		n.holdResume(ctx, launch, pane, id, agentLaunch{Agent: kind, Launcher: launcher, ID: id})
		return session, nil
	}
}
