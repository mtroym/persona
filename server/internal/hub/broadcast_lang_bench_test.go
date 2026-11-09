package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// benchLangClients adds n apps to h, spread over the three languages and all taking extension operations.
func benchLangClients(h *Hub, n int) {
	langs := []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja}
	for i := range n {
		c := &client{hub: h, out: make(chan []byte, 4), acceptLang: langs[i%len(langs)], urlFeatures: map[string]bool{extensionOperationsFeature: true}}
		h.clients[c] = struct{}{}
	}
}

// benchDrain empties every app's queue, as its writer would.
func benchDrain(h *Hub) {
	for c := range h.clients {
		for len(c.out) > 0 {
			<-c.out
		}
	}
}

// BenchmarkBroadcastExtensionOperation is one install progress step (pushed about once a second during a download)
// going to 12 apps across en, zh-Hans and ja. It runs under Hub.mu, which every broadcast and every connecting app
// waits on.
func BenchmarkBroadcastExtensionOperation(b *testing.B) {
	h := &Hub{clients: map[*client]struct{}{}}
	benchLangClients(h, 12)
	msg := []byte(`{"type":"extension_operation","manager":"extmgr","operation":{"id":"op-1","kind":"install","extension":"example",` +
		`"status":"running","steps":[{"id":"download","state":"running","progress":0.42,"bytes":44040192,"total":104857600},` +
		`{"id":"nodes","state":"running","machines":{"done":["m1","m2"],"errors":{"m3":"SSH diagnostic: refused"}}}]}}`)
	b.ReportAllocs()
	for b.Loop() {
		h.toWanting(extensionOperationsFeature, msg)
		benchDrain(h)
	}
}

// BenchmarkBroadcastRawExtensionOperation measures the event-log broadcast path with the same 12 apps and three
// languages as BenchmarkBroadcastExtensionOperation.
func BenchmarkBroadcastRawExtensionOperation(b *testing.B) {
	h := &Hub{clients: map[*client]struct{}{}}
	benchLangClients(h, 12)
	msg := []byte(`{"type":"extension_operation","manager":"extmgr","operation":{"id":"op-1","kind":"install","extension":"example",` +
		`"status":"running","steps":[{"id":"download","state":"running","progress":0.42,"bytes":44040192,"total":104857600},` +
		`{"id":"nodes","state":"running","machines":{"done":["m1","m2"],"errors":{"m3":"SSH diagnostic: refused"}}}]}}`)
	b.ReportAllocs()
	for b.Loop() {
		h.broadcastRaw(extensionOperationEvent, msg)
		benchDrain(h)
	}
}
