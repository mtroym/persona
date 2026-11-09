package extmgr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/proc"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/rpc"
	wechat "github.com/nwverse/persona/server/internal/wechat/spec"
)

// Plugins (docs/extensions/extensions.md): one registry of every plugin — built into personad, written in hub.json, installed on
// this machine — and the plugin host running them. A plugin's contributions plug into the hub where they belong: a
// connector into the connector registry, a voice plugin into the app's voice conversation (relayed under
// /api/v1/plugins/<id>/…).

// ExtensionConfig is hub.json's plugins[]: a process the hub runs next to itself (docs/extensions/extensions.md "Extensions in
// hub.json"). The hub starts it, restarts it when it exits and relays /api/v1/plugins/{id}/… to the HTTP server it
// serves on the unix socket in PERSONA_PLUGIN_SOCKET (WebSockets included). Without a command the plugin runs elsewhere
// and only its socket is here (e.g. forwarded with ssh -R from a machine that reaches what the plugin needs). The
// registry gives it a manifest made from the entry; it is changed in hub.json only.
type ExtensionConfig struct {
	ID       string            `json:"id"`
	Name     string            `json:"name,omitempty"`
	Kind     string            `json:"kind"`
	Command  string            `json:"command,omitempty"`
	Socket   string            `json:"socket,omitempty"` // default ~/.persona/plugins/<id>.sock
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Dir      string            `json:"dir,omitempty"` // working directory; "~/" is the hub machine's home
	Disabled bool              `json:"disabled,omitempty"`
	// Config is the plugin's own settings, handed over as JSON in PERSONA_PLUGIN_CONFIG (the built-in voice plugin's
	// providers, docs/extensions/extensions.md "Built-in voice extension").
	Config json.RawMessage `json:"config,omitempty"`
}

// ExtensionView is a plugin as the app sees it (GET /api/v1/plugins). id, name, kind, status and error are what it has
// always said; the rest came with the plugin base.
type ExtensionView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // disabled | needs_setup | incompatible | stopped | starting | running | error
	Error  string `json:"error,omitempty"`

	Version     string              `json:"version,omitempty"`
	Source      string              `json:"source,omitempty"` // builtin | hub.json | installed
	Enabled     bool                `json:"enabled"`
	Description string              `json:"description,omitempty"`
	Contributes map[string][]string `json:"contributes,omitempty"`
	NeedsSetup  []string            `json:"needs_setup,omitempty"`
	Warnings    []string            `json:"warnings,omitempty"`

	// A plugin from a market (extension_autoupgrade.go): its own auto-upgrade switch, why that switch does not apply
	// now ("disabled": the extension is switched off; the switch is kept for when it is on), its newer version and what
	// happened last with an upgrade.
	AutoUpgrade       *bool                 `json:"auto_upgrade,omitempty"`
	AutoUpgradePaused string                `json:"auto_upgrade_paused,omitempty"`
	Upgrade           *ExtensionUpgrade     `json:"upgrade,omitempty"`
	UpgradeNote       *ExtensionUpgradeNote `json:"upgrade_note,omitempty"`
	// Nodes: its program on each computer it is switched on for (extension_nodes.go), for a plugin with one.
	Nodes []ExtensionNodeView `json:"nodes,omitempty"`
	// Sandbox: an installed extension's sandbox and its connections through the egress proxy (extension_sandbox.go).
	Sandbox *SandboxView `json:"sandbox,omitempty"`
	// Auth is its sign-in (manifest.auth, extension_auth.go); Tools the command-line programs this version runs with
	// (manifest.tools).
	Auth  *ExtensionAuth      `json:"auth,omitempty"`
	Tools []ExtensionToolView `json:"tools,omitempty"`
	// Attached: an extension running on its own that the manager reaches on its sockets (a remote: install), and how it
	// is connected.
	Attached *ExtensionAttached `json:"attached,omitempty"`
}

// ExtensionAttached is how an attached extension is connected (docs/extensions/extensions.md "Attached extensions").
type ExtensionAttached struct {
	Online bool `json:"online"`
	// Since is when it came online or went offline (none while it is switched off or nothing needs it), in the user's
	// zone.
	Since time.Time `json:"since,omitzero"`
	// Error is why it is offline, also while its status is still starting.
	Error string `json:"error,omitempty"`
	// Version is the version the program says it is, when that is not the approved Version: it is not used until
	// that version is approved.
	Version string `json:"version,omitempty"`
}

// ExtensionToolView is one of an extension's command-line programs: Reused when it is the machine's own copy.
type ExtensionToolView struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
	Reused  bool   `json:"reused,omitempty"`
}

// Statuses besides the host's.
const (
	extensionDisabled     = "disabled"
	extensionNeedsSetup   = "needs_setup"
	extensionIncompatible = "incompatible"
)

var extensionIDPattern = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// hubExtension is one plugin as the hub runs it now.
type hubExtension struct {
	contributionsMu sync.Mutex
	contributions   map[string][]extension.ExportedContribution

	id     string
	kind   string
	socket string // its HTTP socket, when it serves HTTP
	proxy  *httputil.ReverseProxy
	client *http.Client
	// proxyTransport is proxy's: closeIdle closes its idle connections.
	proxyTransport *http.Transport

	pending bool         // made before the plugins start (newExtensions): startExtensions runs it
	proc    *host.Proc   // nil while it does not run (disabled, needs setup, incompatible, nothing to run)
	conns   []*proc.Conn // its connectors
	links   []*proc.Link // theirs, to proc
	status  string       // when proc is nil
	err     string

	// gateCommands are the commands of its assistant gates (the gate files' command), read when it starts, for an enabled
	// extension without a problem whether or not it runs a program (the ones assistantGates reads): which writes the
	// node's gate proxy reports (lark.sent) are its own (sent_recall_gate.go).
	gateCommands []string
	// recallNotBefore is when this install of an installed extension began (zero for any other: a built-in or a dev one is
	// not replaced by another under its id): a ledger row from before it was not the extension's (sent_recall_gate.go).
	recallNotBefore time.Time

	// agentSources are its agent_sources registered with the agents service (extension_agents.go).
	agentSources []string
	// stats count its calls, runs and log (extension_metrics.go); nil in tests that make one by hand.
	stats *extensionStats
}

// healthy says whether its HTTP side answers /health.
func (p *hubExtension) healthy(ctx context.Context) bool { return p.proc != nil && p.proc.Healthy(ctx) }

// plugins is the registry and what runs of it.
type extensions struct {
	// checks limit how often each extension's settings.check runs (extension_settings_check.go).
	checks checkLimiter
	dir    string // the hub's data directory
	root   string // where the extensions are kept: <dir>/extensions (extensionsRoot)
	reg    *extension.Registry
	// logf is the hub's log, each line about an extension kept with its log too; journal only the hub's (the host's
	// lines reach the extension's log through its Observe).
	logf, journal func(string, ...any)
	// stats are how the extensions run (extension_metrics.go).
	stats *extensionMetrics

	h    *Manager
	ctx  context.Context
	mu   sync.Mutex
	byID map[string]*hubExtension
	// messaging coalesces the messaging sources' changes before they reach the apps (extension_messaging.go).
	messaging messagingChanges
	// skillsSent remembers which skill digests each machine was sent (machine id → *sync.Map of name → digest), so
	// the files go once (again after the hub restarts).
	skillsSent sync.Map
	// markets are the plugin markets (extension_market.go); upgrades their plugins' auto-upgrade
	// (extension_autoupgrade.go).
	markets  markets
	upgrades extensionUpgrades
	// ops are the installs, upgrades and uninstalls in progress or just over (extension_operations.go).
	ops extensionOps
	// auth is the last sign-in status of the extensions that sign in (extension_auth.go).
	auth authCache
	// dev streams what the extensions being written log (extension_dev.go).
	dev devTaps
	// badges are the plugins' badges on their nav_pages' entries ("<plugin>/<ui>" → extension.Badge; extension_ui.go).
	badges sync.Map
	// events and todoEvents keep slow extension programs out of the assistant bus read loop: the knowledge base's and the
	// todos' queues, apart so that a flood of one does not push out the other's.
	events     extensionEvents
	todoEvents extensionEvents
	// filtersSent are the agent filters last handed to the nodes, as JSON (agent_filters.go); nil before the first.
	filtersSent []byte
	// invalid are the extensions' ui.invalidate waiting to be told to the apps (extension_ui.go), at most one a second.
	invalid invalidations
	// widgets are the views drawn for widgets and the widgets each device has (extension_system.go).
	widgets widgets
	// nodes are plugins' programs on computers (extension_nodes.go).
	nodes extensionNodes
	// sandboxes are the owner's sandbox choices and the egress proxy (extension_sandbox.go).
	sandboxes extensionSandboxes
	// sentLimit is the rate limit of the extensions' sent.record calls (sent_ledger.go).
	sentLimit sentLimiter
	// voice is the hub.json entry the built-in voice extension took over (nil without one); voiceID the built-in's id
	// once registered, "" when there is none (voice_extension.go).
	voice   *ExtensionConfig
	voiceID string
	// hubRelay is how a relayed manager's extensions reach the hub's API (hub_relay.go).
	hubRelay hubRelay
}

// newExtensions is the registry with hub.json's plugins: a bad entry is logged and skipped. Built-in and installed ones
// join when the hub attaches its database (attachExtensions).
func newExtensions(dir string, cfgs []ExtensionConfig, logf func(string, ...any)) *extensions {
	root := extensionsRoot(dir, logf)
	ps := &extensions{dir: dir, root: root, reg: extension.NewRegistry(root, nil, nil, 0), journal: logf, byID: map[string]*hubExtension{},
		stats: newExtensionMetrics()}
	// The hub's lines about an extension go into its log too (the host's own go there through the spec's Observe).
	ps.logf = func(format string, args ...any) {
		logf(format, args...)
		ps.stats.hubLine(fmt.Sprintf(format, args...))
	}
	for _, c := range cfgs {
		if !extensionIDPattern.MatchString(c.ID) || (c.Command == "" && c.Socket == "") {
			logf("plugin %q: skipped (needs an id of a-z0-9_- and a command or a socket)", c.ID)
			continue
		}
		if c.Name == "" {
			c.Name = c.ID
		}
		socket, kind := ps.legacySocket(c), c.Kind
		switch {
		case ps.reg.Get(c.ID) != nil || ps.voice != nil && ps.voice.ID == c.ID:
			logf("plugin %q: skipped (duplicate id)", c.ID)
			continue
		case ps.voice == nil && takesOverVoice(c):
			// `personad voice`: the built-in voice extension under this id (attachExtensions, voice_extension.go).
			taken := c
			ps.voice, socket, kind = &taken, filepath.Join(ps.root, c.ID+".sock"), "voice"
			if c.Socket != "" {
				logf("plugin %q: socket is not used: the built-in voice extension serves on %s", c.ID, socket)
			}
		default:
			if err := ps.reg.AddHubJSON(legacyManifest(c), c, !c.Disabled); err != nil {
				logf("plugin %q: skipped (duplicate id)", c.ID)
				continue
			}
		}
		// Known before it runs: its socket (the model settings ask a voice plugin for its models through it).
		hp := &hubExtension{id: c.ID, kind: kind, socket: socket, status: host.StateStarting, pending: true, stats: ps.stats.of(c.ID)}
		if c.Disabled {
			hp.status = extensionDisabled
		}
		hp.setProxy(c.ID)
		ps.byID[c.ID] = hp
	}
	return ps
}

// legacySocket is where a hub.json plugin serves HTTP.
func (ps *extensions) legacySocket(c ExtensionConfig) string {
	if c.Socket != "" {
		return expandHome(c.Socket)
	}
	return filepath.Join(ps.root, c.ID+".sock")
}

// maxSocketPath is the longest path a Unix socket can have here: sockaddr_un's sun_path less its NUL (macOS 103
// bytes, Linux 107).
const maxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// checkSocketPath fails, saying what to do, when path is too long for a Unix socket here; binding it would fail with
// only "invalid argument". An external manager's directory (managers/<id> in the data directory) leaves the least room.
func checkSocketPath(path string) error {
	if len(path) <= maxSocketPath {
		return nil
	}
	return i18n.Err("hub-extensions.socket_path_too_long", i18n.Args{"path": path, "length": len(path), "max": maxSocketPath})
}

// legacyManifest is a hub.json entry as a manifest: a program serving HTTP (no protocol) contributing its kind, run
// from the start, with the hub's API (it has always had the hub's token).
func legacyManifest(c ExtensionConfig) *extension.Manifest {
	kind := c.Kind
	if kind == "" {
		kind = "voice"
	}
	m := &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: c.ID, Name: c.Name, Version: "0.0.0",
		Runtime:          &extension.Runtime{Command: extension.Command{Path: c.Command}, Args: c.Args, RPC: "none", HTTP: true},
		ActivationEvents: []string{extension.OnStartup}, Permissions: extension.Permissions{HubAPI: true},
		Contributes: map[string]json.RawMessage{}}
	item, _ := json.Marshal([]map[string]string{{"id": c.ID, "name": c.Name}})
	m.Contributes[kind] = item
	return m
}

// list is every plugin, by id.
func (ps *extensions) list() []ExtensionView {
	out := []ExtensionView{}
	for _, p := range ps.reg.List() {
		out = append(out, ps.view(context.Background(), p))
	}
	return out
}

func (ps *extensions) view(ctx context.Context, p *extension.Extension) ExtensionView {
	m := p.Full
	l := ps.h.contextLang(ctx)
	v := ExtensionView{ID: p.Record.ID, Name: m.Name, Kind: m.Kind(), Version: p.Record.Version, Source: p.Record.Source,
		Enabled: p.Record.Enabled, Description: m.Description, Contributes: m.Contributions(), Warnings: p.WarningTexts(l)}
	if c, ok := p.Legacy.(ExtensionConfig); ok {
		v.Kind, v.Version = c.Kind, ""
	}
	ps.mu.Lock()
	hp := ps.byID[p.Record.ID]
	ps.mu.Unlock()
	switch {
	case !p.Record.Enabled:
		v.Status = extensionDisabled
	case p.Problem != "":
		v.Status, v.Error = extensionIncompatible, p.ProblemText(ps.h.contextLang(ctx))
	case hp == nil:
		v.Status = host.StateStarting
	case hp.proc == nil:
		v.Status, v.Error = hp.status, hp.err
	default:
		v.Status, v.Error = hp.proc.Status()
	}
	if v.Status == extensionNeedsSetup || (hp == nil && p.Record.Enabled) {
		v.NeedsSetup = ps.reg.Missing(ctx, p)
		if len(v.NeedsSetup) > 0 {
			v.Status = extensionNeedsSetup
			v.Error = i18n.T(l, "hub-extensions.missing", i18n.Args{"fields": strings.Join(v.NeedsSetup, i18n.T(l, "hub-extensions.list_separator"))})
		}
	}
	v.Error = i18n.ExtensionStatusText(l, v.Error)
	if p.Record.Remote != nil {
		v.Attached = &ExtensionAttached{}
		if hp != nil && hp.proc != nil && v.Status != host.StateStopped { // stopped: nothing needs it, so not connected
			link := hp.proc.Link()
			v.Attached.Online, v.Attached.Since = link.Online, link.Since
			if !link.Since.IsZero() {
				v.Attached.Since = link.Since.In(ps.h.userLocation()) // `extension list` prints it as it comes
			}
			if !link.Online {
				v.Attached.Error = i18n.ExtensionStatusText(l, link.Why)
			}
			if link.Said != "" && link.Said != p.Record.Version {
				v.Attached.Version = link.Said
				if v.Status == host.StateError {
					v.Error = i18n.T(l, "extension-status.attached_version", i18n.Args{"current": link.Said, "approved": p.Record.Version, "id": p.Record.ID})
				}
			}
		}
	}
	if p.Record.Enabled && ps.h != nil && ps.h.hubJSONWaitsForTriageModel(ctx, p) { // runs, but its inbox waits
		v.Status, v.NeedsSetup = extensionNeedsSetup, append(v.NeedsSetup, needsTriageModelSetup(ps.h.contextLang(ctx)))
	}
	if ps.h != nil {
		ps.voiceView(ctx, p, &v)    // runs, but no conversation starts without an upstream (voice_extension.go)
		ps.imessageView(ctx, p, &v) // no connector until its one Mac is picked (imessage_extension.go)
	}
	if ps.h != nil {
		ps.h.extensionUpgrades().decorate(ctx, &v, p)
	}
	if m.NodeProgram() != nil {
		v.Nodes = ps.nodes.views(p.Record.ID)
	}
	if v.Auth = ps.authView(p, l); v.Auth != nil && ps.h != nil {
		if line := authWarning(ps.h.contextLang(ctx), v.Auth); line != "" {
			v.Warnings = append(slices.Clone(v.Warnings), line)
		}
	}
	for _, t := range ps.reg.Tools(p.Record.ID, p.Record.Version) {
		v.Tools = append(v.Tools, ExtensionToolView{Name: t.Name, Title: t.Title, Version: t.Version, Reused: t.Reused})
	}
	if st := ps.sandboxOf(ctx, p); st.applies {
		sv := st.SandboxView
		v.Sandbox = &sv
		if st.Why != "" {
			v.Warnings = append(slices.Clone(v.Warnings), st.Why)
		}
	}
	return v
}

// get is the running state of plugin id (nil when none).
func (ps *extensions) get(id string) *hubExtension {
	if ps.reg != nil {
		id = ps.reg.CanonicalID(id)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.byID[id]
}

// hubSealer seals plugin secrets with secret.key.
type hubSealer struct{ h *Manager }

func (s hubSealer) Seal(plain string) (string, error)  { return s.h.sealKey(plain) }
func (s hubSealer) Open(sealed string) (string, error) { return s.h.openKey(sealed) }

// attachExtensions gives the registry the database, the hub's build and the built-in plugins, reads the rows and
// registers the connectors plugins contribute (startConnectors calls it between the hub's own connectors and Start).
func (h *Manager) attachExtensions(ctx context.Context) {
	if h.extensions == nil {
		h.extensions = newExtensions(h.dataDirOrTemp(), nil, h.logf)
	}
	ps := h.extensions
	ps.h, ps.ctx = h, ctx
	var store extension.Store
	if s, ok := h.store.(extension.Store); ok {
		store = s
	}
	reg := extension.NewRegistry(ps.root, store, hubSealer{h}, h.build.Number)
	for _, p := range ps.reg.List() { // hub.json's
		_ = reg.AddHubJSON(p.Manifest, p.Legacy, p.Record.Enabled)
	}
	reg.RemoteManifest = ps.remoteManifest
	reg.Logf = h.logf                    // what Load did about an interrupted uninstall
	reg.CanEnable = h.canEnableExtension // the inbox's rules (extension_inbox.go)
	reg.PlanCandidates = h.extensionPlanCandidates
	reg.PlanGuard = h.guardExtensionPlan
	// The voice extension's key goes with its address (voice_extension.go).
	reg.BoundSecrets, reg.SecretNeeds = ps.voiceKeyFollowsURL, ps.voiceKeyNeedsURL
	reg.SettingsCheck = ps.settingsCheck // settings_check (extension_settings_check.go)
	var own []string
	for _, c := range h.connectors.All() {
		if !slices.Contains(connector.MigratableConnectors, c.ID()) { // a plugin may take these over (connectors_migrate.go)
			own = append(own, c.ID())
		}
	}
	reg.Reserve("connectors", own...)
	// Current and retired app-drawn IDs stay reserved; the hub separately gives its own packs precedence.
	reg.Reserve("looks", extension.BuiltinLooks...)
	reg.Reserve("looks", extension.RetiredLookIDs...)
	// No agent_sources id is reserved: the hub has no personal agents source of its own (extension_agents.go).
	if path := siblingProgram(wechat.Program); path != "" {
		if err := h.addWeChatBuiltin(reg, path); err != nil {
			h.logf("plugin wechat: %v", err)
		}
	}
	if err := reg.AddBuiltin(cuaManifest(h), "", false); err != nil { // off until the user turns it on (computer.go)
		h.logf("plugin cua: %v", err)
	}
	if err := reg.AddBuiltin(egoManifest(h), "", false); err != nil { // the same, for ego lite (browser.go)
		h.logf("plugin ego: %v", err)
	}
	if err := reg.AddBuiltin(imessageManifest(h), "", false); err != nil { // which Mac's Messages (imessage_extension.go)
		h.logf("plugin imessage: %v", err)
	}
	h.addVoiceExtension(ctx, reg, store) // `personad voice`, taking hub.json's entry of it over (voice_extension.go)
	if err := reg.Load(ctx); err != nil {
		h.logf("plugins: %v", err)
	}
	h.fixMachineSettings(ctx, reg) // machine names typed into settings become ids (extension_machines.go)
	reg.OnChange = ps.changed
	ps.mu.Lock()
	ps.reg = reg
	ps.mu.Unlock()
	ps.loadSandboxes(ctx) // before any of them starts (extension_sandbox.go)
	for _, p := range reg.DependencyOrder() {
		if len(p.Manifest.Items("connectors")) > 0 {
			ps.apply(ctx, p.Record.ID)
		}
	}
	// A node that connected while the extensions were loading was told of none (agent_filters.go).
	h.agentFiltersChanged(ctx)
}

// dataDirOrTemp is where plugins keep their packages and sockets.
func (h *Manager) dataDirOrTemp() string {
	if h.cfg != nil && h.cfg.dir != "" {
		return h.cfg.dir
	}
	return os.TempDir()
}

// startExtensions runs every plugin that is not running yet (connectors' ones run since attachExtensions).
func (h *Manager) startExtensions(ctx context.Context) {
	if h.extensions == nil {
		return
	}
	ps := h.extensions
	if ps.h == nil {
		ps.h, ps.ctx = h, ctx
	}
	for _, p := range ps.reg.DependencyOrder() {
		if hp := ps.get(p.Record.ID); hp == nil || hp.pending {
			ps.apply(ctx, p.Record.ID)
		}
	}
}

// changed is the registry's news: the plugin runs again as it is now; one just upgraded that fails to come up is
// rolled back.
func (ps *extensions) changed(c extension.Change) {
	ctx := ps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if c.Kind == "config" {
		ps.settingsMoved(c.ID) // before the restart: the old address is refused from now on
	}
	if c.Old != nil && c.Old.Record.ID != c.ID {
		// Retire the old public identity synchronously before opening the same data slot.
		// This is not an uninstall: connector data and upgrade preferences survive.
		ps.apply(ctx, c.Old.Record.ID)
		ps.forgetAuth(c.Old.Record.ID)
		ps.nodes.drop(ps.root, c.Old.Record.ID, "")
	}
	ps.apply(ctx, c.ID)
	ps.forgetAuth(c.ID) // another version, removed, switched, set up again: ask again
	switch c.Kind {
	case "removed", "upgraded", "rolled_back": // only the version installed now keeps its packages for computers
		keep := ""
		if p := ps.reg.Get(c.ID); p != nil {
			keep = p.Record.Version
		}
		ps.nodes.drop(ps.root, c.ID, keep)
	}
	affected := ps.modelChanged(ctx, c)
	nodeChanged := c.Old != nil && c.Old.Manifest.NodeProgram() != nil
	for _, p := range affected {
		nodeChanged = nodeChanged || p.Manifest.NodeProgram() != nil
	}
	if ps.h != nil && nodeChanged {
		ps.h.syncExtensionNodesEverywhere(ctx) // its program on computers starts, changes or stops there (extension_nodes.go)
	}
	if ps.h != nil {
		ps.h.agentFiltersChanged(ctx) // only when they changed (agent_filters.go)
	}
	if ps.h != nil {
		ps.h.sendReport() // its looks came or went: the hub serves them from the report (hub/looks.go)
		if c.Kind == "removed" && c.Old != nil && len(c.Old.Full.Looks()) > 0 && ps.h.opts.Hub != nil {
			var ids []string
			for _, l := range c.Old.Full.Looks() {
				ids = append(ids, l.ID)
			}
			_ = ps.h.opts.Hub.Notify(extwire.EventLooksGone, extwire.LooksGone{IDs: ids})
		}
	}
	if c.ID == cuaExtensionID && ps.h != nil {
		ps.h.computerChanged(ctx)
	}
	if c.ID == egoExtensionID && ps.h != nil {
		ps.h.browserChanged(ctx)
	}
	if c.ID == imessageExtensionID && ps.h != nil {
		ps.h.applyIMessage() // its Mac changed, or it went on or off (imessage_extension.go)
	}
	if c.Kind == "removed" && c.Old != nil && ps.h != nil {
		now := ps.reg.Get(c.ID) // a built-in the removed plugin had taken the place of (Yields) keeps its connectors' sign-in
		for _, id := range c.Old.Manifest.Items("connectors") {
			if now != nil && slices.Contains(now.Manifest.Items("connectors"), id) {
				continue
			}
			if ps.h.hasOwnConnector(id) { // the hub's own is back (a migratable id): its sign-in and settings stay
				continue
			}
			ps.h.forgetConnector(ctx, id)
		}
	}
	if c.Kind == "removed" && c.Old != nil && ps.h != nil && ps.reg.Get(c.ID) == nil {
		if c.Old.Record.StorageKey() != c.ID {
			ps.forgetSandbox(ctx, c.Old.Record.StorageKey())
		}
		ps.h.extensionUpgrades().forget(ctx, c.Old.Record.StorageKey()) // installed again later, it starts afresh (extension_autoupgrade.go)
	}
	if (c.Kind == "upgraded" || c.Kind == "renamed") && c.Old != nil {
		from := c.Old.Record.Version
		to := ""
		if p := ps.reg.Get(c.ID); p != nil {
			to = p.Record.Version
		}
		go func() {
			rolledBack, why := ps.watchUpgrade(ctx, c.ID)
			if ps.h != nil {
				ps.h.extensionUpgrades().watched(ctx, c.ID, from, to, rolledBack, why)
			}
		}()
	}
}

// watchUpgrade rolls an upgraded plugin back when it does not come up (only one that runs by itself or for a
// connector that is on: a plugin started by calls is not started for this), and says whether it did and why.
func (ps *extensions) watchUpgrade(ctx context.Context, id string) (rolledBack bool, why string) {
	hp := ps.get(id)
	if hp == nil || hp.proc == nil {
		return false, ""
	}
	wctx, cancel := context.WithTimeout(ctx, host.ReadyWithin+5*time.Second)
	defer cancel()
	for wctx.Err() == nil {
		state, why := hp.proc.Status()
		switch state {
		case host.StateRunning:
			return false, ""
		case host.StateError:
			ps.logf("plugin %s: the new version does not come up (%s): back to the one before", id, why)
			if _, err := ps.reg.Rollback(ctx, id, "hub"); err != nil {
				ps.logf("plugin %s: rollback: %v", id, err)
				return false, ""
			}
			return true, why
		}
		select {
		case <-wctx.Done():
		case <-hp.proc.Changes():
		}
	}
	return false, ""
}

// apply makes plugin id run as the registry says: whatever ran of it stops (its connectors leave the registry), and
// it starts again if it is there, switched on, compatible and set up.
func (ps *extensions) apply(ctx context.Context, id string) {
	ps.mu.Lock()
	old := ps.byID[id]
	delete(ps.byID, id)
	ps.mu.Unlock()
	why := tokenStopReason(ps.reg.GetExact(id))
	if ps.h != nil {
		ps.h.widgetsDropExtension(id)
		// Wake after the replacement is published, so a catalog refresh sees the new contributions.
		defer ps.h.widgetsWake(id, nil, true)
		ps.h.pageDropExtension(id)
		// Switched off, upgraded (maybe to fewer scopes), uninstalled or reconfigured: its token goes now, and a new
		// one comes with its next start.
		ps.h.extTokens.revoke(id, why)
	}
	ps.forgetEgress(id) // the same for its proxy credential (extension_sandbox.go)
	if ps.h != nil {
		ps.h.forgetImages(id) // and the pictures it served (extension_images.go)
	}
	if old != nil {
		ps.removeAgentSources(old)
		for _, c := range old.conns {
			if ps.h != nil && ps.h.connectors != nil {
				ps.h.removeExtensionConnector(c.ID())
			}
		}
		if old.proc != nil {
			old.proc.Stop()
		}
		old.closeIdle()
	}
	if ps.h != nil {
		// Stop waits for the old start/initialize callbacks too: a token minted during shutdown must not survive.
		ps.h.extTokens.revoke(id, why)
	}
	p := ps.reg.GetExact(id)
	if p == nil {
		// An alias still owns this slot; only a genuine removal forgets its preferences.
		if ps.reg.Get(id) == nil {
			ps.forgetSandbox(ctx, id)
		}
		return
	}
	hp := &hubExtension{id: id, kind: p.Manifest.Kind(), stats: ps.stats.of(id)}
	if p.Manifest.Exports != nil {
		hp.contributions = ps.reg.ContributionsToContext(ctx, id)
	}
	if c, ok := p.Legacy.(ExtensionConfig); ok {
		hp.kind = c.Kind
	}
	if p.Record.Enabled && p.Problem == "" { // the extensions whose gates the assistant is given (assistantGates), running or not
		hp.gateCommands = gateCommandsOf(p)
	}
	if p.Record.Source == extension.SourceInstalled {
		hp.recallNotBefore = p.Record.InstalledAt
	}
	switch missing := ps.reg.Missing(ctx, p); {
	case !p.Record.Enabled:
		hp.status = extensionDisabled
	case p.Problem != "":
		hp.status, hp.err = extensionIncompatible, p.Problem
	case len(missing) > 0:
		l := ps.h.contextLang(ctx)
		hp.status, hp.err = extensionNeedsSetup, i18n.T(l, "hub-extensions.missing", i18n.Args{"fields": strings.Join(missing, i18n.T(l, "hub-extensions.list_separator"))})
	case p.Manifest.Runtime == nil:
		hp.status = host.StateRunning // nothing to run: its declarations are in effect
	default:
		spec, err := ps.spec(ctx, p, hp)
		if err != nil {
			hp.status, hp.err = host.StateError, err.Error()
			break
		}
		hp.proc = host.Start(ctx, spec)
		for _, l := range hp.links {
			l.Proc = hp.proc
		}
	}
	if hp.socket != "" {
		hp.setProxy(id)
	}
	ps.addAgentSources(id, p.Manifest, hp) // before hp is published: its agentsrc.updated finds them
	ps.mu.Lock()
	ps.byID[id] = hp
	ps.mu.Unlock()
	if hp.proc != nil && ps.h != nil && ps.h.connectors != nil {
		for _, c := range hp.conns {
			if err := ps.h.addExtensionConnector(ctx, c, p.Record.Source == extension.SourceBuiltin); err != nil {
				ps.logf("plugin %s: %v", id, err)
			} else if c.ID() == "github" {
				// after this start: the move changes its settings, which restarts it
				go ps.h.migrateGitHubConfig(context.WithoutCancel(ctx), id)
			}
		}
	}
}

// setProxy relays the app's requests to the plugin's HTTP side.
func (hp *hubExtension) setProxy(id string) {
	socket := hp.socket
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	hp.client = &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 5 * time.Second}
	hp.proxyTransport = &http.Transport{DialContext: dial}
	prefix := "/api/v1/extensions/" + id
	hp.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host = "http", "plugin"
			r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, prefix)
			if r.Out.URL.Path == "" {
				r.Out.URL.Path = "/"
			}
			// Keep what the caller escaped (an id with "/" in it, as %2F): the plugin reads EscapedPath.
			r.Out.URL.RawPath = ""
			if raw := strings.TrimPrefix(r.In.URL.EscapedPath(), prefix); raw != "" && raw != r.Out.URL.Path {
				r.Out.URL.RawPath = raw
			}
			// The hub has already checked the caller: the plugin never sees the app's credential.
			q := r.Out.URL.Query()
			q.Del("token")
			r.Out.URL.RawQuery = q.Encode()
			r.Out.Header.Del("Authorization")
			r.Out.Host = "plugin"
		},
		Transport:     hp.proxyTransport,
		FlushInterval: -1, // stream (audio, server-sent events) as it comes
		// The plugin says how its answers may be cached (images, emojis); the hub's no-store only when it says nothing.
		// The caller drops the hub's own Cache-Control first (serveExtensionHTTP): the proxy adds headers, it does not
		// replace them, and "no-store, private, max-age=3600" kept the apps from caching anything.
		ModifyResponse: func(res *http.Response) error {
			if res.Header.Get("Cache-Control") == "" {
				res.Header.Set("Cache-Control", "no-store")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Cache-Control", "no-store")
			writeErr(w, http.StatusBadGateway, fmt.Errorf("plugin %s is not answering: %w", id, err))
		},
	}
}

// closeIdle closes the idle connections of its client and proxy once it no longer runs: their transports have no
// idle timeout, and an extension served from elsewhere stays up, so each restart would leave them open for good.
func (hp *hubExtension) closeIdle() {
	if hp.client != nil {
		hp.client.CloseIdleConnections()
	}
	if hp.proxyTransport != nil {
		hp.proxyTransport.CloseIdleConnections()
	}
}

// serveExtensionHTTP hands one request to the plugin's HTTP side, counted by route (extension_metrics_relay.go).
func (hp *hubExtension) serveExtensionHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Del("Cache-Control") // set by the hub's auth for its own answers: the plugin's own goes instead
	if hp.stats == nil {
		hp.proxy.ServeHTTP(w, r)
		return
	}
	sw := &relayStatusWriter{ResponseWriter: w}
	start := time.Now()
	hp.proxy.ServeHTTP(sw, r)
	took := time.Since(start)
	if sw.status == http.StatusSwitchingProtocols {
		took = -1 // a WebSocket: how long it was open is not a latency
	}
	hp.stats.Call(httpRoute(hp.id, r.URL.Path), took, httpCode(r, sw.status))
}

// selfExecutable is this very binary (a built-in plugin's "personad").
func selfExecutable() string {
	if self, err := os.Executable(); err == nil {
		return self
	}
	return "personad"
}

// spec is how the host runs p; it also makes p's connectors (hp.conns) and its HTTP socket (hp.socket).
func (ps *extensions) spec(ctx context.Context, p *extension.Extension, hp *hubExtension) (host.Spec, error) {
	s, err := ps.specOf(ctx, p, hp)
	if err == nil {
		err = ps.confine(ctx, p, hp, &s) // its sandbox and the egress proxy (extension_sandbox.go)
	}
	// The HTTP socket the manager hands out, where confine left it; the program binds it itself. One that serves only
	// HTTP has nothing to run without it. One that speaks the protocol still starts and its log says why its HTTP side
	// will not: a program that keeps serving the protocol without that socket keeps working (the Go SDK's Main exits).
	if err == nil && s.HTTP && p.Record.Remote == nil && p.Legacy == nil {
		if err = checkSocketPath(s.Socket); err != nil && s.RPC {
			ps.logf("extension %s: %v", p.Record.ID, err)
			err = nil
		}
	}
	s.Env = withExtensionEnv(s.Env)
	return s, err
}

// specOf is spec with only the old names of the extension's variables (PERSONA_PLUGIN_*).
func (ps *extensions) specOf(ctx context.Context, p *extension.Extension, hp *hubExtension) (host.Spec, error) {
	if ps.reg != nil {
		if snapshot := ps.reg.SnapshotExact(p.Record.ID); snapshot != nil {
			p = snapshot
		}
	}
	m := p.Manifest
	id := p.Record.ID
	s := host.Spec{ID: id, Name: m.Name, Logf: ps.hostLogf()}
	if st := ps.stats.of(id); st != nil {
		s.Observe = st
	}
	if p.Record.Source == extension.SourceDev { // what it logs and the hub's calls to it go to `personad extension dev` too
		s.Observe = &devObserver{next: s.Observe, taps: &ps.dev, id: id}
	}
	hubEnv := ps.hubEnv()
	var hubToken startToken // its token of the hub's API this start (extension_scopes.go)
	// A relayed manager hands its extensions no token of the hub's own: those granted it get one of every scope, through
	// its relay (hub_relay.go). An extension of a manager other than the built-in one is told whose it is.
	relayed := ps.h != nil && ps.h.relayed()
	var managerEnv []string
	if ps.h != nil && !ps.h.builtin() {
		managerEnv = []string{"PERSONA_MANAGER=" + ps.h.opts.ManagerID}
	}

	if c, ok := p.Legacy.(ExtensionConfig); ok { // hub.json's, exactly as it has always run
		hp.socket = ps.legacySocket(c)
		s.HTTP, s.Socket, s.Startup = true, hp.socket, true
		if c.Command != "" {
			command := expandHome(c.Command)
			if extension.IsBuiltinCommand(command) {
				command = selfExecutable()
			}
			s.Command, s.Args, s.Dir = command, c.Args, expandHome(c.Dir)
			s.Env = append(slices.Clone(hubEnv), "PERSONA_PLUGIN_ID="+c.ID, "PERSONA_PLUGIN_SOCKET="+hp.socket)
			s.Env = append(s.Env, managerEnv...)
			for k, v := range c.Env {
				s.Env = append(s.Env, k+"="+v)
			}
			if len(c.Config) > 0 {
				s.Env = append(s.Env, "PERSONA_PLUGIN_CONFIG="+string(c.Config))
			}
			if relayed {
				start, err := ps.hubStartEnv(&hubToken, p, true)
				if err != nil {
					return s, err
				}
				s.StartEnv = func() []string { return identity.Environment(start()) }
			}
		}
		return s, nil
	}

	rt := m.Runtime
	activation := m.Activation()
	conns := m.Connectors()
	s.RPC = rt.SpeaksRPC()
	s.Startup = slices.Contains(activation, extension.OnStartup)
	s.OnCall = slices.Contains(activation, extension.OnCall) && len(conns) == 0 // a connector's switch holds it instead
	s.OnHTTP = slices.Contains(activation, extension.OnHTTP)
	if len(conns) > 0 {
		s.CallTimeout = 2 * time.Minute // the connector protocol's
	}
	if rt.IsDeno() && p.Record.Remote != nil {
		return s, errors.New("source runtimes cannot use remote process sockets")
	}
	if r := p.Record.Remote; r != nil {
		s.RPCSocket, s.Socket = r.RPCSocket, r.HTTPSocket
		s.RPC, s.HTTP = r.RPCSocket != "", r.HTTPSocket != ""
		hp.socket = r.HTTPSocket
		s.Attached = true
	} else {
		if rt.IsDeno() {
			if err := ps.sourceSpec(ctx, p, &s); err != nil {
				return s, err
			}
		} else {
			cmd, ok := rt.Command.For(runtime.GOOS + "-" + runtime.GOARCH)
			if !ok {
				return s, i18n.Err("hub-extensions.no_program", i18n.Args{"platform": runtime.GOOS + "-" + runtime.GOARCH})
			}
			if extension.IsBuiltinCommand(cmd) {
				s.Command = selfExecutable()
			} else {
				s.Command = filepath.Join(p.Dir, filepath.FromSlash(cmd))
			}
			s.Args = rt.Args
		}
		s.Dir = p.Dir
		s.CleanEnv = p.Record.Source == extension.SourceInstalled || p.Record.Source == extension.SourceDev // not the hub's own credentials
		dataDir := ps.reg.DataDir(id)
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return s, err
		}
		s.Env = []string{"PERSONA_PLUGIN_ID=" + id, "PERSONA_PLUGIN_DIR=" + p.Dir, "PERSONA_PLUGIN_DATA=" + dataDir}
		s.Env = append(s.Env, managerEnv...)
		// Its command-line programs (manifest.tools, installed with this version) first on PATH, for it and what it runs.
		if len(ps.reg.Tools(id, p.Record.Version)) > 0 {
			s.Env = append(s.Env, "PATH="+ps.reg.ToolPath(id, p.Record.Version, os.Getenv("PATH")))
		}
		for k, v := range rt.Env {
			s.Env = append(s.Env, k+"="+v)
		}
		if rt.HTTP {
			hp.socket = filepath.Join(ps.root, p.Record.StorageKey()+".sock")
			s.HTTP, s.Socket = true, hp.socket
			s.Env = append(s.Env, "PERSONA_PLUGIN_SOCKET="+hp.socket)
		}
		if full := p.Record.Granted.FullHub(); full && !relayed {
			s.Env = append(s.Env, hubEnv...)
		} else if (full || len(p.Record.Granted.Hub) > 0) && ps.h != nil {
			start, err := ps.hubStartEnv(&hubToken, p, full)
			if err != nil {
				return s, err
			}
			s.StartEnv = func() []string { return identity.Environment(start()) }
		}
		if !s.RPC { // no initialize to carry them
			secrets, err := ps.reg.Secrets(ctx, id)
			if err != nil {
				return s, err
			}
			b, _ := json.Marshal(secrets)
			s.Env = append(s.Env, "PERSONA_PLUGIN_CONFIG="+string(ps.reg.Config(p)), "PERSONA_PLUGIN_SECRETS="+string(b))
		}
		ps.voiceSpec(p, &s) // what the hub.json entry it took over had (voice_extension.go)
	}
	builtin := p.Record.Source == extension.SourceBuiltin
	s.Init = func(ctx context.Context) (extension.InitializeParams, error) {
		secrets, err := ps.reg.Secrets(ctx, id)
		if err != nil {
			return extension.InitializeParams{}, err
		}
		info := extension.ExtensionInfo{ID: id, Version: p.Record.Version, Dir: p.Dir}
		if p.Record.Remote == nil {
			info.DataDir = ps.reg.DataDir(id)
		}
		var build int64
		if ps.h != nil {
			build = ps.h.build.Number
		}
		role, tz, locale := "center", "", ""
		if ps.h != nil && ps.h.isMember() {
			role = "member"
		}
		if ps.h != nil {
			tz, locale = ps.h.userTZName(), string(ps.h.userLang())
		}
		params := extension.InitializeParams{Hub: extension.HubInfo{Build: build, ExtensionPoints: extension.PointVersions(), Role: role, Timezone: tz,
			Locale:       locale,
			Capabilities: extension.HubCapabilities()}, Extension: info,
			Config: ps.reg.Config(p), Secrets: secrets, Granted: p.Record.Granted, Active: map[string][]string{}}
		if ps.h != nil && !ps.h.builtin() {
			params.Hub.Manager = ps.h.opts.ManagerID
		}
		if g := p.Record.Granted; !g.FullHub() && len(g.Hub) > 0 && ps.h != nil {
			url, err := ps.hubURL()
			if err != nil {
				return extension.InitializeParams{}, err
			}
			// The program the hub started has it already (StartEnv); one elsewhere gets its first here.
			tok := hubToken.current()
			if tok == "" || p.Record.Remote != nil {
				tok = hubToken.renew(ps, p, false)
			}
			params.Hub.URL, params.Hub.Token, params.Hub.Scopes = url, tok, slices.Clone(g.Hub)
		}
		for _, c := range conns {
			if ps.h == nil || ps.h.connectors.Enabled(c.ID) {
				params.Active["connectors"] = append(params.Active["connectors"], c.ID)
			}
		}
		for _, c := range m.Channels() {
			params.Active["channel"] = append(params.Active["channel"], c.ID)
		}
		for point := range m.Contributes { // the rest, without what the user switched off (docs/extensions/extensions.md "Contribution switches")
			if ids := m.Items(point); len(ids) > 0 && point != "connectors" && point != "channel" {
				params.Active[point] = ids
			}
		}
		if m.Exports != nil {
			hp.contributionsMu.Lock()
			params.ContributionsToMe = ps.reg.ContributionsToContext(ctx, id)
			hp.contributions = params.ContributionsToMe
			hp.contributionsMu.Unlock()
		}
		if err := ps.storageStart(ctx, p); err != nil {
			return extension.InitializeParams{}, storeError(ctx, err)
		}
		return params, nil
	}
	if builtin && id == wechat.ID && ps.h != nil {
		s.Check = sameBuild(ps.h, p.Manifest.Name, s.Command) // the program must be the hub's build
	}
	s.Accept = func(res extension.InitializeResult) error {
		ps.clearBadges(id)
		var said []extension.ConnectorItem
		_ = json.Unmarshal(res.Contributes["connectors"], &said)
		for _, c := range conns {
			i := slices.IndexFunc(said, func(x extension.ConnectorItem) bool { return x.ID == c.ID })
			switch {
			case i < 0:
				ps.logf("plugin %s: the program does not say it has connector %s (the manifest's is used)", id, c.ID)
			case !sameItem(said[i], c):
				ps.logf("plugin %s: the program declares connector %s as %+v, the manifest as %+v (the manifest's is used)", id, c.ID, said[i], c)
			}
		}
		return nil
	}
	byID := map[string]*proc.Conn{}
	s.Handle = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if current := ps.reg.SnapshotExact(id); current == nil || current.Record.Version != p.Record.Version || !current.Record.UpdatedAt.Equal(p.Record.UpdatedAt) {
			return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: "retired extension runtime"}
		}
		if strings.HasPrefix(method, "storage.") {
			// Keep this process's captured epoch/grants. Re-resolving after the
			// preliminary fence could lend a retired run the replacement's scope.
			return ps.storageCall(ctx, p, method, params)
		}
		switch method {
		case "kb.list", "kb.get", "kb.day", "kb.search", "kb.update", "kb.owner", "kb.recent":
			return ps.extensionKBCall(ctx, id, method, params)
		case "assistant.conversations", "assistant.messages": // assistant.read (extension_assistant_read.go)
			return ps.extensionAssistantCall(ctx, id, method, params)
		case "model.complete": // assistantwire.MethodModelComplete (extension_model_calls.go)
			return ps.extensionModelCall(ctx, id, params)
		case connector.MethodSentRecord:
			return ps.extensionSentRecord(ctx, id, params)
		}
		if rest, ok := strings.CutPrefix(method, serve.Namespace+"."); ok {
			c := byID[contributionOf(params)]
			if c == nil && len(byID) == 1 {
				for _, only := range byID {
					c = only
				}
			}
			if c == nil {
				return nil, &rpc.Error{Code: extension.CodeNoContribution, Message: "no connector " + contributionOf(params)}
			}
			return c.Handle(ctx, rest, params)
		}
		if rest, ok := strings.CutPrefix(method, channel.Namespace+"."); ok && ps.h != nil {
			return ps.h.extensionChannelCall(id, rest, params)
		}
		if method == "quota.http" || method == "gateway.http" {
			return ps.quotaHTTP(ctx, id, params)
		}
		if (method == "gateway.launchers" || method == "gateway.open") && ps.h != nil {
			return ps.h.gatewayCall(ctx, id, method, params)
		}
		if method == "usage.gateway_accounts" && ps.h != nil { // the gateways' account lists (extension_usage.go)
			return ps.h.usageGatewayAccounts(ctx, id)
		}
		if method == "nodes.call" && ps.h != nil { // its program on a computer (extension_nodes.go)
			return ps.h.nodesCall(ctx, id, params)
		}
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
	}
	s.OnEvent = func(event string, params json.RawMessage) {
		if current := ps.reg.SnapshotExact(id); current == nil || current.Record.Version != p.Record.Version || !current.Record.UpdatedAt.Equal(p.Record.UpdatedAt) {
			return // A retired run cannot publish through an owner alias or replace newer state.
		}
		if event == "ui.page.snapshot" && ps.h != nil && hp.proc != nil {
			ps.h.pageSnapshot(id, hp.proc, params)
			return
		}
		if event == "api.broadcast" {
			ps.apiBroadcast(id, params)
			return
		}
		if event == "messaging.changed" {
			ps.messagingChanged(id, params)
			return
		}
		if event == "agentsrc.updated" {
			ps.agentSourceUpdated(id, hp, params)
			return
		}
		if event == "ui.badge" {
			ps.setBadge(id, params)
			return
		}
		if event == "ui.invalidate" {
			ps.invalidate(id, params)
			return
		}
		if event == "auth.changed" {
			ps.authChanged(id, params)
			return
		}
		if rest, ok := strings.CutPrefix(event, serve.Namespace+"."); ok {
			if c := byID[contributionOf(params)]; c != nil {
				c.Event(rest, params)
			} else if len(byID) == 1 {
				for _, only := range byID {
					only.Event(rest, params)
				}
			}
		}
	}
	// The connectors reach the process through their links, filled in by apply once the host has started it.
	for _, c := range conns {
		link := &proc.Link{ID: c.ID}
		hp.links = append(hp.links, link)
		var secrets connector.Secrets
		var kbase connector.KnowledgeBase
		if ps.h != nil {
			secrets, kbase = ps.h.secretsFor(c.ID), ps.h.kbFor(c.ID)
		}
		for kind, d := range c.KindDescriptions {
			extensionKinds.Store(c.ID+"/"+kind, d)
		}
		if p.Record.Source == extension.SourceBuiltin {
			extensionConnectors.Delete(c.ID)
		} else {
			extensionConnectors.Store(c.ID, p.Record.Source)
		}
		cfg := proc.Config{ID: c.ID, Name: c.Name, Manifest: ps.knownOnly(id, connectorManifest(c)), Secrets: secrets, KB: kbase,
			Logf: ps.logf, Link: link}
		cfg.RecallNotBefore = hp.recallNotBefore // the program is not asked about a send before this install, whoever asks
		if p.Record.Granted.GhLogin && ps.h != nil {
			cfg.GhToken = ps.h.ghTokenFor
		}
		conn := proc.New(cfg)
		byID[c.ID] = conn
		hp.conns = append(hp.conns, conn)
	}
	return s, nil
}

func contributionOf(params json.RawMessage) string {
	var t extension.Target
	_ = json.Unmarshal(params, &t)
	return t.Contribution
}

func sameItem(a, b extension.ConnectorItem) bool {
	ja, _ := json.Marshal(connectorManifest(a))
	jb, _ := json.Marshal(connectorManifest(b))
	return string(ja) == string(jb)
}

// knownOnly is a plugin connector's manifest without the capabilities this hub does not have (a plugin made for a
// newer hub): the connector runs with the rest, rather than being refused, and each is logged once per plugin.
func (ps *extensions) knownOnly(extensionID string, m connector.Manifest) connector.Manifest {
	kept := m.Capabilities[:0:0]
	for _, cp := range m.Capabilities {
		if connector.Known(cp) {
			kept = append(kept, cp)
		} else if _, logged := unknownCapabilities.LoadOrStore(extensionID+"\x00"+string(cp), true); !logged {
			ps.logf("plugin %s: this hub has no connector capability %q: its connectors run without it", extensionID, cp)
		}
	}
	m.Capabilities = kept
	return m
}

var unknownCapabilities sync.Map

// extensionKinds are the plugin connectors' kinds of inbox things as they describe them: "<connector>/<kind>" → words.
var extensionKinds sync.Map

// extensionConnectors are the connectors a plugin provides (installed, or from hub.json), not a built-in plugin's: their
// new inbox items are triaged before they enter (the manager's inbox_admit.go; a round says it: InboxSource.Extension).
// Connector id → the plugin's
// source (extension.SourceInstalled, extension.SourceHubJSON).
var extensionConnectors sync.Map

// fromExtension says whether a connector (an item's source) is provided by a plugin.
func fromExtension(connectorID string) bool {
	_, ok := extensionConnectors.Load(connectorID)
	return ok
}

// extensionSource is where the plugin providing a connector comes from ("" not a plugin's).
func extensionSource(connectorID string) string {
	v, _ := extensionConnectors.Load(connectorID)
	s, _ := v.(string)
	return s
}

// connectorManifest is a contribution to connectors as the connector registry declares it.
func connectorManifest(c extension.ConnectorItem) connector.Manifest {
	m := serve.Manifest{ID: c.ID, Name: c.Name, Capabilities: slices.Clone(c.Capabilities), Hosts: c.Hosts, AgentTools: c.AgentTools,
		Unlisted: c.Unlisted, ConfirmAlways: c.ConfirmAlways, PollSeconds: c.PollSeconds, Kinds: c.Kinds, ArchiveSources: c.ArchiveSources,
		MetaDescription: c.MetaDescription, CLI: c.CLI}.Of()
	slices.Sort(m.Capabilities)
	return m
}

// hubEnv is what a plugin granted hub_api (and every hub.json one) gets from a manager that is not relayed (the
// built-in one): the hub's address and token on this machine. A relayed manager's get a token of every scope instead
// (hubStartEnv): none of the hub's own, even should its directory hold a hub.json with one.
func (ps *extensions) hubEnv() []string {
	if ps.h == nil || ps.h.cfg == nil || ps.h.relayed() {
		return nil
	}
	// A plugin granted it is configured or accepted by whoever runs the hub, so it gets the hub's own credential to
	// call its API with (the knowledge base, the agents, …): like a node, it is part of the hub.
	return identity.Environment(nil, "PERSONA_HUB_URL="+hubLocalURL(ps.h.cfg.Listen), "PERSONA_HUB_TOKEN="+ps.h.cfg.Token)
}

// hubStartEnv is the hub's address and p's new token of the hub's API at each start of its program (all: of every
// scope; else of the scopes it was granted).
func (ps *extensions) hubStartEnv(tok *startToken, p *extension.Extension, all bool) (func() []string, error) {
	url, err := ps.hubURL()
	if err != nil {
		return nil, err
	}
	return func() []string {
		return []string{"PERSONA_HUB_URL=" + url, "PERSONA_HUB_TOKEN=" + tok.renew(ps, p, all)}
	}, nil
}

// remoteManifest asks a plugin elsewhere for its manifest: initialize over its protocol socket says who it is and
// what it contributes; one serving HTTP only answers GET /manifest with persona-plugin.json.
func (ps *extensions) remoteManifest(ctx context.Context, r extension.Remote) (*extension.Manifest, error) {
	if r.RPCSocket == "" {
		dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", r.HTTPSocket)
		}
		client := &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 10 * time.Second}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://plugin/manifest", nil)
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET /manifest: HTTP %d", res.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(res.Body, extension.ManifestMost+1))
		if err != nil {
			return nil, err
		}
		return extension.Parse(b)
	}
	var d net.Dialer
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	conn, err := d.DialContext(dctx, "unix", r.RPCSocket)
	cancel()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	peer := rpc.New("h", conn)
	go func() { _ = peer.Serve(ctx, bufio.NewReader(conn)) }()
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var m extension.Manifest
	if err := peer.Call(ictx, extension.MManifest, struct{}{}, &m); err != nil {
		return nil, fmt.Errorf("the plugin does not answer manifest: %w", err)
	}
	b, _ := json.Marshal(m)
	return extension.Parse(b)
}

// addExtensionConnector registers a plugin's connector: built-in ones start switched off until the user turns them on,
// installed ones on; the user's switches and settings apply.
func (h *Manager) addExtensionConnector(ctx context.Context, c *proc.Conn, off bool) error {
	if off {
		if h.connectorsOff == nil {
			h.connectorsOff = map[string]bool{}
		}
		h.connectorsOff[c.ID()] = true
	}
	cs := h.connectorSettings(ctx)
	if !h.connectorOn(cs, c.ID()) {
		h.connectors.StartOff(c.ID()) // added after Start, it must not run first
	}
	h.yieldOwnConnector(c.ID())
	if err := h.addConnector(c); err != nil {
		h.restoreOwnConnector(c.ID())
		return err
	}
	h.restoreSwitchesOf(c, cs)
	if c.ID() == "github" {
		h.githubExtensionTaken(ctx, c, cs)
	}
	return nil
}

// forgetConnector deletes what the hub kept for a connector of a removed plugin: its secret and its settings.
func (h *Manager) forgetConnector(ctx context.Context, id string) {
	if h.store == nil {
		return
	}
	if st, ok := h.store.(secretStore); ok {
		if err := st.DeleteConnectorSecret(ctx, id); err != nil {
			h.logf("connector %s: removed, but its sign-in is still stored: %v", id, err)
		}
	}
	var cs connectorSettings
	ok, err := h.store.Setting(ctx, connectorSettingsKey, &cs)
	if err != nil {
		h.logf("connector %s: removed, but its settings could not be read to drop it: %v", id, err)
	}
	if err != nil || !ok {
		return
	}
	delete(cs.Switch, id)
	delete(cs.Options, id)
	for _, f := range []connector.Feed{connector.FeedArchive, connector.FeedInbox} {
		delete(*cs.feeds(f), id)
	}
	if err := h.store.PutSetting(ctx, connectorSettingsKey, cs); err != nil {
		h.logf("connector %s: removed, but its settings are still stored: %v", id, err)
	}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

// hubLocalURL is how a plugin on this machine reaches the hub's API.
func hubLocalURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1:18660"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// roleChanged restarts the plugins that run something for the group's center (connectors, a part of the API): they
// got this hub's role at initialize (extension.HubInfo.Role), and a member leaves that work to the center.
func (ps *extensions) roleChanged(ctx context.Context) {
	if ps == nil {
		return
	}
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Problem != "" || len(p.Manifest.Connectors()) == 0 && len(p.Manifest.APIs()) == 0 {
			continue
		}
		if hp := ps.get(p.Record.ID); hp != nil && hp.proc != nil {
			ps.logf("plugin %s: restarted for the hub's new role in its group", p.Record.ID)
			ps.apply(ctx, p.Record.ID)
		}
	}
}
