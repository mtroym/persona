package node

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// oldNodePlist is the agent both installers wrote before #5944 (no ProcessType), with a home that needs escaping.
const oldNodePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.persona.node</string>
  <key>ProgramArguments</key><array><string>/Users/a&amp;b/.persona/bin/personad</string><string>node</string><string>--attach</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PERSONA_HOME</key><string>/Users/a&amp;b/.persona</string>
    <key>PATH</key><string>/usr/bin:/bin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardErrorPath</key><string>/Users/a&amp;b/.persona/node-attach.log</string>
</dict></plist>
`

func plutilLint(t *testing.T, b []byte) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	p := filepath.Join(t.TempDir(), "x.plist")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", p).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %s\n%s", out, b)
	}
}

func TestWithProcessTypeAddsOneLine(t *testing.T) {
	got, err := withProcessType([]byte(oldNodePlist))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(oldNodePlist, "</dict></plist>", "  <key>ProcessType</key><string>Interactive</string>\n</dict></plist>", 1)
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	plutilLint(t, got)
	// Again: the value is replaced in place, nothing else moves.
	again, err := withProcessType(got)
	if err != nil || string(again) != want {
		t.Fatalf("second pass: %v\n%s", err, again)
	}
	entries, _, err := plistTop(got)
	if err != nil || entries["ProcessType"].text != "Interactive" || entries["StandardErrorPath"].text != "/Users/a&b/.persona/node-attach.log" {
		t.Fatalf("%v %+v", err, entries)
	}
}

func TestWithProcessTypeReplacesAnotherValue(t *testing.T) {
	// plutil's own layout (tabs, one key per line), as a plist edited with plutil or Xcode looks.
	in := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\">\n<dict>\n\t<key>Label</key>\n\t<string>com.persona.node</string>\n" +
		"\t<key>ProcessType</key>\n\t<string>Background</string>\n\t<key>KeepAlive</key>\n\t<true/>\n</dict>\n</plist>\n"
	got, err := withProcessType([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(in, "<string>Background</string>", "<string>Interactive</string>", 1); string(got) != want {
		t.Fatalf("got\n%s", got)
	}
	plutilLint(t, got)
}

func TestWithProcessTypeRefusesWhatItCannotRead(t *testing.T) {
	for name, in := range map[string]string{
		"binary":     "bplist00\x01\x02",
		"not xml":    "Label = com.persona.node",
		"not a dict": `<plist version="1.0"><array><string>x</string></array></plist>`,
		"empty dict": `<plist version="1.0"><dict/></plist>`,
		"cut short":  `<plist version="1.0"><dict><key>Label</key><string>x</string>`,
	} {
		if _, err := withProcessType([]byte(in)); err == nil {
			t.Errorf("%s: rewritten", name)
		}
	}
	if _, _, err := plistTop([]byte("bplist00")); !errors.Is(err, errBinaryPlist) {
		t.Errorf("binary: %v", err)
	}
}

// fakeLaunchd is launchctl for one agent: print says whether it is loaded and how (the spawn type its plist gave it
// when it was loaded), bootout unloads, bootstrap loads the plist as it is on disk then. The node's other label is
// not loaded.
type fakeLaunchd struct {
	t         *testing.T
	label     string
	plist     string
	loaded    bool
	spawn     string
	calls     []string
	bootstrap func(n int) error // nil: succeeds
	tries     int
	// onBootout and onBootstrap run when the agent goes and when it is loaded again (nil: nothing).
	onBootout, onBootstrap func()
}

func (f *fakeLaunchd) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "print":
		label, ok := strings.CutPrefix(args[1], "gui/501/")
		if !ok || !isNodeLaunchdLabel(label) {
			f.t.Errorf("print %v", args)
		}
		if label != f.label || !f.loaded {
			return "Could not find service \"" + label + "\" in domain for user gui: 501\n", errors.New("exit status 113")
		}
		return args[1] + " = {\n\tactive count = 1\n\tstate = running\n\tspawn type = " + f.spawn + " (3)\n}\n", nil
	case "bootout":
		if args[1] != "gui/501/"+f.label {
			f.t.Errorf("bootout %v", args)
		}
		f.loaded = false
		if f.onBootout != nil {
			f.onBootout()
		}
		return "", nil
	case "bootstrap":
		f.tries++
		if f.bootstrap != nil {
			if err := f.bootstrap(f.tries); err != nil {
				return "Bootstrap failed: 5: Input/output error\n", err
			}
		}
		if args[1] != "gui/501" || args[2] != f.plist {
			f.t.Errorf("bootstrap %v", args)
		}
		b, _ := os.ReadFile(f.plist)
		f.loaded, f.spawn = true, "daemon"
		if e, _, _ := plistTop(b); e["ProcessType"].text == "Interactive" {
			f.spawn = "interactive"
		}
		if f.onBootstrap != nil {
			f.onBootstrap()
		}
		return "", nil
	}
	f.t.Errorf("launchctl %v", args)
	return "", errors.New("unexpected")
}

// fakeTarget is the agent as the fake launchctl names it.
const fakeTarget = "gui/501/" + NodeLaunchdLabel

// launchdDoctor is a doctor whose only checks of interest are the LaunchAgent's: the plist under today's label in a
// temp home, launchctl faked.
func launchdDoctor(t *testing.T, plist string, loaded bool, spawn string) (DoctorOptions, *fakeLaunchd) {
	t.Helper()
	home := t.TempDir()
	if plist != "" {
		writeLaunchAgent(t, home, NodeLaunchdLabel, plist)
	}
	return launchdDoctorAt(t, home, NodeLaunchdLabel, loaded, spawn)
}

// launchdDoctorAt is launchdDoctor for the plists already in home, with launchd running the agent called label (when
// loaded). The options are picked the way the node picks them (withLaunchAgent); launchctl calls made while picking
// are dropped.
func launchdDoctorAt(t *testing.T, home, label string, loaded bool, spawn string) (DoctorOptions, *fakeLaunchd) {
	t.Helper()
	e := newDoctorEnv(t)
	e.healthy(t)
	f := &fakeLaunchd{t: t, label: label, plist: NodeLaunchAgentPath(home, label), loaded: loaded, spawn: spawn}
	opts := e.opts
	opts.withLaunchAgent(context.Background(), home, "gui/501", "", f.run)
	f.calls = nil
	return opts, f
}

// writeLaunchAgent writes plist as home's agent called label.
func writeLaunchAgent(t *testing.T, home, label, plist string) string {
	t.Helper()
	path := NodeLaunchAgentPath(home, label)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctorReportsAPlistWithoutProcessType(t *testing.T) {
	opts, f := launchdDoctor(t, oldNodePlist, true, "daemon")
	rep := RunDoctor(context.Background(), opts)
	c := wantStatus(t, rep, launchdCheck, DoctorWarn, "")
	if !strings.Contains(c.Detail, "没写 ProcessType") || !strings.Contains(c.Hint, "node doctor --fix") || !slices.Equal(c.Items, []string{opts.LaunchAgent}) {
		t.Errorf("%+v", c)
	}
	if !rep.OK {
		t.Error("a throttled node is a warning, not a failure")
	}
	// Only read: the plist as it was, launchd only asked.
	if string(mustRead(t, opts.LaunchAgent)) != oldNodePlist {
		t.Error("the doctor without --fix rewrote the plist")
	}
	if !slices.Equal(f.calls, []string{"print " + fakeTarget}) {
		t.Errorf("calls %q", f.calls)
	}
}

func TestDoctorFixWritesProcessTypeAndReloads(t *testing.T) {
	opts, f := launchdDoctor(t, oldNodePlist, true, "daemon")
	if err := os.Chmod(opts.LaunchAgent, 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Fix = true
	rep := RunDoctor(context.Background(), opts)
	c := wantStatus(t, rep, launchdCheck, DoctorOK, "")
	if !strings.Contains(c.Detail, "interactive") {
		t.Errorf("detail %q", c.Detail)
	}
	if !slices.Equal(rep.Fixed, []string{launchdCheck, launchdReloaded}) || !rep.OK {
		t.Errorf("fixed %v ok %v", rep.Fixed, rep.OK)
	}
	want := strings.Replace(oldNodePlist, "</dict></plist>", "  <key>ProcessType</key><string>Interactive</string>\n</dict></plist>", 1)
	if string(mustRead(t, opts.LaunchAgent)) != want {
		t.Errorf("plist:\n%s", mustRead(t, opts.LaunchAgent))
	}
	if st, _ := os.Stat(opts.LaunchAgent); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	wantCalls := []string{"print " + fakeTarget, "print " + fakeTarget, "bootout " + fakeTarget,
		"bootstrap gui/501 " + opts.LaunchAgent, "print " + fakeTarget}
	if !slices.Equal(f.calls, wantCalls) {
		t.Errorf("calls %q\nwant  %q", f.calls, wantCalls)
	}
	var out strings.Builder
	PrintDoctor(&out, rep)
	if !strings.Contains(out.String(), "写上 ProcessType=Interactive") || !strings.Contains(out.String(), "已重新加载") ||
		strings.Contains(out.String(), "已重新生成") {
		t.Errorf("printed:\n%s", out.String())
	}

	// Fixed: a second --fix does nothing.
	f.calls = nil
	rep = RunDoctor(context.Background(), opts)
	if len(rep.Fixed) != 0 || slices.ContainsFunc(f.calls, func(c string) bool { return !strings.HasPrefix(c, "print") }) {
		t.Errorf("fixed again: %v %q", rep.Fixed, f.calls)
	}
}

// residentSockets stands in for the resident node's node.sock (answers a ping) and runtime.sock: stop takes them down
// the way a node that bootout stopped leaves them (the files stay, nobody answers), start serves them again.
type residentSockets struct {
	t        *testing.T
	node, rt string
	mu       sync.Mutex
	lns      []net.Listener
	started  int
}

func newResidentSockets(t *testing.T, dir string) *residentSockets {
	r := &residentSockets{t: t, node: filepath.Join(dir, "n2.sock"), rt: filepath.Join(dir, "r2.sock")}
	r.start()
	t.Cleanup(r.stop)
	return r
}

func (r *residentSockets) start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started++
	for _, path := range []string{r.node, r.rt} {
		_ = os.Remove(path)
		ln, err := net.Listen("unix", path)
		if err != nil {
			r.t.Error(err)
			return
		}
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		r.lns = append(r.lns, ln)
		ping := path == r.node
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				if !ping {
					c.Close()
					continue
				}
				go func() {
					defer c.Close()
					if _, err := bufio.NewReader(c).ReadBytes('\n'); err == nil {
						_, _ = c.Write([]byte(`{"ok":true}` + "\n"))
					}
				}()
			}
		}()
	}
}

func (r *residentSockets) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ln := range r.lns {
		ln.Close()
	}
	r.lns = nil
}

// launchd returns from bootstrap before the new node listens. --fix waits for the sockets that answered before to
// answer again, then checks: the report is the restarted node's, not the gap before it (a false node.sock failure
// and exit status 1 otherwise).
func TestDoctorFixWaitsForTheRestartedNode(t *testing.T) {
	opts, f := launchdDoctor(t, oldNodePlist, true, "daemon")
	r := newResidentSockets(t, filepath.Dir(opts.Dir))
	opts.NodeSock, opts.RuntimeSock = r.node, r.rt
	f.onBootout = r.stop
	var loadedAt time.Time
	f.onBootstrap = func() {
		loadedAt = time.Now()
		time.AfterFunc(500*time.Millisecond, r.start) // the new node publishes its sockets a moment later
	}
	opts.Fix = true
	rep := RunDoctor(context.Background(), opts)
	if !rep.OK || !slices.Equal(rep.Fixed, []string{launchdCheck, launchdReloaded}) {
		t.Fatalf("ok %v fixed %v checks %+v", rep.OK, rep.Fixed, rep.Checks)
	}
	wantStatus(t, rep, "node.sock", DoctorOK, "")
	wantStatus(t, rep, "runtime.sock", DoctorOK, "")
	if took := time.Since(loadedAt); r.started != 2 || took < 500*time.Millisecond {
		t.Errorf("started %d, checked %s after the reload", r.started, took)
	}
	var out strings.Builder
	PrintDoctor(&out, rep)
	if !strings.Contains(out.String(), "已给常驻 node 的 LaunchAgent 写上 ProcessType=Interactive\n") ||
		!strings.Contains(out.String(), "已重新加载常驻 node 的 LaunchAgent") {
		t.Errorf("printed:\n%s", out.String())
	}
}

// A node that does not come back in time is still a failure, said to be right after the reload, with its log.
func TestDoctorFixSaysTheNodeIsNotBackYet(t *testing.T) {
	opts, f := launchdDoctor(t, oldNodePlist, true, "daemon")
	r := newResidentSockets(t, filepath.Dir(opts.Dir))
	opts.NodeSock, opts.RuntimeSock = r.node, r.rt
	f.onBootout = r.stop // and nothing starts it again
	opts.Fix, opts.RestartWait = true, 300*time.Millisecond
	start := time.Now()
	rep := RunDoctor(context.Background(), opts)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("took %s", took)
	}
	if rep.OK {
		t.Error("a node that is not back is ok")
	}
	for _, name := range []string{"node.sock", "runtime.sock"} {
		c := wantStatus(t, rep, name, DoctorFail, "")
		if !strings.Contains(c.Detail, "刚重新加载过，300ms 内还没有应答") || !strings.Contains(c.Hint, "/Users/a&b/.persona/node-attach.log") {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

// What was not answering before the reload is not waited for: the report says what it says at once.
func TestDoctorFixWaitsOnlyForWhatAnsweredBefore(t *testing.T) {
	opts, _ := launchdDoctor(t, oldNodePlist, true, "daemon")
	opts.NodeSock, opts.RuntimeSock = filepath.Join(filepath.Dir(opts.Dir), "none.sock"), filepath.Join(filepath.Dir(opts.Dir), "none2.sock")
	opts.Fix = true
	start := time.Now()
	rep := RunDoctor(context.Background(), opts)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("waited %s", took)
	}
	if c := wantStatus(t, rep, "node.sock", DoctorFail, ""); strings.Contains(c.Detail, "刚重新加载") {
		t.Errorf("%+v", c)
	}
}

// The plist already has it, but launchd still runs what it loaded before: --fix only reloads.
func TestDoctorFixReloadsAStaleLoad(t *testing.T) {
	plist := strings.Replace(oldNodePlist, "</dict></plist>", "  <key>ProcessType</key><string>Interactive</string>\n</dict></plist>", 1)
	opts, f := launchdDoctor(t, plist, true, "daemon")
	c := wantStatus(t, RunDoctor(context.Background(), opts), launchdCheck, DoctorWarn, "")
	if !strings.Contains(c.Detail, "spawn type = daemon") {
		t.Errorf("detail %q", c.Detail)
	}
	opts.Fix = true
	f.calls = nil
	rep := RunDoctor(context.Background(), opts)
	wantStatus(t, rep, launchdCheck, DoctorOK, "")
	if string(mustRead(t, opts.LaunchAgent)) != plist {
		t.Error("the plist was rewritten")
	}
	if !slices.Contains(f.calls, "bootout "+fakeTarget) || !slices.Equal(rep.Fixed, []string{launchdReloaded}) {
		t.Errorf("calls %q fixed %v", f.calls, rep.Fixed)
	}
	// Only reloaded: not said to have written anything.
	var out strings.Builder
	PrintDoctor(&out, rep)
	if strings.Contains(out.String(), "写上") || !strings.Contains(out.String(), "已重新加载常驻 node 的 LaunchAgent") {
		t.Errorf("printed:\n%s", out.String())
	}
}

// An agent launchd does not have loaded gets the new plist and stays unloaded: --fix does not start a node the user
// stopped.
func TestDoctorFixLeavesAnUnloadedAgentUnloaded(t *testing.T) {
	opts, f := launchdDoctor(t, oldNodePlist, false, "")
	opts.Fix = true
	rep := RunDoctor(context.Background(), opts)
	wantStatus(t, rep, launchdCheck, DoctorOK, "")
	if !strings.Contains(string(mustRead(t, opts.LaunchAgent)), "<key>ProcessType</key><string>Interactive</string>") {
		t.Error("not written")
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return !strings.HasPrefix(c, "print") }) || f.loaded ||
		!slices.Equal(rep.Fixed, []string{launchdCheck}) {
		t.Errorf("calls %q fixed %v", f.calls, rep.Fixed)
	}
}

// Inside the node (node.doctor.fix from the web page, or a claude under the agent) the plist is left alone: a reload
// would stop the node answering.
func TestDoctorFixInsideTheNodeLeavesTheAgent(t *testing.T) {
	opts, f := launchdDoctor(t, oldNodePlist, true, "daemon")
	opts.InsideNode = true
	// The hint sends the user to a terminal: running --fix from here would not do it.
	c := wantStatus(t, RunDoctor(context.Background(), opts), launchdCheck, DoctorWarn, "")
	if c.Hint != launchdFixHintInside || strings.Contains(c.Detail, "这次修复") {
		t.Errorf("%+v", c)
	}
	opts.Fix = true
	rep := RunDoctor(context.Background(), opts)
	c = wantStatus(t, rep, launchdCheck, DoctorWarn, "")
	if string(mustRead(t, opts.LaunchAgent)) != oldNodePlist || len(rep.Fixed) != 0 {
		t.Errorf("fixed %v", rep.Fixed)
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return !strings.HasPrefix(c, "print") }) {
		t.Errorf("calls %q", f.calls)
	}
	// Not silently: the report says this fix left it, and why.
	if c.Hint != launchdFixHintInside || !strings.Contains(c.Detail, "这次修复没动它") {
		t.Errorf("%+v", c)
	}
	// Outside the node the hint is the plain one.
	opts.InsideNode, opts.Fix = false, false
	if c := wantStatus(t, RunDoctor(context.Background(), opts), launchdCheck, DoctorWarn, ""); c.Hint != launchdFixHint {
		t.Errorf("%+v", c)
	}
}

// launchd still taking the old job down refuses the bootstrap for a moment: tried again.
func TestDoctorFixRetriesTheBootstrap(t *testing.T) {
	opts, f := launchdDoctor(t, oldNodePlist, true, "daemon")
	f.bootstrap = func(n int) error {
		if n == 1 {
			return errors.New("exit status 5")
		}
		return nil
	}
	opts.Fix = true
	rep := RunDoctor(context.Background(), opts)
	wantStatus(t, rep, launchdCheck, DoctorOK, "")
	if f.tries != 2 || !rep.OK {
		t.Errorf("tries %d ok %v", f.tries, rep.OK)
	}
}

// A bootstrap that keeps failing leaves the node unloaded: said loudly, with the command that loads it back.
func TestDoctorFixSaysHowToLoadItBack(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the retries (3 s)")
	}
	opts, f := launchdDoctor(t, oldNodePlist, true, "daemon")
	f.bootstrap = func(int) error { return errors.New("exit status 5") }
	opts.Fix = true
	rep := RunDoctor(context.Background(), opts)
	if rep.OK || f.tries != launchdRetries {
		t.Fatalf("ok %v tries %d", rep.OK, f.tries)
	}
	c := doctorCheckNamed(t, rep, "fix")
	if c.Status != DoctorFail || !strings.Contains(c.Detail, "launchctl bootstrap gui/501 "+opts.LaunchAgent) || !strings.Contains(c.Detail, "Input/output error") {
		t.Errorf("%+v", c)
	}
	if !slices.Equal(rep.Fixed, []string{launchdCheck}) { // the plist was rewritten all the same; not reloaded
		t.Errorf("fixed %v", rep.Fixed)
	}
	// Said as it went: written, and the reload failed; never "reloaded".
	var out strings.Builder
	PrintDoctor(&out, rep)
	if !strings.Contains(out.String(), "写上 ProcessType=Interactive") || strings.Contains(out.String(), "已重新加载") {
		t.Errorf("printed:\n%s", out.String())
	}
}

func TestDoctorLaunchAgentOtherStates(t *testing.T) {
	// None installed: the node runs over ssh.
	opts, f := launchdDoctor(t, "", false, "")
	opts.Fix = true
	rep := RunDoctor(context.Background(), opts)
	if c := wantStatus(t, rep, launchdCheck, DoctorOK, ""); !strings.Contains(c.Detail, "没有") || len(rep.Fixed) != 0 || len(f.calls) != 0 {
		t.Errorf("%+v %v %q", c, rep.Fixed, f.calls)
	}
	// Another ProcessType.
	opts, _ = launchdDoctor(t, strings.Replace(oldNodePlist, "</dict></plist>", "<key>ProcessType</key><string>Background</string></dict></plist>", 1), true, "background")
	if c := wantStatus(t, RunDoctor(context.Background(), opts), launchdCheck, DoctorWarn, ""); !strings.Contains(c.Detail, "Background") {
		t.Errorf("%+v", c)
	}
	// Binary: how to make it readable.
	opts, _ = launchdDoctor(t, "bplist00xx", true, "daemon")
	opts.Fix = true
	rep = RunDoctor(context.Background(), opts)
	if c := wantStatus(t, rep, launchdCheck, DoctorWarn, ""); !strings.Contains(c.Hint, "plutil -convert xml1") || len(rep.Fixed) != 0 {
		t.Errorf("%+v", c)
	}
	if string(mustRead(t, opts.LaunchAgent)) != "bplist00xx" {
		t.Error("a plist it cannot read was rewritten")
	}
	// Elsewhere than a Mac there is no such check.
	e := newDoctorEnv(t)
	for _, c := range RunDoctor(context.Background(), e.opts).Checks {
		if c.Name == launchdCheck {
			t.Error("launchd check without a LaunchAgent to look at")
		}
	}
}

// The node's own options: the agent looked at on a Mac, never reloaded from inside.
func TestNodeDoctorOptionsStayInsideTheNode(t *testing.T) {
	e := newDoctorEnv(t)
	opts := e.n.doctorOptions()
	if !opts.InsideNode {
		t.Error("node.doctor may reload the agent it runs under")
	}
	if (opts.LaunchAgent != "") != (runtime.GOOS == "darwin") {
		t.Errorf("LaunchAgent %q on %s", opts.LaunchAgent, runtime.GOOS)
	}
	// No agent in this home: today's label, where a new install writes it.
	if runtime.GOOS == "darwin" && (opts.LaunchAgent != NodeLaunchAgentPath(e.n.home, NodeLaunchdLabel) ||
		!strings.HasSuffix(opts.LaunchdTarget, "/"+NodeLaunchdLabel)) {
		t.Errorf("LaunchAgent %q %q", opts.LaunchAgent, opts.LaunchdTarget)
	}
	if (opts.Preflight != nil) != (runtime.GOOS != "windows") {
		t.Error("preflight")
	}
}

// A Mac set up before the rename keeps its installed agent label; a new install writes com.persona.node.
// The doctor looks at the one whose plist is there and, with both there, at the
// one launchd has loaded: that is the running node, and the one --fix has to reload.
func TestPickNodeLaunchdLabel(t *testing.T) {
	const fresh, legacy = NodeLaunchdLabel, legacyNodeLaunchdLabel
	for _, tc := range []struct {
		name           string
		plists, loaded []string
		self           string
		noLaunchctl    bool
		want           string
		asks           bool // launchctl is asked
	}{
		{name: "neither", want: fresh},
		{name: "only the new one", plists: []string{fresh}, loaded: []string{fresh}, want: fresh},
		{name: "only the old one", plists: []string{legacy}, loaded: []string{legacy}, want: legacy},
		{name: "only the old one, not loaded", plists: []string{legacy}, want: legacy},
		{name: "both, the old one loaded", plists: []string{fresh, legacy}, loaded: []string{legacy}, want: legacy, asks: true},
		{name: "both, the new one loaded", plists: []string{fresh, legacy}, loaded: []string{fresh}, want: fresh, asks: true},
		{name: "both, both loaded", plists: []string{fresh, legacy}, loaded: []string{fresh, legacy}, want: fresh, asks: true},
		{name: "both, neither loaded", plists: []string{fresh, legacy}, want: fresh, asks: true},
		{name: "both, under the old one", plists: []string{fresh, legacy}, loaded: []string{fresh, legacy}, self: legacy, want: legacy},
		{name: "both, under the new one", plists: []string{fresh, legacy}, loaded: []string{legacy}, self: fresh, want: fresh},
		{name: "both, a terminal", plists: []string{fresh, legacy}, loaded: []string{legacy}, self: "0", want: legacy, asks: true},
		{name: "both, nobody to ask", plists: []string{fresh, legacy}, loaded: []string{legacy}, noLaunchctl: true, want: fresh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for _, label := range tc.plists {
				writeLaunchAgent(t, home, label, strings.Replace(oldNodePlist, NodeLaunchdLabel, label, 1))
			}
			var calls []string
			launchctl := func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, strings.Join(args, " "))
				label, _ := strings.CutPrefix(args[1], "gui/501/")
				if args[0] != "print" || !isNodeLaunchdLabel(label) {
					t.Errorf("launchctl %q", args)
				}
				if !slices.Contains(tc.loaded, label) {
					return "", errors.New("exit status 113")
				}
				return args[1] + " = {\n}\n", nil
			}
			if tc.noLaunchctl {
				launchctl = nil
			}
			var opts DoctorOptions
			opts.withLaunchAgent(context.Background(), home, "gui/501", tc.self, launchctl)
			if opts.LaunchAgent != NodeLaunchAgentPath(home, tc.want) || opts.LaunchdTarget != "gui/501/"+tc.want {
				t.Errorf("picked %q %q, want %s", opts.LaunchAgent, opts.LaunchdTarget, tc.want)
			}
			if asked := len(calls) > 0; asked != tc.asks {
				t.Errorf("launchctl calls %q", calls)
			}
		})
	}
}

// The agent from before the rename is checked and fixed under its own label: its plist rewritten, launchd asked for
// it, and nothing written under today's label (a second agent would start a second node at the next login).
func TestDoctorFixesTheAgentUnderTheOldLabel(t *testing.T) {
	legacyPlist := strings.Replace(oldNodePlist, NodeLaunchdLabel, legacyNodeLaunchdLabel, 1)
	const target = "gui/501/" + legacyNodeLaunchdLabel
	for _, both := range []bool{false, true} {
		home := t.TempDir()
		legacy := writeLaunchAgent(t, home, legacyNodeLaunchdLabel, legacyPlist)
		fresh := NodeLaunchAgentPath(home, NodeLaunchdLabel)
		if both { // reinstalled under today's label, not loaded: launchd still runs the old one
			writeLaunchAgent(t, home, NodeLaunchdLabel, oldNodePlist)
		}
		opts, f := launchdDoctorAt(t, home, legacyNodeLaunchdLabel, true, "daemon")
		if opts.LaunchAgent != legacy || opts.LaunchdTarget != target {
			t.Fatalf("both %v: picked %q %q", both, opts.LaunchAgent, opts.LaunchdTarget)
		}
		c := wantStatus(t, RunDoctor(context.Background(), opts), launchdCheck, DoctorWarn, "")
		if !slices.Equal(c.Items, []string{legacy}) || !slices.Equal(f.calls, []string{"print " + target}) {
			t.Errorf("both %v: %+v %q", both, c, f.calls)
		}
		opts.Fix = true
		f.calls = nil
		rep := RunDoctor(context.Background(), opts)
		wantStatus(t, rep, launchdCheck, DoctorOK, "")
		wantCalls := []string{"print " + target, "print " + target, "bootout " + target, "bootstrap gui/501 " + legacy, "print " + target}
		if !slices.Equal(rep.Fixed, []string{launchdCheck, launchdReloaded}) || !slices.Equal(f.calls, wantCalls) {
			t.Errorf("both %v: fixed %v calls %q", both, rep.Fixed, f.calls)
		}
		want := strings.Replace(legacyPlist, "</dict></plist>", "  <key>ProcessType</key><string>Interactive</string>\n</dict></plist>", 1)
		if string(mustRead(t, legacy)) != want {
			t.Errorf("both %v: plist:\n%s", both, mustRead(t, legacy))
		}
		if b, err := os.ReadFile(fresh); both && string(b) != oldNodePlist || !both && err == nil {
			t.Errorf("both %v: today's plist %q %v", both, b, err)
		}
	}
}

func TestIsNodeLaunchdLabel(t *testing.T) {
	for name, want := range map[string]bool{
		NodeLaunchdLabel: true, legacyNodeLaunchdLabel: true,
		"": false, "0": false, "application.com.apple.Terminal.123": false, "com.persona.hub": false,
	} {
		if isNodeLaunchdLabel(name) != want {
			t.Errorf("%q: %v", name, !want)
		}
	}
}
