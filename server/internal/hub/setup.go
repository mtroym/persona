package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mdp/qrterminal/v3"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/pgdocker"
)

// `personad hub setup` (docs/assistant/onboarding.md 4.1, 5.5): one command that leaves a working hub on this machine. It writes
// hub.json (a whole new file only when there is none yet), makes sure there is a database (the same Postgres container
// scripts/deploy/db.sh makes, or the one hub.json names), installs the service that keeps the hub running (the same
// systemd units as scripts/deploy/hub-systemd.sh; a launchd agent on a Mac), waits for the hub, has the built-in manager
// bound and installed as a service of its own (setup_manager.go), checks it, and prints what to do next: a QR code for
// the phone and the admin URL with the first-run setup code in its fragment.
//
// Every step prints one line; a step that fails says what to do and stops. Running it again is safe: values already
// in hub.json are never changed, and a service that is already right is left running.

// ErrSetupStopped: a step failed and said why; the command exits non-zero without printing anything more.
var ErrSetupStopped = errors.New("hub setup stopped")

type setupOptions struct {
	yes       bool
	listen    string
	tls       string
	listenSet bool
	tlsSet    bool
	outside   bool
	noService bool
	json      bool
	// managerService: an existing hub's built-in manager becomes a service of its own (a new install's is anyway).
	managerService bool
}

// The database `personad hub setup` creates: scripts/deploy/db.sh's container, so deploy.sh keeps working with it.
const (
	setupDBContainer = "persona-postgres"
	setupDBVolume    = "persona-pgdata"
	setupDBPort      = 18661
	hubLaunchdLabel  = "com.persona.hub" // like com.persona.node (persona-link's nodesvc.go)
)

// SetupCommand is `personad hub setup [--yes] [--listen :18660] [--tls :18662] [--outside] [--no-service]
// [--manager-service] [--json]`.
func SetupCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("hub setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o setupOptions
	flags.BoolVar(&o.yes, "yes", false, "不问问题，都按默认做（包括用 sudo 打开 linger）")
	flags.StringVar(&o.listen, "listen", ":18660", "新装时 hub 的明文端口")
	flags.StringVar(&o.tls, "tls", ":18662", "新装时 hub 的 TLS 端口（不写地址时只绑局域网 IPv4）")
	flags.BoolVar(&o.outside, "outside", false, "出门也要用：TLS 端口绑所有地址，自检也查公网")
	flags.BoolVar(&o.noService, "no-service", false, "不装常驻服务（自己运行 personad hub run）；hub 自己拉起内置管理者")
	flags.BoolVar(&o.managerService, "manager-service", false, "已有的 hub：把内置管理者改成自己的常驻服务（新装的本来就是）")
	flags.BoolVar(&o.json, "json", false, "一行一个 JSON：每步 {step, ok, detail, next}，最后 {done, …}")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("hub setup 不接受位置参数")
	}
	if o.noService && o.managerService {
		return fmt.Errorf("--manager-service 要装内置管理者的常驻服务，不能和 --no-service 一起用")
	}
	flags.Visit(func(f *flag.Flag) {
		o.listenSet = o.listenSet || f.Name == "listen"
		o.tlsSet = o.tlsSet || f.Name == "tls"
	})
	for _, a := range []string{o.listen, o.tls} {
		if _, err := doctorPort(a); err != nil {
			return fmt.Errorf("%q 不是有效的监听地址：写成 :端口 或 地址:端口", a)
		}
	}
	e, err := newSetupEnv(stdout, o)
	if err != nil {
		return err
	}
	return e.setup(ctx)
}

// setupEnv is everything setup touches outside its own files, so that tests swap them out.
type setupEnv struct {
	home, root string
	goos       string
	uid        int
	user       string
	out        io.Writer
	o          setupOptions

	run      func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	runTTY   func(ctx context.Context, name string, args ...string) error // sudo: the password prompt needs the terminal
	lookPath func(string) (string, error)
	ask      func(question string) (yes, asked bool)
	lanIPv4  func() string
	portFree func(port int) bool
	exe      func() (string, error)
	pingDB   func(ctx context.Context, url string) error
	doctor   func(ctx context.Context, dir string, outside bool) []string
	getenv   func(string) string
	sleep    func(time.Duration)

	isContainer func() bool
	// restartHub: config changed what a running hub reads only at start, so service restarts it; restartWhy says what.
	restartHub bool
	restartWhy []string
	// hubRestart restarts the hub's service again (set by service when the hub runs as it): undoing a switch to the
	// built-in manager's own service that could not be finished. switched: this run set assistant.mode service on a
	// hub.json a hub had run with (--manager-service); prevMode is what it was ("" for none).
	hubRestart func(ctx context.Context) error
	switched   bool
	prevMode   string
	// builtinCode makes a one-time code that binds the built-in manager's own service (ManagerCode's); voidCode voids
	// it when the manager's program did not trade it.
	builtinCode func(ctx context.Context, cfg *Config) (string, error)
	voidCode    func(ctx context.Context, cfg *Config, code string) error
	// keptMachines: machine → the manager that keeps it, as the hub's database has it (keptMachines).
	keptMachines func(ctx context.Context, cfg *Config) (map[string]string, error)

	healthWait  time.Duration
	dbWait      time.Duration
	managerWait time.Duration // for the built-in manager's service to come online
	// The database setup creates; another name only for tests and trial runs (_PERSONA_SETUP_DB_CONTAINER…).
	dbContainer, dbVolume string
	dbPort                int
}

func newSetupEnv(out io.Writer, o setupOptions) (*setupEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root, err := identity.PrepareHome(home)
	if err != nil {
		return nil, err
	}
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	e := &setupEnv{
		home: home, root: root, goos: runtime.GOOS, uid: os.Getuid(), user: name, out: out, o: o,
		run:         runCommand,
		runTTY:      runOnTerminal,
		lookPath:    exec.LookPath,
		ask:         askTerminal,
		lanIPv4:     defaultLANIPv4,
		portFree:    tcpPortFree,
		exe:         ServerBinary, // persona-hub setup installs the personad beside it (the service: splitHub)
		pingDB:      pingPostgres,
		doctor:      setupDoctor,
		getenv:      identity.Getenv,
		sleep:       time.Sleep,
		isContainer: inContainer,
		builtinCode: func(ctx context.Context, cfg *Config) (string, error) {
			code, _, err := builtinCode(ctx, cfg)
			return code, err
		},
		voidCode:     voidBuiltinCode,
		keptMachines: keptMachines,

		healthWait:  90 * time.Second,
		dbWait:      30 * time.Second,
		managerWait: 90 * time.Second,
		dbContainer: setupDBContainer, dbVolume: setupDBVolume, dbPort: setupDBPort,
	}
	if o.json {
		e.ask = func(string) (bool, bool) { return false, false } // nobody to ask: the caller is a program
	}
	// Hidden, for trying setup out next to a real hub on the same machine (never in docs for users).
	if v := os.Getenv("_PERSONA_SETUP_DB_CONTAINER"); v != "" {
		e.dbContainer, e.dbVolume = v, v+"-data"
	}
	if v, err := strconv.Atoi(os.Getenv("_PERSONA_SETUP_DB_PORT")); err == nil && v > 0 && v < 65536 {
		e.dbPort = v
	}
	return e, nil
}

// --- output ---------------------------------------------------------------------------------------------------

var setupStepNames = map[string]string{
	"config": "配置", "database": "数据库", "service": "常驻", "health": "启动", "manager": "管理者", "doctor": "自检",
}

func (e *setupEnv) report(step string, ok bool, detail, next string) {
	if e.o.json {
		b, _ := marshalNoEscape(map[string]any{"step": step, "ok": ok, "detail": detail, "next": next})
		_, _ = e.out.Write(append(b, '\n'))
		return
	}
	mark := "完成"
	if !ok {
		mark = "有问题"
	}
	fmt.Fprintf(e.out, "%s｜%s：%s\n", mark, setupStepNames[step], detail)
	if next != "" {
		fmt.Fprintf(e.out, "  下一步：%s\n", next)
	}
}

// fail reports a step that stops setup.
func (e *setupEnv) fail(step, detail, next string) error {
	e.report(step, false, detail, next)
	return ErrSetupStopped
}

// --- the steps ------------------------------------------------------------------------------------------------

func (e *setupEnv) setup(ctx context.Context) error {
	if !e.o.json {
		fmt.Fprintln(e.out, "安装 hub（personad hub setup）")
	}
	cfg, err := e.config()
	if err != nil {
		return err
	}
	if err := e.database(ctx, cfg); err != nil {
		return err
	}
	if err := e.localMachine(ctx, cfg); err != nil {
		return err
	}
	if err := e.service(ctx, cfg); err != nil {
		return err
	}
	if err := e.health(ctx, cfg); err != nil {
		return err
	}
	if err := e.builtinManager(ctx, cfg); err != nil {
		return err
	}
	problems := e.doctor(ctx, e.root, e.o.outside)
	for _, p := range problems {
		e.report("doctor", false, p, "")
	}
	if len(problems) == 0 {
		e.report("doctor", true, "没发现问题", "")
	}
	return e.finish(ctx, cfg)
}

// installInputs are what newInstallConfig needs to know about this machine.
type installInputs struct {
	lanIP   string // this machine's LAN IPv4 ("" when none was found)
	listen  string // ":18660"
	tls     string // ":18662", or with an address: used as given
	outside bool   // TLS on every address
	agents  bool   // herdr or claude is here: keep this machine as a local machine
}

// newInstallConfig is the hub.json a new install gets (docs/assistant/onboarding.md 4.1). These defaults are written into new
// files only, never added to defaultConfig(): LoadConfig decodes every existing hub.json over defaultConfig(), so a
// default there would change every hub whose file does not name the field (Public is a value: a TLSListen default
// would turn TLS on for every old hub.json without "public").
func newInstallConfig(in installInputs) *Config {
	cfg := defaultConfig()
	cfg.Listen = in.listen
	if in.lanIP != "" {
		cfg.PublicURL = "http://" + net.JoinHostPort(in.lanIP, portOf(in.listen))
	}
	host, port, _ := net.SplitHostPort(in.tls)
	switch {
	case host != "":
		cfg.Public.TLSListen = in.tls
	case in.outside:
		cfg.Public.TLSListen = ":" + port
	case in.lanIP != "":
		// Only the LAN IPv4 address: Persona Link and mDNS need TLS, the internet over IPv6 does not get the port.
		cfg.Public.TLSListen = net.JoinHostPort(in.lanIP, port)
	}
	cfg.Admin.FirstSetup = firstSetupCode
	if !in.agents {
		cfg.Machines = []MachineConfig{}
	}
	return cfg
}

// setupConfig is what the later steps need from hub.json (environment overrides applied, like the hub does).
type setupConfig struct {
	*Config
	fresh bool
	// noLocal: a hub.json a hub has run with lists no machine, and claude or herdr is here (localMachine).
	noLocal bool
}

func (e *setupEnv) hubJSON() string { return filepath.Join(e.root, "hub.json") }

// config writes hub.json. A hub.json no hub has read yet (none, or one without a token: db.sh and `hub admin access`
// write such files before the first run) gets every new-install default it does not have; one a hub has run with
// keeps every value, and only gets what it cannot run without.
func (e *setupEnv) config() (*setupConfig, error) {
	raw, err := readHubJSON(e.root)
	if err != nil {
		return nil, e.fail("config", e.hubJSON()+" 读不了或不是 JSON 对象："+err.Error(), "修好或挪走这个文件，再运行 personad hub setup")
	}
	_, hasToken := raw.Get("token")
	fresh := !hasToken
	e.restartHub, e.restartWhy, e.hubRestart, e.switched, e.prevMode = false, nil, nil, false, ""
	var notes []string
	services := e.canManagerService(context.Background())
	if e.o.managerService && !services {
		return nil, e.fail("config", "这里装不了内置管理者的常驻服务（"+e.noManagerServiceWhy(context.Background())+"）", "去掉 --manager-service 重跑")
	}
	if fresh {
		in := e.installInputs(&notes)
		gen, err := marshalNoEscape(newInstallConfig(in))
		if err != nil {
			return nil, err
		}
		genObj, err := parseJSONObject(gen)
		if err != nil {
			return nil, err
		}
		if err := editHubJSON(e.root, func(o *jsonObject) error {
			if err := fillMissing(o, genObj); err != nil {
				return err
			}
			return freshManagerMode(o, services, e.o.managerService)
		}); err != nil {
			return nil, e.fail("config", "写不了 "+e.hubJSON()+"："+err.Error(), "检查这个目录的权限")
		}
	} else {
		if e.o.managerService {
			changed, err := e.toManagerService(context.Background())
			if err != nil {
				return nil, err
			}
			if changed {
				e.restartHub = true // a running hub reads assistant.mode only when it starts
				e.restartWhy = append(e.restartWhy, "内置管理者改成了自己的常驻服务")
				notes = append(notes, "assistant.mode 改成 service：hub 不再拉起内置管理者，它是自己的常驻服务")
			}
		}
		if note := e.keptListenNote(raw); note != "" {
			notes = append(notes, note)
		}
		if e.o.outside {
			if err := editHubJSON(e.root, outsideTLS); err != nil {
				return nil, e.fail("config", "写不了 "+e.hubJSON()+"："+err.Error(), "检查这个目录的权限")
			}
		}
	}
	b, err := os.ReadFile(e.hubJSON())
	if err != nil {
		return nil, e.fail("config", "读不了 "+e.hubJSON()+"："+err.Error(), "检查这个目录的权限")
	}
	cfg := &Config{Listen: ":18660", dir: e.root}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, e.fail("config", e.hubJSON()+" 格式有误："+err.Error(), "修正 JSON 后重跑")
	}
	cfg.applyEnv()
	if _, err := doctorPort(cfg.Listen); err != nil {
		return nil, e.fail("config", "hub.json 的 listen（"+cfg.Listen+"）没有有效端口", "改成 \":18660\" 这样的写法后重跑")
	}
	detail := "已写好 " + e.hubJSON()
	if !fresh {
		detail = "用已有的 " + e.hubJSON() + "，已有的值不改"
	}
	if fresh {
		detail += "（明文端口 " + portOf(cfg.Listen)
		if cfg.Public.TLSListen != "" {
			detail += "，TLS " + cfg.Public.TLSListen
		} else {
			detail += "，没找到局域网地址，先不开 TLS"
		}
		detail += "）"
	}
	if len(notes) > 0 {
		detail += "；" + strings.Join(notes, "；")
	}
	e.report("config", true, detail, "")
	return &setupConfig{Config: cfg, fresh: fresh, noLocal: !fresh && noMachines(raw) && e.hasAgents()}, nil
}

// localMachine adds this machine to a hub.json a hub has run with that lists none, once claude or herdr is here
// (installed before them, #4083), and has service restart the hub, which reads machines only when it starts. Not while
// a manager keeps any machine (machines_kept.go): their entries left hub.json for its own list, and one added here
// would be handed to a manager on every rerun, over the entry it keeps. It reads that from the database, so it runs
// after database (which may start it).
func (e *setupEnv) localMachine(ctx context.Context, cfg *setupConfig) error {
	if !cfg.noLocal {
		return nil
	}
	kept, err := e.keptMachines(ctx, cfg.Config)
	if err != nil {
		e.report("config", false, "hub.json 里没有电脑，但读不了哪些电脑归管理者管（"+err.Error()+"），这次没补本机",
			"看管理界面的「电脑」里有没有这台电脑，没有就加上；修好后重跑 personad hub setup")
		return nil
	}
	if len(kept) > 0 {
		return nil
	}
	if err := editHubJSON(e.root, func(o *jsonObject) error {
		if !noMachines(o) {
			return nil
		}
		return o.Set("machines", []MachineConfig{defaultLocalMachine()})
	}); err != nil {
		return e.fail("config", "写不了 "+e.hubJSON()+"："+err.Error(), "检查这个目录的权限")
	}
	e.restartHub = true
	e.restartWhy = append(e.restartWhy, "hub.json 补了本机")
	e.report("config", true, "补上本机作为电脑（kind: local）", "")
	return nil
}

// keptMachines is machine → the manager that keeps it, as the hub reads it (loadMachinesKept). It migrates nothing:
// the hub running may be an older build than this program.
func keptMachines(ctx context.Context, cfg *Config) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	database, err := db.Attach(ctx, cfg.DatabaseURL, db.HubSet)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", machinesKeptKey, err)
	}
	defer database.Close()
	h := &Hub{cfg: cfg, store: database, logger: log.New(io.Discard, "", 0)}
	if err := h.loadMachinesKept(ctx); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "42P01" {
			return nil, nil // no hub_settings: a hub from before managers kept machines
		}
		return nil, fmt.Errorf("read %s: %w", machinesKeptKey, err)
	}
	return h.devices.kept, nil
}

// keptListenNote is what a rerun says about --listen / --tls on a hub.json a hub has run with: those flags only shape
// a new install, so a value given explicitly that differs from the file's is named with both sides; the same value
// again (or a flag not given) says nothing.
func (e *setupEnv) keptListenNote(raw *jsonObject) string {
	var file struct {
		Listen string
		Public struct {
			TLSListen string `json:"tls_listen"`
		}
	}
	file.Listen = ":18660" // Config's default when hub.json has none
	if b, ok := raw.Get("listen"); ok {
		_ = json.Unmarshal(b, &file.Listen)
	}
	if b, ok := raw.Get("public"); ok {
		_ = json.Unmarshal(b, &file.Public)
	}
	var diffs []string
	if e.o.listenSet && !sameListen(e.o.listen, file.Listen, "") {
		diffs = append(diffs, "listen 仍是 "+file.Listen+"（这次给的 --listen "+e.o.listen+"）")
	}
	// A new install binds a TLS address given without a host to the LAN IPv4 (newInstallConfig): that is the same.
	if e.o.tlsSet && !sameListen(e.o.tls, file.Public.TLSListen, e.lanIPv4()) {
		have := file.Public.TLSListen
		if have == "" {
			have = "空（没开 TLS）"
		}
		diffs = append(diffs, "public.tls_listen 仍是 "+have+"（这次给的 --tls "+e.o.tls+"）")
	}
	if len(diffs) == 0 {
		return ""
	}
	return strings.Join(diffs, "，") + "：--listen、--tls 只用于新装，已有的不改"
}

// sameListen: the address a flag asked for is the one the file has; a flag without a host also matches the file's
// port on lan (what a new install writes for it), and "localhost" / "0.0.0.0" read like their usual spellings.
func sameListen(flag, file, lan string) bool {
	if flag == file {
		return true
	}
	fh, fp, err1 := net.SplitHostPort(flag)
	hh, hp, err2 := net.SplitHostPort(file)
	if err1 != nil || err2 != nil || fp != hp {
		return false
	}
	norm := func(h string) string {
		switch h {
		case "0.0.0.0", "::", "*":
			return ""
		case "localhost":
			return "127.0.0.1"
		}
		return h
	}
	fh, hh = norm(fh), norm(hh)
	return fh == hh || (fh == "" && lan != "" && hh == lan)
}

// installInputs looks at this machine for a new hub.json: the LAN address, free ports (another hub on the default
// ones moves this one along by 100), whether there are agents to drive here.
func (e *setupEnv) installInputs(notes *[]string) installInputs {
	in := installInputs{lanIP: e.lanIPv4(), listen: e.o.listen, tls: e.o.tls, outside: e.o.outside}
	lp, _ := strconv.Atoi(portOf(in.listen))
	tp, _ := strconv.Atoi(portOf(in.tls))
	if !e.portFree(lp) || !e.portFree(tp) {
		for k := 1; k <= 20; k++ {
			l, t := lp+100*k, tp+100*k
			if t > 65535 {
				break
			}
			if e.portFree(l) && e.portFree(t) {
				*notes = append(*notes, fmt.Sprintf("端口 %d / %d 被别的程序占着，可能是另一个 Persona，改用 %d / %d", lp, tp, l, t))
				lh, _, _ := net.SplitHostPort(in.listen)
				th, _, _ := net.SplitHostPort(in.tls)
				in.listen, in.tls = net.JoinHostPort(lh, strconv.Itoa(l)), net.JoinHostPort(th, strconv.Itoa(t))
				break
			}
		}
	}
	in.agents = e.hasAgents()
	return in
}

// noMachines: hub.json says "machines": []. Without the key the hub starts with this machine already (defaultConfig).
func noMachines(o *jsonObject) bool {
	raw, ok := o.Get("machines")
	var list []json.RawMessage
	return ok && json.Unmarshal(raw, &list) == nil && len(list) == 0
}

func (e *setupEnv) isInsideContainer() bool {
	if e.isContainer != nil {
		return e.isContainer()
	}
	return inContainer()
}

func (e *setupEnv) hasAgents() bool {
	if e.getenv("PERSONA_LOCAL_NODE") == "0" || e.isInsideContainer() {
		return false
	}
	return e.hasProgram("herdr") || e.hasProgram("claude")
}

// hasProgram: on PATH, or where the node looks besides (install.sh's has_herdr).
func (e *setupEnv) hasProgram(name string) bool {
	if _, err := e.lookPath(name); err == nil {
		return true
	}
	for _, d := range []string{filepath.Join(e.home, ".local", "bin"), filepath.Join(e.home, "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
		if fi, err := os.Stat(filepath.Join(d, name)); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// fillMissing adds what src has and dst does not, into nested objects too; nothing dst has is changed.
func fillMissing(dst, src *jsonObject) error {
	for _, k := range src.Keys() {
		want, _ := src.Get(k)
		have, ok := dst.Get(k)
		if !ok {
			dst.SetRaw(k, want)
			continue
		}
		if !isJSONObject(want) || !isJSONObject(have) {
			continue
		}
		srcNested, srcErr := parseJSONObject(want)
		dstNested, dstErr := parseJSONObject(have)
		if srcErr != nil || dstErr != nil {
			continue
		}
		if err := fillMissing(dstNested, srcNested); err != nil {
			return err
		}
		dst.SetRaw(k, dstNested.Marshal())
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// outsideTLS is --outside on an existing hub.json: TLS on every address (a LAN-only "<address>:18662" becomes
// ":18662"; none becomes ":18662"). The person asked for it, so this one existing value does change.
func outsideTLS(o *jsonObject) error {
	pub, err := o.Object("public")
	if err != nil {
		return err
	}
	var cur string
	if raw, ok := pub.Get("tls_listen"); ok {
		_ = json.Unmarshal(raw, &cur)
	}
	want := ":18662"
	if cur != "" {
		want = ":" + portOf(cur)
	}
	if cur == want {
		return nil
	}
	if err := pub.Set("tls_listen", want); err != nil {
		return err
	}
	o.SetObject("public", pub)
	return nil
}

// --- database ---------------------------------------------------------------------------------------------------

func (e *setupEnv) database(ctx context.Context, cfg *setupConfig) error {
	if cfg.DatabaseURL != "" {
		err := e.pingDB(ctx, cfg.DatabaseURL)
		if err != nil && e.containerExists(ctx) {
			// The container we made may just be stopped (Docker Desktop not started at login, say).
			if e.startContainer(ctx, cfg.DatabaseURL) == nil {
				err = e.pingDB(ctx, cfg.DatabaseURL)
			}
		}
		if err != nil {
			return e.fail("database", "hub.json 里的数据库连不上："+err.Error(), "确认 Postgres 在运行、database_url 对，再重跑")
		}
		e.report("database", true, "用 hub.json 里的数据库", "")
		return nil
	}
	switch err := pgdocker.CheckDocker(ctx, e.run, e.lookPath); {
	case errors.Is(err, pgdocker.ErrNoDocker):
		return e.noDocker()
	case errors.Is(err, pgdocker.ErrDockerDenied):
		return e.fail("database", "有 Docker，但当前用户没有权限用它",
			"运行 sudo usermod -aG docker "+e.user+"，退出登录再登录，然后重跑 personad hub setup")
	case err != nil:
		return e.fail("database", "有 Docker，但它没在运行", e.startDockerHint())
	}
	pg := e.pg()
	created := false
	var password string
	if !pg.Exists(ctx) {
		password = randomHex(24)
		var step *pgdocker.StepError
		if err := pg.Create(ctx, password); errors.As(err, &step) && step.Step == "volume" {
			return e.fail("database", "建数据卷 "+e.dbVolume+" 失败："+step.Output, "看 docker 的报错，处理后重跑")
		} else if err != nil {
			why := err.Error()
			if step != nil {
				why = step.Output
			}
			return e.fail("database", "建数据库容器 "+e.dbContainer+" 失败："+why,
				fmt.Sprintf("端口 %d 被占着或拉不到镜像 %s 时处理后重跑", e.dbPort, pgdocker.Image))
		}
		created = true
	} else {
		// The container is there but hub.json lost its address: the password it was made with is in its environment.
		var err error
		if password, err = pg.Password(ctx); err != nil {
			return e.fail("database", "已有数据库容器 "+e.dbContainer+"，但 hub.json 里没有它的地址，也读不出它的密码",
				"把 database_url 写回 hub.json（postgres://persona:<密码>@127.0.0.1:"+strconv.Itoa(e.dbPort)+"/persona?sslmode=disable）再重跑")
		}
	}
	if err := e.startContainer(ctx, ""); err != nil {
		return e.fail("database", "数据库容器 "+e.dbContainer+" 没准备好："+err.Error(), "运行 docker logs "+e.dbContainer+" 看原因")
	}
	port, err := pg.HostPort(ctx)
	if err != nil {
		port = e.dbPort // a container on the host's network (db.sh): its port is the one it was made with
	}
	dbURL := pgdocker.URL(password, port)
	if err := editHubJSON(e.root, func(o *jsonObject) error { return o.Set("database_url", dbURL) }); err != nil {
		return e.fail("database", "写不了 "+e.hubJSON()+"："+err.Error(), "检查这个目录的权限")
	}
	cfg.DatabaseURL = dbURL
	if err := e.pingUntil(ctx, dbURL); err != nil {
		return e.fail("database", "数据库容器起来了，但连不上："+err.Error(), "运行 docker logs "+e.dbContainer+" 看原因")
	}
	if created {
		e.report("database", true, "建好了 Docker 里的 Postgres（容器 "+e.dbContainer+"，数据卷 "+e.dbVolume+"），地址写进了 hub.json", "")
	} else {
		e.report("database", true, "用已有的 Docker 容器 "+e.dbContainer+"，地址写回了 hub.json", "")
	}
	return nil
}

// pingUntil connects to the database, again for a while: the port Docker publishes may lag the server inside.
func (e *setupEnv) pingUntil(ctx context.Context, dbURL string) error {
	deadline := time.Now().Add(e.dbWait)
	for {
		err := e.pingDB(ctx, dbURL)
		if err == nil || !time.Now().Before(deadline) || ctx.Err() != nil {
			return err
		}
		e.sleep(time.Second)
	}
}

func (e *setupEnv) noDocker() error {
	if !e.portFree(5432) {
		return e.fail("database", "没有 Docker；这台电脑的 5432 端口上像是有 Postgres",
			"在它里面建用户 persona 和库 persona，把 postgres://persona:<密码>@127.0.0.1:5432/persona 写进 "+e.hubJSON()+
				" 的 database_url，再重跑（docs/server/server.md「数据库」）；或者装 Docker 后重跑")
	}
	if e.goos == "darwin" {
		return e.fail("database", "这台电脑还缺数据库", "装 Docker Desktop 或 OrbStack 后重跑这条命令")
	}
	return e.fail("database", "这台电脑还缺数据库", "装 Docker（https://docs.docker.com/engine/install/）后重跑这条命令")
}

func (e *setupEnv) startDockerHint() string {
	if e.goos == "darwin" {
		return "打开 Docker Desktop 或 OrbStack，等它启动好后重跑"
	}
	return "运行 sudo systemctl start docker，再重跑"
}

// pg is the database container setup makes (persona-postgres unless a trial run named another).
func (e *setupEnv) pg() *pgdocker.Container {
	return &pgdocker.Container{Name: e.dbContainer, Volume: e.dbVolume, Port: e.dbPort, Run: e.run, Sleep: e.sleep, Wait: e.dbWait}
}

func (e *setupEnv) containerExists(ctx context.Context) bool { return e.pg().Exists(ctx) }

// startContainer starts the container and waits until Postgres in it answers (pgdocker.Container.Start).
func (e *setupEnv) startContainer(ctx context.Context, dbURL string) error {
	return e.pg().Start(ctx, dbURL)
}

// --- running it ---------------------------------------------------------------------------------------------------

func (e *setupEnv) health(ctx context.Context, cfg *setupConfig) error {
	wait := e.healthWait
	if e.o.noService {
		wait = min(wait, 5*time.Second) // nothing was started here: it runs already, or not
	}
	deadline := time.Now().Add(wait)
	for {
		if e.hubAnswers(ctx, cfg.Config) {
			e.report("health", true, "hub 在运行，本机检查 /api/v1/health 通过", "")
			return nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			break
		}
		e.sleep(time.Second)
	}
	next := "运行 journalctl --user -u personad -n 50 看 hub 的日志"
	switch {
	case e.o.noService:
		next = "在另一个终端运行 personad hub run，起来后重跑 personad hub setup --no-service"
	case e.goos == "darwin":
		next = "看 " + filepath.Join(e.root, "hub.log") + " 里 hub 的日志"
	}
	return e.fail("health", fmt.Sprintf("%d 秒内 hub 没有在本机端口 %s 上应答", int(wait.Seconds()), portOf(cfg.Listen)), next)
}

func loopbackClient() (*http.Client, func()) {
	transport := &http.Transport{Proxy: nil}
	return &http.Client{Timeout: 5 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport.CloseIdleConnections
}

// hubAnswers: the hub this hub.json describes answers on loopback (with its token: another hub on the port does not).
func (e *setupEnv) hubAnswers(ctx context.Context, cfg *Config) bool {
	client, done := loopbackClient()
	defer done()
	for _, path := range []string{"/api/v1/health", "/api/v1/endpoints"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", portOf(cfg.Listen))+path, nil)
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
	}
	return true
}

// finish prints the way in: the phone's QR code and the admin URL with the setup code (docs/assistant/onboarding.md 4.1).
func (e *setupEnv) finish(ctx context.Context, cfg *setupConfig) error {
	p, err := cfg.requestPairCode(ctx)
	if err != nil {
		return e.fail("health", "hub 在运行，但没拿到配对码："+err.Error(), "运行 personad hub pair 再试")
	}
	code, codeExpires, err := e.setupCode(ctx, cfg.Config)
	if err != nil {
		return e.fail("health", "hub 在运行，但没拿到管理界面的设置码："+err.Error(), "运行 personad hub admin setup-code 再试")
	}
	adminURL, localOnly := e.adminURL(cfg.Config)
	if code != "" {
		adminURL += "#setup=" + url.QueryEscape(code)
	}
	expires := p.ExpiresAt
	if code != "" && codeExpires.Before(expires) {
		expires = codeExpires
	}
	if e.o.json {
		done := map[string]any{"done": true, "pair_url": p.PairURL, "admin_url": adminURL, "admin_local_only": localOnly,
			"setup_code": code, "expires_at": expires.UTC().Format(time.RFC3339)}
		b, _ := marshalNoEscape(done)
		_, _ = e.out.Write(append(b, '\n'))
		return nil
	}
	fmt.Fprintf(e.out, "\nhub 装好了。\n\n1. 用 Persona App 扫这个二维码，配对你的手机（%d 分钟内有效）：\n\n", minutesLeft(p.ExpiresAt))
	qrterminal.GenerateHalfBlock(p.PairURL, qrterminal.L, e.out)
	fmt.Fprintf(e.out, "\n   扫不了时，把这条配对链接发到手机上打开：%s\n\n", p.PairURL)
	browser := "用手机浏览器"
	if localOnly {
		browser = "在这台电脑的浏览器里"
	}
	if code != "" {
		fmt.Fprintf(e.out, "2. %s打开下面的地址，设一个管理员密码（设置码已经带在地址里，%d 分钟内有效）：\n   %s\n",
			browser, minutesLeft(codeExpires), adminURL)
	} else {
		fmt.Fprintf(e.out, "2. 管理员密码已经设过了，管理界面在：\n   %s\n", adminURL)
	}
	if localOnly {
		fmt.Fprintf(e.out, "   hub 现在只在这台电脑上监听（listen 是 %s），这个地址只能在这台电脑上打开；"+
			"要用手机打开，把 %s 里的 \"listen\" 改成 \":%s\"（让 hub 在所有地址上监听），再运行一次 personad hub setup。\n", cfg.Listen, e.hubJSON(), portOf(cfg.Listen))
	}
	fmt.Fprintln(e.out, "\n过期了就再运行一次 personad hub setup（已经装好的不会重做），或者 personad hub pair 只出配对码。")
	return nil
}

func minutesLeft(t time.Time) int { return max(1, int(time.Until(t).Round(time.Minute).Minutes())) }

// setupCode asks the running hub for a first-run setup code ("" when the admin password is set already).
func (e *setupEnv) setupCode(ctx context.Context, cfg *Config) (string, time.Time, error) {
	resp, err := adminLoopbackPost(ctx, cfg, "/api/v1/admin/setup-code") // `hub admin setup-code`'s request
	if err != nil {
		return "", time.Time{}, errors.New("连不上本机 hub")
	}
	defer resp.Body.Close()
	var body struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(raw, &body); err != nil || body.Code == "" {
			return "", time.Time{}, errors.New("hub 返回的内容无效")
		}
		return body.Code, body.ExpiresAt, nil
	case http.StatusConflict: // already_set
		return "", time.Time{}, nil
	}
	return "", time.Time{}, fmt.Errorf("HTTP %d", resp.StatusCode)
}

// adminURL is where the admin UI opens, on the plaintext port: a hub listening on loopback only answers there
// (localOnly: only a browser on this machine can open it), one bound to a single address answers on that one, and
// one on every address is opened by a phone on the LAN at this machine's LAN address.
func (e *setupEnv) adminURL(cfg *Config) (u string, localOnly bool) {
	port := portOf(cfg.Listen)
	if h, _, err := net.SplitHostPort(cfg.Listen); err == nil {
		switch ip := net.ParseIP(h); {
		case h == "localhost" || (ip != nil && ip.IsLoopback()):
			if ip == nil || ip.To4() != nil {
				h = "127.0.0.1"
			}
			return "http://" + net.JoinHostPort(h, port) + "/admin", true
		case ip != nil && !ip.IsUnspecified():
			return "http://" + net.JoinHostPort(h, port) + "/admin", false
		}
	}
	host := e.lanIPv4()
	if host == "" {
		if u, err := url.Parse(cfg.PublicURL); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
	}
	if host == "" {
		host = "<这台电脑的局域网 IP>"
	}
	return "http://" + net.JoinHostPort(host, port) + "/admin", false
}

// --- the machine --------------------------------------------------------------------------------------------------

func runCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute) // docker run may pull the image
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = time.Second
	return cmd.CombinedOutput()
}

func runOnTerminal(ctx context.Context, name string, args ...string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	return cmd.Run()
}

// askTerminal asks a yes / no question on the terminal (install.sh runs as `curl | sh`: stdin is the script).
func askTerminal(question string) (bool, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, false
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s [Y/n] ", question)
	var answer string
	_, _ = fmt.Fscanln(tty, &answer)
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		return true, true
	}
	return false, true
}

func tcpPortFree(port int) bool {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func pingPostgres(ctx context.Context, dbURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	return conn.Ping(ctx)
}

// defaultLANIPv4 is this machine's private IPv4 address on the interface of the default route (several interfaces:
// the one traffic goes out of), else the first private one on an interface that is up and not virtual.
func defaultLANIPv4() string {
	// A UDP "connect" only picks the route: nothing is sent (192.0.2.1 is TEST-NET-1).
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		ip := c.LocalAddr().(*net.UDPAddr).IP
		c.Close()
		if ip.IsPrivate() {
			return ip.String()
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || virtualInterfaceName(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
				return n.IP.String()
			}
		}
	}
	return ""
}

func virtualInterfaceName(name string) bool {
	name = strings.ToLower(name)
	for _, p := range []string{"docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "bridge", "cni", "flannel", "podman"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return vpnInterface(name)
}

func setupFirstLine(s string) string { return pgdocker.FirstLine(s) }
