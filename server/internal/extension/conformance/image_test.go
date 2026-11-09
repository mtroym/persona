package conformance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/serve"
)

// fatal is a testing.TB whose Fatalf ends the check and keeps what it said, to test what Check refuses.
type fatal struct {
	testing.TB
	said string
}

func (f *fatal) Fatalf(format string, args ...any) {
	f.said = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (f *fatal) Fatal(args ...any) {
	f.said = fmt.Sprint(args...)
	runtime.Goexit()
}

// checks runs Check on prog with f, and says what it refused ("" for nothing).
func checks(t *testing.T, m *extension.Manifest, prog serve.Program) string {
	f := &fatal{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Check(f, m, Serve(t, prog), extension.InitializeParams{})
	}()
	<-done
	return f.said
}

// imageProg draws a picture by ref and answers ui.image with image (nil: it has no ui.image).
func imageProg(image func() (any, error)) serve.Program {
	return serve.Program{ID: "example-camera", Version: "0.1.0",
		Namespaces: map[string]serve.Handler{"ui": func(_ context.Context, method string, params json.RawMessage) (any, error) {
			if method != "render" && (method != "image" || image == nil) {
				return nil, serve.UnknownMethod(method)
			}
			if c := serve.Contribution(params); c != "home" {
				return nil, serve.NoContribution(c)
			}
			switch {
			case method == "render":
				return extension.View{Sections: []extension.Section{{Items: []extension.UIElem{
					{Type: "image", Src: &wire.ImageSource{Ref: "front"}, Alt: "前门"}}}}}, nil
			case method == "image" && image != nil:
				return image()
			}
			return nil, serve.UnknownMethod(method)
		}}}
}

// A ui that draws a picture by ref serves it (ui.image) as the hub's image proxy takes it; an error in words is fine.
func TestUIImageConforms(t *testing.T) {
	m, err := extension.Parse([]byte(`{"manifest_version": 1, "id": "example-camera", "name": "Camera", "version": "0.1.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": "bin/camera"},
		"contributes": {"ui": [{"id": "home", "title": "摄像头", "placement": "nav_page"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 2)))
	good := base64.StdEncoding.EncodeToString(b.Bytes())
	if said := checks(t, m, imageProg(func() (any, error) { return map[string]any{"mime": "image/png", "data": good}, nil })); said != "" {
		t.Fatalf("a served picture refused: %s", said)
	}
	if said := checks(t, m, imageProg(func() (any, error) { return nil, fmt.Errorf("the camera is off") })); said != "" {
		t.Fatalf("an error in words refused: %s", said)
	}
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	for name, prog := range map[string]serve.Program{
		"no ui.image": imageProg(nil),
		"an svg":      imageProg(func() (any, error) { return map[string]any{"mime": "image/png", "data": svg}, nil }),
	} {
		if said := checks(t, m, prog); !strings.Contains(said, `ref "front"`) {
			t.Errorf("%s: taken (%q)", name, said)
		}
	}
}
