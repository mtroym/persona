package conformance

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/serve"
)

func TestServeConforms(t *testing.T) {
	m, err := extension.Parse([]byte(`{"manifest_version": 1, "id": "example-hello", "name": "Hello", "version": "0.1.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": "bin/hello"},
		"contributes": {"connectors": [{"id": "hello", "name": "Hello", "capabilities": ["status"]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	prog := serve.Program{ID: "example-hello", Version: "0.1.0",
		Init: func(context.Context, extension.InitializeParams, *serve.Hub) (map[string]any, error) {
			return map[string]any{"connectors": []extension.ConnectorItem{{ID: "hello", Name: "Hello", Capabilities: []string{"status"}}}}, nil
		},
		Namespaces: map[string]serve.Handler{"connector": func(_ context.Context, method string, params json.RawMessage) (any, error) {
			if c := serve.Contribution(params); c != "" && c != "hello" {
				return nil, serve.NoContribution(c)
			}
			if method == "status" {
				return map[string]string{"state": "ok"}, nil
			}
			return nil, serve.UnknownMethod(method)
		}}}
	Check(t, m, Serve(t, prog), extension.InitializeParams{})
}

// A plugin with UI draws each of its slots a View the apps can draw there, told where it is drawn.
func TestUIConforms(t *testing.T) {
	m, err := extension.Parse([]byte(`{"manifest_version": 1, "id": "example-usage", "name": "Usage", "version": "0.1.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": "bin/usage"},
		"contributes": {"ui": [{"id": "usage", "title": "用量", "placement": "nav_page", "icon": "chart"},
			{"id": "today", "title": "今天", "placement": "overview_card", "opens": "usage"},
			{"id": "gpu", "title": "显卡", "placement": "machine_section"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	drawn := map[string]extension.UIParams{}
	versions := map[int]bool{}
	prog := serve.Program{ID: "example-usage", Version: "0.1.0",
		Namespaces: map[string]serve.Handler{"ui": func(_ context.Context, method string, params json.RawMessage) (any, error) {
			var p extension.UIParams
			_ = json.Unmarshal(params, &p)
			if method != "render" {
				return nil, serve.UnknownMethod(method)
			}
			if !slices.Contains([]string{"usage", "today", "gpu"}, p.Contribution) {
				return nil, serve.NoContribution(p.Contribution)
			}
			if _, ok := drawn[p.Contribution]; !ok {
				drawn[p.Contribution] = p // the first: for this hub's UI version
			}
			versions[p.UIVersion] = true
			chart := extension.UIElem{Type: "chart", Kind: "bar", Unit: "tokens", Series: []extension.ChartSeries{{Points: []extension.ChartPoint{{Label: "一", Value: 1}}}}}
			return extension.View{RefreshAfter: 300, Sections: []extension.Section{{Items: []extension.UIElem{
				{Type: "stat_grid", Stats: []extension.Stat{{Label: "今天", Value: "1.2M"}}}, chart}}}}, nil
		}}}
	Check(t, m, Serve(t, prog), extension.InitializeParams{})
	if drawn["today"].Size != extension.SizeCompact || drawn["usage"].Size != extension.SizeFull || drawn["gpu"].Machine == nil ||
		drawn["usage"].TZ == "" || drawn["usage"].UIVersion != extension.UIVersion {
		t.Fatalf("%+v", drawn)
	}
	// and drawn again for an app of the version before
	if !versions[extension.UIVersion] || !versions[extension.UIVersion-1] {
		t.Fatalf("drawn for UI versions %v", versions)
	}
}

// A plugin's inbox_item is drawn for an example item of its connector.
func TestInboxItemConforms(t *testing.T) {
	m, err := extension.Parse([]byte(`{"manifest_version": 1, "id": "example-chat", "name": "Chat", "version": "0.1.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": "bin/chat"},
		"contributes": {"connectors": [{"id": "chat", "name": "Chat", "capabilities": ["status", "inbox"]}],
			"ui": [{"id": "event", "title": "事件", "placement": "inbox_item", "connector": "chat"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var drawn extension.UIParams
	prog := serve.Program{ID: "example-chat", Version: "0.1.0",
		Init: func(context.Context, extension.InitializeParams, *serve.Hub) (map[string]any, error) {
			return map[string]any{"connectors": []extension.ConnectorItem{{ID: "chat", Name: "Chat", Capabilities: []string{"inbox", "status"}}}}, nil
		},
		Namespaces: map[string]serve.Handler{
			"connector": func(_ context.Context, method string, params json.RawMessage) (any, error) {
				if c := serve.Contribution(params); c != "" && c != "chat" {
					return nil, serve.NoContribution(c)
				}
				if method == "status" {
					return map[string]string{"state": "ok"}, nil
				}
				return nil, serve.UnknownMethod(method)
			},
			"ui": func(_ context.Context, method string, params json.RawMessage) (any, error) {
				if method != "render" {
					return nil, serve.UnknownMethod(method)
				}
				if c := serve.Contribution(params); c != "event" {
					return nil, serve.NoContribution(c)
				}
				_ = json.Unmarshal(params, &drawn)
				return extension.View{Sections: []extension.Section{{Items: []extension.UIElem{{Type: "text", Text: drawn.Item.Title}}}}}, nil
			}}}
	Check(t, m, Serve(t, prog), extension.InitializeParams{})
	if drawn.Placement != "inbox_item" || drawn.Item == nil || !strings.HasPrefix(drawn.Item.ID, "chat:") || drawn.Item.Title == "" {
		t.Fatalf("%+v", drawn)
	}
}
