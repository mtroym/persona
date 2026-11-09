package ext

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// ui.image goes to the page that drew the ref; a page without Image answers unknown_method, another contribution
// no_contribution; what Image and ImageFile make passes the hub's check.
func TestUIImage(t *testing.T) {
	pic := pngBytes(t)
	file := filepath.Join(t.TempDir(), "map.bin")
	if err := os.WriteFile(file, pic, 0o600); err != nil {
		t.Fatal(err)
	}
	c := start(t, Program{ID: "example", Version: "1.0.0", UI: map[string]Page{
		"home": {
			Render: func(context.Context, *UIRequest) (*wire.View, error) {
				return &wire.View{Sections: []wire.Section{}}, nil
			},
			Image: func(_ context.Context, r *ImageRequest) (*wire.UIImage, error) {
				if r.Ref == "map" {
					return ImageFile(file, 30)
				}
				if r.Width != 640 {
					return nil, &Error{Code: "invalid", Message: "width"}
				}
				return Image("image/png", pic, 10), nil
			},
		},
		"plain": {Render: func(context.Context, *UIRequest) (*wire.View, error) {
			return &wire.View{Sections: []wire.Section{}}, nil
		}},
	}})
	c.call("initialize", map[string]any{"protocol": 1, "hub": map[string]any{"build": 1}, "extension": map[string]any{"id": "example", "version": "1.0.0"}})
	for ref, age := range map[string]int{"cam": 10, "map": 30} {
		f := c.call("ui.image", wire.UIImageParams{Contribution: "home", Ref: ref, Width: 640})
		var img wire.UIImage
		if f.Error != nil || json.Unmarshal(f.Result, &img) != nil {
			t.Fatalf("%s: %+v %s", ref, f.Error, f.Result)
		}
		got, err := wire.CheckImage(&img)
		if err != nil || got.Mime != "image/png" || got.MaxAge != age || !bytes.Equal(got.Data, pic) {
			t.Fatalf("%s: %+v %v", ref, got, err)
		}
	}
	if f := c.call("ui.image", wire.UIImageParams{Contribution: "plain", Ref: "x"}); f.Error == nil || f.Error.Code != wire.ErrorCodeUnknownMethod {
		t.Fatalf("a page without Image: %+v", f.Error)
	}
	if f := c.call("ui.image", wire.UIImageParams{Contribution: "nope", Ref: "x"}); f.Error == nil || f.Error.Code != wire.ErrorCodeNoContribution {
		t.Fatalf("another contribution: %+v", f.Error)
	}
	// Image reads the type when it is not given, and holds max_age to its bounds
	img := Image("", pic, 999999)
	if img.Mime != "image/png" || *img.MaxAge != wire.ImageMaxAgeMost || img.Data != base64.StdEncoding.EncodeToString(pic) {
		t.Fatalf("%+v", img)
	}
	if _, err := ImageFile(filepath.Join(t.TempDir(), "none"), 1); err == nil {
		t.Fatal("a missing file")
	}
}

// The value readers read what UI version 6 writes, in the user's time zone.
func TestValues(t *testing.T) {
	r := &UIRequest{UIParams: wire.UIParams{TZ: "Asia/Shanghai", Values: map[string]string{
		"on": "true", "off": "false", "level": " 22.5 ", "bad": "x", "day": "2026-11-06", "at": "07:30",
		"until": "2026-11-06T22:00", "rooms": `["kitchen","bedroom"]`, "notjson": "a,b", "baddate": "2026-13-01",
	}}}
	if !r.Bool("on") || r.Bool("off") || r.Bool("none") {
		t.Fatal("Bool")
	}
	if x, err := r.Number("level"); err != nil || x != 22.5 {
		t.Fatalf("Number %v %v", x, err)
	}
	for _, name := range []string{"bad", "none"} {
		if _, err := r.Number(name); err == nil {
			t.Errorf("Number(%s) taken", name)
		}
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if d, err := r.Date("day"); err != nil || !d.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, loc)) {
		t.Fatalf("Date %v %v", d, err)
	}
	if h, m, err := r.Clock("at"); err != nil || h != 7 || m != 30 {
		t.Fatalf("Clock %d %d %v", h, m, err)
	}
	if u, err := r.Time("until"); err != nil || !u.Equal(time.Date(2026, 11, 6, 22, 0, 0, 0, loc)) || u.UTC().Hour() != 14 {
		t.Fatalf("Time %v %v", u, err)
	}
	for _, name := range []string{"baddate", "none", "at"} {
		if _, err := r.Date(name); err == nil {
			t.Errorf("Date(%s) taken", name)
		}
	}
	if got := r.Multi("rooms"); len(got) != 2 || got[0] != "kitchen" {
		t.Fatalf("Multi %v", got)
	}
	if r.Multi("notjson") != nil || r.Multi("none") != nil {
		t.Fatal("Multi of what is not an array")
	}
	if (&UIRequest{UIParams: wire.UIParams{TZ: "Nowhere/Here"}}).Location() != time.UTC {
		t.Fatal("an unknown zone is UTC")
	}
}
