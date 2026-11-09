// Package exttest holds an extension program to the process protocol from its own tests (go test), with no hub
// around: the handshake, the error codes, snake_case keys, that each ui contribution draws a View the apps can
// draw, and that it exits when the hub goes away. `personad extension test` runs the hub's own, stricter check
// (the same one the hub applies when it installs and draws) on the built program.
//
//	func TestConformance(t *testing.T) {
//		exttest.Check(t, manifest, exttest.Serve(t, program()))
//	}
package exttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Run is a running program: what the hub writes to and reads from it, and its exit (nil when it cannot be watched).
type Run struct {
	ToProgram   io.WriteCloser
	FromProgram io.Reader
	Exited      <-chan error
}

// Program runs the executable at path (with args).
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

// Serve runs prog in this process.
func Serve(t testing.TB, prog ext.Program) Run {
	t.Helper()
	progR, hubW := io.Pipe()
	hubR, progW := io.Pipe()
	exited := make(chan error, 1)
	go func() {
		err := ext.Serve(context.Background(), progR, progW, prog)
		_ = progW.Close()
		if errors.Is(err, io.EOF) {
			err = nil
		}
		exited <- err
	}()
	t.Cleanup(func() { hubW.Close(); progR.Close(); hubR.Close() })
	return Run{ToProgram: hubW, FromProgram: hubR, Exited: exited}
}

// Check holds the program to the protocol; manifest is its persona-extension.json. Config is sent with
// initialize (nil: none). It returns what initialize answered.
func Check(t testing.TB, manifest []byte, run Run, config ...json.RawMessage) wire.InitializeResult {
	t.Helper()
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newHub(run)
	h.manifest = &m // what it declares is granted here
	go h.serve(ctx)

	if err := h.call(ctx, wire.MethodPing, struct{}{}, nil); code(err) != wire.ErrorCodeNotInitialized {
		t.Fatalf("ping before initialize: want %s, got %v", wire.ErrorCodeNotInitialized, err)
	}
	if err := h.call(ctx, wire.MethodInitialize, wire.InitializeParams{Protocol: ext.Protocol + 100}, nil); code(err) != wire.ErrorCodeWrongProtocol {
		t.Fatalf("initialize of another protocol: want %s, got %v", wire.ErrorCodeWrongProtocol, err)
	}
	info := &wire.ExtensionInfo{ID: m.ID, Version: m.Version, Dir: t.TempDir(), DataDir: t.TempDir()}
	init := wire.InitializeParams{Protocol: ext.Protocol, Extension: info, Plugin: info,
		Hub: wire.HubInfo{Build: 0, Role: wire.HubRoleCenter, Timezone: "Asia/Shanghai", ExtensionPoints: map[string]int{}}}
	if len(config) > 0 {
		init.Config = config[0]
	}
	if m.Permissions != nil {
		init.Granted = *m.Permissions // what it declares is granted here, as the host refuses by it
	}
	var raw json.RawMessage
	if err := h.call(ctx, wire.MethodInitialize, init, &raw); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	snakeKeys(t, "initialize", raw, 0)
	var res wire.InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if res.Protocol != ext.Protocol || res.ID != m.ID || res.Version != m.Version {
		t.Fatalf("initialize answered %d %q %q, the manifest says %d %q %q", res.Protocol, res.ID, res.Version, ext.Protocol, m.ID, m.Version)
	}
	declared := contributions(manifest)
	for point, items := range res.Contributes {
		var list []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(items, &list); err != nil {
			t.Fatalf("contributes.%s: a list of contributions", point)
		}
		for _, it := range list {
			if !slices.Contains(declared[point], it.ID) {
				t.Fatalf("contributes.%s: %q is not in the manifest", point, it.ID)
			}
		}
	}
	if err := h.call(ctx, wire.MethodPing, struct{}{}, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := h.call(ctx, "nonexistent.method", map[string]any{}, nil); code(err) != wire.ErrorCodeUnknownMethod {
		t.Fatalf("an unknown method: want %s, got %v", wire.ErrorCodeUnknownMethod, err)
	}
	if len(m.Contributes.UI) > 0 {
		if err := h.call(ctx, "ui.nonexistent_method", map[string]any{}, nil); code(err) != wire.ErrorCodeUnknownMethod {
			t.Fatalf("an unknown method of ui: want %s, got %v", wire.ErrorCodeUnknownMethod, err)
		}
		if err := h.call(ctx, "ui.render", map[string]any{"contribution": "no-such-contribution"}, nil); code(err) != wire.ErrorCodeNoContribution {
			t.Fatalf("ui.render of a contribution it does not have: want %s, got %v", wire.ErrorCodeNoContribution, err)
		}
	}

	for _, u := range m.Contributes.UI {
		size := wire.SizeFull
		if u.Placement == wire.PlacementOverviewCard {
			size = wire.SizeCompact
		}
		sizes := []string{size}
		if u.Placement == wire.PlacementWidget { // drawn once per family it says it has
			sizes = u.Families
		}
		for _, size := range sizes {
			params := wire.UIParams{Contribution: u.ID, Placement: u.Placement, Size: size, UIVersion: uiVersion,
				TZ: "Asia/Shanghai", Locale: "zh-Hans-CN", Platform: wire.PlatformWeb}
			switch u.Placement {
			case wire.PlacementMachineSection:
				params.Machine = &wire.UIMachine{ID: "example-machine", Name: "Example", OS: "darwin", Online: true}
			case wire.PlacementLauncherSection:
				params.Launcher = &wire.UILauncher{Name: "example", BaseURL: "https://gateway.example.com", QueryID: "exttest"}
			case wire.PlacementInboxItem:
				params.Item = &wire.UIInboxItem{ID: u.Connector + ":exttest", Title: "示例", Summary: "一件示例的事", Tier: "fyi",
					Meta: json.RawMessage("null")}
			}
			var raw json.RawMessage
			err := h.call(ctx, "ui.render", params, &raw)
			switch c := code(err); {
			case c == wire.ErrorCodeUnknownMethod || c == wire.ErrorCodeNoContribution:
				t.Fatalf("ui %s (%s): ui.render answered %v", u.ID, size, err)
			case err != nil:
				continue // in words: its service is not reachable from a test
			}
			snakeKeys(t, "ui "+u.ID, raw, 0)
			if err := CheckView(raw, size != wire.SizeFull); err != nil {
				t.Fatalf("ui %s (%s): %v", u.ID, size, err)
			}
			if u.Placement == wire.PlacementWidget {
				continue
			}
			var v wire.View
			_ = json.Unmarshal(raw, &v)
			checkImages(ctx, t, h, u.ID, u.Placement, &v)
			// drawn for an app of the version before, it is still a view: the extension does not count on every app
			// drawing the newest version
			older := params
			older.UIVersion = uiVersion - 1
			var oldRaw json.RawMessage
			err = h.call(ctx, "ui.render", older, &oldRaw)
			switch c := code(err); {
			case c == wire.ErrorCodeUnknownMethod || c == wire.ErrorCodeNoContribution:
				t.Fatalf("ui %s: ui.render for UI version %d answered %v", u.ID, older.UIVersion, err)
			case err != nil:
				continue
			}
			if err := CheckView(oldRaw, size != wire.SizeFull); err != nil {
				t.Fatalf("ui %s drawn for UI version %d: %v", u.ID, older.UIVersion, err)
			}
		}
	}

	// Each shortcut runs: it may refuse the empty values in words, never not know the method or the shortcut; a view
	// it answers is drawn as a card.
	for _, sc := range m.Contributes.Shortcuts {
		var raw json.RawMessage
		err := h.call(ctx, "shortcut.run", wire.ShortcutRunParams{Contribution: sc.ID, Values: map[string]string{}, UIVersion: uiVersion,
			TZ: "Asia/Shanghai", Locale: "zh-Hans-CN", Platform: wire.PlatformIos}, &raw)
		switch c := code(err); {
		case c == wire.ErrorCodeUnknownMethod || c == wire.ErrorCodeNoContribution:
			t.Fatalf("shortcut %s: shortcut.run answered %v", sc.ID, err)
		case err != nil:
			continue
		}
		snakeKeys(t, "shortcut "+sc.ID, raw, 0)
		var res wire.ShortcutResult
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("shortcut %s: %v", sc.ID, err)
		}
		if res.View != nil {
			b, _ := json.Marshal(res.View)
			if err := CheckView(b, true); err != nil {
				t.Fatalf("shortcut %s: its view: %v", sc.ID, err)
			}
		}
	}

	if run.Exited != nil {
		_ = run.ToProgram.Close()
		select {
		case <-run.Exited:
		case <-time.After(5 * time.Second):
			t.Fatal("still running 5 seconds after the hub closed its stdin")
		}
	}
	return res
}

// imageRefsChecked is how many of a view's refs are asked for.
const imageRefsChecked = 20

// checkImages asks ui.image for each picture v drew by ref (UI version 6) and holds the answer to what the hub's image
// proxy takes (wire.CheckImage). An error in words is allowed (a camera the test cannot reach), not knowing the
// method or the contribution is not.
func checkImages(ctx context.Context, t testing.TB, h *hub, ui, placement string, v *wire.View) {
	for k, ref := range wire.ImageRefs(v) {
		if k == imageRefsChecked {
			return
		}
		var img wire.UIImage
		err := h.call(ctx, "ui.image", wire.UIImageParams{Contribution: ui, Ref: ref, Width: 640, Placement: placement}, &img)
		switch c := code(err); {
		case c == wire.ErrorCodeUnknownMethod || c == wire.ErrorCodeNoContribution:
			t.Fatalf("ui %s: it draws ref %q, and ui.image answered %v", ui, ref, err)
		case err != nil:
			continue
		}
		if _, err := wire.CheckImage(&img); err != nil {
			t.Fatalf("ui %s: ref %q: %v", ui, ref, err)
		}
	}
}

// uiVersion is the UI version the views are checked for: the newest these wire types know.
var uiVersion = func() int {
	v := 1
	for _, s := range wire.ComponentSince {
		v = max(v, s)
	}
	return v
}()

// CheckView checks a View the way the apps will read it: against the schema (wire.ValidateJSON: only known fields
// and components, the tones, the limits of each), https links, at most 300 components and 16 KB of state, and for
// a card (compact: an overview card, a widget, a shortcut's result) at most 2 sections and 12 components and no
// fields. The hub holds a widget to less still (FitWidget): `personad extension test` checks that.
func CheckView(raw json.RawMessage, compact bool) error {
	if err := wire.ValidateJSON("View", raw); err != nil {
		return err
	}
	var v wire.View
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("not a view: %w", err)
	}
	if len(v.State) > 16<<10 {
		return errors.New("state is over 16 KB")
	}
	n := 0
	for si, s := range v.Sections {
		for ii, c := range s.Items {
			n++
			at := fmt.Sprintf("sections[%d].items[%d] (%s)", si, ii, c.Type)
			if c.Type == wire.ComponentLink && !strings.HasPrefix(c.URL, "https://") {
				return fmt.Errorf("%s: links are https", at)
			}
			if compact && c.Type == wire.ComponentField {
				return fmt.Errorf("%s: a card has no fields (open a page for them)", at)
			}
			if err := longStrings(c); err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
		}
	}
	switch {
	case n > 300:
		return fmt.Errorf("%d components: 300 at most", n)
	case compact && (len(v.Sections) > 2 || n > 12):
		return fmt.Errorf("a card has at most 2 sections and 12 components, not %d and %d", len(v.Sections), n)
	}
	return nil
}

func longStrings(c wire.Component) error {
	b, _ := json.Marshal(c)
	var walk func(any) error
	walk = func(x any) error {
		switch x := x.(type) {
		case string:
			if len([]rune(x)) > 2000 {
				return errors.New("a string over 2000 characters")
			}
		case map[string]any:
			for _, v := range x {
				if err := walk(v); err != nil {
					return err
				}
			}
		case []any:
			for _, v := range x {
				if err := walk(v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var x any
	_ = json.Unmarshal(b, &x)
	return walk(x)
}

// contributions are the manifest's contribution ids by extension point.
func contributions(manifest []byte) map[string][]string {
	var m struct {
		Contributes map[string][]struct {
			ID string `json:"id"`
		} `json:"contributes"`
	}
	_ = json.Unmarshal(manifest, &m)
	out := map[string][]string{}
	for point, items := range m.Contributes {
		for _, it := range items {
			out[point] = append(out[point], it.ID)
		}
	}
	return out
}

var snake = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func snakeKeys(t testing.TB, at string, raw json.RawMessage, depth int) {
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
			// These two catalog payloads belong to the upstream tool. Keep
			// checking their surrounding protocol fields, including these keys.
			if at == "connector.catalog.list.sources.tools" && k == "input_schema" ||
				at == "connector.catalog.list.sources.recipes" && k == "args" {
				continue
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

func code(err error) string {
	var e *ext.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
