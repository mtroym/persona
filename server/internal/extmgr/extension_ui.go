package extmgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/proc"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/quota"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Declarative UI and events (docs/extensions/extensions.md "Declarative UI" "Events"). The apps ask the hub what plugins draw where
// (GET /api/v1/plugins/-/ui) and for one of them (POST /api/v1/plugins/{id}/-/ui/{ui}); the hub asks the plugin
// (ui.render, ui.action), holds the View to what the apps draw (extension.CheckView) and gives it to them. A launcher
// section may reach the launcher's gateway with its key the way quota does (gateway.http), never seeing the key. An
// inbox item's page is drawn from that item (context.item), only for the items of the plugin's own connector.

const uiTimeout = 20 * time.Second

// UIPlace is one thing a plugin draws, as the apps list it.
type UIPlace struct {
	Extension     string `json:"extension"`
	ExtensionName string `json:"extension_name,omitempty"`
	ID            string `json:"id"`
	Title         string `json:"title"`
	Placement     string `json:"placement"`
	Size          string `json:"size,omitempty"`
	// Icon: a nav_page's (extension.UIIcons); Opens: the page an overview_card opens; Badge: on a nav_page's entry;
	// Connector: the connector whose items an inbox_item draws.
	Icon      string           `json:"icon,omitempty"`
	Opens     string           `json:"opens,omitempty"`
	Badge     *extension.Badge `json:"badge,omitempty"`
	Connector string           `json:"connector,omitempty"`
}

// uiPlaces are the plugins' UI of placement (every slot's but launcher sections' when it is empty); for launcher
// sections, those whose hosts have the gateway at base.
func (ps *extensions) uiPlaces(placement, base string) []UIPlace {
	out := []UIPlace{}
	if ps == nil {
		return out
	}
	host := ""
	if base != "" {
		if u, err := quota.Root(base); err == nil {
			host = u.Hostname()
		}
	}
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Problem != "" {
			continue
		}
		hp := ps.get(p.Record.ID)
		if hp == nil || hp.proc == nil {
			continue
		}
		for _, u := range p.Manifest.UIs() {
			slot, known := extension.Slot(u.Placement)
			switch {
			case !known:
				continue
			case placement == "" && (u.Placement == "launcher_section" || u.Placement == "widget"):
				continue // a launcher's sections are asked for with the launcher; widgets are listed on their own
			case placement != "" && u.Placement != placement && !(extension.PageSlot(placement) && extension.PageSlot(u.Placement)):
				continue
			case u.Placement == "launcher_section" && (host == "" || (len(u.Hosts) > 0 && !hostMatches(host, u.Hosts)) || !p.Record.Granted.LauncherKeys):
				continue
			case u.Placement == "inbox_item" && !ps.servesConnector(hp, u.Connector):
				continue
			}
			place := UIPlace{Extension: p.Record.ID, ExtensionName: p.Manifest.Name, ID: u.ID, Title: u.Title, Placement: u.Placement,
				Size: slot.Size, Icon: u.Icon, Opens: u.Opens, Connector: u.Connector}
			if extension.PageSlot(u.Placement) {
				place.Placement = "extension_page" // the old paths answer plugin_page (legacyUIPlaces)
			}
			if slot.Badge {
				if b, ok := ps.badges.Load(p.Record.ID + "/" + u.ID); ok {
					badge := b.(extension.Badge)
					place.Badge = &badge
				}
			}
			out = append(out, place)
		}
	}
	return out
}

// servesConnector: the connector registered as id is hp's own (the plugin brought it, or took it over from the hub).
func (ps *extensions) servesConnector(hp *hubExtension, id string) bool {
	if ps.h == nil || ps.h.connectors == nil || id == "" {
		return false
	}
	c := ps.h.connectors.Get(id)
	return c != nil && slices.ContainsFunc(hp.conns, func(own *proc.Conn) bool { return connector.Connector(own) == c })
}

// uiItem is inbox item id as an inbox_item is drawn for it, and the connector it came from (its source). The item is
// read as its JSON (the manager's, inbox.item), not its fields: meta and the source blocks go to the plugin as they
// are stored. False while the manager can't say: the app draws its own body.
func (h *Manager) uiItem(id string) (*extension.UIInboxItem, string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, ok := h.inboxItemRaw(ctx, id)
	if !ok {
		return nil, "", false
	}
	return uiItemOf(raw)
}

// uiItemOf reads an inbox item's JSON as an inbox_item gets it, and its source.
func uiItemOf(raw []byte) (*extension.UIInboxItem, string, bool) {
	var row struct {
		ID      string          `json:"id"`
		Source  string          `json:"source"`
		Title   string          `json:"title"`
		Summary string          `json:"summary"`
		Tier    string          `json:"tier"`
		Reason  string          `json:"reason"`
		DoneAt  json.RawMessage `json:"done_at"`
		Meta    json.RawMessage `json:"meta"`
		// Count and StartedAt: for an event, the inbox keeps both itself (the manager's inbox_group.go).
		Count     int       `json:"count"`
		StartedAt time.Time `json:"started_at"`
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(raw, &row) != nil || json.Unmarshal(raw, &all) != nil {
		return nil, "", false
	}
	out := &extension.UIInboxItem{ID: row.ID, Title: row.Title, Summary: row.Summary, Tier: row.Tier, Reason: row.Reason,
		Done: len(row.DoneAt) > 0 && string(row.DoneAt) != "null", Meta: row.Meta, Count: row.Count, StartedAt: row.StartedAt}
	for _, k := range uiItemBlocks {
		if b, ok := all[k]; ok && string(b) != "null" {
			if out.Extra == nil {
				out.Extra = map[string]json.RawMessage{}
			}
			out.Extra[k] = b
		}
	}
	return out, row.Source, true
}

// uiItemBlocks are an inbox item's source blocks an inbox_item gets as extra.
var uiItemBlocks = []string{"lark", "doc", "moment", "mail", "connector"}

// uiRequest is what the apps send: render (no action) or an action, with where it is drawn, for whom, and the
// plugin's state.
type uiRequest struct {
	// lang is set only by an explicit HTTP preference; older clients retain their body locale.
	lang         i18n.Lang
	nativeResult *json.RawMessage // HTTP-only fitted source-presence result, not guest input
	Context      struct {
		Launcher string `json:"launcher,omitempty"`
		Machine  string `json:"machine,omitempty"`
		// Item is the inbox item an inbox_item is drawn for.
		Item     string `json:"item,omitempty"`
		TZ       string `json:"tz,omitempty"`
		Locale   string `json:"locale,omitempty"`
		Platform string `json:"platform,omitempty"`
		Size     string `json:"size,omitempty"`
		// UIVersion is the UI version the app draws: 1 (the first eight items) when it says nothing.
		UIVersion int `json:"ui_version,omitempty"`
	} `json:"context"`
	State  string            `json:"state,omitempty"`
	Action string            `json:"action,omitempty"`
	Values map[string]string `json:"values,omitempty"`
}

// uiFallbackLang keeps legacy requests on the user's language while matching explicit UI locales.
func (h *Manager) uiFallbackLang(ctx context.Context, locale string) i18n.Lang {
	if locale == "" {
		return h.contextLang(ctx)
	}
	return i18n.Resolve(locale)
}

var localePattern = lazyre.New(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{1,8}){0,4}$`)

func uiLocaleMatches(l i18n.Lang, tag string) bool {
	primary, _, _ := strings.Cut(strings.ReplaceAll(tag, "_", "-"), "-")
	want, _, _ := strings.Cut(string(l), "-")
	return strings.EqualFold(primary, want) && i18n.Resolve(tag) == l
}

// uiParams are what the plugin is told of a request to draw item: the app's context held to what is known (a time
// zone that loads, the user's when the app says none; a locale that looks like one; a known platform and size).
func (h *Manager) uiParams(item extension.UIItem, slot extension.UISlot, in uiRequest) extension.UIParams {
	c := in.Context
	out := extension.UIParams{Contribution: item.ID, State: in.State, Placement: item.Placement, Size: slot.Size,
		UIVersion: min(max(c.UIVersion, 1), extension.UIVersion)}
	if c.Size == extension.SizeCompact || c.Size == extension.SizeFull {
		out.Size = c.Size
	}
	if _, err := time.LoadLocation(c.TZ); c.TZ != "" && len(c.TZ) <= 64 && err == nil {
		out.TZ = c.TZ
	} else {
		out.TZ = h.userTZName()
	}
	if len(c.Locale) <= 35 && localePattern.MatchString(c.Locale) {
		out.Locale = c.Locale
	}
	if slices.Contains(extension.UIPlatforms, c.Platform) {
		out.Platform = c.Platform
	}
	if in.Action != "" {
		// a multichoice the hub drew an older app as toggles comes back as UI version 6 writes it (#5464)
		out.Action, out.Values = in.Action, extension.CollectValues(in.Values)
	}
	return out
}

// withUI checks the same contribution, context and permissions for every UI path.
// The callback runs while temporary gateway capabilities remain scoped and live.
func (h *Manager) withUI(ctx context.Context, id, uiID string, in uiRequest, call func(*extension.Extension, *hubExtension, extension.UIItem, extension.UISlot, extension.UIParams) (extension.ActionResult, error)) (extension.ActionResult, error) {
	var res extension.ActionResult
	p := h.extensions.reg.Get(id)
	hp := h.extensions.get(id)
	if p == nil || !p.Record.Enabled || hp == nil || hp.proc == nil {
		return res, errNoExtension
	}
	var item *extension.UIItem
	for _, u := range p.Manifest.UIs() {
		if u.ID == uiID {
			u := u
			item = &u
		}
	}
	if item == nil {
		return res, i18n.Err("hub-extension_ui.no_ui", i18n.Args{"id": id, "ui": uiID})
	}
	slot, known := extension.Slot(item.Placement)
	if !known {
		return res, i18n.Err("hub-extension_ui.no_placement", i18n.Args{"placement": item.Placement})
	}
	if item.Placement == "widget" {
		return res, errUseWidget
	}
	params := h.uiParams(*item, slot, in)
	// Older clients may send their system locale in the body. The request's UI language wins when supplied.
	if l := in.lang; i18n.Valid(l) && !uiLocaleMatches(l, params.Locale) {
		params.Locale = string(l)
	} else if params.Locale == "" {
		params.Locale = string(h.contextLang(ctx))
	}
	switch item.Placement {
	case "launcher_section":
		if in.Context.Launcher == "" {
			return res, i18n.Err("hub-extension_ui.needs_launcher")
		}
		if !p.Record.Granted.LauncherKeys {
			return res, i18n.Err("hub-extension_ui.no_launcher_keys")
		}
		gw, err := h.launcherGateway(ctx, in.Context.Launcher, in.Context.Machine)
		if err != nil {
			return res, err
		}
		root, err := quota.Root(gw.base)
		if err != nil || (len(item.Hosts) > 0 && !hostMatches(root.Hostname(), item.Hosts)) {
			return res, i18n.Err("hub-extension_ui.not_its_gateway")
		}
		var b [12]byte
		_, _ = rand.Read(b[:])
		qid := hex.EncodeToString(b[:])
		quotaQueries.Store(qid, &quotaQuery{extension: id, send: gw.send})
		defer quotaQueries.Delete(qid)
		params.Launcher = &extension.UILauncher{Name: in.Context.Launcher, BaseURL: root.String(), QueryID: qid}
	case "machine_section":
		m := h.machineByID(in.Context.Machine)
		if m == nil && in.Context.Machine != "" {
			m = h.machineByName(in.Context.Machine)
		}
		if m == nil {
			return res, errNoMachine
		}
		m.mu.Lock()
		params.Machine = &extension.UIMachine{ID: m.cfg.ID, Name: m.cfg.Name, OS: m.info.OS, Online: m.online}
		m.mu.Unlock()
	case "inbox_item":
		if in.Context.Item == "" {
			return res, errNoItem
		}
		if !h.extensions.servesConnector(hp, item.Connector) {
			return res, errNotServed
		}
		it, source, ok := h.uiItem(in.Context.Item)
		if !ok {
			return res, errNoItem
		}
		if source != item.Connector {
			return res, errNotItsItem
		}
		params.Item = it
	}
	return call(p, hp, *item, slot, params)
}

func (h *Manager) uiCall(ctx context.Context, id, uiID string, in uiRequest) (extension.ActionResult, error) {
	return h.withUI(ctx, id, uiID, in, func(p *extension.Extension, hp *hubExtension, item extension.UIItem, slot extension.UISlot, params extension.UIParams) (extension.ActionResult, error) {
		var res extension.ActionResult
		if result, handled, err := h.pageLegacy(ctx, p, hp, item, slot, params, in); handled {
			return result, err
		}
		method := "ui.render"
		if in.Action != "" {
			method = "ui.action"
		}
		cctx, cancel := context.WithTimeout(ctx, uiTimeout)
		defer cancel()
		var out json.RawMessage
		if err := hp.proc.Call(cctx, method, params, &out); err != nil {
			var re *rpc.Error
			if errors.As(err, &re) {
				if re.Code == extension.CodeUnsupported {
					return res, errUnsupportedGateway
				}
				return res, errors.New(re.Message)
			}
			return res, err
		}
		fit := func(v *extension.View) error {
			dropped, err := extension.Fit(v, slot, params.UIVersion, h.uiFallbackLang(ctx, params.Locale))
			if len(dropped) > 0 && params.UIVersion == extension.UIVersion {
				h.extensions.logf("plugin %s: ui %s drew items this hub does not know (%s): left out", id, uiID, strings.Join(dropped, ", "))
			}
			if err != nil {
				return i18n.Err("hub-extension_ui.view_unsupported", i18n.Args{"err": err.Error()})
			}
			return nil
		}
		if method == "ui.render" {
			var v extension.View
			if err := json.Unmarshal(out, &v); err != nil {
				return res, i18n.Err("hub-extension_ui.view_unsupported", i18n.Args{"err": err.Error()})
			}
			if err := fit(&v); err != nil {
				return res, err
			}
			if err := checkPushes(p.Manifest, item, &v, nil); err != nil {
				return res, err
			}
			if err := h.signPictures(id, uiID, params.UIVersion, &v, nil); err != nil {
				return res, err
			}
			res.View = &v
			return res, nil
		}
		if err := json.Unmarshal(out, &res); err != nil {
			return res, err
		}
		if res.View != nil {
			if err := fit(res.View); err != nil {
				return extension.ActionResult{}, err
			}
		}
		if err := extension.FitResult(&res, slot, params.UIVersion, h.uiFallbackLang(ctx, params.Locale)); err != nil {
			return extension.ActionResult{}, i18n.Err("hub-extension_ui.view_unsupported", i18n.Args{"err": err.Error()})
		}
		if err := checkPushes(p.Manifest, item, &extension.View{Sections: []extension.Section{{Items: res.Items}}}, nil); err != nil {
			return extension.ActionResult{}, err
		}
		if err := checkPushes(p.Manifest, item, res.View, res.Push); err != nil {
			return extension.ActionResult{}, err
		}
		if err := h.signPictures(id, uiID, params.UIVersion, res.View, res.Items); err != nil {
			return extension.ActionResult{}, err
		}
		if params.UIVersion < 3 {
			res.Push, res.Pop = nil, false // an older app knows neither: it draws the view it got, or draws again
		}
		if err := checkOpenURL(id, item, in.Context.Item, res.OpenURL); err != nil {
			return extension.ActionResult{}, err
		}
		return res, nil

	})
}

// checkPushes holds the pages a view's actions (and an action's answer) push to the extension's own whole pages: a
// push names one of its ui contributions that is an extension_page or a nav_page, or none for the one drawing (which
// must then be a whole page itself: a card or a machine's section opens a page by name).
func checkPushes(m *extension.Manifest, from extension.UIItem, v *extension.View, also *extension.Push) error {
	pushes := []*extension.Push{also}
	for _, a := range extension.Actions(v) {
		pushes = append(pushes, a.Push)
	}
	for _, p := range pushes {
		if p == nil {
			continue
		}
		if len(p.State) > extension.StateMost {
			return i18n.Err("hub-extension_ui.state_too_large", i18n.Args{"max": extension.StateMost})
		}
		target := from
		if p.UI != "" {
			i := slices.IndexFunc(m.UIs(), func(u extension.UIItem) bool { return u.ID == p.UI })
			if i < 0 {
				return i18n.Err("hub-extension_ui.push_not_its_ui", i18n.Args{"ui": p.UI})
			}
			target = m.UIs()[i]
		}
		if !extension.FullPage(target.Placement) {
			return i18n.Err("hub-extension_ui.push_not_page", i18n.Args{"ui": target.ID})
		}
	}
	return nil
}

// checkOpenURL holds what extension id's action opens to https; an inbox_item may also open its item's own source page
// (extension.InboxSourceURL), that item's only, and any of its pages its own settings (extension.SettingsURL), its own
// only.
func checkOpenURL(id string, ui extension.UIItem, itemID, raw string) error {
	if raw == "" || raw == extension.SettingsURL(id) {
		return nil
	}
	if ui.Placement == "inbox_item" && itemID != "" && raw == extension.InboxSourceURL(itemID) {
		return nil
	}
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" || u.Host == "" {
		if strings.HasPrefix(raw, "persona:") {
			return i18n.Err("hub-extension_ui.open_not_source")
		}
		return i18n.Err("hub-extension_ui.open_not_https")
	}
	return nil
}

var (
	errNoExtension = i18n.ErrEnglish("extension-status.not_found")
	// errUnsupportedGateway: a launcher section without hosts whose plugin says the launcher's gateway is not its own;
	// the apps leave the section out.
	errUnsupportedGateway = i18n.ErrEnglish("extension-status.unsupported_gateway")
	// errNoMachine: a machine section for a machine the hub does not have.
	errNoMachine = i18n.Err("hub-extension_ui.no_machine")
	// errNoItem: an inbox item's page without the item, or for one the inbox does not have.
	errNoItem = i18n.Err("hub-extension_ui.no_item")
	// errNotItsItem: an inbox item's page for an item of another source than its connector.
	errNotItsItem = i18n.Err("hub-extension_ui.not_its_item")
	// errNotServed: an inbox item's page whose connector is not the plugin's now (not registered, or another's); the
	// apps draw the item's own detail.
	errNotServed = i18n.Err("hub-extension_ui.not_served")
)

func (h *Manager) extensionUIRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/extensions/-/ui", func(w http.ResponseWriter, r *http.Request) {
		placement := r.URL.Query().Get("placement")
		base := ""
		if name := r.URL.Query().Get("launcher"); name != "" && placement == "launcher_section" {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			gw, err := h.launcherGateway(ctx, name, r.URL.Query().Get("machine"))
			cancel()
			if err == nil {
				base = gw.base
			}
		}
		writeJSON(w, map[string]any{"ui": uiPlacesOut(r, h.extensions.uiPlaces(placement, base))})
	})
	api.HandleFunc("POST /api/v1/extensions/{id}/-/ui/{ui}", func(w http.ResponseWriter, r *http.Request) {
		if principalOf(r.Context()).Role != roleFull {
			writeExtensionResponseErr(w, r, http.StatusForbidden, i18n.ErrEnglish("extension-status.read_only_device"))
			return
		}
		var in uiRequest
		if !readExtensionJSON(w, r, &in) {
			return
		}
		in.lang = i18n.FromRequest(r)
		var nativeResult json.RawMessage
		in.nativeResult = &nativeResult
		res, err := h.uiCall(r.Context(), r.PathValue("id"), r.PathValue("ui"), in)
		switch {
		case errors.Is(err, errNoExtension), errors.Is(err, errNoMachine), errors.Is(err, errNoItem):
			writeExtensionResponseError(w, r, http.StatusNotFound, "not_found", err)
		case errors.Is(err, errUnsupportedGateway), errors.Is(err, errNotServed):
			writeExtensionResponseError(w, r, http.StatusNotFound, "unsupported", err)
		case errors.Is(err, errNotItsItem):
			writeExtensionResponseError(w, r, http.StatusForbidden, "forbidden", err)
		case errors.Is(err, errUseWidget):
			writeExtensionResponseError(w, r, http.StatusBadRequest, "bad_request", err)
		case errors.Is(err, errOffline):
			writeExtensionResponseError(w, r, http.StatusServiceUnavailable, "offline", err)
		case err != nil:
			writeExtensionResponseError(w, r, http.StatusBadGateway, "plugin_failed", err)
		default:
			if len(nativeResult) > 0 {
				writeJSON(w, nativeResult)
			} else {
				writeJSON(w, res)
			}
		}
	})
}

// setBadge takes a plugin's ui.badge: the badge on one of its nav_pages' entries, told to the apps (plugin_ui).
func (ps *extensions) setBadge(id string, params json.RawMessage) {
	p := ps.reg.Get(id)
	var ev extension.BadgeEvent
	if p == nil || json.Unmarshal(params, &ev) != nil {
		return
	}
	i := slices.IndexFunc(p.Manifest.UIs(), func(u extension.UIItem) bool { return u.ID == ev.Contribution })
	if i < 0 {
		ps.logf("plugin %s: ui.badge for %q, which it does not have: dropped", id, ev.Contribution)
		return
	}
	if slot, _ := extension.Slot(p.Manifest.UIs()[i].Placement); !slot.Badge {
		ps.logf("plugin %s: ui.badge for %s, which has no entry to badge: dropped", id, ev.Contribution)
		return
	}
	if err := extension.CheckBadge(ev.Text, ev.Tone); err != nil {
		ps.logf("plugin %s: ui.badge for %s: %v: dropped", id, ev.Contribution, err)
		return
	}
	key := id + "/" + ev.Contribution
	var badge *extension.Badge
	if ev.Text == "" {
		if _, had := ps.badges.LoadAndDelete(key); !had {
			return
		}
	} else {
		b := extension.Badge{Text: ev.Text, Tone: ev.Tone}
		if old, had := ps.badges.Swap(key, b); had && old.(extension.Badge) == b {
			return
		}
		badge = &b
	}
	if ps.h != nil {
		ps.h.broadcastExtension("ui", id, map[string]any{"id": ev.Contribution, "badge": badge})
	}
}

// invalidations are ui.invalidate events waiting for their second: what each extension said changed, told to the
// apps together (extension_ui {invalidate}) a second after the first, so an extension changing a lot at once is one
// redraw. Only the new event: the apps that know plugin_ui take it for a badge.
type invalidations struct {
	mu      sync.Mutex
	pending map[string]map[string]bool // extension → its ui ids ("" all of them)
}

// invalidateWait is how long ui.invalidate events are gathered before the apps are told.
var invalidateWait = time.Second

// invalidate takes an extension's ui.invalidate: what it draws changed, so the apps showing it draw it again.
func (ps *extensions) invalidate(id string, params json.RawMessage) {
	p := ps.reg.Get(id)
	var ev extension.InvalidateEvent
	if p == nil || (len(params) > 0 && string(params) != "null" && json.Unmarshal(params, &ev) != nil) {
		return
	}
	if ev.Contribution != "" && !slices.ContainsFunc(p.Manifest.UIs(), func(u extension.UIItem) bool { return u.ID == ev.Contribution }) {
		ps.logf("extension %s: ui.invalidate for %q, which it does not have: dropped", id, ev.Contribution)
		return
	}
	in := &ps.invalid
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.pending == nil {
		in.pending = map[string]map[string]bool{}
	}
	ids, waiting := in.pending[id]
	if !waiting {
		ids = map[string]bool{}
		in.pending[id] = ids
		time.AfterFunc(invalidateWait, func() { ps.tellInvalid(id) })
	}
	ids[ev.Contribution] = true
}

// tellInvalid tells the apps what extension id said changed in the last second.
func (ps *extensions) tellInvalid(id string) {
	in := &ps.invalid
	in.mu.Lock()
	ids := in.pending[id]
	delete(in.pending, id)
	in.mu.Unlock()
	if ps.h == nil || len(ids) == 0 {
		return
	}
	ps.h.widgetsChanged(id, ids)
	if ids[""] {
		ps.h.broadcast(map[string]any{"type": "extension_ui", "extension": id, "invalidate": true})
		return
	}
	for _, ui := range slices.Sorted(maps.Keys(ids)) {
		ps.h.broadcast(map[string]any{"type": "extension_ui", "extension": id, "id": ui, "invalidate": true})
	}
}

// clearBadges forgets a plugin's badges: it (re)starts, and says them again.
func (ps *extensions) clearBadges(id string) {
	ps.badges.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), id+"/") {
			ps.badges.Delete(k)
		}
		return true
	})
}

// emit tells the plugins subscribed to topic (running ones: events need on_startup) about it; never waits.
func (ps *extensions) emit(topic string, payload map[string]any) {
	if ps == nil {
		return
	}
	for _, id := range ps.reg.EventSubscribers(topic, extension.EventScopes[topic]) {
		if hp := ps.get(id); hp != nil && hp.proc != nil {
			_ = hp.proc.Notify("event."+topic, payload)
		}
	}
}
