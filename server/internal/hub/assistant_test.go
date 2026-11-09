package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAskUserReturnsAnswers(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = []*Machine{m}, m
	connectAssistant(t, h)
	args := json.RawMessage(`{"questions":[{"question":"发吗？","options":[{"label":"发"},{"label":"不发"}]}]}`)
	done := make(chan proto.ToolResult, 1)
	go func() { done <- h.callAssistantTool(context.Background(), m, "conv-1", "ask_user", args) }()
	eventually(t, 10*time.Second, "the question on the phone", func() bool { return len(h.pendingInteractions()) > 0 })
	ix := h.pendingInteractions()[0]
	if ix.Kind != proto.IxQuestion || ix.SessionID != "conv-1" || ix.MachineID != "box" || len(ix.Questions) != 1 {
		t.Fatalf("unexpected interaction %+v", ix)
	}
	if err := h.resolve(ix.ID, proto.Resolution{Answers: map[string]string{"发吗？": "发"}}); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.IsError || !strings.Contains(textOf(r), "发吗？ → 发") {
		t.Fatalf("unexpected result %+v", r)
	}
}

func TestAgentsList(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = []*Machine{m}, m
	now := time.Now().UTC()
	m.sessions = []proto.Session{
		{SessionID: "aaaaaaaa-1", Title: "修登录", Cwd: "/code/app", Status: proto.StatusWorking, UpdatedAt: now, LastText: "正在跑测试"},
		{SessionID: "bbbbbbbb-2", Title: "旧的", Cwd: "/code/old", Status: proto.StatusIdle, UpdatedAt: now.Add(-time.Hour)},
	}
	connectAssistant(t, h)
	agents := func(args string) string {
		return textOf(h.callAssistantTool(context.Background(), m, "", "agents_list", json.RawMessage(args)))
	}
	out := agents(`{}`)
	if !strings.Contains(out, "修登录") || strings.Contains(out, "旧的") || !strings.Contains(out, "正在跑测试") {
		t.Fatalf("active list:\n%s", out)
	}
	if all := agents(`{"state":"all","query":"old"}`); !strings.Contains(all, "旧的") || strings.Contains(all, "修登录") {
		t.Fatalf("query filter:\n%s", all)
	}
}

func TestAssistantToolsOnlyForAssistant(t *testing.T) {
	h := newTestHub(t)
	// The connectors' tools are the manager's: it says which are the assistant's only (lark_*).
	fakeExtOf(h).on(fakeConnectorTools, func(raw json.RawMessage) (any, error) {
		var a struct {
			Assistant bool `json:"assistant"`
		}
		_ = json.Unmarshal(raw, &a)
		tools := []proto.Tool{{Name: "examplenotes_search", InputSchema: json.RawMessage(`{"type":"object"}`)}}
		only := []proto.Tool{}
		if a.Assistant {
			only = append(only, proto.Tool{Name: "lark_search", InputSchema: json.RawMessage(`{"type":"object"}`)})
		}
		return map[string]any{"tools": tools, "assistant_tools": only}, nil
	})
	connectAssistant(t, h)
	for _, tool := range h.mcpTools(false) {
		if h.isAssistantTool(context.Background(), tool.Name) || strings.HasPrefix(tool.Name, "lark_") {
			t.Fatalf("%s leaked to ordinary sessions", tool.Name)
		}
	}
	names := map[string]bool{}
	for _, tool := range h.mcpTools(true) {
		names[tool.Name] = true
		var v any
		if err := json.Unmarshal(tool.InputSchema, &v); err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
	}
	for _, want := range []string{"ask_user", "notify_user", "schedule_followup", "agents_list", "agent_read", "lark_search", "examplenotes_search"} {
		if !names[want] {
			t.Fatalf("assistant is missing %s", want)
		}
	}
}
