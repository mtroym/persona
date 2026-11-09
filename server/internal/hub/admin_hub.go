package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// The overview page: what this hub is and how it runs, its name, the doctor, a restart.

type restartView struct {
	Required bool     `json:"required"`
	Reasons  []string `json:"reasons"`
}

type tlsView struct {
	Listen string `json:"listen"`
	Port   int    `json:"port"`
	FP     string `json:"fp"`
}

func (h *Hub) adminTLS() *tlsView {
	port, fp := h.tlsPin()
	if h.cfg.Public.TLSListen == "" && port == 0 {
		return nil
	}
	return &tlsView{Listen: h.cfg.Public.TLSListen, Port: port, FP: fp}
}

// diskConfig is hub.json as it is on disk now, with the environment's overrides like the running hub's.
func (h *Hub) diskConfig() (*Config, error) {
	b, err := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json"))
	if err != nil {
		return nil, err
	}
	// The same defaults LoadConfig starts from (no public_url or machines in the file means the default ones).
	cfg := defaultConfig()
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	cfg.applyEnv()
	return cfg, nil
}

// pendingRestart lists what hub.json says that the running hub does not do yet: it read the file when it started.
func (h *Hub) pendingRestart(l i18n.Lang) restartView {
	v := restartView{Reasons: []string{}}
	disk, err := h.diskConfig()
	if err != nil {
		return v
	}
	running := map[string]MachineConfig{}
	for _, m := range h.cfg.Machines {
		running[m.ID] = m
	}
	for _, m := range h.addedLive() { // added while running: already connecting (devices_handover.go addMachineLive)
		running[m.ID] = m
	}
	seen := map[string]bool{}
	for _, m := range disk.Machines {
		seen[m.ID] = true
		was, ok := running[m.ID]
		switch {
		case !ok:
			v.Reasons = append(v.Reasons, i18n.T(l, "hub-admin_hub.restart_add_computer", i18n.Args{"id": m.ID}))
		case !reflect.DeepEqual(was.withoutName(), m.withoutName()): // a name is taken at once (machine_rename.go)
			v.Reasons = append(v.Reasons, i18n.T(l, "hub-admin_hub.restart_computer_settings", i18n.Args{"id": m.ID}))
		}
	}
	gone := []string{}
	for id := range running {
		// One its manager keeps now left hub.json without a restart, and one removed from its own list is gone already
		// (machines_kept.go).
		if !seen[id] && h.keptBy(id) == "" && (h.knownEntry(id) != nil || h.machineByID(id) != nil) {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		v.Reasons = append(v.Reasons, i18n.T(l, "hub-admin_hub.restart_remove_computer", i18n.Args{"id": id}))
	}
	if disk.Listen != h.cfg.Listen {
		v.Reasons = append(v.Reasons, i18n.T(l, "hub-admin_hub.restart_listen"))
	}
	if disk.PublicURL != h.cfg.PublicURL {
		v.Reasons = append(v.Reasons, i18n.T(l, "hub-admin_hub.restart_public_url"))
	}
	was, now := h.cfg.Public, disk.Public
	if was.TLSListen != now.TLSListen || was.CertFile != now.CertFile || was.KeyFile != now.KeyFile {
		v.Reasons = append(v.Reasons, i18n.T(l, "hub-admin_hub.restart_tls"))
	}
	if !reflect.DeepEqual(was.Advertise, now.Advertise) || !reflect.DeepEqual(was.AdvertiseIPv6, now.AdvertiseIPv6) ||
		!reflect.DeepEqual(was.AdvertiseLAN, now.AdvertiseLAN) {
		v.Reasons = append(v.Reasons, i18n.T(l, "hub-admin_hub.restart_advertise"))
	}
	v.Required = len(v.Reasons) > 0
	return v
}

// configuredHubName is hub.json's "name" as the admin UI last set it ("" when unset): hubName reads it through here so
// a change shows without a restart (h.cfg never changes while the hub runs).
func (h *Hub) configuredHubName() string {
	h.admin.mu.Lock()
	defer h.admin.mu.Unlock()
	if h.admin.name != nil {
		return *h.admin.name
	}
	return strings.TrimSpace(h.cfg.Name)
}

// adminReload reads hub.json's "admin" and "name" again: what `personad hub admin access` changed on this machine.
func (h *Hub) adminReload() error {
	o, err := readHubJSON(h.cfg.dir)
	if err != nil {
		return err
	}
	var file AdminConfig
	if raw, ok := o.Get("admin"); ok {
		if err := json.Unmarshal(raw, &file); err != nil {
			return err
		}
	}
	var name string
	if raw, ok := o.Get("name"); ok {
		_ = json.Unmarshal(raw, &name)
	}
	h.adminInit()
	h.admin.mu.Lock()
	name = strings.TrimSpace(name)
	h.admin.file, h.admin.name, h.admin.env = file, &name, adminEnv()
	h.admin.mu.Unlock()
	h.logf("admin: access is now %s", h.adminScope().access)
	return nil
}

func (h *Hub) adminHubRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/overview", func(w http.ResponseWriter, r *http.Request) {
		total, online := 0, 0
		for _, m := range h.machineList() {
			total++
			if m.Online {
				online++
			}
		}
		devices := 0
		if h.store != nil {
			if list, err := h.store.DeviceTokens(r.Context()); err == nil {
				for _, d := range list {
					if d.RevokedAt == nil {
						devices++
					}
				}
			}
		}
		writeJSON(w, map[string]any{
			"hub": map[string]any{
				"hub_id": h.hubID(), "name": h.hubName(), "name_set": h.configuredHubName() != "",
				"default_name": h.defaultHubName(), "version": h.version, "started_at": processStart.UTC(),
				"os": runtime.GOOS, "arch": runtime.GOARCH, "container": h.adminContainer(), "can_restart": h.supervised(),
			},
			"listen": h.cfg.Listen, "public_url": h.cfg.PublicURL, "tls": h.adminTLS(), "endpoints": h.endpointList(),
			"machines": map[string]int{"total": total, "online": online}, "devices": devices,
			"restart": h.pendingRestart(h.requestLang(r)),
		})
	})
	mux.HandleFunc("PUT /api/v1/admin/hub", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name *string `json:"name"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if len([]rune(name)) > 40 || strings.ContainsAny(name, "\n\r\t") {
				writeErrCode(w, http.StatusBadRequest, "invalid_name", i18n.Err("hub-admin_hub.invalid_name"))
				return
			}
			err := editHubJSON(h.cfg.dir, func(o *jsonObject) error {
				if name == "" {
					o.Del("name")
					return nil
				}
				return o.Set("name", name)
			})
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			h.adminInit()
			h.admin.mu.Lock()
			h.admin.name = &name
			h.admin.mu.Unlock()
			h.logf("admin: hub name set to %q", name)
			h.sendLinkEndpoints() // the links show it (the Mac menu bar)
		}
		writeJSON(w, map[string]any{"name": h.hubName(), "name_set": h.configuredHubName() != "", "restart": h.pendingRestart(h.requestLang(r))})
	})
	mux.HandleFunc("POST /api/v1/admin/doctor", func(w http.ResponseWriter, r *http.Request) {
		if !h.admin.doctorMu.TryLock() {
			writeErrCode(w, http.StatusConflict, "busy", errors.New("a check is already running"))
			return
		}
		defer h.admin.doctorMu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var out bytes.Buffer
		_ = Doctor(ctx, &out)
		items, next := parseDoctor(out.String())
		writeJSON(w, map[string]any{"items": items, "next": next, "ran_at": time.Now().UTC()})
	})
	mux.HandleFunc("POST /api/v1/admin/restart", func(w http.ResponseWriter, r *http.Request) {
		if !h.supervised() {
			writeErrCode(w, http.StatusConflict, "not_supervised",
				errors.New("nothing would start the hub again: restart it where it runs (systemctl --user restart personad, docker compose restart hub)"))
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(300 * time.Millisecond) // the answer gets out first
			h.restartHub()
		}()
	})
}

type doctorItem struct {
	Level  string `json:"level"` // ok | problem | info
	Item   string `json:"item"`
	Detail string `json:"detail"`
}

// parseDoctor turns `personad hub doctor`'s report (doctor.go: "OK | item: detail", "Problem | item: action", then "Next:"
// and its - lines) into items for the page; any other line is information about the item before it.
func parseDoctor(text string) (items []doctorItem, next []string) {
	items, next = []doctorItem{}, []string{}
	inNext := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case trimmed == "下一步：":
			inNext = true
			continue
		case inNext && strings.HasPrefix(trimmed, "- "):
			next = append(next, strings.TrimPrefix(trimmed, "- "))
			continue
		}
		level, rest := "info", trimmed
		if after, ok := strings.CutPrefix(trimmed, "正常｜"); ok {
			level, rest = "ok", after
		} else if after, ok := strings.CutPrefix(trimmed, "有问题｜"); ok {
			level, rest = "problem", after
		}
		if level == "info" && strings.HasPrefix(line, "  ") && len(items) > 0 {
			last := &items[len(items)-1]
			last.Detail = strings.TrimSpace(last.Detail + "\n" + trimmed)
			continue
		}
		item, detail, found := strings.Cut(rest, "：")
		if !found {
			item, detail = "", rest
		}
		items = append(items, doctorItem{Level: level, Item: item, Detail: detail})
	}
	return items, next
}
