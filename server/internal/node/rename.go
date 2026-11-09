package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// renameWorktree targets a listed host container, including one with no Agents.
func (n *Node) renameWorktree(ctx context.Context, kind, workspace, name string) error {
	name = strings.Join(strings.Fields(name), " ")
	if workspace == "" || name == "" || utf8.RuneCountInString(name) > maxLabel {
		return fmt.Errorf("worktree and a name of 1 to %d characters are required", maxLabel)
	}
	h, err := n.hostOfKind(kind)
	if err != nil {
		return err
	}
	org, ok := h.(host.Organizer)
	if !ok {
		return unsupported(h)
	}
	launch, ok := h.(host.Launcher)
	if !ok {
		return unsupported(h)
	}
	spaces, err := launch.Spaces(ctx)
	if err != nil {
		return err
	}
	for _, space := range spaces {
		if space.ID == workspace {
			if err := org.RenameSpace(ctx, workspace, name); err != nil {
				return err
			}
			_ = n.refreshHosts(ctx, h)
			return nil
		}
	}
	return fmt.Errorf("the selected worktree is no longer available")
}

// renameSpace renames a herdr workspace (what the phone calls a space) and returns the name it shows now: a named herdr
// session's with the session in front (sessionLabel, #2196), which the hub keys pins and to-do links by.
func (n *Node) renameSpace(ctx context.Context, workspaceID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if workspaceID == "" || name == "" {
		return "", fmt.Errorf("workspace and name are required")
	}
	org, err := hostCap[host.Organizer](n) // space ids carry no host kind yet: the engine's
	if err != nil {
		return "", err
	}
	if err := org.RenameSpace(ctx, workspaceID, name); err != nil {
		return "", err
	}
	if _, ok := org.(*herdrHost); ok {
		if session, _ := herdr.SplitID(workspaceID); session != "" {
			return sessionLabel(session, strings.TrimPrefix(name, session+sessionLabelSep)), nil
		}
	}
	return name, nil
}

// renameSession renames the herdr tab a session runs in and / or the Claude session itself. A live claude gets
// `/rename <title>` (it records a custom-title, shown in its prompt, /resume and our lists); for one that is not
// running we append the same custom-title line Claude Code writes.
func (n *Node) renameSession(ctx context.Context, id string, tab, title *string) error {
	if tab != nil {
		if name := strings.TrimSpace(*tab); name != "" {
			if done, tabID := n.starts.nameTab(id, name); done {
				tab = nil
			} else if tabID != "" { // a tab start() made: in the host sessions start in
				org, err := hostCap[host.Organizer](n)
				if err != nil {
					return err
				}
				if err := org.RenameSlot(ctx, tabID, name); err != nil {
					return err
				}
				tab = nil
			}
		}
	}
	a, live := n.agent(id)
	if tab != nil {
		name := strings.TrimSpace(*tab)
		if name == "" || !live || a.TabID == "" {
			return fmt.Errorf("the tab can only be renamed while the session runs in herdr")
		}
		org, _, err := paneCap[host.Organizer](n, a.PaneID)
		if err != nil {
			return err
		}
		if err := org.RenameSlot(ctx, a.TabID, name); err != nil {
			return err
		}
	}
	if title != nil {
		t := strings.Join(strings.Fields(*title), " ")
		if t == "" {
			return fmt.Errorf("empty title")
		}
		if home, thread, codex := n.codexThread(id); codex {
			if server := n.codexArchiveServer(id, home); server != nil {
				call, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				return server.wire.call(call, "thread/name/set", map[string]any{"threadId": thread, "name": t}, nil)
			}
			if !live {
				return fmt.Errorf("%w: connect this Codex session before renaming it", host.ErrUnsupported)
			}
		}
		if live && !n.isAssistantSession(id) {
			return n.prompt(ctx, a.PaneID, "/rename "+t)
		}
		return n.retitle(id, t)
	}
	return nil
}

// titleRetry is how often a title given before its conversation's transcript existed looks for it again, titleWait how
// long it looks (a conversation that has not written its first turn by then is not going to). Tests shorten them.
var (
	titleRetry = 500 * time.Millisecond
	titleWait  = 5 * time.Minute
)

// retitle appends the custom-title line Claude Code writes for /rename to the session's transcript. An assistant
// conversation renamed right after it started (the Feishu /agent names its conversation at once) has no transcript
// yet: Claude writes it with the first turn. Its title is kept and written once the transcript is there, the newest
// one if it is renamed again meanwhile; one whose process ends first is dropped (#5866). titleMu orders the writes, so
// a kept title never lands after a newer one written directly.
func (n *Node) retitle(id, title string) error {
	n.titleMu.Lock()
	defer n.titleMu.Unlock()
	n.mu.Lock()
	_, waiting := n.titles[id]
	if waiting {
		n.titles[id] = title
	}
	proc := n.procs[id]
	n.mu.Unlock()
	if waiting {
		return nil
	}
	if p := n.transcriptPath(id); p != "" {
		return appendCustomTitle(p, id, title)
	}
	if proc == nil {
		return fmt.Errorf("transcript for session %s not found", id)
	}
	n.mu.Lock()
	if n.titles == nil {
		n.titles = map[string]string{}
	}
	n.titles[id] = title
	n.mu.Unlock()
	go n.titleWhenWritten(id, proc.exited)
	return nil
}

// titleWhenWritten waits for the transcript of a conversation retitle kept a title for, then writes the newest one.
// When the process ends first, it looks once more (the last turn may have just been written) and drops the title.
func (n *Node) titleWhenWritten(id string, exited <-chan struct{}) {
	tick := time.NewTicker(titleRetry)
	defer tick.Stop()
	giveUp := time.NewTimer(titleWait)
	defer giveUp.Stop()
	for last := false; ; {
		if p := n.transcriptPath(id); p != "" {
			n.titleMu.Lock()
			n.mu.Lock()
			title := n.titles[id]
			delete(n.titles, id)
			n.mu.Unlock()
			if err := appendCustomTitle(p, id, title); err != nil {
				n.logf("rename %s: %v", id, err)
			}
			n.titleMu.Unlock()
			return
		}
		if last {
			n.mu.Lock()
			delete(n.titles, id)
			n.mu.Unlock()
			n.logf("rename %s: no transcript appeared, the title is dropped", id)
			return
		}
		select {
		case <-tick.C:
		case <-exited:
			last = true
		case <-giveUp.C:
			last = true
		}
	}
}

// appendCustomTitle appends to a transcript the custom-title line Claude Code writes for /rename.
func appendCustomTitle(p, id, title string) error {
	line, _ := json.Marshal(map[string]string{"type": "custom-title", "customTitle": title, "sessionId": id})
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
