package hub

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extwire"
)

func (h *Hub) uiPrincipal(c *client) extwire.UIPrincipal {
	principal := c.device
	if principal == "" {
		principal = "owner"
	}
	return extwire.UIPrincipal{Hub: h.hubID(), Principal: principal, Connection: c.uiConnection, Role: c.role, Paired: strings.HasPrefix(c.device, "pair_"), Lang: string(c.lang())}
}

// Only forwarding and connection-targeted delivery live in the hub. Page state,
// gates, quotas and diffing remain manager-owned. There is no second app socket.
func (h *Hub) forwardUIStream(ctx context.Context, c *client, raw []byte) {
	if len(raw) > 64<<10 || !c.wants(extwire.FeatureUIStream) || c.role != roleFull || c.uiConnection == "" {
		return
	}
	var envelope wire.UIStreamClientEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	manager := envelope.Manager
	if manager == "" {
		manager = h.defaultOwner(ctx)
	}
	// The manager is a routing selector, never a source-supplied owner. Only a
	// manager on this hub's bus can receive the authenticated connection identity.
	c.mu.Lock()
	if len(c.uiManagers) >= 16 && !c.uiManagers[manager] {
		c.mu.Unlock()
		return
	}
	c.uiManagers[manager] = true
	c.mu.Unlock()
	select {
	case c.uiRequests <- struct{}{}:
	default:
		c.cancel()
		return
	}
	who := h.uiPrincipal(c)
	go func() {
		defer func() { <-c.uiRequests }()
		callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		var out json.RawMessage
		if err := h.callBus(callCtx, manager, extwire.MethodUIStream, extwire.UIStreamCall{Who: who, Envelope: raw}, &out); err != nil {
			// Never include private RPC failure text on the app transport.
			requestID := ""
			if envelope.Request != nil {
				requestID = envelope.Request.RequestID
			}
			c.sendJSON(wire.UIStreamServerEnvelope{Type: "extension_ui_stream", Kind: "error", RequestID: requestID, Manager: manager, Error: &wire.UIStreamFailure{Code: "manager_unavailable", Message: ""}})
		}
	}()
}
func (h *Hub) uiConnectionGone(c *client) {
	if c.uiConnection == "" {
		return
	}
	who := h.uiPrincipal(c)
	c.mu.Lock()
	var managers []string
	for manager := range c.uiManagers {
		managers = append(managers, manager)
	}
	c.mu.Unlock()
	for _, manager := range managers {
		_ = h.notifyBus(manager, extwire.EventUIConnectionGone, who)
	}
}

func init() {
	handleBusEvent(extwire.EventUIStream, func(h *Hub, bus *busConn, params json.RawMessage) {
		if len(params) > (1<<20)+(64<<10) {
			return
		}
		var target extwire.UITarget
		if json.Unmarshal(params, &target) != nil || target.Who.Hub != h.hubID() || len(target.Envelope) > 1<<20 {
			return
		}
		// This event is private: do not call broadcastRaw or add to the global log.
		h.mu.Lock()
		defer h.mu.Unlock()
		for c := range h.clients {
			who := h.uiPrincipal(c)
			if who.Connection != target.Who.Connection || who.Principal != target.Who.Principal || c.role != roleFull || !c.wants(extwire.FeatureUIStream) {
				continue
			}
			c.mu.Lock()
			allowed := c.uiManagers[bus.managerID()]
			c.mu.Unlock()
			if !allowed {
				return
			}
			var envelope map[string]json.RawMessage
			if json.Unmarshal(target.Envelope, &envelope) != nil {
				return
			}
			// Private stream frames skip the event log; only the hub owns the resume cursor.
			delete(envelope, "event_seq")
			envelope["manager"], _ = json.Marshal(bus.managerID())
			raw, err := json.Marshal(envelope)
			if err == nil {
				c.queue(raw)
			}
			return
		}
	})
}
