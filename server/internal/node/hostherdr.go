package node

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// herdrHost is the herdr host: every method is one herdr client call (Close: the tab's pane count, then a close), on
// the client of the session the id names (hostherdr_sessions.go). This file, hostherdr_sessions.go and engine.go are
// the only places in the node that know the herdr client.
type herdrHost struct {
	c *herdr.Client // the node's own server: its ids are herdr's as they are
	// named are the account's other running herdr sessions, driven too; nil: c alone (newHerdrSessions).
	named *herdrSessions
}

// newHerdrHost is the herdr host on c, with the account's named sessions when c is the default server.
func newHerdrHost(c *herdr.Client, logf func(format string, args ...any)) *herdrHost {
	return &herdrHost{c: c, named: newHerdrSessions(c, logf)}
}

// herdrHosts is a registry holding only the herdr host on c.
func herdrHosts(c *herdr.Client) *host.Registry { return host.NewRegistry(&herdrHost{c: c}) }

var (
	_ host.Host         = (*herdrHost)(nil)
	_ host.Watcher      = (*herdrHost)(nil)
	_ host.Launcher     = (*herdrHost)(nil)
	_ host.Organizer    = (*herdrHost)(nil)
	_ host.SlotCloser   = (*herdrHost)(nil)
	_ host.Splitter     = (*herdrHost)(nil)
	_ host.Streamer     = (*herdrHost)(nil)
	_ host.Controller   = (*herdrHost)(nil)
	_ host.FullClienter = (*herdrHost)(nil)
	_ host.Worktreer    = (*herdrHost)(nil)
)

// herdrPaneEnv is the variable herdr sets in every pane; herdrSessionEnv and herdrSocketEnv say which server's
// (herdr.EnvSession, #2196).
const (
	herdrPaneEnv    = nodeclient.HerdrPaneEnv
	herdrSessionEnv = nodeclient.HerdrSessionEnv
	herdrSocketEnv  = nodeclient.HerdrSocketEnv
)

// herdrAgentEnv tells herdr which agent a pane runs. herdr knows Claude and Codex by the foreground program's name
// (basename claude / claude-code, codex), so a wrapper that execs a binary of another name ("claude-2.1.318") was never
// seen as Claude: the start waited out claudeStartWait with Claude's input box on screen (#3454). herdr reads this
// variable from the foreground process's environment as it was exec'd (/proc/<pid>/environ, macOS KERN_PROCARGS2)
// before it looks at the name: herdr 0.7.1 on Linux, 0.7.5 on macOS; older ones and Windows ignore it. So the node
// puts it on the agent's own command only, `HERDR_AGENT=claude <launcher> …` where the pane's shell takes that form
// (typedHerdrAgent; codex-start sets it in the environment it execs): set on the pane's shell, or the tab's
// environment, it would be authoritative for everything run there later.
const herdrAgentEnv = "HERDR_AGENT"

// herdrAgentName is what herdr calls an agent of kind, "" for one it has no integration for (a compatible CLI, pi: left to its
// own detection) and on Windows, where herdr does not read HERDR_AGENT.
func herdrAgentName(kind string) string {
	if runtime.GOOS != "windows" && (kind == "claude" || kind == "codex") {
		return kind
	}
	return ""
}

// herdrAgentFor is the HERDR_AGENT value for an agent of kind started in launch's panes: herdrAgentName on herdr, ""
// on other hosts.
func herdrAgentFor(launch host.Launcher, kind string) string {
	if _, ok := launch.(*herdrHost); !ok {
		return ""
	}
	return herdrAgentName(kind)
}

// typedHerdrAgent is herdrAgentFor for a command typed into a pane whose shell is shells (the programs in front of
// it): "" unless they take `VAR=val cmd`, so csh, pwsh, nu, xonsh, elvish and a shell not known get the command as
// before, without it.
func typedHerdrAgent(launch host.Launcher, kind string, shells ...string) string {
	if !takesAssign(shells...) {
		return ""
	}
	return herdrAgentFor(launch, kind)
}

// takesAssign: shells (names or paths, "-zsh" for a login shell) are there and each is one of assignShells.
func takesAssign(shells ...string) bool {
	for _, s := range shells {
		if !assignShells[strings.TrimPrefix(filepath.Base(s), "-")] {
			return false
		}
	}
	return len(shells) > 0
}

// assignShells are the shells whose `VAR=val cmd` runs cmd with VAR set and leaves the shell without it (fish from
// 3.1): the ones the node types HERDR_AGENT in front for.
var assignShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true, "fish": true}

// withHerdrAgent is the command line cmd with HERDR_AGENT=agent in front, for the shell that runs it to put in the
// agent's environment alone; cmd as it is when agent is "".
func withHerdrAgent(agent, cmd string) string {
	if agent == "" {
		return cmd
	}
	return herdrAgentEnv + "=" + agent + " " + cmd
}

func (h *herdrHost) Kind() string    { return host.Herdr }
func (h *herdrHost) PaneEnv() string { return herdrPaneEnv }

func (h *herdrHost) Version(ctx context.Context) string { return h.c.Version(ctx) }

// Snapshot is the own server's layout with every running named session's (each one's last when it does not answer
// in time). The own server failing is the host failing, whatever the named sessions say.
func (h *herdrHost) Snapshot(ctx context.Context) (*host.Layout, error) {
	if h.named == nil {
		return layoutOf(ctx, h.c, "")
	}
	others := fromNamedSessions(ctx, h.named, func(ctx context.Context, ns *herdrSession) (*host.Layout, error) {
		l, err := layoutOf(ctx, ns.c, ns.name)
		if err == nil {
			ns.mu.Lock()
			ns.layout, ns.spaces = l, nil
			ns.mu.Unlock()
		}
		return l, err
	}, (*herdrSession).lastLayout)
	l, err := layoutOf(ctx, h.c, "")
	named := others()
	if err != nil {
		return nil, err
	}
	for _, o := range named {
		mergeLayout(l, o)
	}
	return l, nil
}

// fromNamedSessions asks s's running sessions alongside the own server: it starts at once (fromNamed) and the
// function it returns waits for what they answered by namedWait from now.
func fromNamedSessions[T any](ctx context.Context, s *herdrSessions, get func(context.Context, *herdrSession) (T, error), last func(*herdrSession) (T, bool)) func() []T {
	var out []T
	done := make(chan struct{})
	go func() {
		defer close(done)
		until := time.Now().Add(namedWait)
		out = fromNamed(ctx, until, s.running(ctx, until), get, last)
	}()
	return func() []T {
		<-done
		return out
	}
}

// layoutOf is one server's snapshot as a layout, its ids qualified with session ("" the own server: as they are).
func layoutOf(ctx context.Context, c *herdr.Client, session string) (*host.Layout, error) {
	snap, err := c.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	l := &host.Layout{Agents: fromHerdrAgents(session, snap.Agents), Panes: fromHerdrAgents(session, snap.Panes)}
	if len(snap.Layouts) > 0 {
		l.PaneIndex = herdr.PaneIndexes(snap.Layouts)
		if session != "" {
			idx := make(map[string]int, len(l.PaneIndex))
			for pane, i := range l.PaneIndex {
				idx[herdr.QualifyID(session, pane)] = i
			}
			l.PaneIndex = idx
		}
	}
	for _, w := range snap.Workspaces {
		l.Spaces = append(l.Spaces, fromHerdrWorkspace(session, w))
	}
	for _, t := range snap.Tabs {
		l.Slots = append(l.Slots, host.Slot{ID: herdr.QualifyID(session, t.ID), SpaceID: herdr.QualifyID(session, t.WorkspaceID),
			Label: t.Label, Number: t.Number})
	}
	fillSpaceDirectories(l.Spaces, l.Panes)
	if len(l.Panes) == 0 {
		fillSpaceDirectories(l.Spaces, l.Agents)
	}
	return l, nil
}

// mergeLayout adds o's spaces, slots, agents and panes to l (their ids never collide: o's are qualified), leaving o as
// it is (a session's last layout is used again).
func mergeLayout(l, o *host.Layout) {
	if o == nil {
		return
	}
	l.Spaces = append(l.Spaces, o.Spaces...)
	l.Slots = append(l.Slots, o.Slots...)
	l.Agents = append(l.Agents, o.Agents...)
	l.Panes = append(l.Panes, o.Panes...)
	if len(o.PaneIndex) > 0 {
		if l.PaneIndex == nil {
			l.PaneIndex = make(map[string]int, len(o.PaneIndex))
		}
		maps.Copy(l.PaneIndex, o.PaneIndex)
	}
}

func (h *herdrHost) Agents(ctx context.Context) ([]host.Agent, error) {
	if h.named == nil {
		agents, err := h.c.Agents(ctx)
		return fromHerdrAgents("", agents), err
	}
	others := fromNamedSessions(ctx, h.named, func(ctx context.Context, ns *herdrSession) ([]host.Agent, error) {
		agents, err := ns.c.Agents(ctx)
		return fromHerdrAgents(ns.name, agents), err
	}, func(ns *herdrSession) ([]host.Agent, bool) {
		l, ok := ns.lastLayout()
		if !ok {
			return nil, false
		}
		return l.Agents, true
	})
	agents, err := h.c.Agents(ctx)
	out := fromHerdrAgents("", agents)
	for _, o := range others() {
		out = append(out, o...)
	}
	return out, err
}

// Agent is agent.get; herdr answers *_not_found for a pane where it detects no agent (a claude it does not recognise,
// #3454), marked ErrNoAgent.
func (h *herdrHost) Agent(ctx context.Context, pane host.PaneRef) (*host.Agent, error) {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return nil, err
	}
	a, err := t.c.AgentGet(ctx, t.id)
	if herdr.IsNotFound(err) {
		return nil, host.Mark(err, host.ErrNoAgent)
	}
	if err != nil {
		return nil, err
	}
	out := fromHerdrAgent(t.session, *a)
	return &out, nil
}

func (h *herdrHost) Read(ctx context.Context, pane host.PaneRef, o host.ReadOpts) (string, error) {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return "", err
	}
	if o.ANSI {
		return t.c.PaneReadANSI(ctx, t.id, o.Source, o.Lines)
	}
	return t.c.PaneRead(ctx, t.id, o.Source, o.Lines)
}

func (h *herdrHost) SendText(ctx context.Context, pane host.PaneRef, text string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.PaneSendText(ctx, t.id, text)
}

func (h *herdrHost) SendKeys(ctx context.Context, pane host.PaneRef, keys ...string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.PaneSendKeys(ctx, t.id, keys...)
}

// Prompt is agent.prompt; herdr answers *_not_found for a pane whose agent it does not detect (a compatible CLI).
func (h *herdrHost) Prompt(ctx context.Context, pane host.PaneRef, text string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	err = t.c.AgentPrompt(ctx, t.id, text)
	if herdr.IsNotFound(err) {
		return host.Mark(err, host.ErrNoAgent)
	}
	if herdr.IsBlocked(err) {
		return host.Mark(err, host.ErrAgentBlocked)
	}
	return err
}

func (h *herdrHost) PaneProcesses(ctx context.Context, pane host.PaneRef) ([]int, error) {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return nil, err
	}
	return t.c.PaneProcesses(ctx, t.id)
}

// PaneForeground names each foreground program by what it was started as (argv0), else its process name.
func (h *herdrHost) PaneForeground(ctx context.Context, pane host.PaneRef) ([]string, error) {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return nil, err
	}
	procs, err := t.c.PaneForeground(ctx, t.id)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(procs))
	for _, p := range procs {
		if p.Argv0 != "" {
			names = append(names, p.Argv0)
		} else {
			names = append(names, p.Name)
		}
	}
	return names, nil
}

func (h *herdrHost) ReportMetadata(ctx context.Context, pane host.PaneRef, source string, tokens map[string]*string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.PaneReportMetadata(ctx, t.id, source, tokens)
}

// PaneReportResume is herdr.Client.PaneReportResume on the pane's session (herdrresume.go).
func (h *herdrHost) PaneReportResume(ctx context.Context, pane host.PaneRef, source, agent, sessionID string, argv []string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.PaneReportResume(ctx, t.id, source, agent, sessionID, argv)
}

// Watch is one events.subscribe per server: the lifecycle events plus agent status for each of its panes (herdr only
// sends those per pane). A status change into or out of blocked is urgent. It ends when the own server's stream does.
// The named sessions watched are the running ones and those the panes are in, looked at again every sessionsEvery (a
// session started since gets its stream, a stopped one's ends); a named session's stream that ends is opened again
// (the events lost in between are covered by the snapshot its first event brings).
func (h *herdrHost) Watch(ctx context.Context, panes []host.PaneRef, changed func(urgent bool)) error {
	byServer := map[string][]string{}
	for _, p := range panes {
		session, id := herdr.SplitID(p.ID)
		byServer[session] = append(byServer[session], id)
	}
	if h.named == nil {
		return watchServer(ctx, h.c, byServer[""], changed)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex // changed is called by one stream at a time, as with one server
	one := func(urgent bool) {
		mu.Lock()
		defer mu.Unlock()
		changed(urgent)
	}
	own := make(chan error, 1)
	go func() { own <- watchServer(ctx, h.c, byServer[""], one) }()

	type watched struct {
		ns   *herdrSession
		stop context.CancelFunc
	}
	streams := map[string]watched{}
	var wg sync.WaitGroup
	reconcile := func() {
		want := map[string]*herdrSession{}
		for _, ns := range h.named.running(ctx, time.Now().Add(namedWait)) {
			want[ns.name] = ns
		}
		for name := range byServer {
			if name != "" && want[name] == nil {
				if ns := h.named.lookup(ctx, name); ns != nil {
					want[name] = ns
				}
			}
		}
		for name, w := range streams {
			if want[name] != w.ns { // stopped, or a new server under the same name
				w.stop()
				delete(streams, name)
			}
		}
		for name, ns := range want {
			if _, ok := streams[name]; ok {
				continue
			}
			sctx, stop := context.WithCancel(ctx)
			streams[name] = watched{ns: ns, stop: stop}
			wg.Add(1)
			go func() {
				defer wg.Done()
				watchNamed(sctx, ns.c, byServer[name], one)
			}()
		}
	}
	// reconcile may wait for herdr's list of sessions: it runs beside the loop (one at a time), so the own stream
	// ending is seen at once.
	var reconciling sync.Mutex
	again := func() {
		if !reconciling.TryLock() {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer reconciling.Unlock()
			reconcile()
		}()
	}
	defer wg.Wait()
	defer cancel()
	again()
	tick := time.NewTicker(sessionsEvery)
	defer tick.Stop()
	for {
		select {
		case err := <-own:
			return err
		case <-tick.C:
			again()
		}
	}
}

// watchNamed keeps a named session's stream open until ctx ends, opening it again with a growing pause when it ends.
func watchNamed(ctx context.Context, c *herdr.Client, panes []string, changed func(urgent bool)) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		_ = watchServer(ctx, c, panes, changed)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// watchServer is one events.subscribe on c for panes (herdr's ids there).
func watchServer(ctx context.Context, c *herdr.Client, panes []string, changed func(urgent bool)) error {
	var w waits
	return c.Subscribe(ctx, subscriptions(panes), func(e herdr.Event) {
		var d struct {
			PaneID      string `json:"pane_id"`
			AgentStatus string `json:"agent_status"`
		}
		status := e.Event == "pane.agent_status_changed" || e.Event == "pane_agent_status_changed"
		changed(status && json.Unmarshal(e.Data, &d) == nil && w.changed(d.PaneID, d.AgentStatus == "blocked"))
	})
}

// herdrLifecycle are the events that change which sessions there are or how they are named.
var herdrLifecycle = []string{
	"workspace.created", "workspace.renamed", "workspace.closed", "workspace.moved",
	"tab.created", "tab.renamed", "tab.closed", "tab.moved",
	"pane.created", "pane.closed", "pane.exited", "pane.updated", "pane.moved", "pane.agent_detected",
}

// subscriptions: the lifecycle events plus agent status for each pane (herdr only sends those per pane).
func subscriptions(panes []string) []herdr.Subscription {
	subs := make([]herdr.Subscription, 0, len(herdrLifecycle)+len(panes))
	for _, t := range herdrLifecycle {
		subs = append(subs, herdr.Subscription{"type": t})
	}
	for _, p := range panes {
		subs = append(subs, herdr.Subscription{"type": "pane.agent_status_changed", "pane_id": p})
	}
	return subs
}

// Spaces are the own server's workspaces and every running named session's (each one's last when it does not answer
// in time).
func (h *herdrHost) Spaces(ctx context.Context) ([]host.Space, error) {
	if h.named == nil {
		wss, err := h.c.Workspaces(ctx)
		return spacesOf("", wss), err
	}
	others := fromNamedSessions(ctx, h.named, func(ctx context.Context, ns *herdrSession) ([]host.Space, error) {
		wss, err := ns.c.Workspaces(ctx)
		if err != nil {
			return nil, err
		}
		out := spacesOf(ns.name, wss)
		ns.mu.Lock()
		ns.spaces = out
		ns.mu.Unlock()
		return out, nil
	}, (*herdrSession).lastSpaces)
	wss, err := h.c.Workspaces(ctx)
	out := spacesOf("", wss)
	for _, o := range others() {
		out = append(out, o...)
	}
	return out, err
}

// spacesOf are the workspaces of session ("" the own server) as spaces.
func spacesOf(session string, wss []herdr.Workspace) []host.Space {
	out := make([]host.Space, 0, len(wss))
	for _, w := range wss {
		out = append(out, fromHerdrWorkspace(session, w))
	}
	return out
}

// CreateSpace opens the workspace on the own server: pane-less work never goes to a named session.
func (h *herdrHost) CreateSpace(ctx context.Context, cwd, label string) (*host.Created, error) {
	c, err := h.c.WorkspaceCreate(ctx, cwd, label)
	return fromHerdrCreated("", c), err
}

func (h *herdrHost) CreateSlot(ctx context.Context, space, cwd, label string) (*host.Created, error) {
	t, err := h.target(ctx, space)
	if err != nil {
		return nil, err
	}
	c, err := t.c.TabCreate(ctx, t.id, cwd, label)
	return fromHerdrCreated(t.session, c), err
}

func (h *herdrHost) Run(ctx context.Context, pane host.PaneRef, command string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.PaneRun(ctx, t.id, command)
}

func (h *herdrHost) NameAgent(ctx context.Context, pane host.PaneRef, name string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.AgentRename(ctx, t.id, name)
}

// RenameSpace renames the workspace; a name given with the session in front, as the host shows a named session's
// (sessionLabel), is given to herdr without it.
func (h *herdrHost) RenameSpace(ctx context.Context, space, name string) error {
	t, err := h.target(ctx, space)
	if err != nil {
		return err
	}
	if t.session != "" {
		name = strings.TrimPrefix(name, t.session+sessionLabelSep)
	}
	return t.c.WorkspaceRename(ctx, t.id, name)
}

func (h *herdrHost) RenameSlot(ctx context.Context, slot, name string) error {
	t, err := h.target(ctx, slot)
	if err != nil {
		return err
	}
	return t.c.TabRename(ctx, t.id, name)
}

// Close closes the pane when its tab is split, else the whole tab. When the panes cannot be counted it closes only the
// pane: herdr closes the tab itself when that was its last pane, and a split tab keeps its other panes (#4873).
func (h *herdrHost) Close(ctx context.Context, pane host.PaneRef, space, slot string) error {
	t, ids, err := h.targets(ctx, pane.ID, space, slot)
	if err != nil {
		return err
	}
	space, slot = ids[0], ids[1]
	panes, err := t.c.TabPanes(ctx, space, slot)
	if err != nil || panes > 1 {
		return t.c.PaneClose(ctx, t.id)
	}
	return t.c.TabClose(ctx, slot)
}

// CloseSlot closes the whole tab, every pane in it.
func (h *herdrHost) CloseSlot(ctx context.Context, slot string) error {
	t, err := h.target(ctx, slot)
	if err != nil {
		return err
	}
	return t.c.TabClose(ctx, t.id)
}

// Split splits the pane; the new pane keeps the host's kind (herdr panes carry no prefix) and the pane's session.
func (h *herdrHost) Split(ctx context.Context, pane host.PaneRef, direction, cwd string) (host.PaneRef, error) {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return host.PaneRef{}, err
	}
	id, err := t.c.PaneSplit(ctx, t.id, direction, cwd)
	if err != nil {
		return host.PaneRef{}, err
	}
	if id == "" {
		return host.PaneRef{}, errors.New("herdr did not return the new pane")
	}
	return host.PaneRef{Kind: pane.Kind, ID: t.qualify(id)}, nil
}

// CreateWorktree has herdr check the branch out at path and open it as a workspace grouped under the source one, in
// the source one's session.
func (h *herdrHost) CreateWorktree(ctx context.Context, space, branch, path string) (*host.Created, error) {
	t, err := h.target(ctx, space)
	if err != nil {
		return nil, err
	}
	c, err := t.c.WorktreeCreate(ctx, t.id, branch, path)
	if err != nil {
		return nil, err
	}
	out := fromHerdrCreated(t.session, &c.Created)
	if out.SpaceID == "" {
		return nil, errors.New("herdr did not return the new workspace")
	}
	return out, nil
}

func (h *herdrHost) WorktreesDir(home string) string { return herdr.WorktreesDir(home) }

// worktreeSupport caches whether this herdr has `worktree` (herdr 0.9 on Unix).
var worktreeSupport struct {
	once sync.Once
	ok   bool
}

func (h *herdrHost) CanWorktree() bool {
	return h.has(&worktreeSupport, "worktree")
}

// observeSupport, controlSupport cache whether this herdr has `terminal session observe` / `control` (herdr 0.9 on
// Unix).
var observeSupport, controlSupport struct {
	once sync.Once
	ok   bool
}

// has runs `herdr <args> --help` once: whether this herdr knows the command.
func (h *herdrHost) has(cache *struct {
	once sync.Once
	ok   bool
}, args ...string) bool {
	cache.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cache.ok = exec.CommandContext(ctx, h.c.Bin, append(args, "--help")...).Run() == nil
	})
	return cache.ok
}

func (h *herdrHost) CanObserve() bool {
	return h.has(&observeSupport, "terminal", "session", "observe")
}
func (h *herdrHost) CanControl() bool {
	return h.has(&controlSupport, "terminal", "session", "control")
}

func (h *herdrHost) PaneSize(ctx context.Context, pane host.PaneRef) (int, int, error) {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return 0, 0, err
	}
	return t.c.PaneSize(ctx, t.id)
}

func (h *herdrHost) Observe(ctx context.Context, pane host.PaneRef, cols, rows int, fn func(host.Frame)) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.Observe(ctx, t.id, cols, rows, func(f herdr.Frame) { fn(host.Frame(f)) })
}

func (h *herdrHost) Control(ctx context.Context, pane host.PaneRef, cols, rows int, takeover bool, fn func(host.Frame)) (host.Control, error) {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return nil, err
	}
	c, err := t.c.StartControl(ctx, t.id, cols, rows, takeover, func(f herdr.Frame) { fn(host.Frame(f)) })
	if err != nil {
		if errors.Is(err, herdr.ErrAttachBusy) {
			err = host.Mark(err, host.ErrAttachBusy)
		}
		return nil, err
	}
	return herdrControl{c}, nil
}

// herdrControl is herdr's control with its errors marked for the host package.
type herdrControl struct{ *herdr.Control }

func (c herdrControl) Mouse(e host.MouseEvent) error { return c.Control.Mouse(herdr.MouseEvent(e)) }

func (c herdrControl) Err() error {
	err := c.Control.Err()
	if errors.Is(err, herdr.ErrTakenOver) {
		return host.Mark(err, host.ErrTakenOver)
	}
	return err
}

// fromHerdrWorkspace is a workspace of session ("" the own server) as a space: a named session's id qualified and its
// label with the session in front (sessionLabel).
func fromHerdrWorkspace(session string, w herdr.Workspace) host.Space {
	return host.Space{ID: herdr.QualifyID(session, w.ID), Label: sessionLabel(session, w.Label), Number: w.Number,
		Cwd: w.Cwd, Branch: w.Branch}
}

func fromHerdrCreated(session string, c *herdr.Created) *host.Created {
	if c == nil {
		return nil
	}
	out := &host.Created{SlotID: herdr.QualifyID(session, c.Tab.ID),
		Pane: host.PaneRef{Kind: host.Herdr, ID: herdr.QualifyID(session, c.RootPane.ID)}}
	if c.Workspace != nil {
		out.SpaceID = herdr.QualifyID(session, c.Workspace.ID)
	}
	return out
}

func fromHerdrAgents(session string, agents []herdr.Agent) []host.Agent {
	if agents == nil {
		return nil
	}
	out := make([]host.Agent, len(agents))
	for i, a := range agents {
		out[i] = fromHerdrAgent(session, a)
	}
	return out
}

// fromHerdrAgent: herdr's pane ids are herdr PaneRefs as they are (no prefix), qualified with session for a named
// session's (herdr.QualifyID), as are its tab and workspace ids.
func fromHerdrAgent(session string, a herdr.Agent) host.Agent {
	q := func(id string) string { return herdr.QualifyID(session, id) }
	out := host.Agent{Agent: a.Agent, Name: a.Name, AgentStatus: a.AgentStatus, Cwd: a.Cwd, ForegroundCwd: a.ForegroundCwd,
		PaneID: q(a.PaneID), TabID: q(a.TabID), WorkspaceID: q(a.WorkspaceID), Title: a.Title, RawTitle: a.RawTitle, Tokens: a.Tokens}
	if a.AgentSession != nil {
		out.AgentSession = &host.AgentSession{Kind: a.AgentSession.Kind, Value: a.AgentSession.Value}
	}
	return out
}

// CanFullClient: herdr on Unix, with this client's socket one of herdr's sessions.
func (h *herdrHost) CanFullClient() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := h.c.SessionName(ctx)
	return err == nil
}

// StartFullClient attaches to the session of opts.Pane (the own server's without one) and shows that pane.
func (h *herdrHost) StartFullClient(ctx context.Context, opts host.FullClientOptions, onData func([]byte)) (host.FullClient, error) {
	t := herdrRoute{c: h.c}
	if opts.Pane != nil && opts.Pane.ID != "" {
		var err error
		if t, err = h.target(ctx, opts.Pane.ID); err != nil {
			return nil, err
		}
	}
	session := t.session
	if session == "" {
		var err error
		if session, err = h.c.SessionName(ctx); err != nil {
			return nil, err
		}
	}
	if t.id != "" {
		_ = t.c.FocusPane(ctx, t.id) // the client then opens on it; not worth failing the open for
	}
	fc, err := t.c.StartFull(herdr.FullOptions{Session: session, Cols: opts.Cols, Rows: opts.Rows}, onData)
	if err != nil {
		return nil, err
	}
	return fc, nil
}

// Focused is the focus the own server shows now (host.Focuser): the computer's own herdr window, which the named
// sessions' focus is not.
func (h *herdrHost) Focused(ctx context.Context) (host.FocusState, error) {
	f, err := h.c.Focused(ctx)
	return host.FocusState{Space: f.Workspace, Slot: f.Tab, Pane: f.Pane}, err
}

func (h *herdrHost) WatchFocus(ctx context.Context, changed func()) error {
	return h.c.WatchFocus(ctx, changed)
}

func (h *herdrHost) FocusPane(ctx context.Context, pane host.PaneRef) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.FocusPane(ctx, t.id)
}
