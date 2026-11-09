package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
)

// `personad hub admin …` on the hub's machine (docs/operations/admin.md): widen or set the admin UI's scope, reset its password.

const adminUsage = `usage:
  personad hub admin access                        show who may open the admin UI
  personad hub admin access --get                  print hub.json's admin.access (empty when not chosen yet)
  personad hub admin access <loopback|lan|cidrs|any> [--cidrs a,b]
                                                   set it (the only way to widen it; the web page only narrows)
  personad hub admin reset-password [--clear]      set a new administrator password (or clear it: first-run
                                                   setup again); every signed-in browser is signed out
  personad hub admin setup-code                    a one-time code for first-run setup from this machine (asks the
                                                   running hub; valid 10 minutes)`

// accessChoices explains each scope; admin_access_help in scripts/deploy/install.sh prints the same.
var accessChoices = []struct{ name, meaning string }{
	{accessLoopback, "只有 hub 这台机器自己（Docker 里是容器自己）能打开；别的电脑要用 ssh 隧道"},
	{accessLAN, "本机 + 和 hub 同一网段的私网地址（+ security.trusted_cidrs）。默认，适合家里 / 办公室的局域网"},
	{accessCIDRs, "本机 + 你列出的网段（例如 VPN 的网段）；公网网段也只能经 TLS"},
	{accessAny, "任何地方经 TLS 端口都能打开（局域网照旧）。暴露在公网上，只靠管理员密码和登录限流挡着"},
}

// AdminCommand runs `personad hub admin <args>`.
func AdminCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(adminUsage)
	}
	switch args[0] {
	case "access":
		return adminAccessCommand(ctx, dataDir(), args[1:], stdout, stderr)
	case "reset-password":
		return adminResetPassword(ctx, dataDir(), args[1:], stdin, stdout, stderr)
	case "setup-code":
		if len(args) != 1 {
			return errors.New(adminUsage)
		}
		return adminSetupCode(ctx, dataDir(), stdout)
	}
	return fmt.Errorf("unknown admin command %q\n%s", args[0], adminUsage)
}

func adminAccessCommand(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("hub admin access", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cidrsFlag := flags.String("cidrs", "", "networks for access cidrs, comma separated (192.0.2.0/24,2001:db8::/48)")
	get := flags.Bool("get", false, "print hub.json's admin.access only")
	// Flags may come after the scope: `access cidrs --cidrs …`.
	var positional []string
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			return err
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	o, err := readHubJSON(dir)
	if err != nil {
		return err
	}
	var current AdminConfig
	if raw, ok := o.Get("admin"); ok {
		_ = json.Unmarshal(raw, &current)
	}
	if *get {
		fmt.Fprintln(stdout, current.Access)
		return nil
	}
	if len(positional) == 0 {
		shown := current.Access
		if shown == "" {
			shown = accessLAN + "（hub.json 没写，用默认值）"
		}
		fmt.Fprintf(stdout, "hub.json 的 admin.access：%s\n", shown)
		if len(current.CIDRs) > 0 {
			fmt.Fprintf(stdout, "admin.cidrs：%s\n", strings.Join(current.CIDRs, ", "))
		}
		if env := adminEnv(); env != nil {
			fmt.Fprintf(stdout, "环境变量 PERSONA_ADMIN_ACCESS=%s 优先于 hub.json\n", env.Access)
		}
		fmt.Fprintln(stdout, "\n可选：")
		for _, c := range accessChoices {
			fmt.Fprintf(stdout, "  %-8s %s\n", c.name, c.meaning)
		}
		return nil
	}
	if len(positional) > 1 {
		return errors.New(adminUsage)
	}
	access := positional[0]
	if !validAccess(access) || access == "" {
		return fmt.Errorf("范围只能是 loopback、lan、cidrs、any，不是 %q", access)
	}
	cidrs := splitList(*cidrsFlag)
	if access == accessCIDRs {
		prefixes, err := parseCIDRs(cidrs)
		if err != nil {
			return err
		}
		if len(prefixes) == 0 {
			return errors.New("cidrs 要用 --cidrs 给出至少一个网段，例如 --cidrs 192.0.2.0/24")
		}
		cidrs = cidrs[:0]
		for _, p := range prefixes {
			cidrs = append(cidrs, p.String())
		}
	} else if len(cidrs) > 0 {
		return errors.New("--cidrs 只和 cidrs 一起用")
	}
	err = editHubJSON(dir, func(o *jsonObject) error {
		admin, err := o.Object("admin")
		if err != nil {
			return err
		}
		if err := admin.Set("access", access); err != nil {
			return err
		}
		if access == accessCIDRs {
			if err := admin.Set("cidrs", cidrs); err != nil {
				return err
			}
		}
		o.SetObject("admin", admin)
		return nil
	})
	if err != nil {
		return err
	}
	for _, c := range accessChoices {
		if c.name == access {
			fmt.Fprintf(stdout, "管理界面的访问范围已设为 %s：%s\n", access, c.meaning)
		}
	}
	if env := adminEnv(); env != nil {
		fmt.Fprintf(stderr, "注意：环境变量 PERSONA_ADMIN_ACCESS=%s 优先于 hub.json，改它才会生效\n", env.Access)
	}
	cfg := &Config{Listen: ":18660", dir: dir}
	if b, err := os.ReadFile(dir + "/hub.json"); err == nil {
		_ = json.Unmarshal(b, cfg)
	}
	cfg.applyEnv()
	if access == accessAny && cfg.Public.TLSListen == "" {
		fmt.Fprintln(stderr, "注意：hub 没开 TLS（public.tls_listen），any 实际只放局域网进来；开了 TLS 重启 hub 后才对外")
	}
	switch err := notifyAdminReload(ctx, cfg); {
	case err == nil:
		fmt.Fprintln(stdout, "正在运行的 hub 已经按新范围生效。")
	case errors.Is(err, errHubNotRunning):
		fmt.Fprintln(stdout, "hub 没在运行：下次启动时生效。")
	default:
		fmt.Fprintf(stderr, "没能通知正在运行的 hub（%v）：重启 hub 后生效。\n", err)
	}
	return nil
}

var errHubNotRunning = errors.New("hub not running")

// adminSetupCode prints a fresh first-run setup code from the running hub.
func adminSetupCode(ctx context.Context, dir string, stdout io.Writer) error {
	cfg := &Config{Listen: ":18660", dir: dir}
	b, err := os.ReadFile(dir + "/hub.json")
	if err != nil {
		return fmt.Errorf("这台机器上没有 hub 的配置（%s/hub.json）：%w", dir, err)
	}
	_ = json.Unmarshal(b, cfg)
	cfg.applyEnv()
	resp, err := adminLoopbackPost(ctx, cfg, "/api/v1/admin/setup-code")
	if errors.Is(err, errHubNotRunning) {
		return errors.New("hub 没在运行：先启动 hub（设置码只在运行中的 hub 里有效）")
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(raw, &out)
	switch {
	case resp.StatusCode == http.StatusConflict:
		return errors.New("管理员密码已经设过了：直接登录，忘了就 personad hub admin reset-password")
	case resp.StatusCode != http.StatusOK || out.Code == "":
		return fmt.Errorf("hub 没给设置码（HTTP %d）：%s", resp.StatusCode, apierr.Describe(raw)) // with the cause in detail (#5728)
	}
	fmt.Fprintf(stdout, "首次设置码：%s\n10 分钟内有效（到 %s），只能用一次；在 hub 机器上打开管理界面设置密码时填它。\n",
		out.Code, out.ExpiresAt.UTC().Format(time.RFC3339))
	return nil
}

// notifyAdminReload asks the hub on this machine to read admin.access again (POST /api/v1/admin/reload over
// loopback with the master token, like `hub pair`).
func notifyAdminReload(ctx context.Context, cfg *Config) error {
	resp, err := adminLoopbackPost(ctx, cfg, "/api/v1/admin/reload")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d (an older hub without the admin UI?)", resp.StatusCode)
	}
	return nil
}

// adminLoopbackPost posts {} to the hub on this machine with the master token (errHubNotRunning when nothing answers).
func adminLoopbackPost(ctx context.Context, cfg *Config, path string) (*http.Response, error) {
	if cfg.Token == "" {
		return nil, errHubNotRunning
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n <= 0 || n > 65535 {
		return nil, errors.New("hub.json listen has no valid port")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+net.JoinHostPort("127.0.0.1", port)+path, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) {
			return nil, errHubNotRunning
		}
		return nil, err
	}
	return resp, nil
}

func adminResetPassword(ctx context.Context, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("hub admin reset-password", flag.ContinueOnError)
	flags.SetOutput(stderr)
	clear := flags.Bool("clear", false, "clear the password: the next visit sets a new one (first-run setup)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(adminUsage)
	}
	b, err := os.ReadFile(dir + "/hub.json")
	if err != nil {
		return fmt.Errorf("这台机器上没有 hub 的配置（%s/hub.json）：%w", dir, err)
	}
	cfg := &Config{}
	if err := json.Unmarshal(b, cfg); err != nil {
		return fmt.Errorf("hub.json: %w", err)
	}
	cfg.applyEnv()
	var password string
	if !*clear {
		if password, err = readNewPassword(stdin, stderr); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	database, err := db.Open(ctx, cfg.DatabaseURL, db.HubSet)
	if err != nil {
		return err
	}
	defer database.Close()
	if *clear {
		if err := database.ClearAdminPassword(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "管理员密码已清除，所有已登录的浏览器都已退出；下次在访问范围内打开 /admin 时重新设置。")
		return nil
	}
	hash, err := hashAdminPassword(password)
	if err != nil {
		return err
	}
	if err := database.SetAdminPasswordSignOut(ctx, hash, ""); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "管理员密码已更新，所有已登录的浏览器都已退出。")
	return nil
}

// readNewPassword asks twice without echo on a terminal; otherwise it reads one line from stdin (a script).
func readNewPassword(stdin io.Reader, stderr io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stderr, "新的管理员密码：")
		first, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(stderr, "再输一遍：")
		second, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", errors.New("两次输入的不一样，没有改")
		}
		if problem := adminPasswordProblem(string(first)); problem != nil {
			return "", problem
		}
		return string(first), nil
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	password := strings.TrimRight(line, "\r\n")
	if problem := adminPasswordProblem(password); problem != nil {
		return "", problem
	}
	return password, nil
}
