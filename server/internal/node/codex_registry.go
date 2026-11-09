package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

type codexRegistry struct {
	mu           sync.Mutex
	config       proto.CodexConfig
	claudeIDs    map[string]bool
	liveRollouts map[string]string // rollout id → what holds it open (codexHolder)
	byPane       map[string]string
	threads      map[string]codexSource
	remotes      map[string]*codexRemote
	report       func(proto.CodexConfig)
	kick         chan struct{}
	scan         func() []codexProcess
	// checked lists the processes as scan does, and says whether it could (scanCodexProcessesChecked); nil: scan's
	// list, taken as complete.
	checked func() ([]codexProcess, bool)
}

type codexSource struct{ home, thread, path string }

func newCodexRegistry() *codexRegistry {
	return &codexRegistry{threads: map[string]codexSource{}, remotes: map[string]*codexRemote{}, scan: scanCodexProcesses, checked: scanCodexProcessesChecked, kick: make(chan struct{}, 1)}
}

func canonicalCodexHome(home, base string) string {
	p := proto.ExpandHome(home, base)
	if !filepath.IsAbs(p) {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(p)
}

func codexSourceID(home, raw, legacy string) string {
	if home == legacy {
		return raw
	}
	h := sha256.Sum256([]byte(compat.CodexHashPrefix() + home + "\x00" + raw))
	// The reserved shape cannot collide with a raw UUID and still satisfies validID.
	return "c0de-" + hex.EncodeToString(h[:16])
}

func (n *Node) configureCodex(c proto.CodexConfig) {
	if n.sessionScope != nil {
		discovery := false
		c = proto.CodexConfig{Homes: []string{n.sessionScope.codex}, LegacyHome: n.sessionScope.codex, Discovery: &discovery}
	}
	r := n.codices
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range c.BlockedIDs {
		if validID(id) && !slices.Contains(r.config.BlockedIDs, id) {
			r.config.BlockedIDs = append(r.config.BlockedIDs, id)
		}
	}
	if c.Discovery != nil {
		v := *c.Discovery
		r.config.Discovery = &v
	}
	if c.LegacyHome != "" && len(r.threads) == 0 {
		r.config.LegacyHome = canonicalCodexHome(c.LegacyHome, n.home)
	}
	if r.config.LegacyHome == "" {
		r.config.LegacyHome = canonicalCodexHome(firstNonEmpty(c.LegacyHome, n.codexHome()), n.home)
	}
	for _, home := range append(c.Homes, n.codexHome()) {
		p := canonicalCodexHome(home, n.home)
		if p != "" && !slices.Contains(r.config.Homes, p) {
			r.config.Homes = append(r.config.Homes, p)
		}
	}
	for _, s := range c.Servers {
		s.Home = canonicalCodexHome(s.Home, n.home)
		if s.ID == "" || s.Home == "" || s.Endpoint == "" {
			continue
		}
		i := slices.IndexFunc(r.config.Servers, func(v proto.CodexServer) bool { return v.ID == s.ID })
		if i >= 0 {
			r.config.Servers[i] = s
		} else {
			r.config.Servers = append(r.config.Servers, s)
		}
		if !slices.Contains(r.config.Homes, s.Home) {
			r.config.Homes = append(r.config.Homes, s.Home)
		}
	}
	if r.config.Joined == nil {
		r.config.Joined = map[string]string{}
	}
	for id, instance := range c.Joined {
		if validID(id) {
			r.config.Joined[id] = instance
		}
	}
}

func cloneCodexConfig(c proto.CodexConfig) proto.CodexConfig {
	c.BlockedIDs = slices.Clone(c.BlockedIDs)
	c.Homes = slices.Clone(c.Homes)
	c.Servers = slices.Clone(c.Servers)
	joined := map[string]string{}
	for k, v := range c.Joined {
		joined[k] = v
	}
	c.Joined = joined
	return c
}

func (n *Node) codexInventory() proto.CodexConfig {
	if n.codices == nil {
		return proto.CodexConfig{}
	}
	n.codices.mu.Lock()
	defer n.codices.mu.Unlock()
	return cloneCodexConfig(n.codices.config)
}

func (n *Node) rememberCodex() {
	r := n.codices
	if r == nil {
		return
	}
	r.mu.Lock()
	cfg, report := cloneCodexConfig(r.config), r.report
	r.mu.Unlock()
	if report != nil {
		report(cfg)
	}
}

func (n *Node) registerCodex(home, thread, path string) string {
	r := n.codices
	if r == nil || !validID(thread) {
		return ""
	}
	home = canonicalCodexHome(home, n.home)
	if n.sessionScope != nil && !sameScopePath(home, n.sessionScope.codex) {
		return ""
	}
	if home == "" || !n.scopedTranscript(path) && path != "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := codexSourceID(home, thread, r.config.LegacyHome)
	old := r.threads[id]
	if path == "" {
		path = old.path
	}
	r.threads[id] = codexSource{home, thread, path}
	return id
}

func (n *Node) codexSource(id string) (codexSource, bool) {
	r := n.codices
	if r == nil {
		return codexSource{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.threads[id]
	return s, ok
}

func (n *Node) externalCodex(id string) bool {
	if n.codexBlocked(id) {
		return true
	}
	if n.isAssistantSession(id) {
		return false
	}
	if a, live := n.agent(id); live && a.Agent == "codex" {
		return false
	}
	if _, ok := n.codexSource(id); ok {
		return true
	}
	// An old opaque key must never fall through to Claude, even before inventory finishes loading.
	return strings.HasPrefix(id, "c0de-")
}

func (n *Node) discoverCodex() {
	if n.sessionScope != nil {
		return
	}
	r := n.codices
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.config.Discovery != nil && !*r.config.Discovery {
		r.mu.Unlock()
		return
	}
	scan := r.scan
	r.mu.Unlock()
	claudeIDs := map[string]bool{}
	for _, path := range n.globProjects("*", "*.jsonl") {
		claudeIDs[strings.TrimSuffix(filepath.Base(path), ".jsonl")] = true
	}
	r.mu.Lock()
	r.claudeIDs = claudeIDs
	r.mu.Unlock()
	found := scan()
	changed := false
	liveRollouts := map[string]string{}
	for pid, paths := range openCodexRollouts(found) {
		for _, p := range found {
			if p.pid != pid {
				continue
			}
			holder := codexHolderCLI
			if p.endpoint != "" {
				holder = codexHolderAppServer
			}
			home := canonicalCodexHome(firstNonEmpty(p.home, n.codexHome()), n.home)
			for _, path := range paths {
				if !pathWithin(path, home) { // Windows paths do not tell case apart (codex_desktop.go)
					continue
				}
				match := rolloutIDRe.FindStringSubmatch(path)
				if match != nil {
					id := n.registerCodex(home, match[1], path)
					liveRollouts[id] = holder
				}
			}
		}
	}
	r.mu.Lock()
	r.liveRollouts = liveRollouts
	r.mu.Unlock()
	byPane := map[string]string{}
	for _, p := range found {
		home := canonicalCodexHome(firstNonEmpty(p.home, n.codexHome()), n.home)
		if home == "" {
			continue
		}
		if p.pane != "" {
			byPane[n.herdrPaneIn(p.pane, p.herdrSession)] = home
		}
		r.mu.Lock()
		if !slices.Contains(r.config.Homes, home) {
			r.config.Homes = append(r.config.Homes, home)
			changed = true
		}
		if p.endpoint != "" && p.endpoint != "stdio://" && p.endpoint != "off" {
			endpoint := p.endpoint
			if endpoint == "unix://" {
				endpoint += filepath.Join(home, "app-server-control", "app-server-control.sock")
			}
			sum := sha256.Sum256([]byte(home + "\x00" + endpoint))
			id := "discovered-" + hex.EncodeToString(sum[:12])
			if !slices.ContainsFunc(r.config.Servers, func(s proto.CodexServer) bool { return s.Home == home && s.Endpoint == endpoint }) {
				r.config.Servers = append(r.config.Servers, proto.CodexServer{ID: id, Home: home, Endpoint: endpoint})
				changed = true
			}
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.byPane = byPane
	r.mu.Unlock()
	cfg := n.codexInventory()
	// Existing official control sockets are discoverable without starting a daemon.
	for _, home := range cfg.Homes {
		socket := filepath.Join(home, "app-server-control", "app-server-control.sock")
		// 0.163's socket is an alias to one in a shared private directory (codexSocketPath).
		if p, err := codexSocketPath(socket); err != nil {
			continue
		} else if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSocket != 0 {
			endpoint := "unix://" + socket
			r.mu.Lock()
			if !slices.ContainsFunc(r.config.Servers, func(s proto.CodexServer) bool { return s.Endpoint == endpoint }) {
				sum := sha256.Sum256([]byte(home + "\x00" + endpoint))
				r.config.Servers = append(r.config.Servers, proto.CodexServer{ID: "discovered-" + hex.EncodeToString(sum[:12]), Home: home, Endpoint: endpoint})
				changed = true
			}
			r.mu.Unlock()
		}
	}
	if changed {
		n.rememberCodex()
	}
}

func (n *Node) runCodex(ctx context.Context) {
	select {
	case <-n.readyCh():
	case <-time.After(configWait):
	case <-ctx.Done():
		return
	}
	n.configureCodex(proto.CodexConfig{})
	go n.publishCodex(ctx)
	n.rememberCodex()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	scanAt := time.Time{}
	for {
		// Also scan right after the Claude scan took a newer process snapshot, so both share one walk of /proc.
		if time.Since(scanAt) >= 30*time.Second || procSnapshotAfter(scanAt) {
			n.discoverCodex()
			scanAt = time.Now()
		}
		cfg := n.codexInventory()
		for _, home := range cfg.Homes {
			for _, f := range rollouts(home) {
				n.registerCodex(home, f.id, f.path)
			}
		}
		for _, s := range cfg.Servers {
			n.ensureCodexRemote(ctx, s)
		}
		select {
		case <-ctx.Done():
			n.codices.mu.Lock()
			remotes := slices.Collect(mapRemoteValues(n.codices.remotes))
			n.codices.mu.Unlock()
			for _, c := range remotes {
				c.close()
			}
			return
		case <-tick.C:
		}
	}
}

func mapRemoteValues(m map[string]*codexRemote) func(func(*codexRemote) bool) {
	return func(yield func(*codexRemote) bool) {
		for _, v := range m {
			if !yield(v) {
				return
			}
		}
	}
}

// What holds a rollout open, as the read-only reason names it.
const (
	codexHolderAppServer = "app-server"
	codexHolderCLI       = "Codex"
)

// codexHeldIdle: a rollout held open by a process node cannot join, untouched this long, is history (#2425). A bridge
// that keeps stdio app-servers for months holds the threads they once loaded: they showed "Unread" all along.
const codexHeldIdle = 24 * time.Hour

// heldRecently: the rollout at path was written within codexHeldIdle (its last line's time or its mtime, the later).
func heldRecently(m quickMeta, path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	at := st.ModTime()
	if m.activity.After(at) {
		at = m.activity
	}
	return time.Since(at) < codexHeldIdle
}

func (n *Node) codexSessions(seen map[string]bool) []proto.Session {
	if n.codices == nil {
		return nil
	}
	cfg := n.codexInventory()
	for _, home := range cfg.Homes {
		for _, f := range rollouts(home) {
			n.registerCodex(home, f.id, f.path)
		}
	}
	n.codices.mu.Lock()
	sources := make(map[string]codexSource, len(n.codices.threads))
	for k, v := range n.codices.threads {
		sources[k] = v
	}
	liveRollouts := make(map[string]string, len(n.codices.liveRollouts))
	for k, v := range n.codices.liveRollouts {
		liveRollouts[k] = v
	}
	n.codices.mu.Unlock()
	var out []proto.Session
	projects := map[string]string{}
	for id, src := range sources {
		src, visible := n.listedCodexSource(id, src)
		if !visible || seen[id] || n.isAssistantSession(id) || n.ownedAssistantSource(src.thread) {
			continue
		}
		if n.programRun(id, src) {
			continue
		}
		if n.agentFiltered("codex", src.path) && !n.keptSessions()[id] { // another platform's runs (agentfilters.go)
			continue
		}
		s := proto.Session{SessionID: id, Agent: "codex", SourceKind: "codex_history", SourceThreadID: src.thread, Status: proto.StatusOffline,
			Capabilities: &proto.SessionCapabilities{Read: true}, ReadOnlyReason: "仅查看：未发现可共享的 app-server 入口，请在电脑上打开会话"}
		var m quickMeta
		if src.path != "" {
			if st, err := os.Stat(src.path); err == nil {
				m = n.meta(src.path, st)
				if !n.sessionScopeAllows(m.cwd) {
					continue
				}
				if identity.IsAssistantDir(m.cwd) {
					continue
				}
				s.Cwd, s.Model, s.LastText, s.UpdatedAt, s.Rev = m.cwd, m.model, snippet(m.lastText), lastUsed(m, st), st.Size()
				s.AgentSurface = m.surface
				m.turnErr.set(&s)
				setContext(&s, m)
				s.Title = threadTitle(src.home, src.thread)
				untitled(&s, m)
			}
		}
		s.AgentName = filepath.Base(src.home)
		if holder := liveRollouts[id]; holder != "" && heldRecently(m, src.path) {
			// A stdio app-server (the Codex desktop App's own) has no way in, but the rollout it holds open says
			// whether a turn runs; unknown put a desktop task at work under the app's quiet spaces.
			s.Status = proto.StatusUnknown
			if m.turnOpen {
				s.Status = proto.StatusWorking
			} else if m.turnClosed {
				s.Status = proto.StatusDone // as a loaded idle thread on an app-server (codex_remote.go)
			}
			s.ReadOnlyReason = holder + " 正在运行，但未提供共享入口；只能同步本地历史"
		}
		if c, t, err := n.codexOwner(id); err == nil {
			s.SourceKind, s.SourceInstanceID, s.Status, s.Attached = "codex_app_server", c.spec.ID, t.status, t.joined
			if t.failure != "" { // turn/completed said it failed: before the rollout read has it
				reported(t.failure, t.failureKind, m.turnErr).set(&s)
			}
			if c.spec.Name != "" {
				s.AgentName = c.spec.Name
			}
			s.ParentThreadID = firstNonEmpty(t.parent, readCodexOrigin(src.path).parent)
			if t.title != "" {
				s.Title = t.title
			}
			if t.cwd != "" {
				s.Cwd = t.cwd
			}
			s.Capabilities.Send, s.Capabilities.Attach = t.parent == "", t.parent == ""
			s.Capabilities.Approve, s.Capabilities.Interrupt = t.joined, t.turn != "" && t.parent == ""
			s.ReadOnlyReason = ""
			if t.parent != "" {
				s.ReadOnlyReason = "仅查看：这个子 Agent 由父 Agent 驱动"
			}
			if src.path == "" {
				s.ReadOnlyReason = "此会话尚未生成本地历史，实时接入后可查看新输出"
			}
			n.codexEffortRow(id, &s.Efforts, &s.Effort, m) // codex_effort.go
		} else if err != errCodexNoOwner {
			s.ReadOnlyReason = err.Error()
		} else if o := readCodexOrigin(src.path); o.desktop {
			switch {
			case o.parent != "":
				s.AgentName, s.ParentThreadID, s.AgentSurface = "Codex App", o.parent, transcript.SurfaceDesktop
				s.ReadOnlyReason = "仅查看：这个子 Agent 由父 Agent 驱动"
				n.placeCodexDesktop(&s, src, o.parent, true)
			case codexDesktopChat(src):
				// The Codex App on this machine runs it (codex_desktop.go): sendable while the App has the thread loaded.
				s.AgentName, s.AgentSurface = "Codex App", transcript.SurfaceDesktop
				s.Capabilities.Send = true
				s.ReadOnlyReason = ""
				n.placeCodexDesktop(&s, src, src.thread, false)
			}
		} else if o.cli {
			s.AgentName, s.AgentSurface = "Codex CLI", transcript.SurfaceCLI
		}
		n.decorateCodexSession(&s, src, cfg.LegacyHome, projects)
		if s.SourceKind == "codex_history" && !s.Capabilities.Send && !s.IsSubagent && n.codexResumable(id) {
			// Nothing has it open: a message resumes it in a new tab (codex_resume.go).
			s.Capabilities.Send = true
			s.ReadOnlyReason = ""
		}
		if n.codexBlocked(id) {
			markCodexConflict(&s)
		}
		if s.Title == "" {
			if s.Cwd != "" {
				s.Title = filepath.Base(s.Cwd)
			} else {
				s.Title = "Codex"
			}
		}
		seen[id] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	resolveCodexHierarchy(out)
	slotCodexDesktop(out)
	return out
}
