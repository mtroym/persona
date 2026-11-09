package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The machines page: what hub.json lists and how each one is doing (with every path the center has to it, and the
// computers it reaches through members). hub.json's machines are read when the hub starts, so adding or removing one
// is written to hub.json and waits for a restart. New computers come in two ways (docs/archive/operations/plan-hub-center.md 4): the
// computer dials the hub (kind link: a persona-link pairing link), or the hub dials the computer's connector (kind
// service: its persona://connector link pasted here). ssh is not offered any more; hub.json entries of kind ssh keep
// working.

var adminMachineID = lazyre.New(`^[a-z0-9][a-z0-9._-]{0,62}$`)

type adminMachine struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Kind         string             `json:"kind"`
	Icon         string             `json:"icon"`
	SSHHost      string             `json:"ssh_host,omitempty"`
	Online       bool               `json:"online"`
	Error        string             `json:"error,omitempty"`
	Transport    string             `json:"transport,omitempty"`
	NodeVersion  string             `json:"node_version,omitempty"`
	NodeMismatch bool               `json:"node_mismatch,omitempty"`
	OS           string             `json:"os,omitempty"`
	LastSeen     *time.Time         `json:"last_seen,omitempty"`
	Link         *proto.MachineLink `json:"link,omitempty"`
	Tailcat      bool               `json:"tailcat,omitempty"`
	Address      string             `json:"address,omitempty"` // service: the connector's host:port
	Proxy        string             `json:"proxy,omitempty"`   // service: the socks5 proxy it is dialed through
	HubID        string             `json:"hub_id,omitempty"`  // the hub it is reached through now
	Paths        []pathInfo         `json:"paths,omitempty"`   // every way to it (devices/paths.go)
	// State: active (running), pending_add (in hub.json, connects after a restart), pending_remove (gone from
	// hub.json, still running until the restart).
	State string `json:"state"`
}

func adminMachineFrom(m proto.Machine, state string) adminMachine {
	a := adminMachine{ID: m.ID, Name: m.Name, Kind: m.Kind, Icon: m.Icon, SSHHost: m.SSHHost, Online: m.Online, Error: m.Error,
		Transport: m.Transport, NodeVersion: m.NodeVersion, NodeMismatch: m.NodeMismatch, OS: m.OS, Link: m.Link,
		Tailcat: m.Tailcat, State: state}
	if !m.LastSeen.IsZero() {
		at := m.LastSeen
		a.LastSeen = &at
	}
	return a
}

func (h *Hub) adminMachines() []adminMachine {
	out := []adminMachine{}
	disk, _ := h.diskConfig()
	onDisk := map[string]bool{}
	if disk != nil {
		for _, m := range disk.Machines {
			onDisk[m.ID] = true
		}
	}
	listed := map[string]bool{}
	for _, m := range h.machineList() {
		listed[m.ID] = true
		state := "active"
		mm := h.localMachine(m.ID)
		if disk != nil && !onDisk[m.ID] && (mm == nil || !mm.derived) {
			state = "pending_remove"
		}
		a := adminMachineFrom(m, state)
		a.HubID = m.HubID
		if mm != nil {
			a.Address, a.Proxy, a.Paths = mm.cfg.Address, redactProxy(mm.cfg.Proxy), h.pathInfos(mm)
		}
		out = append(out, a)
	}
	// Entries the hub has that the manager has not reported (yet): connecting. An entry whose computer another one
	// has is that one's.
	for _, mc := range h.knownEntries() {
		if listed[mc.ID] || h.machineByID(mc.ID) != nil {
			continue
		}
		icon := mc.Icon
		if icon == "" {
			icon = "laptop"
		}
		out = append(out, adminMachine{ID: mc.ID, Name: mc.Name, Kind: mc.Kind, Icon: icon, SSHHost: mc.SSHHost,
			Tailcat: mc.TailcatAddr != "", Address: mc.Address, Proxy: redactProxy(mc.Proxy), State: "active"})
	}
	if disk != nil {
		for _, m := range disk.Machines {
			if h.knownEntry(m.ID) != nil {
				continue
			}
			icon := m.Icon
			if icon == "" {
				icon = "laptop"
			}
			out = append(out, adminMachine{ID: m.ID, Name: m.Name, Kind: m.Kind, Icon: icon, SSHHost: m.SSHHost,
				Tailcat: m.TailcatAddr != "", Address: m.Address, Proxy: redactProxy(m.Proxy), State: "pending_add"})
		}
	}
	return out
}

func (h *Hub) adminMachineRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/machines", func(w http.ResponseWriter, r *http.Request) {
		// "tls": whether a Persona Link can be paired (the hub's TLS port, or an HTTPS reverse proxy in front of it).
		writeJSON(w, map[string]any{"machines": h.adminMachines(), "tls": h.linkPairable(), "restart": h.pendingRestart(h.requestLang(r))})
	})
	mux.HandleFunc("POST /api/v1/admin/machines", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			// Local machines execute on this registered manager, never on the carrier first.
			Manager  string         `json:"manager"`
			Config   *MachineConfig `json:"config"`
			Kind     string         `json:"kind"`
			ID       string         `json:"id"`
			Name     string         `json:"name"`
			Icon     string         `json:"icon"`
			PairLink string         `json:"pair_link"` // service: the connector's persona://connector link
			Address  string         `json:"address"`   // service: host:port, when not the link's
			Proxy    string         `json:"proxy"`     // service: socks5://host:port
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		mc := MachineConfig{ID: strings.TrimSpace(in.ID), Name: strings.TrimSpace(in.Name), Kind: in.Kind, Icon: in.Icon}
		if !adminMachineID.MatchString(mc.ID) {
			writeErrCode(w, http.StatusBadRequest, "invalid_id", i18n.Err("hub-admin_machines.invalid_id"))
			return
		}
		switch mc.Kind {
		case "local":
			if in.Config != nil {
				local := *in.Config
				local.ID, local.Name, local.Kind, local.Icon = mc.ID, mc.Name, mc.Kind, mc.Icon
				mc = local
			}
		case "ssh", "tailcat":
			writeErrCode(w, http.StatusBadRequest, "ssh_unsupported", i18n.Err("hub-admin_machines.ssh_unsupported"))
			return
		case kindLink:
		case kindService:
			if h.localMachine(mc.ID) != nil || h.knownEntry(mc.ID) != nil {
				writeErrCode(w, http.StatusConflict, "exists", i18n.Err("hub-admin_machines.exists"))
				return
			}
			proxyURL := strings.TrimSpace(in.Proxy)
			if proxyURL != "" {
				if err := validProxy(proxyURL); err != nil {
					writeErrCode(w, http.StatusBadRequest, "invalid_proxy", err)
					return
				}
			}
			ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
			addr, _, err := h.pairConnector(ctx, in.PairLink, mc.ID, in.Address, proxyURL)
			cancel()
			if err != nil {
				writeErrCode(w, http.StatusBadGateway, "pair_failed", err)
				return
			}
			mc.Address, mc.Proxy = addr, proxyURL
		default:
			writeErrCode(w, http.StatusBadRequest, "invalid_kind", errors.New("kind: local, link or service"))
			return
		}
		var addErr error
		if mc.Kind == "local" {
			addErr = h.addOwnedLocalMachine(r.Context(), &mc, strings.TrimSpace(in.Manager))
		} else if in.Manager != "" || in.Config != nil {
			addErr = refuseAdd(http.StatusBadRequest, "invalid_config", errors.New("manager and config are only supported for local machines"))
		} else {
			addErr = h.addMachine(&mc, "admin")
		}
		if err := addErr; err != nil {
			writeAddMachineErr(w, err)
			return
		}
		var added adminMachine
		for _, m := range h.adminMachines() {
			if m.ID == mc.ID {
				added = m
			}
		}
		writeJSON(w, map[string]any{"machine": added, "restart": h.pendingRestart(h.requestLang(r))})
	})
	mux.HandleFunc("PUT /api/v1/admin/machines/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name *string `json:"name"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		if in.Name == nil { // the name is the one thing to change here
			writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("name: the computer's new name (\"\": its id)"))
			return
		}
		name, err := h.renameMachine(r.Context(), r.PathValue("id"), *in.Name, "admin", "admin")
		if err != nil {
			writeAddMachineErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "name": name})
	})
	mux.HandleFunc("DELETE /api/v1/admin/machines/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// One waiting to be confirmed goes as rejecting it does: its pending row and place too (machine_trust.go).
		if h.machinePending(id) {
			switch err := h.rejectMachine(r.Context(), id, "admin", "remove"); {
			case err == nil:
				writeJSON(w, map[string]any{"ok": true, "restart": h.pendingRestart(h.requestLang(r))})
				return
			case errors.Is(err, errKeeperAway):
				writeErrCode(w, http.StatusServiceUnavailable, "assistant_offline", err)
				return
			case !errors.Is(err, errNotPending): // confirmed meanwhile: removed below as one
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		// Its Persona Link goes now, before the entry: until a restart a hub.json entry is still there to connect to,
		// and a credential left under the id would let a computer added with it again back in. One that cannot be
		// revoked leaves the entry, to delete again.
		revoked, err := h.revokeComputer(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		removed := func(where string) {
			detail, _ := json.Marshal(map[string]any{"credentials_revoked": revoked})
			h.machineAudit(r.Context(), id, "remove", "admin", detail)
			h.logf("admin: machine %q removed%s", id, where)
			writeJSON(w, map[string]any{"ok": true, "restart": h.pendingRestart(h.requestLang(r))})
		}
		// One its manager keeps: it lets it go now (machines_kept.go).
		if kept, err := h.removeKeptMachine(r.Context(), id); kept {
			if err != nil {
				status, code := keeperErr(err)
				writeErrCode(w, status, code, err)
				return
			}
			removed("")
			return
		}
		found, err := removeMachineEntry(h.cfg.dir, id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeErrCode(w, http.StatusNotFound, "not_found", errors.New("hub.json has no such machine"))
			return
		}
		h.clearKept(r.Context(), id) // an old record of a manager that kept it (machines_kept.go)
		removed(" from hub.json")
	})
	// Pair a service machine's connector again (it was paired with another hub, or reinstalled).
	mux.HandleFunc("POST /api/v1/admin/machines/{id}/connector", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var in struct {
			PairLink string `json:"pair_link"`
			Address  string `json:"address"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		mc := h.knownEntry(id)
		if mc == nil || mc.Kind != kindService {
			writeErrCode(w, http.StatusBadRequest, "not_service", errors.New("only a computer of kind service pairs a connector"))
			return
		}
		address := strings.TrimSpace(in.Address)
		if address == "" {
			address = mc.Address
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		addr, version, err := h.pairConnector(ctx, in.PairLink, id, address, mc.Proxy)
		if err != nil {
			writeErrCode(w, http.StatusBadGateway, "pair_failed", err)
			return
		}
		restart := false
		if addr != mc.Address {
			// One its manager keeps: changed in its list, connected again at once (machines_kept.go).
			if kept, err := h.editKeptMachine(r.Context(), id, map[string]any{"address": addr}); kept {
				if err != nil {
					status, code := keeperErr(err)
					writeErrCode(w, status, code, err)
					return
				}
			} else {
				err := editHubJSON(h.cfg.dir, func(o *jsonObject) error { return setMachineField(o, id, "address", addr) })
				if err != nil {
					writeErr(w, http.StatusInternalServerError, err)
					return
				}
				restart = true
			}
		}
		writeJSON(w, map[string]any{"ok": true, "address": addr, "version": version, "restart_required": restart})
	})
	mux.HandleFunc("POST /api/v1/admin/machines/{id}/reconnect", func(w http.ResponseWriter, r *http.Request) {
		m := h.localMachine(r.PathValue("id"))
		if m == nil {
			writeErrCode(w, http.StatusConflict, "not_active", errors.New("this machine connects after the hub restarts"))
			return
		}
		m.reconnect()
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/v1/admin/machines/{id}/link-pair", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		mc := h.knownEntry(id)
		if mc == nil {
			writeErrCode(w, http.StatusConflict, "not_active", errors.New("restart the hub first: it reads new machines when it starts"))
			return
		}
		if mc.Kind != kindLink {
			writeErrCode(w, http.StatusBadRequest, "not_link", errors.New("only a link machine pairs a Persona Link"))
			return
		}
		if !h.linkPairable() {
			writeErrCode(w, http.StatusConflict, "tls_required", errors.New("Persona Link only uses TLS: turn on the TLS listener, "+
				"or put the hub behind an HTTPS reverse proxy (public_url)"))
			return
		}
		p, err := h.adminPairCode(r, id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.logf("admin: persona-link pairing link for %q", id)
		h.machineAudit(r.Context(), id, "code", "admin", nil)
		writeJSON(w, map[string]any{"pair_url": p.PairURL, "expires_at": p.ExpiresAt, "command": "persona-link pair '" + p.PairURL + "'"})
	})
}

// machineEntries is hub.json's "machines", each entry as it is in the file.
func machineEntries(o *jsonObject) ([]json.RawMessage, error) {
	var list []json.RawMessage
	if raw, ok := o.Get("machines"); ok && strings.TrimSpace(string(raw)) != "null" {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, errors.New("hub.json machines is not a list")
		}
	}
	if list == nil {
		list = []json.RawMessage{}
	}
	return list, nil
}

func machineEntryID(raw json.RawMessage) string {
	var m struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.ID
}

// adminPairCode makes a one-time pairing code like `personad hub pair` (pairing.go): for the app with machine "",
// else for that link machine's persona-link.
func (h *Hub) adminPairCode(r *http.Request, machine string) (*pairCode, error) {
	return h.newPairCode(r.Context(), machine)
}

// redactProxy is a proxy URL without its password.
func redactProxy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User(u.User.Username())
	return u.String()
}

// setMachineField sets one key of a hub.json machine entry, keeping the others as they are.
func setMachineField(o *jsonObject, id, key string, v any) error {
	list, err := machineEntries(o)
	if err != nil {
		return err
	}
	for i, raw := range list {
		if machineEntryID(raw) != id {
			continue
		}
		entry, err := parseJSONObject(raw)
		if err != nil {
			return err
		}
		if err := entry.Set(key, v); err != nil {
			return err
		}
		list[i] = entry.Marshal()
		return o.Set("machines", list)
	}
	return errors.New("hub.json has no such machine")
}
