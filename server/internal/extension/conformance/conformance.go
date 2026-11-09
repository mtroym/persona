// Package conformance holds a plugin program to the plugin protocol (docs/extensions/extensions.md "Conformance tests"): the handshake,
// the error codes, the shape of what it answers, and that it exits when the hub goes away. A program written in any
// language can be held to it with Program; one in Go with Serve. What each extension point's methods answer is
// checked by that point's own tests (connector/conformance for connectors); a ui contribution's View is checked here.
// What the handshake and a View hold is checked against schema/extension (wire.Validate), the apps' own truth.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Run is a running program: what the hub writes to and reads from it, and its exit (nil when it cannot be watched).
type Run struct {
	ToProgram   io.WriteCloser
	FromProgram io.Reader
	Exited      <-chan error
}

// Program runs the executable at path (with args) for Check.
func Program(t testing.TB, path string, args ...string) Run {
	t.Helper()
	cmd := exec.Command(path, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return Run{ToProgram: in, FromProgram: out, Exited: exited}
}

// Serve runs prog in this process for Check.
func Serve(t testing.TB, prog serve.Program) Run {
	t.Helper()
	progR, hubW := io.Pipe()
	hubR, progW := io.Pipe()
	exited := make(chan error, 1)
	go func() {
		err := serve.Serve(context.Background(), progR, progW, prog)
		_ = progW.Close()
		if errors.Is(err, io.EOF) {
			err = nil
		}
		exited <- err
	}()
	t.Cleanup(func() { hubW.Close(); progR.Close(); hubR.Close() })
	return Run{ToProgram: hubW, FromProgram: hubR, Exited: exited}
}

var snake = lazyre.New(`^[a-z][a-z0-9_]*$`)

// T is what Check reports to: a test's testing.TB, or `personad extension test`'s own (Fatalf ends the check).
type T interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Check holds the program to the protocol, m being its manifest; init is what to send with initialize (the plugin's
// id and version are filled in from m). It returns what initialize answered.
func Check(t T, m *extension.Manifest, run Run, init extension.InitializeParams) extension.InitializeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hub := rpc.New("h", run.ToProgram)
	hub.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return struct{}{}, nil }
	go func() { _ = hub.Serve(ctx, run.FromProgram) }()
	code := func(err error) string {
		var re *rpc.Error
		if errors.As(err, &re) {
			return re.Code
		}
		return ""
	}

	if err := hub.Call(ctx, extension.MPing, struct{}{}, nil); code(err) != extension.CodeNotInitialized {
		t.Fatalf("ping before initialize: want %s, got %v", extension.CodeNotInitialized, err)
	}
	if err := hub.Call(ctx, extension.MInitialize, extension.InitializeParams{Protocol: extension.Protocol + 100}, nil); code(err) != extension.CodeWrongProtocol {
		t.Fatalf("initialize of another protocol: want %s, got %v", extension.CodeWrongProtocol, err)
	}

	init.Protocol = extension.Protocol
	init.Extension.ID, init.Extension.Version = m.ID, m.Version
	if init.Hub.ExtensionPoints == nil {
		init.Hub.ExtensionPoints = extension.PointVersions()
	}
	var raw json.RawMessage
	if err := hub.Call(ctx, extension.MInitialize, init, &raw); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	snakeKeys(t, "initialize", raw, 0)
	if err := wire.ValidateJSON("InitializeResult", raw); err != nil {
		t.Fatalf("initialize: the answer does not hold to the schema: %v", err)
	}
	var res extension.InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if res.Protocol != extension.Protocol || res.ID != m.ID || res.Version != m.Version {
		t.Fatalf("initialize answered %d %q %q, the manifest says %d %q %q", res.Protocol, res.ID, res.Version, extension.Protocol, m.ID, m.Version)
	}
	declared := m.Contributions()
	for point, items := range res.Contributes {
		p, ok := extension.Points[point]
		if !ok {
			continue
		}
		var list []json.RawMessage
		if err := json.Unmarshal(items, &list); err != nil {
			t.Fatalf("contributes.%s: a list", point)
		}
		for _, item := range list {
			id, err := p.Item(item)
			if err != nil {
				t.Fatalf("contributes.%s: %v", point, err)
			}
			if !slices.Contains(declared[point], id) {
				t.Fatalf("contributes.%s: %q is not in the manifest", point, id)
			}
		}
	}

	if err := hub.Call(ctx, extension.MPing, struct{}{}, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := hub.Call(ctx, "nonexistent.method", map[string]any{}, nil); code(err) != extension.CodeUnknownMethod {
		t.Fatalf("an unknown method: want %s, got %v", extension.CodeUnknownMethod, err)
	}
	for point := range declared {
		ns := extension.Points[point].Namespace
		if err := hub.Call(ctx, ns+".nonexistent_method", map[string]any{}, nil); code(err) != extension.CodeUnknownMethod {
			t.Fatalf("an unknown method of %s: want %s, got %v", ns, extension.CodeUnknownMethod, err)
		}
		if err := hub.Call(ctx, ns+".status", map[string]any{"contribution": "no-such-contribution"}, nil); code(err) != extension.CodeNoContribution &&
			code(err) != extension.CodeUnknownMethod {
			t.Fatalf("a contribution it does not have: want %s, got %v", extension.CodeNoContribution, err)
		}
	}

	// Each ui contribution draws, for an app of this hub's UI version, a View the apps can draw where it goes; or
	// says in words why it cannot now (its service is not reachable from the test): never that it has no ui.render.
	for _, u := range m.UIs() {
		slot, ok := extension.Slot(u.Placement)
		if !ok {
			continue
		}
		params := extension.UIParams{Contribution: u.ID, Placement: u.Placement, Size: slot.Size, UIVersion: extension.UIVersion,
			TZ: "Asia/Shanghai", Locale: "zh-Hans-CN", Platform: "web"}
		switch slot.Context {
		case "machine":
			params.Machine = &extension.UIMachine{ID: "example-machine", Name: "Example", OS: "darwin", Online: true}
		case "launcher":
			params.Launcher = &extension.UILauncher{Name: "example", BaseURL: "https://gateway.example.com", QueryID: "conformance"}
		case "item":
			params.Item = &extension.UIInboxItem{ID: u.Connector + ":conformance", Title: "示例", Summary: "一件示例的事", Tier: "fyi",
				Meta: json.RawMessage("null")}
		}
		// A widget draws at each of its families (what a widget does not draw may be left out: it is cut to fit).
		sizes := []string{params.Size}
		if u.Placement == "widget" {
			sizes = u.Families
		}
		for _, size := range sizes {
			params.Size = size
			var raw json.RawMessage
			err := hub.Call(ctx, "ui.render", params, &raw)
			switch c := code(err); {
			case c == extension.CodeUnknownMethod || c == extension.CodeNoContribution:
				t.Fatalf("ui %s: ui.render answered %v", u.ID, err)
			case err != nil:
				continue
			}
			snakeKeys(t, "ui "+u.ID, raw, 0)
			var v extension.View
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("ui %s: %v", u.ID, err)
			}
			if u.Placement == "widget" {
				if _, err := extension.FitWidget(&v, size, extension.UIVersion); err != nil {
					t.Fatalf("ui %s: a %s widget cannot draw it: %v", u.ID, size, err)
				}
				continue
			}
			if dropped, err := extension.Fit(&v, slot, extension.UIVersion); err != nil || len(dropped) > 0 {
				t.Fatalf("ui %s: the apps cannot draw it at %s: %v (unknown items: %v)", u.ID, u.Placement, err, dropped)
			}
			// UI version 6: each picture it drew by ref is served (ui.image), held to what the hub's image proxy takes
			// (wire.CheckImage); an error in words is allowed (a camera the test cannot reach), not knowing ui.image is not.
			for k, ref := range wire.ImageRefs(&v) {
				if k == imageRefsChecked {
					break
				}
				var img wire.UIImage
				err := hub.Call(ctx, "ui.image", wire.UIImageParams{Contribution: u.ID, Ref: ref, Width: 640, Placement: u.Placement}, &img)
				switch c := code(err); {
				case c == extension.CodeUnknownMethod || c == extension.CodeNoContribution:
					t.Fatalf("ui %s: it draws ref %q, and ui.image answered %v", u.ID, ref, err)
				case err != nil:
					continue
				}
				if _, err := wire.CheckImage(&img); err != nil {
					t.Fatalf("ui %s: ref %q: %v", u.ID, ref, err)
				}
			}
			// and drawn for an app of the version before, it is still a view the hub can fit: the extension does not
			// count on every app drawing this hub's version
			older := params
			older.UIVersion = extension.UIVersion - 1
			var oldRaw json.RawMessage
			err = hub.Call(ctx, "ui.render", older, &oldRaw)
			switch c := code(err); {
			case c == extension.CodeUnknownMethod || c == extension.CodeNoContribution:
				t.Fatalf("ui %s: ui.render for UI version %d answered %v", u.ID, older.UIVersion, err)
			case err != nil:
				continue
			}
			var old extension.View
			if err := json.Unmarshal(oldRaw, &old); err != nil {
				t.Fatalf("ui %s (UI version %d): %v", u.ID, older.UIVersion, err)
			}
			if _, err := extension.Fit(&old, slot, older.UIVersion); err != nil {
				t.Fatalf("ui %s: drawn for UI version %d, the apps cannot draw it: %v", u.ID, older.UIVersion, err)
			}
		}
	}

	// Each shortcut runs (shortcut.run): it may refuse the empty values in words, never not know the method.
	for _, s := range m.Shortcuts() {
		var res struct {
			Text string          `json:"text"`
			View *extension.View `json:"view"`
		}
		err := hub.Call(ctx, "shortcut.run", map[string]any{"contribution": s.ID, "values": map[string]string{},
			"ui_version": extension.UIVersion, "tz": "Asia/Shanghai", "locale": "zh-Hans-CN", "platform": "ios"}, &res)
		switch c := code(err); {
		case c == extension.CodeUnknownMethod || c == extension.CodeNoContribution:
			t.Fatalf("shortcut %s: shortcut.run answered %v", s.ID, err)
		case err == nil && res.View != nil:
			if _, err := extension.FitWidget(res.View, "large", extension.UIVersion); err != nil {
				t.Fatalf("shortcut %s: its view cannot be drawn: %v", s.ID, err)
			}
		}
	}

	if run.Exited != nil {
		_ = run.ToProgram.Close()
		select {
		case <-run.Exited:
		case <-time.After(5 * time.Second):
			t.Fatalf("still running 5 seconds after the hub closed its stdin")
		}
	}
	return res
}

// imageRefsChecked is how many of a view's refs the conformance tests ask ui.image for.
const imageRefsChecked = 20

// snakeKeys checks every object key in raw is snake_case.
func snakeKeys(t T, at string, raw json.RawMessage, depth int) {
	t.Helper()
	if depth > 6 {
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		for k, v := range obj {
			if !snake.MatchString(k) {
				t.Fatalf("%s: key %q is not snake_case", at, k)
			}
			snakeKeys(t, at+"."+k, v, depth+1)
		}
		return
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		for _, v := range list {
			snakeKeys(t, at, v, depth+1)
		}
	}
}
