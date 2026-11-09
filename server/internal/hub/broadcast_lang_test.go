package hub

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Every app gets the same bytes as localizing the numbered event for that app alone. Apps using the same language
// share the result, but subsequent broadcasts must get their own result and leave the canonical event log intact.
func TestBroadcastPerLanguage(t *testing.T) {
	h := &Hub{clients: map[*client]struct{}{}}
	langs := []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja}
	for i := range 2 {
		for _, l := range langs {
			c := &client{hub: h, out: make(chan []byte, 4), acceptLang: l}
			c.meta.features = map[string]bool{"elicitation": i == 0}
			c.canonicalContracts.Store(true)
			h.clients[c] = struct{}{}
		}
	}
	machines, err := json.Marshal(map[string]any{
		"type": "machines",
		"machines": []proto.Machine{{
			ID:           "example-machine",
			Name:         "Example",
			DeployError:  i18n.T(i18n.Default, "devices-deploy.hub_changed"),
			DeployReason: i18n.T(i18n.Default, "devices-deploy.node_temporary"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		typ         string
		raw         []byte
		localize    func(i18n.Lang, []byte) []byte
		translated  bool
		elicitation bool
	}{
		{"machines", machines, localizedMachineEvent, true, false},
		{"extension_operation", []byte(`{"type":"extension_operation","operation":{"id":"op1","kind":"install","error":"SDK error: invalid package","steps":[{"id":"nodes","machines":{"errors":{"example-machine":"SSH diagnostic: refused"}}}]}}`), i18n.ExtensionEvent, true, false},
		{"extension_auth", []byte(`{"type":"extension_auth","extension":"example","hub_auth_message":true,"auth":{"state":"error","message":"not answering"}}`), i18n.ExtensionEvent, true, false},
		{"extension_upgrade", []byte(`{"type":"extension_upgrade","extension":"example"}`), i18n.ExtensionEvent, false, false},
		{"interaction", []byte(`{"type":"interaction","interaction":{"id":"example-form","kind":"elicitation"}}`), i18n.ExtensionEvent, false, true},
	}
	var seq int64
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			original := bytes.Clone(tc.raw)
			if tc.translated {
				for i, l := range langs {
					for _, other := range langs[i+1:] {
						if bytes.Equal(tc.localize(l, tc.raw), tc.localize(other, tc.raw)) {
							t.Fatalf("fixture does not distinguish %s from %s", l, other)
						}
					}
				}
			}
			var numbered [2][]byte
			for i := range numbered {
				seq++
				numbered[i] = stampEvent(tc.raw, seq)
				if tc.elicitation {
					h.broadcastTo(tc.typ, tc.raw, true)
				} else {
					h.broadcastRaw(tc.typ, tc.raw)
				}
				logged := h.events.events[len(h.events.events)-1]
				if h.events.seq != seq || len(h.events.events) != int(seq) || logged.Seq != seq || logged.Elicitation != tc.elicitation || !bytes.Equal(logged.Body, numbered[i]) {
					t.Fatalf("event log changed: %+v", logged)
				}
			}
			shared := [2]map[i18n.Lang][]byte{{}, {}}
			for c := range h.clients {
				l := c.lang()
				if tc.elicitation && !c.has("elicitation") {
					if len(c.out) != 0 {
						t.Fatalf("%s: an app without elicitation support received the event", l)
					}
					continue
				}
				if len(c.out) != len(numbered) {
					t.Fatalf("%s: %d messages, want %d", l, len(c.out), len(numbered))
				}
				for i, body := range numbered {
					got := <-c.out
					if want := tc.localize(l, body); !bytes.Equal(got, want) {
						t.Fatalf("%s event %d:\n got %s\nwant %s", l, i, got, want)
					}
					if previous, ok := shared[i][l]; ok && &got[0] != &previous[0] {
						t.Fatalf("%s event %d: apps did not share the localized bytes", l, i)
					}
					shared[i][l] = got
				}
			}
			if !bytes.Equal(tc.raw, original) {
				t.Fatal("broadcast changed the input bytes")
			}
		})
	}
}
