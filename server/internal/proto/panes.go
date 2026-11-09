package proto

// PanesResponse is a machine's terminal host layout for the terminal browser (docs/integrations/terminal.md "Standalone terminal"): every
// workspace, tab and pane of each host, agent or plain shell. It is read from the snapshot the node already takes;
// ids are host-local (classic herdr's also per herdr session: a named session's are "@<session>:<id>", herdr.QualifyID).
type PanesResponse struct {
	Hosts []PaneHost `json:"hosts"`
}

// PaneHost is one terminal host (herdr, herdr-core, Orca…) and what it can do with a pane.
type PaneHost struct {
	Kind string `json:"kind"`
	// Stream: the host streams a pane's terminal (observe); without it a pane can only be read once a second.
	Stream bool `json:"stream"`
	// Control: a phone can drive a pane at its own size ("Take over").
	Control bool `json:"control"`
	// NewTab: the host can open a new shell tab in a workspace (panes.new).
	NewTab bool `json:"new_tab"`
	// Worktree: the host can make a Git worktree of a workspace's repository and open it as a workspace (worktree.create).
	Worktree   bool        `json:"worktree"`
	Workspaces []PaneSpace `json:"workspaces"`
	Tabs       []PaneTab   `json:"tabs"`
	Panes      []PaneItem  `json:"panes"`
}

type PaneSpace struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Number int    `json:"number,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	Branch string `json:"branch,omitempty"`
}

type PaneTab struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number,omitempty"`
}

// PaneItem is one pane. Terminal is what the terminal stream calls it ("pane:<pane>", docs/protocol/protocol.md "Terminal stream");
// SessionID is set when an agent session of this machine runs in it (open that one instead: it shares the stream the
// agent's own terminal panel uses).
type PaneItem struct {
	Pane        string `json:"pane"`
	Terminal    string `json:"terminal"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	// Agent is the kind of agent running there ("claude", "codex"…), "" for a plain shell.
	Agent     string `json:"agent,omitempty"`
	Status    string `json:"status,omitempty"` // the host's status for the agent (Status*)
	Title     string `json:"title,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// NewShellResult is what panes.new opened.
type NewShellResult struct {
	Pane     string `json:"pane"`
	Terminal string `json:"terminal"`
	TabID    string `json:"tab_id,omitempty"`
}

// PaneSplitResult is what panes.split opened: the new pane next to the one split.
type PaneSplitResult struct {
	Pane     string `json:"pane"`
	Terminal string `json:"terminal"`
}

// PaneGitResult is what panes.git did in a pane's or workspace's directory. Message is a one-line summary for a toast,
// Output what git printed (cut short).
type PaneGitResult struct {
	Op      string `json:"op"`
	Message string `json:"message"`
	Output  string `json:"output,omitempty"`
}

// WorktreePrepare is what worktree.prepare knows of a workspace's repository, for the "New worktree…" dialog. Path is the
// directory the host puts a repository's checkouts under (`<dir>/<repo>`): a checkout of branch b goes in
// Path + "/" + Slug(b), the host's own naming, which is what the dialog fills in from the branch.
type WorktreePrepare struct {
	Root     string `json:"root"`
	RepoName string `json:"repoName"`
	// Branch is the branch checked out in the workspace ("" on a detached HEAD).
	Branch string `json:"branch"`
	Path   string `json:"path"`
	// ChooseParent permits parent_workspace_id independently of the source workspace. Old nodes omit it.
	ChooseParent bool `json:"choose_parent,omitempty"`
	// AutomaticName permits an unnamed create. The host owns naming when supported, otherwise the node chooses a unique name.
	AutomaticName bool `json:"automatic_name,omitempty"`
}

// WorktreeAdded is the worktree worktree.add made for a new agent (#6115): its checkout, its branch, the repository's root.
type WorktreeAdded struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Root   string `json:"root"`
}

// WorktreeSnapshot is what worktree.snapshot made (#2173): a detached checkout of Root's HEAD at Path with Root's
// uncommitted change in it, for a reviewer to read without touching Root. Skipped counts the untracked files left
// out (over the copy's limits).
type WorktreeSnapshot struct {
	Path    string `json:"path"`
	Root    string `json:"root"`
	Head    string `json:"head"`
	Skipped int    `json:"skipped,omitempty"`
}

// WorktreeStatus is what worktree.status says of a directory (#6115): whether it is a linked Git worktree, whether
// Persona made it (it lies under the directory the computer puts this repository's checkouts in), and what removing
// it would lose: changed files, and commits not on the branch the repository's main checkout is on. Checkouts is the
// directory a new agent's worktree of this repository goes under, for any folder in a repository.
type WorktreeStatus struct {
	Worktree   bool   `json:"worktree"`
	Ours       bool   `json:"ours"`
	Path       string `json:"path,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Root       string `json:"root,omitempty"`
	MainBranch string `json:"main_branch,omitempty"`
	Checkouts  string `json:"checkouts,omitempty"`
	Dirty      int    `json:"dirty"`
	Unmerged   int    `json:"unmerged"`
}

// WorktreeRemoved is what worktree.remove did.
type WorktreeRemoved struct {
	Removed       bool `json:"removed"`
	BranchDeleted bool `json:"branch_deleted"`
}

// WorktreeCreateResult is what worktree.create opened: the new workspace, and the checkout it is in.
type WorktreeCreateResult struct {
	WorkspaceID       string `json:"workspace_id"`
	ParentWorkspaceID string `json:"parent_workspace_id,omitempty"`
	TabID             string `json:"tab_id,omitempty"`
	Pane              string `json:"pane,omitempty"`
	Terminal          string `json:"terminal,omitempty"`
	Path              string `json:"path"`
	Branch            string `json:"branch"`
}
