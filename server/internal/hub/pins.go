package hub

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Pins are the spaces and agents the user keeps at the top of the "Develop" list (saved in Postgres, so a reinstall or
// another phone sees the same). Spaces are "machine/space name" — herdr workspace ids change when herdr restarts,
// names do not; agents are "machine/session id".
type Pins struct {
	Spaces   []string `json:"spaces"`
	Sessions []string `json:"sessions"`
	// Allowed (mutes only): agents that keep their notifications although their space is muted.
	Allowed []string `json:"allowed,omitempty"`
}

// Mutes use the same shape: muted spaces / agents get no push notifications (finished, needs you) and the phone
// shows them quietly. Same keys as pins.

func (h *Hub) loadMutes(ctx context.Context) Pins {
	p, _ := h.readMutes(ctx)
	return p
}

// readMutes is loadMutes, false when they could not be read (none then; read again next time).
func (h *Hub) readMutes(ctx context.Context) (Pins, bool) {
	h.mutesMu.Lock()
	defer h.mutesMu.Unlock()
	if h.mutes == nil {
		p := Pins{Spaces: []string{}, Sessions: []string{}}
		if h.store != nil {
			if _, err := h.store.Setting(ctx, "mutes", &p); err != nil {
				return Pins{Spaces: []string{}, Sessions: []string{}}, false // try again next time
			}
		}
		h.mutes = &p
	}
	return *h.mutes, true
}

// muted reports whether pushes about this session are off (the agent or its space is muted).
func (h *Hub) muted(machineID, sessionID string) bool {
	space := ""
	if m := h.localMachine(machineID); m != nil {
		if s, ok := m.session(sessionID); ok {
			space = s.SpaceName
		}
	}
	return h.mutedAs(machineID, sessionID, space)
}

// mutedAs is muted for a machine under the id the apps know, its session in space (empty: none known).
func (h *Hub) mutedAs(machineID, sessionID, space string) bool {
	p := h.loadMutes(context.Background())
	key := machineID + "/" + sessionID
	if slices.Contains(p.Sessions, key) {
		return true
	}
	if slices.Contains(p.Allowed, key) { // turned back on inside a muted space
		return false
	}
	return space != "" && slices.Contains(p.Spaces, machineID+"/"+space)
}

func (h *Hub) pinRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/mutes", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.readMutes(r.Context())
		if !ok {
			writeErr(w, http.StatusInternalServerError, i18n.Err("http-status.internal"))
			return
		}
		writeJSON(w, p)
	})
	api.HandleFunc("PUT /api/v1/mutes", func(w http.ResponseWriter, r *http.Request) {
		var p Pins
		if err := decodeJSONBody(w, r, &p, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if h.store == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		if p.Spaces == nil {
			p.Spaces = []string{}
		}
		if p.Sessions == nil {
			p.Sessions = []string{}
		}
		if err := h.store.PutSetting(r.Context(), "mutes", p); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.mutesMu.Lock()
		h.mutes = &p
		h.mutesMu.Unlock()
		h.mutesChanged() // the manager decides the pushes (fleet_link.go)
		writeJSON(w, p)
	})
	api.HandleFunc("GET /api/v1/pins", func(w http.ResponseWriter, r *http.Request) {
		p := Pins{Spaces: []string{}, Sessions: []string{}}
		if h.store != nil {
			if _, err := h.store.Setting(r.Context(), "pins", &p); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeJSON(w, p)
	})
	api.HandleFunc("PUT /api/v1/pins", func(w http.ResponseWriter, r *http.Request) {
		var p Pins
		if err := decodeJSONBody(w, r, &p, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if h.store == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		if p.Spaces == nil {
			p.Spaces = []string{}
		}
		if p.Sessions == nil {
			p.Sessions = []string{}
		}
		if err := h.store.PutSetting(r.Context(), "pins", p); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, p)
	})
}

// renameSpace renames a herdr workspace and carries its pins / mutes over (they are keyed by "machine/name").
func (h *Hub) renameSpace(w http.ResponseWriter, r *http.Request, m *Machine) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSONBody(w, r, &in, 4<<10); err != nil || strings.TrimSpace(in.Name) == "" {
		writeBodyErr(w, err, errors.New("name is required"))
		return
	}
	wid, name := r.PathValue("wid"), strings.TrimSpace(in.Name)
	old := ""
	for _, s := range m.sessionList() {
		if s.SpaceID == wid && s.SpaceName != "" {
			old = s.SpaceName
			break
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var res struct {
		Name string `json:"name"`
	}
	if err := m.call(ctx, "space.rename", map[string]any{"workspace_id": wid, "name": name}, &res); err != nil {
		callErr(w, err)
		return
	}
	if res.Name != "" { // the name the space shows now (a named herdr session's has the session in front); older nodes say none
		name = res.Name
	}
	if old != "" && old != name {
		// A todo linked to it by name follows (the manager's; not while it is not connected).
		h.asst().TodoSpaceRenamed(context.Background(), assistantwire.TodoSpaceRenamed{MachineID: m.cfg.ID, Old: old, New: name})
	}
	if old != "" && old != name && h.store != nil {
		from, to := m.cfg.ID+"/"+old, m.cfg.ID+"/"+name
		for _, key := range []string{"pins", "mutes"} {
			p := Pins{}
			if found, err := h.store.Setting(ctx, key, &p); err != nil || !found {
				continue
			}
			changed := false
			for i, s := range p.Spaces {
				if s == from {
					p.Spaces[i], changed = to, true
				}
			}
			if changed {
				_ = h.store.PutSetting(ctx, key, p)
				if key == "mutes" {
					h.mutesMu.Lock()
					h.mutes = &p
					h.mutesMu.Unlock()
					h.mutesChanged()
				}
			}
		}
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}
