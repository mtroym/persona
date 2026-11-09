package hub

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's cards (docs/assistant/assistant-views.md "Prompts and switches", #2023): hub.json's assistant.persona_view, switched from
// the admin UI without a restart. What reads it per request (Hello: the view tools and actions, inline registration,
// the channel relay) follows at once. The assistant's own instructions do not: its CLAUDE.md is written when its
// directory is next prepared (a new conversation, or a warm spare process), and a process reads it and its tool list
// when it starts, so one already running keeps what it loaded until it is started again. Off keeps teaching ```persona-card, which every app draws.

type adminAssistantView struct {
	PersonaView bool `json:"persona_view"`
}

// assistantPersonaView is assistant.persona_view as the admin UI last set it, else as the hub read it at start. Every
// hub-side read of the switch goes through here, not h.cfg.Assistant.PersonaView (which never changes while running).
func (h *Hub) assistantPersonaView() bool {
	h.admin.mu.Lock()
	defer h.admin.mu.Unlock()
	return h.assistantPersonaViewLocked()
}

func (h *Hub) assistantPersonaViewLocked() bool {
	if h.admin.personaView != nil {
		return *h.admin.personaView
	}
	return h.cfg != nil && h.cfg.Assistant.PersonaView
}

// setAssistantPersonaView writes assistant.persona_view into hub.json, every other key of "assistant" (and of the
// file) kept as it was; off removes the key (its default) and an "assistant" left empty with it.
func setAssistantPersonaView(dir string, on bool) error {
	return editHubJSON(dir, func(o *jsonObject) error {
		a, err := o.Object("assistant") // not an object: an error, the file stays as it is
		if err != nil {
			return err
		}
		if on {
			if err := a.Set("persona_view", true); err != nil {
				return err
			}
		} else {
			a.Del("persona_view")
		}
		o.SetObject("assistant", a)
		return nil
	})
}

var assistantSwitchMu sync.Mutex

func (h *Hub) assistantToolsGeneration() uint64 {
	h.admin.mu.Lock()
	defer h.admin.mu.Unlock()
	return h.admin.personaViewGeneration
}

// storeAssistantTools commits a reply only while its switch generation is still current.
func (h *Hub) storeAssistantTools(generation uint64, tools []proto.Tool) bool {
	h.admin.mu.Lock()
	defer h.admin.mu.Unlock()
	if generation != h.admin.personaViewGeneration ||
		slices.ContainsFunc(tools, func(t proto.Tool) bool { return t.Name == "view_show" }) != h.assistantPersonaViewLocked() {
		return false
	}
	a := &h.assistant
	a.toolsMu.Lock()
	a.tools = tools
	a.toolsMu.Unlock()
	return true
}

// assistantToolsFollow asks for tools after Hello follows the switch; only this generation can update the cache.
// A process already running sees a changed MCP tool list only when it is started again.
func (h *Hub) assistantToolsFollow(on bool, generation uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for try := 0; try < 10; try++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond): // after assistantUpdated's 200 ms gathering
		}
		if h.busConnOf(assistantwire.AssistantID) == nil {
			return // it says its tools when it connects
		}
		var res struct {
			Tools []proto.Tool `json:"tools"`
		}
		if err := h.callBus(ctx, assistantwire.AssistantID, assistantwire.MethodTools, nil, &res); err != nil {
			h.logf("admin: assistant tools: %v", err)
			return
		}
		if slices.ContainsFunc(res.Tools, func(t proto.Tool) bool { return t.Name == "view_show" }) != on {
			continue // its Hello is not the new one yet
		}
		if !h.storeAssistantTools(generation, res.Tools) {
			return
		}
		return
	}
	h.logf("admin: the assistant's tools did not follow assistant.persona_view=%v", on)
}

func (h *Hub) adminAssistantRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/assistant", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, adminAssistantView{PersonaView: h.assistantPersonaView()})
	})
	mux.HandleFunc("PUT /api/v1/admin/assistant", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PersonaView *bool `json:"persona_view"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		if in.PersonaView == nil {
			writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("persona_view is required"))
			return
		}
		on := *in.PersonaView
		assistantSwitchMu.Lock() // the file and the live value change together, in the same order
		defer assistantSwitchMu.Unlock()
		if err := setAssistantPersonaView(h.cfg.dir, on); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.adminInit()
		h.admin.mu.Lock()
		h.admin.personaView = &on
		h.admin.personaViewGeneration++
		generation := h.admin.personaViewGeneration
		h.admin.mu.Unlock()
		h.logf("admin: assistant.persona_view is now %v", on)
		h.assistantUpdated()                      // the assistant process keeps its own copy of Hello (manager.updated)
		go h.assistantToolsFollow(on, generation) // and the hub keeps the tools it said when it connected
		writeJSON(w, adminAssistantView{PersonaView: on})
	})
}
