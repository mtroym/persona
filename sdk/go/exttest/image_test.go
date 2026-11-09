package exttest_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

var cameraManifest = []byte(`{
  "manifest_version": 1, "id": "camera", "name": "Camera", "version": "1.0.0",
  "engines": {"extension_protocol": 1}, "runtime": {"command": "bin/camera"},
  "contributes": {"ui": [{"id": "home", "title": "摄像头", "placement": "nav_page"}]}
}`)

func cameraProgram(serve func(*ext.ImageRequest) (*wire.UIImage, error)) ext.Program {
	page := ext.Page{Render: func(_ context.Context, r *ext.UIRequest) (*wire.View, error) {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{
			{Type: wire.ComponentImage, Src: &wire.ImageSource{Ref: "front"}, Alt: "前门"},
			{Type: wire.ComponentGrid, Tiles: []wire.Tile{{Title: "院子", Image: &wire.ImageSource{Ref: "yard"}}}},
		}}}}, nil
	}}
	if serve != nil {
		page.Image = func(_ context.Context, r *ext.ImageRequest) (*wire.UIImage, error) { return serve(r) }
	}
	return ext.Program{ID: "camera", Version: "1.0.0", Manifest: cameraManifest, UI: map[string]ext.Page{"home": page}}
}

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

// checked runs Check on prog and says what it refused ("" for nothing).
func checked(t *testing.T, prog ext.Program) string {
	f := &fatal{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		exttest.Check(f, cameraManifest, exttest.Serve(t, prog))
	}()
	<-done
	return f.said
}

// Each picture a page draws by ref is served (ui.image) as the hub's image proxy takes it.
func TestCheckImages(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 2)))
	asked := map[string]int{}
	ok := cameraProgram(func(r *ext.ImageRequest) (*wire.UIImage, error) {
		asked[r.Ref] = r.Width
		return ext.Image("", b.Bytes(), 10), nil
	})
	if said := checked(t, ok); said != "" || asked["front"] == 0 || asked["yard"] == 0 {
		t.Fatalf("refused %q, asked %v", said, asked)
	}
	for name, prog := range map[string]ext.Program{
		"no Image": cameraProgram(nil),
		"an svg": cameraProgram(func(*ext.ImageRequest) (*wire.UIImage, error) {
			return ext.Image("image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 10), nil
		}),
	} {
		if said := checked(t, prog); !strings.Contains(said, `ref "front"`) {
			t.Errorf("%s: taken (%q)", name, said)
		}
	}
}
