package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// The knowledge base is the assistant's (internal/assistant kb*.go, its own tests there); these are the hub's ends:
// the tools and the routes the hub relays to it.

// testKBs are the knowledge bases the tests' assistants keep, by hub (dialAssistant passes them on).
var testKBs sync.Map

// testKBOf is the knowledge base h's assistant keeps; nil: none.
func testKBOf(h *Hub) *assistant.KBOptions {
	v, _ := testKBs.Load(h)
	o, _ := v.(*assistant.KBOptions)
	return o
}

// withKB gives the assistant connected to h from now on a knowledge base in a directory of its own, and returns an
// engine on the same files for the test to write and read them.
func withKB(t *testing.T, h *Hub) *kb.Engine {
	t.Helper()
	dir := t.TempDir()
	testKBs.Store(h, &assistant.KBOptions{Dir: dir})
	t.Cleanup(func() { testKBs.Delete(h) })
	st, err := kb.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &kb.Engine{Store: st, Owner: "我"}
}

// The kb_* tools, the assistant's, on its knowledge base: written, listed, read (a project by its name), searched.
func TestKBTools(t *testing.T) {
	h := newTestHub(t)
	e := withKB(t, h)
	c := &kb.Chat{ID: kb.ChatID("lark", "f1"), Source: "lark", SourceID: "f1", Kind: "group", Name: "发布群", Created: time.Now()}
	_ = e.Store.PutChat(c)
	_ = e.Store.AppendMessages(c, []kb.Line{{At: time.Now(), Sender: "张三", Text: "周五灰度 Persona"}})
	_ = e.Store.PutSummary(kb.KindChat, c.ID, "# 发布群\n\n在推 Persona 发布")
	connectAssistant(t, h)
	tool := func(name, args string) string {
		t.Helper()
		r := h.asst().Tool(context.Background(), assistantwire.ToolCall{Name: name, Arguments: json.RawMessage(args)})
		if r.IsError {
			t.Fatalf("%s: %s", name, textOf(r))
		}
		return textOf(r)
	}
	if out := tool("kb_update", `{"kind":"projects","id":"Persona 发布","project_kind":"long","importance":"P0","relevance":95,"log":"定了周五灰度","chats":["lark-f1"],"dirs":["server:/code/persona"]}`); !strings.Contains(out, "projects/persona-发布") {
		t.Fatalf("update: %q", out)
	}
	if p, _ := e.Store.Project("persona-发布"); p == nil || p.Kind != "long" || !p.Locked || p.Dirs[0] != "server:/code/persona" {
		t.Fatalf("project: %+v", p)
	}
	if out := tool("kb_get", `{"kind":"projects","id":"Persona 发布"}`); !strings.Contains(out, "定了周五灰度") {
		t.Fatalf("get project by name: %q", out)
	}
	if out := tool("kb_search", `{"query":"灰度"}`); !strings.Contains(out, "messages/") {
		t.Fatalf("search: %q", out)
	}
}

// Without a knowledge base the assistant's kb_* tools say it is off.
func TestKBToolsWithoutAKnowledgeBase(t *testing.T) {
	h := newTestHub(t)
	connectAssistant(t, h)
	r := h.asst().Tool(context.Background(), assistantwire.ToolCall{Name: "kb_search", Arguments: json.RawMessage(`{"query":"x"}`)})
	if !r.IsError || textOf(r) != "the knowledge base is off (manager.json kb.enabled)" {
		t.Fatalf("kb_search: %s", textOf(r))
	}
}

// The apps' /api/v1/kb routes are relayed to the assistant with the shapes they had; while it is not connected they
// answer 503 assistant_offline.
func TestKBRoutesRelayed(t *testing.T) {
	h := newTestHub(t)
	withKB(t, h)
	a := connectAssistant(t, h)
	mux := h.routes()
	tok := h.cfg.Token
	if code, out := call(t, mux, "PUT", "/api/v1/kb/projects/X", tok, `{"name":"X","tags":["a"]}`); code != 200 || out["id"] != "x" {
		t.Fatalf("put: %d %v", code, out)
	}
	code, out := call(t, mux, "GET", "/api/v1/kb/projects?tag=a", tok, "")
	if items, _ := out["items"].([]any); code != 200 || len(items) != 1 || items[0].(map[string]any)["id"] != "x" || out["tags"] == nil {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/kb/projects/x", tok, ""); code != 200 || out["detail"].(map[string]any)["kind"] != "projects" {
		t.Fatalf("get: %d %v", code, out)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/kb/chats/missing", tok, ""); code != 404 {
		t.Fatalf("missing: %d", code)
	}
	if code, out := call(t, mux, "GET", "/api/v1/kb-search?q=x&results=1", tok, ""); code != 200 || out["results"] == nil {
		t.Fatalf("search: %d %v", code, out)
	}

	a.stop()
	<-a.done
	eventually(t, 5*time.Second, "the hub sees the assistant gone", func() bool { return h.busConnOf(assistantwire.AssistantID) == nil })
	if code, out := call(t, mux, "GET", "/api/v1/kb/projects", tok, ""); code != 503 || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("while the assistant is away: %d %v", code, out)
	}
}

// The kb section is the manager's (its manager.json): the hub hands none over, and a hub.json that still has one
// (an older layout) changes nothing the hub says.
func TestKBSectionIsNotTheHubs(t *testing.T) {
	h := newTestHub(t)
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"kb":{"enabled":false,"ai":{"model":"k"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if hello, _ := (assistantLink{h}).Hello(context.Background()); hello.KB != nil || hello.Models.KBAI != nil ||
		hello.LegacyConfig != nil {
		t.Fatalf("hello %+v %+v", hello.KB, hello.Models)
	}
	// What a built-in manager the hub did not start gets at its handshake (managerHello), raw, to take over once.
	if got := string(h.legacyConfig()); got != `{"kb":{"enabled":false,"ai":{"model":"k"}}}` {
		t.Fatalf("legacy %s", got)
	}
}
