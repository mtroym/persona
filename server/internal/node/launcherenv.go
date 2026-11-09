package node

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// A provider's environment for claude (#2455, docs/integrations/launchers.md): one pure function both ways of running the main
// conversation and every Agent use, the CLI (its --settings env, `personad launch`'s env) and the Agent SDK bridge (the
// child's env and settings.env). Cindy's env-builder, the Anthropic part.

// legacyEnvClear: what `personad launch` has always dropped from the shell (another launcher or account) before a
// managed launcher from before #2455 runs. A trailing "*" is a prefix.
var legacyEnvClear = []string{"ANTHROPIC_*", "CLAUDE_CODE_USE_BEDROCK*", "CLAUDE_CODE_USE_VERTEX*",
	"CLAUDE_CODE_USE_FOUNDRY*", "CLAUDE_CODE_OAUTH_TOKEN*", "CLAUDE_CODE_SUBAGENT_MODEL*"}

// providerEnvClear: what claude must not inherit before a provider's own env is set: legacyEnvClear and every other
// variable providerEnv may set but the proxy: the shell's stays (a gateway reachable only through it) unless the
// provider has its own (proxyEnvClear).
var providerEnvClear = append(slices.Clone(legacyEnvClear), "CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_CODE_MAX_CONTEXT_TOKENS",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "API_TIMEOUT_MS")

// proxyEnvClear: the shell's proxy, dropped when the provider sets its own.
var proxyEnvClear = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}

// envClearFor is what to drop before l runs: a launcher from before #2455 keeps the shell's proxy and timeout, as it
// always has; a provider keeps the shell's proxy unless it has one (a dead one is dropped at launch,
// withoutDeadProxies). Its bypass list replaces the shell's in both spellings (`personad launch` adds the inherited
// ones to it first when it follows the shell's proxy, mergesNoProxy). What the launcher's own environment sets (Env)
// goes too, a proxy variable in both spellings: its value is the one that counts.
func envClearFor(l proto.ManagedLauncher) []string {
	var clear []string
	switch {
	case !providerShaped(l):
		clear = slices.Clone(legacyEnvClear)
	case l.Proxy != "":
		clear = append(slices.Clone(providerEnvClear), proxyEnvClear...)
	case l.NoProxy != "":
		clear = append(slices.Clone(providerEnvClear), "NO_PROXY", "no_proxy")
	default:
		clear = slices.Clone(providerEnvClear)
	}
	for _, kv := range launcherEnvVars(l.Env) {
		for _, k := range append([]string{kv[0]}, otherSpelling(kv[0])...) {
			if !slices.Contains(clear, k) {
				clear = append(clear, k)
			}
		}
	}
	return clear
}

// proxyNames are the proxy variables Claude Code (Node's fetch) reads in either spelling, the lower-case one first; an
// empty value falls through to the other one.
var proxyNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}

// mergesNoProxy says whether l's bypass list is added to the ones its claude would otherwise read (#6238): the user's
// settings env's, else the shell's at launch or the node's for the main conversation (withInheritedNoProxy). Only for a
// provider that follows the
// computer's proxy, whose list goes with it. One with a proxy of its own brings its whole list: the computer's is for
// the computer's proxy, and its "*" or the gateway in it would turn the provider's off. Nor when the provider's own
// environment sets the list or a proxy (it is set after everything else), or the list is "*" (no proxy at all).
func mergesNoProxy(l proto.ManagedLauncher) bool {
	if l.Proxy != "" || l.NoProxy == "" || l.NoProxy == "*" {
		return false
	}
	return !slices.ContainsFunc(launcherEnvVars(l.Env), func(kv [2]string) bool {
		return slices.Contains([]string{"NO_PROXY", "HTTP_PROXY", "HTTPS_PROXY"}, strings.ToUpper(kv[0]))
	})
}

// withInheritedNoProxy is env (KEY=value entries) with the bypass lists lookup gives added to the provider's
// (addNoProxy), for a launcher that mergesNoProxy: the provider's hosts go direct, and the inherited ones keep going
// direct for the commands Claude runs (#6238).
func withInheritedNoProxy(env []string, lookup func(string) string) []string {
	upper, lower := addNoProxy(envValue(env, "NO_PROXY"), lookup)
	out := slices.Clone(env)
	for i, kv := range out {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "NO_PROXY":
			out[i] = k + "=" + upper
		case "no_proxy":
			out[i] = k + "=" + lower
		}
	}
	return out
}

// addNoProxy is ours (a provider's bypass list) added to each spelling's list that lookup gives, else to the other
// spelling's, so a tool that reads one spelling first keeps the hosts it had (Go NO_PROXY, curl and the runtime under
// Claude Code no_proxy): NO_PROXY's, then no_proxy's.
func addNoProxy(ours string, lookup func(string) string) (upper, lower string) {
	u, l := lookup("NO_PROXY"), lookup("no_proxy")
	return mergeNoProxy(ours, cmp.Or(u, l)), mergeNoProxy(ours, cmp.Or(l, u))
}

// mergeNoProxy is two bypass lists as one, each host once (the first's order first); "*" in either is all of them.
func mergeNoProxy(a, b string) string {
	var out []string
	seen := map[string]bool{}
	for _, h := range strings.FieldsFunc(a+","+b, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if h == "*" {
			return "*"
		}
		if k := strings.ToLower(h); !seen[k] {
			seen[k] = true
			out = append(out, h)
		}
	}
	return strings.Join(out, ",")
}

// otherSpelling is k's other spelling when k is one of proxyNames in either case, none otherwise.
func otherSpelling(k string) []string {
	for _, p := range proxyNames {
		switch k {
		case p:
			return []string{strings.ToLower(p)}
		case strings.ToLower(p):
			return []string{p}
		}
	}
	return nil
}

// launcherEnvVars is a launcher's own environment (Env), in order: one NAME=value a line, "export " before it is fine,
// a line starting with # is a comment. A value in quotes is what is inside them; one without loses a trailing " # …"
// comment, as in a shell. Lines that are no assignment are skipped: the hub refuses them (checkEnv, the same rules).
func launcherEnvVars(raw string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := envLine(line)
		if ok && k != "" {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

// envLine reads one line of a launcher's environment: k "" (and ok) for a blank line or a comment, ok false for a line
// that is no assignment.
func envLine(line string) (k, v string, ok bool) {
	line = strings.TrimFunc(line, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", true
	}
	line = exportRe.ReplaceAllString(line, "")
	k, v, ok = strings.Cut(line, "=")
	k = strings.TrimSpace(k)
	if !ok || !envNameRe.MatchString(k) {
		return "", "", false
	}
	rest := strings.TrimLeft(v, " \t")
	if rest != v && strings.HasPrefix(rest, "#") {
		return k, "", true // `A= # a note`: empty, as in a shell
	}
	v = rest
	if v != "" && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return k, v[1 : end+1], true
		}
	}
	for _, sep := range []string{" #", "\t#"} { // a comment after the value
		if i := strings.Index(v, sep); i >= 0 {
			v = v[:i]
		}
	}
	return k, strings.TrimSpace(v), true
}

var envNameRe = lazyre.New(`^[A-Za-z_][A-Za-z0-9_]*$`)

var exportRe = lazyre.New(`^export\s+`)

// withoutEnv is environ (KEY=value entries) without the variables clear names.
func withoutEnv(environ, clear []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		for _, c := range clear {
			if p, ok := strings.CutSuffix(c, "*"); ok && strings.HasPrefix(key, p) || key == c {
				return true
			}
		}
		return false
	})
}

// withoutProviderEnv is environ without what providerEnvClear names (the Agent SDK bridge's env).
func withoutProviderEnv(environ []string) []string { return withoutEnv(environ, providerEnvClear) }

const (
	// oneMillionSuffix: Claude Code runs a model with a 1M context window only when its name says so.
	oneMillionSuffix = "[1m]"
	// standardWindow: the window Claude Code assumes for a model, and the main conversation's compact window.
	standardWindow = 200000
	// gatewayTimeoutMS: API_TIMEOUT_MS for a gateway that sets none (slow third-party gateways, cindy's default).
	gatewayTimeoutMS = 3000000
)

// providerModel is what l knows of model m ("<id>" or "<id>[1m]").
func providerModel(l proto.ManagedLauncher, m string) (proto.ProviderModel, bool) {
	id := strings.TrimSuffix(m, oneMillionSuffix)
	for _, pm := range l.Models {
		if pm.ID == m || pm.ID == id {
			return pm, true
		}
	}
	return proto.ProviderModel{}, false
}

// sdkModel is the name Claude Code runs m (pm what is known of it) as: with [1m] for a window over 200K.
func sdkModel(m string, pm proto.ProviderModel) string {
	if pm.ContextWindow > standardWindow && !strings.HasSuffix(m, oneMillionSuffix) {
		return m + oneMillionSuffix
	}
	return m
}

// providerShaped: a provider as #2455 models it (an API provider, or any of its fields set), which gets a gateway's
// extras (no nonessential traffic, a long timeout). A launcher from before gets exactly the env it always had.
func providerShaped(l proto.ManagedLauncher) bool {
	return l.Kind == "api" || len(l.Models) > 0 || l.OpusModel != "" || l.SonnetModel != "" || l.HaikuModel != "" ||
		l.FableModel != "" || l.SubagentForce || l.Headers != "" || l.HasHeaders || l.Proxy != "" || l.NoProxy != "" ||
		l.Env != "" || l.HasEnv ||
		l.TimeoutMS != 0 || l.CompactWindow != 0 || l.MaxContext != 0 || l.Effort != "" || l.Source != ""
}

// providerEnv is the environment claude runs provider l with, for this model and effort ("" for the provider's own)
// and the main conversation or not. A "" value means set empty: the variable must not come from anywhere else (the
// other credential of the two, which a gateway seeing both refuses with 401). What the shell carries is cleared
// first (providerEnvClear); settings env cannot unset, so a value here is the only way to override one there.
//
// Kept as it was for a launcher from before #2455 (managedEnvOrder): the gateway URL always (empty for none); no key
// stored (or the hub could not open it), neither credential, so Claude uses whatever sign-in it has; no model, none of
// the model variables. The Haiku tier is HaikuModel, else SmallModel (what it was), else the model.
func providerEnv(l proto.ManagedLauncher, model, effort string, mainConversation bool) map[string]string {
	env := map[string]string{"ANTHROPIC_BASE_URL": l.BaseURL}
	if l.BaseURL != "" && providerShaped(l) {
		env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
		env["API_TIMEOUT_MS"] = strconv.Itoa(cmp.Or(l.TimeoutMS, gatewayTimeoutMS))
	} else if l.TimeoutMS > 0 {
		env["API_TIMEOUT_MS"] = strconv.Itoa(l.TimeoutMS)
	}
	switch {
	case l.APIKey == "":
	case l.BearerAuth():
		env["ANTHROPIC_AUTH_TOKEN"], env["ANTHROPIC_API_KEY"] = strings.TrimPrefix(l.APIKey, "Bearer "), ""
	default:
		env["ANTHROPIC_API_KEY"], env["ANTHROPIC_AUTH_TOKEN"] = l.APIKey, ""
	}
	set := func(k, v string) {
		if v != "" {
			env[k] = v
		}
	}
	m := cmp.Or(model, l.Model)
	pm, _ := providerModel(l, m)
	run := sdkModel(m, pm)
	set("ANTHROPIC_MODEL", run)
	set("ANTHROPIC_DEFAULT_OPUS_MODEL", cmp.Or(l.OpusModel, run))
	set("ANTHROPIC_DEFAULT_SONNET_MODEL", cmp.Or(l.SonnetModel, run))
	set("ANTHROPIC_DEFAULT_FABLE_MODEL", cmp.Or(l.FableModel, run))
	set("ANTHROPIC_DEFAULT_HAIKU_MODEL", cmp.Or(l.HaikuModel, l.SmallModel, run))
	set("ANTHROPIC_SMALL_FAST_MODEL", cmp.Or(l.SmallModel, run))
	// Subagents follow the model unless the provider says otherwise (workflows, multi-agent runs).
	set("CLAUDE_CODE_SUBAGENT_MODEL", cmp.Or(l.SubagentModel, run))
	if l.SubagentForce {
		env["CLAUDE_CODE_SUBAGENT_MODEL_FORCE"] = "1"
	}
	w := pm.ContextWindow
	if l.MaxContext > 0 {
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = strconv.Itoa(l.MaxContext)
	} else if w > standardWindow {
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = strconv.Itoa(w)
	}
	if mainConversation {
		env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = strconv.Itoa(standardWindow)
	} else if c := cmp.Or(l.CompactWindow, w); c > 0 {
		env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = strconv.Itoa(c)
	}
	// Claude Code puts CLAUDE_CODE_EFFORT_LEVEL above --effort and /effort, which would override the caller's
	// choice and lock the session's effort control (#2307). A default effort goes to the command line (--effort)
	// instead, and the environment variable is set only when the provider's own env sets it explicitly.
	set("ANTHROPIC_CUSTOM_HEADERS", l.Headers)
	// Both spellings: Node's fetch reads the lower-case one first, so an upper-case value alone would lose to a lower-case
	// one in the user's settings env.
	if l.Proxy != "" {
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			env[k] = l.Proxy
		}
	}
	// Both spellings: Claude Code's own check reads both, but the runtime under it (Bun) takes no_proxy || NO_PROXY,
	// so a lower-case list from the shell alone would still send the gateway through the proxy (#6238). A provider with
	// a proxy of its own brings its whole list, an empty one too: one from the shell or the user's settings would apply
	// to its proxy. "*" is no proxy at all.
	if l.NoProxy != "" || l.Proxy != "" {
		env["NO_PROXY"], env["no_proxy"] = l.NoProxy, l.NoProxy
	}
	if l.CustomModel != "" {
		env["ANTHROPIC_CUSTOM_MODEL_OPTION"] = l.CustomModel
		set("ANTHROPIC_CUSTOM_MODEL_OPTION_NAME", l.CustomModelName)
		set("ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION", l.CustomModelDescription)
	}
	k, v, _ := strings.Cut(artifactsOff, "=")
	env[k] = v
	// The launcher's own environment last: it wins over all of the above (#5953); an empty value sets the variable
	// empty. A proxy variable goes in both spellings unless both are given, or the shell's other spelling would still
	// count. Not for the main conversation what it decides itself (perStartEnv).
	vars := launcherEnvVars(l.Env)
	given := map[string]bool{}
	for _, kv := range vars {
		given[kv[0]] = true
	}
	for _, kv := range vars {
		if mainConversation && slices.Contains(perStartEnv, kv[0]) {
			continue
		}
		env[kv[0]] = kv[1]
		for _, o := range otherSpelling(kv[0]) {
			if !given[o] {
				env[o] = kv[1]
			}
		}
	}
	if mainConversation {
		c := compactionLimits(false, providerCapacity(l, model))
		env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = strconv.Itoa(c.window)
		if c.capacity > 0 {
			env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = strconv.Itoa(c.capacity)
		}
	}
	return env
}

// managedEnvOrder: the order `personad launch` has always set a managed launcher's variables in; any other comes
// after them, sorted.
var managedEnvOrder = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	"ANTHROPIC_CUSTOM_MODEL_OPTION", "ANTHROPIC_CUSTOM_MODEL_OPTION_NAME", "ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_DISABLE_ARTIFACT",
}

// envList is env as KEY=value entries, managedEnvOrder first. The two credentials keep the order they always had: the
// one with the key first, the emptied one after it.
func envList(env map[string]string) []string {
	order := slices.Clone(managedEnvOrder)
	if env["ANTHROPIC_AUTH_TOKEN"] == "" {
		order[1], order[2] = order[2], order[1]
	}
	rest := maps.Clone(env)
	var out []string
	for _, k := range order {
		if v, ok := rest[k]; ok {
			out = append(out, k+"="+v)
			delete(rest, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(rest)) {
		out = append(out, k+"="+rest[k])
	}
	return out
}

// perStartEnv: what a start decides (the main conversation keeps its 200000 compact window and switches its thinking
// level while it runs) and so never goes into settings env, which would win over the start's own environment.
var perStartEnv = []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_EFFORT_LEVEL"}

// pinnedEnv is env without perStartEnv: the part pinned over the user's ~/.claude/settings.json (the gateway, its key,
// the models), as a settings "env".
func pinnedEnv(env map[string]string) map[string]string {
	out := maps.Clone(env)
	for _, k := range perStartEnv {
		delete(out, k)
	}
	return out
}

// launchEnvFor is what `personad launch` drops from the shell and sets for a launcher (the node's clear and env lists):
// in the assistant's process (PERSONA_ASSISTANT=1), neither touches perStartEnv, so its own compact window and
// thinking level stay.
func launchEnvFor(clear, env []string, assistant bool) ([]string, []string) {
	if !assistant {
		return clear, env
	}
	clear = slices.DeleteFunc(slices.Clone(clear), func(c string) bool { return slices.Contains(perStartEnv, c) })
	env = slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(perStartEnv, k)
	})
	return clear, env
}

// launcherDefaultEffort returns the provider's default effort level (its configured Effort,
// or its default model's DefaultEffort).
func launcherDefaultEffort(l proto.ManagedLauncher) string {
	pm, _ := providerModel(l, l.Model)
	return cmp.Or(l.Effort, pm.DefaultEffort)
}
