// Package github is the GitHub connector: the user's PRs, issues, reviews and checks, read with the token of a
// machine's gh login (docs/plans/plan-connectors.md "4. GitHub connector").
//
// The token lives only in this process's memory: it is never stored, logged, returned to a client, put in an error
// or handed to the assistant.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Config is hub.json's "github".
type Config struct {
	Auth AuthConfig `json:"auth"`
	// Inbox: the defaults of what comes into the inbox until the user chooses in the app (rules.go).
	Inbox InboxOptions `json:"inbox,omitempty"`
	// Egress gives github.proxy: an http:// proxy for everything sent to GitHub (the device flow on its web host,
	// the REST API, CI logs), for networks that cannot reach github.com directly; empty is direct.
	connector.Egress
	// APIRoot is where the REST requests go (no trailing slash) instead of the host's own API, read when the connector is
	// made: a fake GitHub for a test in another package. It is not a setting (never read from hub.json).
	APIRoot string `json:"-"`
}

// AuthConfig: whose gh login the hub reads, and for which host. Host is github.com when empty; a GitHub Enterprise
// host goes here and only here (not built yet: only github.com is tried).
type AuthConfig struct {
	FromMachine string `json:"from_machine,omitempty"`
	Host        string `json:"host,omitempty"`
}

// Configured says whether the hub should run the connector at all.
func (c *Config) Configured() bool { return c != nil && c.Auth.FromMachine != "" }

func (c *Config) host() string {
	if c.Auth.Host == "" {
		return "github.com"
	}
	return strings.ToLower(c.Auth.Host)
}

// Why a token could not be had (TokenSource's errors, errors.Is).
var (
	ErrNoMachine   = errors.New("github.auth.from_machine is not one of the manager's machines")
	ErrOffline     = errors.New("the machine is offline")
	ErrNodeTooOld  = errors.New("the machine's personad is too old")
	ErrNoGh        = errors.New("gh is not installed")
	ErrNotSignedIn = errors.New("gh is not signed in")
	// errBadProxy: hub.json's github.proxy cannot be used; every request fails with it rather than going direct.
	errBadProxy = errors.New("github.proxy")
)

// refuse is the transport of a connector whose proxy is malformed.
type refuse struct{ err error }

func (r refuse) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }

// httpClient is the connector's client: through github.proxy when set, direct when not.
func httpClient(cfg Config) *http.Client {
	const timeout = 30 * time.Second
	cl, err := cfg.HTTPClient(timeout)
	if err != nil {
		return &http.Client{Timeout: timeout, Transport: refuse{fmt.Errorf("%w: %v", errBadProxy, err)}}
	}
	return cl
}

// TokenSource asks the machine for its gh login's token for host.
type TokenSource func(ctx context.Context, host string) (string, error)

// Connector is GitHub. Run keeps its status (and token) fresh.
type Connector struct {
	cfg     Config
	machine string // the machine's name, for messages
	fetch   TokenSource
	api     string
	client  *http.Client

	mu     sync.Mutex
	token  string
	status connector.Status
	poke   chan struct{}
	// rateLow: under a tenth of the hour's core requests are left (X-RateLimit-Remaining); only notifications are
	// polled until rateReset.
	rateLow   bool
	rateReset time.Time
	// searchLow: under a tenth of the search API's requests for this minute are left (its own quota); the archive
	// (Changes) waits until searchReset.
	searchLow   bool
	searchReset time.Time
	// pausedUntil: GitHub asked for a pause (a secondary rate limit, or Retry-After); the background reads, the
	// notifications poll included, wait until then. Only notePause sets it, and only later: a good answer in between
	// does not lift it.
	pausedUntil time.Time
	// pauseStep: the length of the last pause notePause chose itself (GitHub named no time), doubled while the limit
	// keeps coming back right after it.
	pauseStep time.Duration
	// backoffUntil: the end of that pause, kept apart from pausedUntil, which pauses GitHub names (Retry-After,
	// X-RateLimit-Reset) move too.
	backoffUntil time.Time

	inbox inboxState   // inbox.go
	opts  InboxOptions // rules.go
	etags etagCache    // thread.go: answers kept for If-None-Match, failed checks' logs, avatars

	// Where the token comes from (source.go): a computer's gh login (machineID, through fetch) or the device flow.
	source    string
	machineID string
	// scopes: what the token in use may do (X-OAuth-Scopes), known once GitHub answered with them.
	scopes      []string
	scopesKnown bool

	// The device flow's sign-in (login.go): GitHub's web host, the hub's store, what it keeps, a code waiting.
	web       string
	secrets   connector.Secrets
	dev       deviceLogin
	devLoaded bool
	pending   *devicePending
}

// noteRate records how many requests GitHub still allows, per quota (X-RateLimit-Resource): the hour's core requests
// (an answer without the header counts as core) or the search API's minute. Other quotas (graphql, code_search, ...)
// are not tracked.
func (c *Connector) noteRate(h http.Header) {
	left, err1 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	limit, err2 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	if err1 != nil || err2 != nil || limit <= 0 {
		return
	}
	reset, _ := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	low, until := left*10 < limit, time.Unix(reset, 0)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch h.Get("X-RateLimit-Resource") {
	case "", "core":
		c.rateLow, c.rateReset = low, until
	case "search":
		c.searchLow, c.searchReset = low, until
	}
}

// How long notePause pauses when GitHub names no time: at least a minute, doubling (up to pauseLongest) while the
// limit comes back right after such a pause.
var (
	pauseFirst   = time.Minute
	pauseLongest = time.Hour
)

// notePause records the pause GitHub asks for with a rate limit (429, or 403 with "rate limit", Retry-After or
// X-RateLimit-Remaining 0), as GitHub's docs say: until Retry-After (seconds or an HTTP date); else, with the hour's
// core requests used up (X-RateLimit-Remaining 0), until X-RateLimit-Reset; else for pauseFirst, doubled while the
// limit keeps coming back: within one step of the end of the last pause it chose itself (backoffUntil). A pause
// GitHub named is none of its steps, and answers in between do not end the backoff (a poll's first page is often
// answered before its next read runs into the limit again); only a quiet step does. X-RateLimit-Reset alone says
// nothing: every answer carries the core quota's, a secondary limit's too. Another quota used up (search) is no
// pause: noteRate keeps it apart. It only extends a pause, never shortens one; err is the request's error, h its
// answer's headers.
func (c *Connector) notePause(h http.Header, err error) {
	message := strings.ToLower(err.Error())
	usedUp := h.Get("X-RateLimit-Remaining") == "0"
	if !strings.Contains(message, " 429 ") && !(strings.Contains(message, " 403 ") && (strings.Contains(message, "rate limit") || h.Get("Retry-After") != "" || usedUp)) {
		return
	}
	if resource := h.Get("X-RateLimit-Resource"); usedUp && resource != "" && resource != "core" && h.Get("Retry-After") == "" {
		return
	}
	now := time.Now()
	var until time.Time
	if seconds, parseErr := strconv.Atoi(h.Get("Retry-After")); parseErr == nil && seconds > 0 {
		until = now.Add(time.Duration(seconds) * time.Second)
	} else if date, parseErr := http.ParseTime(h.Get("Retry-After")); parseErr == nil && date.After(now) {
		until = date
	} else if epoch, parseErr := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); parseErr == nil && usedUp && time.Unix(epoch, 0).After(now) {
		until = time.Unix(epoch, 0)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if until.IsZero() {
		if now.Before(c.pausedUntil) {
			return // already paused: another read ran into the same limit
		}
		step := pauseFirst
		if c.pauseStep > 0 && now.Before(c.backoffUntil.Add(c.pauseStep)) {
			step = min(2*c.pauseStep, pauseLongest)
		}
		c.pauseStep, c.backoffUntil, until = step, now.Add(step), now.Add(step)
	}
	if until.After(c.pausedUntil) {
		c.pausedUntil = until
	}
}

// pauseLeft is how long the pause GitHub asked for still lasts (notePause), zero when there is none.
func (c *Connector) pauseLeft() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return max(time.Until(c.pausedUntil), 0)
}

// limitedUntil is when the rate limits stop holding the connector back: the later of a low quota's reset and a
// pause's end, zero when neither holds at now. c.mu is held.
func (c *Connector) limitedUntil(now time.Time) time.Time {
	var until time.Time
	if c.rateLow && now.Before(c.rateReset) {
		until = c.rateReset
	}
	if now.Before(c.pausedUntil) && c.pausedUntil.After(until) {
		until = c.pausedUntil
	}
	return until
}

// ratesLow says whether to save requests (only notifications are polled).
func (c *Connector) ratesLow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.limitedUntil(time.Now()).IsZero()
}

// searchQuotaLow says whether the search API's minute is nearly used up.
func (c *Connector) searchQuotaLow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.searchLow && time.Now().Before(c.searchReset)
}

// New is the connector for cfg, reading the token of machine (its display name) through fetch.
func New(cfg Config, machine string, fetch TokenSource) *Connector {
	api := "https://api.github.com"
	if h := cfg.host(); h != "github.com" {
		api = "https://" + h + "/api/v3"
	}
	if cfg.APIRoot != "" {
		api = cfg.APIRoot
	}
	waiting, source := "正在读取 "+machine+" 上的 gh 登录…", SourceMachine
	if fetch == nil {
		waiting, source = notSignedIn, SourceDevice
	}
	return &Connector{cfg: cfg, machine: machine, machineID: cfg.Auth.FromMachine, fetch: fetch, source: source, api: api,
		web: "https://" + cfg.host(), client: httpClient(cfg), opts: cfg.Inbox,
		status: connector.Status{State: connector.StateNotSignedIn, Error: waiting}, poke: make(chan struct{}, 1)}
}

func (c *Connector) ID() string   { return "github" }
func (c *Connector) Name() string { return "GitHub" }

// Manifest: everything; a PR's or issue's thread opens as GitHub's own page (threads).
func (c *Connector) Manifest() connector.Manifest {
	return connector.Manifest{
		Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools, connector.CapActions, connector.CapEvents,
			connector.CapInbox, connector.CapSettle, connector.CapArchive, connector.CapLogin, connector.CapOptions, connector.CapThreads,
			connector.CapMachines},
		Hosts: []string{c.cfg.host()},
		Kinds: []string{"review_requested", "mention", "team_mention", "assign", "author", "ci_failed"},
	}
}

func (c *Connector) Status(context.Context) connector.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.status
	switch {
	case c.source == SourceDevice && c.dev.Token != "":
		st.Via = "设备码登录"
	case c.source == SourceMachine && c.fetch != nil:
		st.Via = "来自 " + c.machine + " 的 gh 登录"
	}
	if until := c.limitedUntil(time.Now()); st.State == connector.StateOK && !until.IsZero() {
		st.State = connector.StateRateLimited
		// minutes, not a clock time: the hub's clock is not in the user's time zone
		st.Error = fmt.Sprintf("这个小时的 GitHub 请求额度快用完了，约 %d 分钟内只看通知", int(time.Until(until).Minutes())+1)
	}
	return st
}

// signedIn: the last check found the token good (rate limits aside).
func (c *Connector) signedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status.State == connector.StateOK
}

// MachineOnline: the machine the token comes from just connected, so a token that was missing may be there now.
func (c *Connector) MachineOnline(id string) {
	c.mu.Lock()
	mine := c.source == SourceMachine && id == c.machineID
	c.mu.Unlock()
	if !mine {
		return
	}
	select {
	case c.poke <- struct{}{}:
	default:
	}
}

// How often Run checks again: when all is well, and when it is not.
var (
	checkOK    = 30 * time.Minute
	checkRetry = time.Minute
)

// Run checks the sign-in now and then until ctx ends: again soon while it fails, and when the machine connects.
func (c *Connector) Run(ctx context.Context) {
	for {
		c.check(ctx)
		wait := checkOK
		if !c.signedIn() {
			wait = checkRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-c.poke:
		case <-time.After(wait):
		}
	}
}

// check asks GitHub who the token is (fetching one first if there is none) and records the answer.
func (c *Connector) check(ctx context.Context) {
	var user struct {
		Login string `json:"login"`
	}
	h, err := c.Get(ctx, "/user", &user)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.scopes, c.scopesKnown = scopesOf(h)
		c.status = connector.Status{State: connector.StateOK, Account: user.Login, LastSync: time.Now().UTC(), LastEvent: c.status.LastEvent}
		return
	}
	c.status.State, c.status.Error = c.explain(err)
}

// notSignedIn is the status of a GitHub with neither a device sign-in nor a computer's gh login.
const notSignedIn = "还没登录 GitHub：在下面选一台电脑上的 gh 登录，或者用设备码登录"

// explain turns an error into a state and one sentence for people.
func (c *Connector) explain(err error) (string, string) {
	m := c.machine
	switch {
	case errors.Is(err, errBadProxy):
		return connector.StateNotConfigured, "manager.json 的 github.proxy 不对（要 http://[用户名:密码@]主机:端口）"
	case errors.Is(err, errUnauthorized) && c.source == SourceDevice:
		return connector.StateNotSignedIn, "设备码登录失效了（在 GitHub 上撤销了授权？）：重新登录"
	case errors.Is(err, errNoDeviceLogin):
		return connector.StateNotSignedIn, notSignedIn
	case errors.Is(err, ErrNoMachine):
		return connector.StateNotConfigured, "manager.json 的 github.auth.from_machine 不是已有的电脑"
	case errors.Is(err, ErrOffline):
		return connector.StateNotSignedIn, "GitHub 未授权（" + m + " 离线）：它连上后会自动再读"
	case errors.Is(err, ErrNodeTooOld):
		return connector.StateError, m + " 的 personad 太旧，读不了 gh 登录：更新它（scripts/deploy/deploy.sh）"
	case errors.Is(err, ErrNoGh):
		return connector.StateNotSignedIn, m + " 上没有 gh：在那台机器上装好 gh 并运行 gh auth login"
	case errors.Is(err, ErrNotSignedIn):
		return connector.StateNotSignedIn, m + " 上的 gh 没有登录 " + c.cfg.host() + "，或者登录存在钥匙串里、经 ssh 读不到：" +
			"在那台机器上运行 gh auth login --insecure-storage"
	case errors.Is(err, errUnauthorized):
		return connector.StateNotSignedIn, m + " 上的 gh 登录失效了：在那台机器上运行 gh auth login"
	}
	return connector.StateError, "连不上 GitHub：" + Scrub(err.Error())
}

var errUnauthorized = errors.New("GitHub says the token is not valid (401)")

// currentToken is the token in memory, fetching one only when there is none or (refresh) GitHub refused it: a machine
// that went offline leaves the one in memory in use.
func (c *Connector) currentToken(ctx context.Context, refresh bool) (string, error) {
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok != "" && !refresh {
		return tok, nil
	}
	c.mu.Lock()
	source, fetch := c.source, c.fetch
	c.mu.Unlock()
	if source == SourceDevice {
		d := c.deviceState(ctx)
		if d.Token == "" {
			return "", errNoDeviceLogin
		}
		c.mu.Lock()
		c.token = d.Token
		c.mu.Unlock()
		return d.Token, nil
	}
	if fetch == nil {
		return "", ErrNoMachine
	}
	fresh, err := fetch(ctx, c.cfg.host())
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.token = fresh
	c.mu.Unlock()
	return fresh, nil
}

// Get reads path of the REST API into out (JSON; a *[]byte takes the body as it is; nil drops it), refetching the
// token once if GitHub refuses it.
func (c *Connector) Get(ctx context.Context, path string, out any) (http.Header, error) {
	_, h, err := c.request(ctx, http.MethodGet, path, nil, nil, out)
	return h, err
}

// raw reads path as text (a job's log).
func (c *Connector) raw(ctx context.Context, path string) (string, error) {
	var b []byte
	_, _, err := c.request(ctx, http.MethodGet, path, nil, nil, &b)
	return string(b), err
}

// request sends one API request with the extra headers hdr and body (JSON), decoding the answer into out (as Get);
// it returns the status (304 is not an error: out is left as it is).
func (c *Connector) request(ctx context.Context, method, path string, hdr http.Header, body []byte, out any) (int, http.Header, error) {
	tok, err := c.currentToken(ctx, false)
	if err != nil {
		return 0, nil, err
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.send(ctx, method, path, hdr, body, tok)
		if errors.Is(err, errBadProxy) {
			return 0, nil, err // names the setting, not the token: nothing to scrub, and explain must see it
		}
		if err != nil {
			return 0, nil, errors.New(Scrub(err.Error()))
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			// The machine may have signed in again since: ask it once more.
			if tok, err = c.currentToken(ctx, true); err != nil {
				return 0, nil, err
			}
			continue
		}
		defer resp.Body.Close()
		c.noteRate(resp.Header)
		limit := int64(8 << 20)
		if _, text := out.(*[]byte); text {
			limit = 32 << 20 // a job's log, of which the end is kept
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, limit))
		code := resp.StatusCode
		switch {
		case code == http.StatusUnauthorized:
			return code, resp.Header, errUnauthorized
		case code == http.StatusNotModified:
			return code, resp.Header, nil
		case code >= 300:
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(raw, &e)
			err := fmt.Errorf("GitHub %s %s: %d %s", method, path, code, Scrub(e.Message))
			if code == http.StatusTooManyRequests || code == http.StatusForbidden {
				// A secondary limit or the core quota holds for the whole token: whichever read runs into it pauses them all.
				c.notePause(resp.Header, err)
			}
			return code, resp.Header, err
		}
		if readErr != nil {
			return code, resp.Header, fmt.Errorf("GitHub %s %s: read response: %s", method, path, Scrub(readErr.Error()))
		}
		if text, ok := out.(*[]byte); ok {
			*text = raw
		} else if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return code, resp.Header, fmt.Errorf("GitHub %s %s: %v", method, path, err)
			}
		}
		return code, resp.Header, nil
	}
}

func (c *Connector) send(ctx context.Context, method, path string, hdr http.Header, body []byte, tok string) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	return c.client.Do(req)
}

var tokenLike = lazyre.New(`\b(gh[opsur]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|[0-9a-f]{40})\b`)

// Scrub blanks anything that looks like a GitHub token in s (an error, a message): tokens never leave the hub.
func Scrub(s string) string { return tokenLike.ReplaceAllString(s, "[token]") }
