package hub

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
)

func TestUIStreamRelayKeepsResumeSequenceHubOwned(t *testing.T) {
	for _, kind := range []string{"snapshot", "delta", "receipt", "error"} {
		t.Run(kind, func(t *testing.T) {
			h := newTestHub(t)
			app := &client{hub: h, role: roleFull, device: "example-device", uiConnection: "example-connection", out: make(chan []byte, 4), urlFeatures: map[string]bool{extwire.FeatureUIStream: true}, uiManagers: map[string]bool{assistantwire.AssistantID: true}}
			other := &client{hub: h, role: roleFull, device: "example-device", uiConnection: "other-connection", out: make(chan []byte, 4), urlFeatures: map[string]bool{extwire.FeatureUIStream: true}, uiManagers: map[string]bool{assistantwire.AssistantID: true}}
			h.clients[app], h.clients[other] = struct{}{}, struct{}{}
			body, err := json.Marshal(map[string]any{"type": "extension_ui_stream", "kind": kind, "manager": "forged-manager", "event_seq": 999999, "payload": map[string]any{"event_seq": 7, "text": "Example page data"}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(extwire.UITarget{Who: h.uiPrincipal(app), Envelope: body})
			if err != nil {
				t.Fatal(err)
			}
			busEvents[extwire.EventUIStream](h, nil, raw)
			if len(app.out) != 1 || len(other.out) != 0 {
				t.Fatalf("targeted delivery: recipient=%d other=%d", len(app.out), len(other.out))
			}
			var got struct {
				Type     string          `json:"type"`
				Kind     string          `json:"kind"`
				Manager  string          `json:"manager"`
				EventSeq json.RawMessage `json:"event_seq"`
				Payload  struct {
					EventSeq int    `json:"event_seq"`
					Text     string `json:"text"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(<-app.out, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.EventSeq) != 0 {
				t.Errorf("manager advanced the app resume cursor: event_seq=%s", got.EventSeq)
			}
			if got.Type != "extension_ui_stream" || got.Kind != kind || got.Manager != assistantwire.AssistantID || got.Payload.EventSeq != 7 || got.Payload.Text != "Example page data" {
				t.Errorf("page data or authenticated manager changed: %+v", got)
			}
			if h.events != nil {
				t.Fatal("private frame entered the global event log")
			}
		})
	}
}

func TestUIStreamRelayRetainsRecipientGuards(t *testing.T) {
	for _, name := range []string{"hub", "principal", "connection", "role", "feature", "manager", "malformed envelope"} {
		t.Run(name, func(t *testing.T) {
			h := newTestHub(t)
			app := &client{hub: h, role: roleFull, device: "example-device", uiConnection: "example-connection", out: make(chan []byte, 4), urlFeatures: map[string]bool{extwire.FeatureUIStream: true}, uiManagers: map[string]bool{assistantwire.AssistantID: true}}
			h.clients[app] = struct{}{}
			target := extwire.UITarget{Who: h.uiPrincipal(app), Envelope: json.RawMessage(`{"type":"extension_ui_stream","kind":"snapshot","event_seq":999999}`)}
			switch name {
			case "hub":
				target.Who.Hub = "another-hub"
			case "principal":
				target.Who.Principal = "another-device"
			case "connection":
				target.Who.Connection = "another-connection"
			case "role":
				app.role = "restricted"
			case "feature":
				app.urlFeatures = map[string]bool{}
			case "manager":
				app.uiManagers = map[string]bool{"another-manager": true}
			case "malformed envelope":
				target.Envelope = json.RawMessage(`[]`)
			}
			raw, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			busEvents[extwire.EventUIStream](h, nil, raw)
			if len(app.out) != 0 {
				t.Fatal("ineligible recipient received a private frame")
			}
			if h.events != nil {
				t.Fatal("rejected private frame entered the global event log")
			}
		})
	}
}
