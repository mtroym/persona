package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The plugin API (docs/extensions/extensions.md "The hub's API"). /api/v1/plugins/<id>/… is relayed to the plugin, so the hub's own
// routes live under the reserved "-" segment: /api/v1/plugins/-/… and /api/v1/plugins/<id>/-/….

// ExtensionDetail is one plugin with its form, settings and permissions (GET /api/v1/plugins/{id}/-/).
type ExtensionDetail struct {
	ExtensionView
	Publisher       string                     `json:"publisher,omitempty"`
	Homepage        string                     `json:"homepage,omitempty"`
	Origin          string                     `json:"origin,omitempty"`
	PreviousVersion string                     `json:"previous_version,omitempty"`
	ReadOnly        bool                       `json:"read_only,omitempty"` // hub.json's: changed there only
	Form            *extension.Schema          `json:"form,omitempty"`
	Config          json.RawMessage            `json:"config,omitempty"`
	Secrets         []ExtensionSecretView      `json:"secrets,omitempty"`
	Permissions     []extension.PermissionLine `json:"permissions"`
	// UI are the pages it draws, opened from its settings (plugin_page, and nav_page also on the main navigation;
	// POST /api/v1/plugins/{id}/-/ui/{ui}).
	UI []UIPlace `json:"ui,omitempty"`
	// Contributions are every contribution with its switch (docs/extensions/extensions.md "Contribution switches"); contributes
	// (the ids by point) stays as it was.
	Contributions []ContributionView          `json:"contributions,omitempty"`
	Depends       []extension.DependencyState `json:"depends,omitempty"`
	ExtensionPack []extension.PackMember      `json:"extension_pack,omitempty"`
	// NodeMachines is the field of Form that picks the computers its program runs on (an extension with one,
	// docs/extensions/extensions.md "Programs that run on computers"): the apps show that program's state on each (Nodes) in the
	// field's rows.
	NodeMachines string `json:"node_machines,omitempty"`
	// SecretsCleared are the secrets this change deleted (PUT …/-/config of the voice extension moving its address to
	// another host deletes its key, docs/extensions/extensions.md "Built-in voice extension"; a line in Warnings says so too). An
	// error answer carries it too when they went before the configuration could not be saved.
	SecretsCleared []string `json:"secrets_cleared,omitempty"`
}

// ExtensionSecretView is a declared secret and whether it has a value (never the value).
type ExtensionSecretView struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Set         bool   `json:"set"`
	// Hint is a set secret's last four characters, when its spec asks (hint "last4", #5906); never more of it.
	Hint string `json:"hint,omitempty"`
}

func (ps *extensions) detail(ctx context.Context, p *extension.Extension) ExtensionDetail {
	m := p.Full
	d := ExtensionDetail{ExtensionView: ps.view(ctx, p), Publisher: m.Publisher, Homepage: m.Homepage, Origin: p.Record.Origin,
		PreviousVersion: p.Record.PreviousVersion, ReadOnly: p.ReadOnly(), Form: p.Schema, Permissions: extension.ExplainWith(ps.h.contextLang(ctx), m, ps.reg.Config(p)),
		Contributions: ps.contributionViews(p), Depends: ps.reg.DependenciesContext(ctx, m), ExtensionPack: m.ExtensionPack}
	if d.Permissions == nil {
		d.Permissions = []extension.PermissionLine{}
	}
	if m.NodeProgram() != nil { // not cua / ego / imessage: built in, their machines show as they always did
		d.NodeMachines = m.Node.Machines
		d.Form = nodeConfirmForm(ps.h.contextLang(ctx), p.Schema, m)
	}
	for _, u := range ps.uiPlaces("", "") {
		if u.Extension == p.Record.ID && extension.FullPage(u.Placement) {
			d.UI = append(d.UI, u)
		}
	}
	if !p.ReadOnly() { // hub.json's settings may hold keys: never sent
		d.Config = p.Record.Config
		if len(d.Config) == 0 {
			d.Config = json.RawMessage(`{}`)
		}
		set := ps.reg.SecretsSet(ctx, p)
		var plain map[string]string // read only when a secret is shown by its end
		for _, s := range m.Secrets {
			if s.Hint != "" && set[s.Name] && plain == nil {
				plain, _ = ps.reg.Secrets(ctx, p.Record.ID)
			}
			d.Secrets = append(d.Secrets, ExtensionSecretView{Name: s.Name, Title: s.Title, Description: s.Description, Required: s.Required, Set: set[s.Name],
				Hint: extension.SecretHint(s, plain[s.Name])})
		}
	}
	return d
}

// actorOr is actor, or def for the master token over loopback.
func actorOr(r *http.Request, def string) string {
	if principalOf(r.Context()).ID == "" {
		return def
	}
	return actor(r)
}

// actor is who changes a plugin, for its audit.
func actor(r *http.Request) string {
	p := principalOf(r.Context())
	if p.ID == "" {
		return "hub token"
	}
	return "device:" + p.ID
}

// fromThisMachine says whether r carries the master token over loopback: installing runs code on this machine, so
// only its owner there (`personad plugin`) may; a phone may not (docs/extensions/extensions.md "Installing"). The hub judged it and
// says so (extwire.HeaderOwner): its credentials stay with it.
func (h *Manager) fromThisMachine(r *http.Request) bool { return principalOf(r.Context()).Owner }

func readExtensionJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, extension.ConfigMost+extension.SecretMost+4096)).Decode(v); err != nil {
		writeExtensionResponseError(w, r, http.StatusBadRequest, "bad_request", err)
		return false
	}
	return true
}

func (h *Manager) extensionRoutes(api *http.ServeMux) {
	h.extensionUIRoutes(api)
	h.extensionImageRoutes(api)
	h.extensionSystemRoutes(api)
	h.marketRoutes(api)
	h.operationRoutes(api)
	h.upgradeRoutes(api)
	h.auditRoutes(api)
	h.extensionDevRoutes(api)
	h.storageRoutes(api)
	h.sandboxRoutes(api)
	h.contributionRoutes(api)
	h.declaredRoutes(api)
	ps := func() *extensions { return h.extensions }
	h.legacyExtensionRoutes(api, "/api/v1/plugins", "/api/v1/extensions")
	api.HandleFunc("GET /api/v1/extensions", func(w http.ResponseWriter, r *http.Request) {
		key := extKey(r, "extensions", "plugins")
		if ps() == nil {
			writeExtensionJSON(w, r, map[string]any{key: []ExtensionView{}})
			return
		}
		writeExtensionJSON(w, r, map[string]any{key: ps().listCtx(r.Context()), "can_install": ps().reg.HasStore(), "markets": h.marketStore() != nil && ps().reg.HasStore()})
	})
	// The hub's own routes under a plugin: what changes it is for a device with full rights.
	full := func(f func(w http.ResponseWriter, r *http.Request, p *extension.Extension)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if principalOf(r.Context()).Role != roleFull {
				writeExtensionResponseErr(w, r, http.StatusForbidden, i18n.ErrEnglish("extension-status.read_only_device"))
				return
			}
			var p *extension.Extension
			if ps() != nil {
				p = ps().reg.Get(r.PathValue("id"))
			}
			if p == nil {
				writeExtensionResponseError(w, r, http.StatusNotFound, "not_found", errNoExtension)
				return
			}
			r.SetPathValue("id", p.Record.ID) // Owner aliases route to the committed public identity.
			f(w, r, p)
		}
	}
	h.authRoutes(api, full)
	// Installing, upgrading and removing run or delete code on this machine: the master token over loopback only.
	owner := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !h.fromThisMachine(r) {
				writeExtensionResponseError(w, r, http.StatusForbidden, "this_machine_only", i18n.Err("hub-extension_routes.owner_only"))
				return
			}
			if ps() == nil {
				writeExtensionResponseError(w, r, http.StatusServiceUnavailable, "no_plugins", i18n.ErrEnglish("extension-status.not_set_up"))
				return
			}
			f(w, r)
		}
	}
	// marketOr is owner for installing, upgrading, rolling back and removing, or a device with full rights when it is a
	// plugin of a registered market (its package pinned by the market's index; docs/extensions/extensions.md "Extension markets").
	marketOr := func(w http.ResponseWriter, r *http.Request, ofMarket func() bool) bool {
		if ps() == nil {
			writeExtensionResponseError(w, r, http.StatusServiceUnavailable, "no_plugins", i18n.ErrEnglish("extension-status.not_set_up"))
			return false
		}
		if h.fromThisMachine(r) || (principalOf(r.Context()).Role == roleFull && ofMarket()) {
			return true
		}
		writeExtensionResponseError(w, r, http.StatusForbidden, "this_machine_only", i18n.Err("hub-extension_routes.market_only"))
		return false
	}
	api.HandleFunc("GET /api/v1/extensions/{id}/-/{$}", full(func(w http.ResponseWriter, r *http.Request, p *extension.Extension) {
		writeExtensionJSON(w, r, detailOut(r, ps().detail(r.Context(), p)))
	}))
	api.HandleFunc("PUT /api/v1/extensions/{id}/-/enabled", full(func(w http.ResponseWriter, r *http.Request, p *extension.Extension) {
		var in struct {
			Enabled bool `json:"enabled"`
			// Cascade: switching off one others depend on switches them off too (else 409 has_dependents).
			Cascade bool `json:"cascade"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if !in.Enabled && !h.takeDependents(w, r, p.Record.ID, in.Cascade) {
			return
		}
		if err := ps().reg.SetEnabled(r.Context(), p.Record.ID, in.Enabled, actor(r)); err != nil {
			writeExtensionChangeError(w, r, http.StatusConflict, "read_only", err)
			return
		}
		writeExtensionJSON(w, r, detailOut(r, ps().detail(r.Context(), ps().reg.Get(p.Record.ID))))
	}))
	api.HandleFunc("PUT /api/v1/extensions/{id}/-/config", full(func(w http.ResponseWriter, r *http.Request, p *extension.Extension) {
		var in struct {
			Config json.RawMessage `json:"config"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if p.Record.ID == imessageExtensionID { // one Mac only, the other named (imessage_extension.go)
			if fe := h.imessageOneMac(in.Config); fe != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				// The sentence alone as the error too: the web app shows it as is (the Chinese "not saved: …"), not as
				// "machines: …".
				_ = json.NewEncoder(w).Encode(map[string]any{"error": fe.Message, "code": "invalid_config", "field": fe.Path, "message": fe.Message})
				return
			}
		}
		// Secrets bound to the old configuration go first (the voice extension's key, moved to another host:
		// voice_extension.go); said in the answer, an error's too.
		cleared, err := ps().reg.SetConfigClearing(r.Context(), p.Record.ID, in.Config, actor(r))
		if err != nil {
			var fe *extension.FieldError
			if errors.As(err, &fe) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": fe.Text(h.requestLang(r)), "code": "invalid_config", "field": fe.Path, "message": fe.MessageText(h.requestLang(r))})
				return
			}
			if writeSettingsCheckError(w, r, err) {
				return
			}
			status, code := http.StatusConflict, "read_only"
			if errors.Is(err, extension.ErrSecretNotCleared) {
				status, code = http.StatusInternalServerError, "secret_not_cleared"
			}
			if len(cleared) == 0 {
				writeExtensionResponseError(w, r, status, code, err)
				return
			}
			summary := i18n.Localize(err, h.contextLang(r.Context()))
			extra := map[string]any{"secrets_cleared": cleared}
			if i18n.FromRequest(r) != "" {
				var detail string
				summary, detail = extensionErrorCopy(h.requestLang(r), err, "extension-status.failed")
				if detail != "" {
					extra["detail"] = detail
				}
			}
			writeCode(w, status, code, summary, extra)
			return
		}
		d := ps().detail(r.Context(), ps().reg.Get(p.Record.ID))
		if len(cleared) > 0 {
			d.SecretsCleared = cleared
			if ps().isVoice(p) {
				d.Warnings = append(slices.Clone(d.Warnings), i18n.T(h.contextLang(r.Context()), "hub-voice_extension.key_cleared"))
			}
		}
		writeExtensionJSON(w, r, detailOut(r, d))
	}))
	api.HandleFunc("PUT /api/v1/extensions/{id}/-/secrets/{name}", full(func(w http.ResponseWriter, r *http.Request, p *extension.Extension) {
		var in struct {
			Value string `json:"value"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if err := ps().reg.SetSecret(r.Context(), p.Record.ID, r.PathValue("name"), in.Value, actor(r)); err != nil {
			code := "invalid_secret"
			if errors.As(err, new(keyNeedsURLError)) { // the voice extension's key is for an address (voice_extension.go)
				code = "needs_url"
			}
			if writeSettingsCheckError(w, r, err) {
				return
			}
			writeExtensionResponseError(w, r, http.StatusBadRequest, code, err)
			return
		}
		writeExtensionJSON(w, r, detailOut(r, ps().detail(r.Context(), ps().reg.Get(p.Record.ID))))
	}))
	api.HandleFunc("DELETE /api/v1/extensions/{id}/-/secrets/{name}", full(func(w http.ResponseWriter, r *http.Request, p *extension.Extension) {
		if err := ps().reg.DeleteSecret(r.Context(), p.Record.ID, r.PathValue("name"), actor(r)); err != nil {
			writeExtensionResponseError(w, r, http.StatusBadRequest, "invalid_secret", err)
			return
		}
		writeExtensionJSON(w, r, detailOut(r, ps().detail(r.Context(), ps().reg.Get(p.Record.ID))))
	}))

	api.HandleFunc("POST /api/v1/extensions/-/preview", owner(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Source  string `json:"source"`
			Upgrade string `json:"upgrade,omitempty"`
			// Async: answer at once with the operation, and preview in the background (#3277).
			Async bool `json:"async,omitempty"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		kind := opInstall
		if in.Upgrade != "" {
			kind = opUpgrade
		}
		job := previewJob{kind: kind, id: in.Upgrade, actor: opActor(r, "personad extension"), lang: h.requestLang(r),
			run: func(ctx context.Context) (*extension.Preview, error) {
				pv, err := ps().reg.Preview(ctx, in.Source, in.Upgrade)
				if err != nil {
					return nil, err
				}
				h.resolveBundle(extension.WithoutProgress(ctx), pv, "")
				return pv, nil
			}}
		op, pv, err := h.launch(r.Context(), job, nil, in.Async)
		switch {
		case in.Async:
			h.writeOp(w, op)
		case err != nil:
			writeExtensionResponseError(w, r, http.StatusBadRequest, "invalid_plugin", err)
		default:
			writeExtensionJSON(w, r, withOperation(pv.In(h.requestLang(r)), op))
		}
	}))
	api.HandleFunc("POST /api/v1/extensions/-/install", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PreviewID string `json:"preview_id"`
			Digest    string `json:"permissions_digest"`
			// Bundle are the previews that install with it (its preview's bundle), each with its digest.
			Bundle []bundleRef `json:"bundle,omitempty"`
			Async  bool        `json:"async,omitempty"`
		}
		if !readExtensionJSON(w, r, &in) || !marketOr(w, r, func() bool { return h.ofMarketPreview(in.PreviewID, in.Bundle) }) {
			return
		}
		h.installRequest(w, r, "", in.PreviewID, in.Digest, in.Bundle, in.Async, "install_failed")
	})
	api.HandleFunc("POST /api/v1/extensions/{id}/-/upgrade", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PreviewID string      `json:"preview_id"`
			Digest    string      `json:"permissions_digest"`
			Bundle    []bundleRef `json:"bundle,omitempty"`
			Async     bool        `json:"async,omitempty"`
		}
		if !readExtensionJSON(w, r, &in) || !marketOr(w, r, func() bool { return h.ofMarketPreview(in.PreviewID, in.Bundle) }) {
			return
		}
		h.installRequest(w, r, r.PathValue("id"), in.PreviewID, in.Digest, in.Bundle, in.Async, "upgrade_failed")
	})
	fromMarket := func(r *http.Request) func() bool {
		return func() bool {
			p := ps().reg.Get(r.PathValue("id"))
			return p != nil && extension.FromMarket(p.Record.Origin) != ""
		}
	}
	api.HandleFunc("POST /api/v1/extensions/{id}/-/rollback", func(w http.ResponseWriter, r *http.Request) {
		if !marketOr(w, r, fromMarket(r)) {
			return
		}
		var left string // the version rolled back from
		if cur := ps().reg.Get(r.PathValue("id")); cur != nil {
			left = cur.Record.Version
		}
		p, err := ps().reg.Rollback(r.Context(), r.PathValue("id"), actorOr(r, "personad extension"))
		if err != nil {
			writeExtensionResponseError(w, r, http.StatusBadRequest, "rollback_failed", err)
			return
		}
		h.extensionUpgrades().rolledBackByUser(r.Context(), p.Record.ID, left)
		writeExtensionJSON(w, r, detailOut(r, ps().detail(r.Context(), p)))
	})
	api.HandleFunc("DELETE /api/v1/extensions/{id}/-/{$}", func(w http.ResponseWriter, r *http.Request) {
		if !marketOr(w, r, fromMarket(r)) {
			return
		}
		id, q, who := r.PathValue("id"), r.URL.Query(), actorOr(r, "personad extension")
		// cascade=1: the extensions that depend on it are switched off (else 409 has_dependents); members=1: a pack's
		// members go with it.
		h.uninstallOp(w, r, id, uninstallOptions{keepData: q.Get("keep_data") == "1", members: q.Get("members") == "1", cascade: q.Get("cascade") == "1", owner: h.fromThisMachine(r), failureStatus: http.StatusBadRequest, actor: who}, q.Get("async") == "1")
	})

	// A plugin's program on a computer: the app's requests to it (extension_nodes.go).
	api.HandleFunc("/api/v1/extensions/{id}/-/machines/{mid}/", h.serveNodeExtensionHTTP)
	api.HandleFunc("/api/v1/extensions/{id}/", func(w http.ResponseWriter, r *http.Request) {
		// A plugin can do anything the hub can (it may hold its token): not for an answer-only device. An extension
		// gets here with the scope to call this one (extensions.call:<id>).
		if answerOnly(r) {
			writeExtensionResponseErr(w, r, http.StatusForbidden, i18n.ErrEnglish("extension-status.read_only_device"))
			return
		}
		id := r.PathValue("id")
		var hp *hubExtension
		if ps() != nil {
			hp = ps().get(id)
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/extensions/"+id)
		if hp == nil || hp.proxy == nil || hp.proc == nil || strings.HasPrefix(rest, "/-/") {
			writeExtensionResponseError(w, r, http.StatusNotFound, "not_found", errNoExtension)
			return
		}
		end, err := hp.proc.BeginHTTP(r.Context())
		if err != nil {
			writeExtensionResponseErr(w, r, http.StatusBadGateway, fmt.Errorf("plugin %s is not answering: %w", id, err))
			return
		}
		defer end()
		h.setCallerHeaders(r) // who is calling, as the hub found it (extension_caller.go)
		hp.serveExtensionHTTP(w, r)
	})
}

// listCtx is list with the request's context (for what needs the database).
func (ps *extensions) listCtx(ctx context.Context) []ExtensionView {
	out := []ExtensionView{}
	for _, p := range ps.reg.List() {
		out = append(out, ps.view(ctx, p))
	}
	return out
}

// nodeConfirmForm is the form with the computers' field asking before a machine is switched on (x-confirm), unless the
// manifest asks something of its own: switching one on starts a program there (plan "7" "Showing permissions"). A copy:
// the plugin's own schema is shared.
func nodeConfirmForm(l i18n.Lang, s *extension.Schema, m *extension.Manifest) *extension.Schema {
	if s == nil {
		return nil
	}
	out := *s
	out.Fields = slices.Clone(s.Fields)
	for i := range out.Fields {
		if out.Fields[i].Name == m.Node.Machines && out.Fields[i].Confirm == "" {
			out.Fields[i].Confirm = i18n.T(l, "hub-extension_routes.node_confirm", i18n.Args{"name": m.Name})
		}
	}
	return &out
}

// ofMarketPreview says whether preview previewID and the previews of its bundle are a registered market's, which a
// device with full rights may install. An install uses its previews up while its operation still runs. So when the
// registry no longer has previewID, it still counts if a market's install took over the operation that was waiting
// for consent with it (claimOpOfPreview): a second install of it (a double click, two clients) is then
// installing_already while that install runs, and fails as an expired preview after it ended, instead of 403
// this_machine_only. That holds as long as the claimed operation is kept (opKeep, opMost, or until it is deleted);
// then it is 403 again. A bundle member the registry still has must be a market's either way; one that is gone cannot
// be installed. The registry is asked first: an install claims its preview before using it up.
func (h *Manager) ofMarketPreview(previewID string, bundle []bundleRef) bool {
	reg := h.extensions.reg
	ok, elsewhere := reg.PreviewMarketOf(previewID) != "", false
	for _, b := range bundle {
		if reg.PreviewMarketOf(b.PreviewID) == "" {
			ok = false
			elsewhere = elsewhere || reg.PreviewOf(b.PreviewID) != nil
		}
	}
	return ok || (!elsewhere && reg.PreviewOf(previewID) == nil && h.claimedByMarket(previewID))
}

// installRequest is an install (upgrade "") or upgrade call as an operation: the one its preview made, or a new one.
// async answers 202 with it at once; else the answer is the extension's detail, as it always was.
func (h *Manager) installRequest(w http.ResponseWriter, r *http.Request, upgrade, previewID, digest string, refs []bundleRef, async bool, code string) {
	ps := h.extensions
	actor := actorOr(r, "personad extension")
	op, err := h.opForInstall(ps.reg.PreviewOf(previewID), previewID, opActor(r, actor), h.requestLang(r))
	if err != nil {
		writeExtensionResponseError(w, r, http.StatusConflict, "installing_already", err)
		return
	}
	if async {
		ctx := context.WithoutCancel(r.Context())
		go func() { _, _ = h.installOp(ctx, op, upgrade, previewID, digest, refs, actor) }()
		h.writeOp(w, op)
		return
	}
	p, err := h.installOp(r.Context(), op, upgrade, previewID, digest, refs, actor)
	if err != nil {
		writeExtensionChangeError(w, r, http.StatusBadRequest, code, err)
		return
	}
	writeExtensionJSON(w, r, withOperation(detailOut(r, ps.detail(r.Context(), p)), op))
}
