package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// The built-in manager's own service in `personad hub setup` (docs/assistant/managers.md "The built-in manager as its
// own service"). A new install's hub.json says assistant.mode service, so the hub never starts the built-in manager;
// an existing hub's stays as it is unless --manager-service. Setup then makes the one-time code (as `persona-hub
// manager-code` does) and runs `<root>/bin/personad manager install --builtin --join <the hub on loopback>` with the
// code in its environment: the manager's program keeps a binding that works or trades the code, and writes its
// manager.json and its own unit (persona-manager-assistant.service, or the launchd agent com.persona.manager.assistant)
// itself; the hub writes neither. Last, setup waits until the hub has it on the bus. A switch of an existing hub
// (--manager-service) is checked with the manager's program first, and undone when it cannot be finished: the hub
// starts its child again, so the built-in manager is never left with nothing to run it.

// canManagerService: setup installs services here, and so the built-in manager's own service too: a Mac, or Linux
// with a systemd user instance (without one the service step stops and says to rerun with --no-service).
func (e *setupEnv) canManagerService(ctx context.Context) bool {
	if e.o.noService || e.isInsideContainer() {
		return false
	}
	switch e.goos {
	case "darwin":
		return true
	case "linux":
		return e.userSystemd(ctx)
	}
	return false
}

// userSystemd: systemctl --user works here.
func (e *setupEnv) userSystemd(ctx context.Context) bool {
	if _, err := e.lookPath("systemctl"); err != nil {
		return false
	}
	return e.systemctl(ctx, "show-environment") == nil
}

// noManagerServiceWhy says why canManagerService is false.
func (e *setupEnv) noManagerServiceWhy(ctx context.Context) string {
	switch {
	case e.o.noService:
		return "--no-service"
	case e.isInsideContainer():
		return "在容器里"
	case e.goos == "linux" && !e.userSystemd(ctx):
		return "没有 systemd 用户服务"
	}
	return e.goos + " 上没有常驻服务"
}

// freshManagerMode is a new install's assistant.mode: service when setup installs the built-in manager's service
// (service true), else the default (child). A mode already in the file stays, but for --manager-service (force),
// which asks for service over a child written there. assistant.external is left alone: the built-in manager runs
// elsewhere then.
func freshManagerMode(o *jsonObject, service, force bool) error {
	a, err := o.Object("assistant")
	if err != nil {
		return err
	}
	if a == nil {
		a = &jsonObject{}
	}
	var external bool
	if raw, ok := a.Get("external"); ok && json.Unmarshal(raw, &external) == nil && external {
		return nil
	}
	var mode string
	if raw, ok := a.Get("mode"); ok {
		_ = json.Unmarshal(raw, &mode)
	}
	if !service || mode == AssistantModeService || (mode != "" && !force) {
		return nil
	}
	if err := a.Set("mode", AssistantModeService); err != nil {
		return err
	}
	o.SetObject("assistant", a)
	return nil
}

// toManagerService sets an existing hub's assistant.mode to service (--manager-service); changed false when it was.
// Before it changes anything, the manager's program that will run the service says it can be installed here (an
// older one does not know how: the hub would be left with no built-in manager).
func (e *setupEnv) toManagerService(ctx context.Context) (changed bool, err error) {
	var mode string
	if raw, err := readHubJSON(e.root); err == nil {
		if a, err := raw.Object("assistant"); err == nil && a != nil {
			if m, ok := a.Get("mode"); ok {
				_ = json.Unmarshal(m, &mode)
			}
		}
	}
	// The binary the service step will run: personad, or an older layout's compat.DaemonBinary it links personad to.
	bin := filepath.Join(e.root, "bin", "personad")
	if _, err := os.Stat(bin); err != nil {
		bin = filepath.Join(e.root, "bin", compat.DaemonBinary)
	}
	if _, statErr := os.Stat(bin); statErr == nil && mode != AssistantModeService {
		if out, err := e.run(ctx, []string{"PERSONA_HOME=" + e.root}, bin, managerconf.BuiltinCheckArgs()...); err != nil {
			why := lastLine(string(out))
			if why == "" {
				why = err.Error()
			}
			return false, e.fail("config", "装不了内置管理者的常驻服务，什么都没改："+why,
				"按这句处理；"+bin+" 太旧时先升级它（重跑安装命令加 --hub），再重跑 personad hub setup --manager-service")
		}
	}
	err = editHubJSON(e.root, func(o *jsonObject) error {
		a, err := o.Object("assistant")
		if err != nil {
			return err
		}
		if a == nil {
			a = &jsonObject{}
		}
		var external bool
		if raw, ok := a.Get("external"); ok && json.Unmarshal(raw, &external) == nil && external {
			return errManagerExternal
		}
		var mode string
		if raw, ok := a.Get("mode"); ok {
			_ = json.Unmarshal(raw, &mode)
		}
		if mode == AssistantModeService {
			return nil
		}
		changed, e.prevMode = true, mode
		if err := a.Set("mode", AssistantModeService); err != nil {
			return err
		}
		o.SetObject("assistant", a)
		return nil
	})
	switch {
	case errors.Is(err, errManagerExternal):
		return false, e.fail("config", "hub.json 的 assistant.external 是 true：内置管理者在别处运行，--manager-service 不动它",
			"要在这台装它的常驻服务，先去掉 "+e.hubJSON()+" 里的 assistant.external，再重跑")
	case err != nil:
		return false, e.fail("config", "写不了 "+e.hubJSON()+"："+err.Error(), "检查这个目录的权限")
	}
	e.switched = changed
	return changed, nil
}

// undoSwitch puts assistant.mode back as it was before this run's --manager-service, takes away the built-in
// manager's service as far as it got (its binding stays; the hub's child gets a token of its own), and restarts the
// hub so that it starts its child again. It says what it did.
func (e *setupEnv) undoSwitch(ctx context.Context, bin string) string {
	err := editHubJSON(e.root, func(o *jsonObject) error {
		a, err := o.Object("assistant")
		if err != nil || a == nil {
			return err
		}
		if e.prevMode == "" {
			a.Del("mode")
		} else if err := a.Set("mode", e.prevMode); err != nil {
			return err
		}
		o.SetObject("assistant", a)
		return nil
	})
	if err != nil {
		return "assistant.mode 没改回去（" + err.Error() + "）：运行 personad hub manager-mode child，再重启 hub 的服务"
	}
	e.switched = false
	_, _ = e.run(ctx, []string{"PERSONA_HOME=" + e.root}, bin, "manager", "uninstall", "--builtin")
	if e.hubRestart == nil {
		return "assistant.mode 改回去了，hub 照旧自己拉起内置管理者"
	}
	if err := e.hubRestart(ctx); err != nil {
		return "assistant.mode 改回去了，但重启 hub 没成功（" + err.Error() + "）：重启 hub 的服务后它才自己拉起内置管理者"
	}
	return "改回了 hub 自己拉起内置管理者，重启了 hub"
}

var errManagerExternal = errors.New("assistant.external")

// managerLog is where the built-in manager's service logs, for the lines setup prints.
func (e *setupEnv) managerLog() string {
	if e.goos == "darwin" {
		return "看 " + filepath.Join(managerconf.DefaultDir(e.root), "manager.log") + " 里内置管理者的日志"
	}
	return "运行 journalctl --user -u " + managerconf.BuiltinService + " -n 50 看内置管理者的日志"
}

// builtinManager is setup's manager step.
func (e *setupEnv) builtinManager(ctx context.Context, cfg *setupConfig) error {
	a := cfg.Assistant
	bin := filepath.Join(e.root, "bin", "personad")
	switch {
	case a.External:
		e.report("manager", true, "hub.json 的 assistant.external：内置管理者在别处运行，没动它", "")
		return nil
	case a.StartsBuiltin():
		next := ""
		if e.canManagerService(ctx) {
			next = "要把它改成自己的常驻服务（hub 重启、升级时它不跟着重启）：personad hub setup --manager-service"
		}
		e.report("manager", true, "hub 自己拉起内置管理者（assistant.mode child）", next)
		return nil
	case !a.ServiceBound():
		e.report("manager", false, "hub.json 的 assistant.mode 是 "+a.Mode+"：hub 不认识，内置管理者没有运行",
			"把 "+e.hubJSON()+" 里的 assistant.mode 改成 service（或者删掉，让 hub 自己拉起它），再重跑")
		return nil
	case !e.canManagerService(ctx) && e.builtinOnline(ctx, cfg.Config):
		e.report("manager", true, "内置管理者是自己的服务（assistant.mode service），在运行；这次没动它（"+e.noManagerServiceWhy(ctx)+"）", "")
		return nil
	case !e.canManagerService(ctx):
		// Nothing runs the built-in manager here unless the user does: a problem, with both ways out.
		e.report("manager", false, "内置管理者是自己的服务（assistant.mode service），这次没装它（"+e.noManagerServiceWhy(ctx)+"），现在没有东西运行它",
			"要么自己让它一直运行：personad hub manager-code 拿一个码，PERSONA_MANAGER_CODE=<码> personad manager join --builtin "+
				hubLocalURL(cfg.Listen)+" 绑定，再一直运行 PERSONA_ASSISTANT_DEPLOY=service "+bin+" assistant；"+
				"要么 personad hub manager-mode child 后重启 hub，让 hub 自己拉起它")
		return nil
	}
	retry := "按这句处理后重跑 personad hub setup"
	if e.switched {
		retry += " --manager-service"
	} else {
		// (a switch an earlier run made is not known here: the way back is said every time)
		retry += "；在那之前要用内置管理者：personad hub manager-mode child，再重启 hub 的服务，让 hub 自己拉起它"
	}
	code, err := e.builtinCode(ctx, cfg.Config)
	if err != nil {
		detail := "没拿到绑定内置管理者的码：" + err.Error()
		if e.switched {
			detail += "；" + e.undoSwitch(ctx, bin)
		}
		return e.fail("manager", detail, retry)
	}
	// The code goes in the environment, never on the command line: any user here can read that while it runs, and
	// the hub trades this code from its own machine without another credential. One the program did not trade (it
	// kept a binding that works, or stopped first) is voided after.
	programBefore, wasActive := e.builtinProgram(), e.builtinActive(ctx)
	out, err := e.run(ctx, []string{"PERSONA_HOME=" + e.root, managerconf.BuiltinCodeEnv + "=" + code},
		bin, managerconf.BuiltinInstallArgs(hubLocalURL(cfg.Listen))...)
	_ = e.voidCode(ctx, cfg.Config, code)
	if err != nil {
		why := lastLine(string(out))
		if why == "" {
			why = err.Error()
		}
		detail := "内置管理者的常驻服务没装好：" + why
		if e.switched {
			detail += "；" + e.undoSwitch(ctx, bin)
		}
		return e.fail("manager", detail, retry)
	}
	unit := managerconf.BuiltinService
	if e.goos == "darwin" {
		unit = managerconf.BuiltinLabel
	}
	switched := ""
	if after := e.builtinProgram(); programBefore != "" && after != "" && resolvedPath(programBefore) != resolvedPath(after) {
		switched = "；服务改成运行 " + filepath.Base(after)
		if wasActive {
			switched += "，重启了它"
		}
	}
	wait := e.managerWait
	deadline := time.Now().Add(wait)
	for {
		if e.builtinOnline(ctx, cfg.Config) {
			e.report("manager", true, "内置管理者是自己的常驻服务（"+unit+"），已绑定这个 hub、连上了"+switched, "")
			return nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			break
		}
		e.sleep(time.Second)
	}
	// The hub runs and the phone can pair: say what is wrong and go on (its service keeps trying).
	// (said every time: a switch an earlier run made is not known here)
	next := e.managerLog() + "；要改回 hub 自己拉起它：personad hub manager-mode child，再重启 hub 的服务"
	e.report("manager", false, fmt.Sprintf("内置管理者的常驻服务（%s）装好了，但 %d 秒内没连上 hub%s", unit, int(wait.Seconds()), switched), next)
	return nil
}

// builtinProgram is the program the built-in manager's service runs, under its name or its former one: its unit as
// the user's systemd reads it, or its launchd agent; "" for none.
func (e *setupEnv) builtinProgram() string {
	id := assistantwire.AssistantID
	if e.goos == "darwin" {
		for _, label := range []string{managerconf.BuiltinLabel, compat.LegacyManagerLabel(id)} {
			if job, ok := readLaunchdJob(filepath.Join(e.home, "Library", "LaunchAgents", label+".plist")); ok && len(job.args) > 0 {
				return homePath(job.args[0], e.home)
			}
		}
		return ""
	}
	for _, unit := range []string{managerconf.BuiltinService + ".service", compat.LegacyManagerUnit(id)} {
		if p := e.unitProgram(unit); p != "" {
			return p
		}
	}
	return ""
}

// builtinActive says the built-in manager's service runs, under its name or its former one (on a Mac: launchd has the
// agent).
func (e *setupEnv) builtinActive(ctx context.Context) bool {
	id := assistantwire.AssistantID
	if e.goos == "darwin" {
		for _, label := range []string{managerconf.BuiltinLabel, compat.LegacyManagerLabel(id)} {
			if _, err := e.run(ctx, nil, "launchctl", "print", "gui/"+strconv.Itoa(e.uid)+"/"+label); err == nil {
				return true
			}
		}
		return false
	}
	for _, unit := range []string{managerconf.BuiltinService + ".service", compat.LegacyManagerUnit(id)} {
		if _, err := e.run(ctx, nil, "systemctl", "--user", "is-active", "--quiet", unit); err == nil {
			return true
		}
	}
	return false
}

// builtinOnline: the hub has the built-in manager on its bus as a service of its own (GET /api/v1/managers on loopback).
func (e *setupEnv) builtinOnline(ctx context.Context, cfg *Config) bool {
	client, done := loopbackClient()
	defer done()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", portOf(cfg.Listen))+"/api/v1/managers", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Managers []managerInfo `json:"managers"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return false
	}
	for _, m := range body.Managers {
		if m.ID == assistantwire.AssistantID { // its hello's deploy is "service" then (PERSONA_ASSISTANT_DEPLOY)
			return m.Online && m.Deploy == AssistantModeService
		}
	}
	return false
}

// builtinCode makes a one-time code (10 minutes) that binds the built-in manager's own service to the hub of cfg, in
// its database: `persona-hub manager-code` and setup.
func builtinCode(ctx context.Context, cfg *Config) (string, time.Time, error) {
	if cfg.DatabaseURL == "" {
		return "", time.Time{}, errors.New("hub.json has no database_url")
	}
	database, err := db.Open(ctx, cfg.DatabaseURL, db.HubSet)
	if err != nil {
		return "", time.Time{}, err
	}
	defer database.Close()
	h := &Hub{cfg: cfg, store: database, logger: log.New(io.Discard, "", 0)}
	return h.newInstallCode(ctx, assistantwire.AssistantID, "助手") // i18n-ignore: its identity's name, as assistant-token gives it
}

// voidBuiltinCode voids the built-in manager's code when it is still good (not traded): ExpireManagerInstallCode.
func voidBuiltinCode(ctx context.Context, cfg *Config, code string) error {
	if cfg.DatabaseURL == "" || code == "" {
		return nil
	}
	database, err := db.Open(ctx, cfg.DatabaseURL, db.HubSet)
	if err != nil {
		return err
	}
	defer database.Close()
	_, err = database.ExpireManagerInstallCode(ctx, hashToken(code))
	return err
}

// lastLine is the last line of s with text.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
