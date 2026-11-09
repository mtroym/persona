package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

// A Codex-compatible CLI can run in herdr panes without being recognized by the host. The node therefore finds
// it itself in every host's panes:
//   - <CLI data>/thread-writer-locks/<thread id>.owner.json names the pid writing each thread (session);
//   - that pid's environment holds the host's pane variable (host.PaneEnv: HERDR_PANE_ID), so pane → session is
//     exact, no guessing by directory;
//   - its conversation is a rollout under <CLI data>/sessions/YYYY/MM/DD, the newer Codex format (transcript/codex.go);
//   - herdr has no state for the pane: a spinner in the terminal title or "esc to interrupt" on screen is working,
//     a numbered choice on screen is blocked, anything else is a finished turn (the hub turns that into "Unread" / "Seen").

const traexAgent = "traex"

func (n *Node) traexHome() string {
	if n.sessionScope != nil {
		return filepath.Join(n.dir, "traex")
	}
	return filepath.Join(n.home, ".trae", "cli")
}

func (n *Node) traexPath(id string) string  { return rolloutPath(n.traexHome(), id) }
func (n *Node) traexTitle(id string) string { return threadTitle(n.traexHome(), id) }

var traexCache = struct {
	sync.Mutex
	panes  map[int]string // pid → its pane, a stored PaneRef (from its environment; a pid never moves)
	status map[string]traexState
}{panes: map[int]string{}, status: map[string]traexState{}}

type traexState struct {
	status string
	at     time.Time
}

// traexOwners maps the live a compatible CLI processes to the conversation each runs. A process holds an owner file for every
// thread it writes, its subagents included: the guardian that reviews tool calls before they run gets a thread (and
// an owner file) newer than the conversation's, and taking the newest file showed the review instead of the
// conversation (2026-11-01). So: the newest thread that is not a subagent, else the conversation of the newest
// subagent, else the newest thread (a compatible CLI versions without thread_source / source in session_meta).
func (n *Node) traexOwners() map[int]string {
	dir := filepath.Join(n.traexHome(), "thread-writer-locks")
	files, _ := filepath.Glob(filepath.Join(dir, "*.owner.json"))
	type owner struct {
		thread string
		mtime  time.Time
	}
	byPID := map[int][]owner{}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var o struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(b, &o) != nil || o.PID <= 0 || !alive(o.PID) {
			continue
		}
		byPID[o.PID] = append(byPID[o.PID], owner{strings.TrimSuffix(filepath.Base(f), ".owner.json"), st.ModTime()})
	}
	home := n.traexHome()
	out := make(map[int]string, len(byPID))
	for pid, owners := range byPID {
		sort.Slice(owners, func(i, j int) bool { return owners[i].mtime.After(owners[j].mtime) })
		out[pid] = mainThread(home, owners[0].thread)
		for _, o := range owners {
			if p := rolloutPath(home, o.thread); p != "" && !metaOf(home, p).sub {
				out[pid] = o.thread
				break
			}
		}
	}
	return out
}

// alive says whether the process behind a pid file this user's claude or a compatible CLI wrote still runs. A pid now held by
// another user's process is a reused one, not the program that wrote the file (#2141). The cost: a claude run as
// another user that still writes into this user's home (`sudo claude`, sudo keeping HOME on macOS) counts as gone.
func alive(pid int) bool { return osproc.AliveMine(pid) }

// paneEnvRe finds a host's pane variable (host.PaneEnv) in a process's environment, NUL- (Linux) or space-separated
// (ps -E).
func paneEnvRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\x00\s])` + regexp.QuoteMeta(name) + `=([^\x00\s]+)`)
}

var herdrPaneEnvRe = paneEnvRe(herdrPaneEnv)

// paneEnvRes caches paneEnvRe per variable.
var paneEnvRes sync.Map // name -> *regexp.Regexp

// paneOfPID reads the pane a process runs in from its environment (Linux: /proc; macOS: ps -E): the variable of the
// first host whose PaneEnv it holds, as a stored PaneRef. Cached per pid.
func (n *Node) paneOfPID(pid int) string {
	traexCache.Lock()
	p, ok := traexCache.panes[pid]
	traexCache.Unlock()
	if ok {
		return p
	}
	if len(n.hosts.All()) == 0 {
		return "" // nothing to look for; not cached, the pid's pane is not known to be none
	}
	var env []byte
	if runtime.GOOS == "linux" {
		env, _ = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		env, _ = exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		cancel()
	}
	for _, h := range n.hosts.All() {
		re, ok := paneEnvRes.Load(h.PaneEnv())
		if !ok {
			re, _ = paneEnvRes.LoadOrStore(h.PaneEnv(), paneEnvRe(h.PaneEnv()))
		}
		if m := re.(*regexp.Regexp).FindSubmatch(env); m != nil {
			p = host.PaneRef{Kind: h.Kind(), ID: string(m[1])}.String()
			if h.Kind() == host.Herdr { // which server's pane: a named session's is qualified (#2196)
				picked := pickedEnv(string(env))
				p = n.envPane(func(name string) string { return picked[name] })
			}
			break
		}
	}
	traexCache.Lock()
	traexCache.panes[pid] = p
	traexCache.Unlock()
	return p
}

// traexAgents turns the panes running a compatible CLI into agents, with their session id and a state of our own.
func (n *Node) traexAgents(ctx context.Context, panes []host.Agent, agents []host.Agent) []host.Agent {
	owners := n.traexOwners()
	if len(owners) == 0 {
		return nil
	}
	isAgent := map[string]bool{}
	for _, a := range agents {
		isAgent[a.PaneID] = true
	}
	byPane := map[string]host.Agent{}
	for _, p := range panes {
		byPane[p.PaneID] = p
	}
	var out []host.Agent
	for pid, thread := range owners {
		pane := n.paneOfPID(pid)
		p, ok := byPane[pane]
		if !ok || isAgent[pane] {
			continue
		}
		p.Agent = traexAgent
		p.AgentSession = &host.AgentSession{Kind: "id", Value: thread}
		p.AgentStatus = n.traexStatus(ctx, p)
		out = append(out, p)
	}
	return out
}

var traexChoiceRe = lazyre.New(`(?m)^\s*[›❯>]\s*1\.\s+\S`)

// traexStatus: working from the title's spinner at once; otherwise the screen, read at most every 2s.
func (n *Node) traexStatus(ctx context.Context, p host.Agent) string {
	if r := []rune(strings.TrimSpace(p.RawTitle)); len(r) > 0 && r[0] >= 0x2800 && r[0] <= 0x28FF {
		return proto.StatusWorking // braille spinner: a turn is running
	}
	traexCache.Lock()
	s, ok := traexCache.status[p.PaneID]
	traexCache.Unlock()
	if ok && time.Since(s.at) < 2*time.Second {
		return s.status
	}
	status := proto.StatusDone
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	text, err := n.readPane(rctx, p.PaneID, "visible", 40)
	cancel()
	switch {
	case err != nil:
		status = s.status // keep the last known
		if status == "" {
			status = proto.StatusUnknown
		}
	case strings.Contains(text, "esc to interrupt"):
		status = proto.StatusWorking
	case traexChoiceRe.MatchString(text):
		status = proto.StatusBlocked
	}
	traexCache.Lock()
	traexCache.status[p.PaneID] = traexState{status, time.Now()}
	traexCache.Unlock()
	return status
}
