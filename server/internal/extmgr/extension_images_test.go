package extmgr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
)

func testPNG() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 3)))
	return b.Bytes()
}

// pngClaiming is a png's header saying it is w × h (enough for the size check).
func pngClaiming(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&b, binary.BigEndian, uint32(13))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

// serveImagesExtensionForTest draws pictures by ref and serves them (ui.image), writing each call to IMAGES_LOG.
func serveImagesExtensionForTest() {
	logCall := func(ref string, width int) {
		f, err := os.OpenFile(os.Getenv("IMAGES_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%s %d\n", ref, width)
			_ = f.Close()
		}
	}
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	view := map[string]any{"sections": []any{map[string]any{"items": []any{
		map[string]any{"type": "image", "key": "cam", "src": map[string]any{"ref": "cam"}, "alt": "前门"},
		map[string]any{"type": "row", "key": "r", "title": "快递", "thumbnail": map[string]any{"ref": "cam"}},
		map[string]any{"type": "image", "key": "web", "src": map[string]any{"url": "https://example.com/a.png"}, "alt": "网上的"},
		map[string]any{"type": "grid", "key": "g", "tiles": []any{map[string]any{"key": "t", "title": "院子", "image": map[string]any{"ref": "slow"}}}},
	}}}}
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-images", Version: "1.0.0",
		Namespaces: map[string]pserve.Handler{
			"ui": func(_ context.Context, method string, params json.RawMessage) (any, error) {
				switch method {
				case "render":
					return view, nil
				case "action":
					return map[string]any{"items": []any{map[string]any{"type": "image", "key": "cam", "src": map[string]any{"ref": "cam"}, "alt": "前门"}}}, nil
				case "image":
					var p wire.UIImageParams
					_ = json.Unmarshal(params, &p)
					logCall(p.Ref, p.Width)
					age := 60
					switch p.Ref {
					case "slow":
						time.Sleep(300 * time.Millisecond) // sleep-ok: a slow camera, so the concurrent requests for it overlap and coalesce into one ui.image
					case "noage":
						age = 0
					case "svg":
						return map[string]any{"mime": "image/png", "data": b64([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))}, nil
					case "html":
						return map[string]any{"mime": "image/png", "data": b64([]byte("<!DOCTYPE html><script>alert(1)</script>"))}, nil
					case "huge":
						return map[string]any{"mime": "image/png", "data": b64(pngClaiming(9000, 10))}, nil
					case "fail":
						return nil, fmt.Errorf("the camera is off")
					}
					return map[string]any{"mime": "image/jpeg", "data": b64(testPNG()), "max_age": age}, nil
				}
				return nil, pserve.UnknownMethod(method)
			}}})
}

func imagesPackage(t *testing.T, log string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "images"), []byte(fmt.Sprintf("#!/bin/sh\nPERSONA_TEST_PLUGIN=images IMAGES_LOG=%q exec %s \"$@\"\n", log, self)), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-images", "name": "Example Images", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/images"}}, "activation_events": ["on_startup"],
		"contributes": {"ui": [{"id": "home", "title": "摄像头", "placement": "extension_page"}]}}`, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

func calls(t *testing.T, log string) []string {
	b, _ := os.ReadFile(log)
	return strings.Fields(strings.ReplaceAll(string(b), "\n", " "))
}

// get fetches a picture's path as a device with token.
func get(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// Pictures through the hub: an app of UI version 6 gets the hub's signed paths for the extension's refs, loads them
// with its own credentials, and gets only what passed wire.CheckImage; the hub keeps them as long as the extension
// says, asks once for many at the same time, and never serves a token it did not sign, one past its time, another
// extension's, a read-only device, or a switched-off extension.
func TestExtensionImages(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	log := filepath.Join(t.TempDir(), "calls.log")
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+imagesPackage(t, log)+`"}`)
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	waitLong(t, "the extension up", func() bool {
		hp := h.extensions.get("example-images")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})
	phone := deviceToken("dev_phone", roleFull)
	watch := deviceToken("dev_watch", roleAnswer)

	// an older app gets no refs (Fit drew the picture as a line of text), so no paths
	_, out := call(t, mux, "POST", "/api/v1/extensions/example-images/-/ui/home", phone, `{"context": {"ui_version": 5}}`)
	if s := fmt.Sprint(out["view"]); strings.Contains(s, "/-/image/") || strings.Contains(s, "ref:") {
		t.Fatalf("v5 got refs or paths: %s", s)
	}
	code, out := call(t, mux, "POST", "/api/v1/extensions/example-images/-/ui/home", phone, `{"context": {"ui_version": 6}}`)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	raw, _ := json.Marshal(out["view"])
	var v extension.View
	_ = json.Unmarshal(raw, &v)
	its := v.Sections[0].Items
	cam, thumb, web, tile := its[0].Src, its[1].Thumbnail, its[2].Src, its[3].Tiles[0].Image
	if !strings.HasPrefix(cam.Path, "/api/v1/extensions/example-images/-/image/") || thumb.Path != cam.Path || web.Path != "" || tile.Path == "" || tile.Path == cam.Path {
		t.Fatalf("paths: cam %q thumb %q web %q tile %q", cam.Path, thumb.Path, web.Path, tile.Path)
	}
	if cam.Ref != "cam" {
		t.Fatal("the ref stays beside its path")
	}
	// an action's components in place are signed too
	_, out = call(t, mux, "POST", "/api/v1/extensions/example-images/-/ui/home", phone, `{"context": {"ui_version": 6}, "action": "x"}`)
	if !strings.Contains(fmt.Sprint(out["items"]), "/-/image/") {
		t.Fatalf("items: %v", out["items"])
	}

	w := get(mux, cam.Path+"?w=700", phone)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), testPNG()) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	hd := w.Header()
	// the type is the one read from the bytes, not the extension's image/jpeg
	if hd.Get("Content-Type") != "image/png" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Content-Security-Policy") != "default-src 'none'" ||
		hd.Get("Content-Disposition") != "inline" || !strings.HasPrefix(hd.Get("Cache-Control"), "private, max-age=") {
		t.Fatalf("headers: %v", hd)
	}
	var age int
	_, _ = fmt.Sscanf(hd.Get("Cache-Control"), "private, max-age=%d", &age)
	if age <= 0 || age > 60 {
		t.Fatalf("max-age %d", age)
	}
	// kept: asked again, the extension is not
	if w := get(mux, cam.Path+"?w=1000", phone); w.Code != 200 { // the same 1280 as 700
		t.Fatal(w.Code)
	}
	if got := calls(t, log); len(got) != 2 || got[0] != "cam" || got[1] != "1280" {
		t.Fatalf("calls %v", got)
	}
	// another width is another picture
	_ = get(mux, cam.Path+"?w=200", phone)
	if got := calls(t, log); len(got) != 4 || got[3] != "320" {
		t.Fatalf("calls %v", got)
	}

	// many at once: one call
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = get(mux, tile.Path, phone).Code
		}()
	}
	wg.Wait()
	if fmt.Sprint(codes) != "[200 200 200 200 200 200]" || strings.Count(strings.Join(calls(t, log), " "), "slow") != 1 {
		t.Fatalf("codes %v calls %v", codes, calls(t, log))
	}

	key, err := h.imageKey()
	if err != nil {
		t.Fatal(err)
	}
	signed := func(ref string) string {
		return imagePath("example-images", signImage(key, imageToken{Ext: "example-images", UI: "home", Ref: ref, Exp: time.Now().Add(time.Minute).Unix()}))
	}
	for ref, want := range map[string]int{"svg": 502, "html": 502, "huge": 502, "fail": 502} {
		if w := get(mux, signed(ref), phone); w.Code != want || strings.Contains(w.Body.String(), "<svg") || strings.Contains(w.Body.String(), "<script") {
			t.Errorf("%s: %d %s", ref, w.Code, w.Body.String())
		}
	}
	// max_age 0 is not kept
	_ = get(mux, signed("noage"), phone)
	_ = get(mux, signed("noage"), phone)
	if n := strings.Count(strings.Join(calls(t, log), " "), "noage"); n != 2 {
		t.Fatalf("noage asked %d times", n)
	}

	tok := strings.TrimPrefix(cam.Path, "/api/v1/extensions/example-images/-/image/")
	payload, sig, _ := strings.Cut(tok, ".")
	forged, _ := json.Marshal(imageToken{Ext: "example-images", UI: "home", Ref: "../../secret", Exp: time.Now().Add(time.Hour).Unix()})
	for name, path := range map[string]string{
		"another extension's address": "/api/v1/extensions/example-ui/-/image/" + tok,
		"a changed payload":           imagePath("example-images", base64.RawURLEncoding.EncodeToString(forged)+"."+sig),
		"a changed signature":         imagePath("example-images", payload+"."+strings.Repeat("A", len(sig))),
		"no signature":                imagePath("example-images", payload),
		"another key":                 imagePath("example-images", signImage([]byte("not the hub's key"), imageToken{Ext: "example-images", UI: "home", Ref: "cam", Exp: time.Now().Add(time.Minute).Unix()})),
		"a ui it does not have":       imagePath("example-images", signImage(key, imageToken{Ext: "example-images", UI: "nope", Ref: "cam", Exp: time.Now().Add(time.Minute).Unix()})),
	} {
		if w := get(mux, path, phone); w.Code != 404 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := get(mux, cam.Path, watch); w.Code != 403 {
		t.Fatalf("a read-only device: %d", w.Code)
	}
	h.images.now = func() time.Time { return time.Now().Add(imageTokenFor + time.Second) }
	w = get(mux, cam.Path, phone)
	var e map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if w.Code != 410 || e["code"] != "expired" {
		t.Fatalf("past its time: %d %v", w.Code, e)
	}
	h.images.now = nil

	// switched off: nothing is served and what was kept is gone
	if entries, _ := os.ReadDir(filepath.Join(h.imagesDir(), "example-images")); len(entries) == 0 {
		t.Fatal("nothing kept")
	}
	call(t, mux, "PUT", "/api/v1/plugins/example-images/-/enabled", master, `{"enabled": false}`)
	if w := get(mux, cam.Path, phone); w.Code != 404 {
		t.Fatalf("switched off: %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(h.imagesDir(), "example-images")); !os.IsNotExist(err) {
		t.Fatalf("kept pictures left: %v", err)
	}
}

// A token is the hub's own: it opens with its key only, untouched, before its time.
func TestImageTokens(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	tok := signImage(key, imageToken{Ext: "e", UI: "u", Ref: "r", Exp: now.Add(time.Minute).Unix()})
	if got, err := openImage(key, tok, now); err != nil || got.Ref != "r" || got.Ext != "e" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := openImage(key, tok, now.Add(time.Minute)); err != errImageExpired {
		t.Fatalf("expired: %v", err)
	}
	if _, err := openImage([]byte("another key of thirty-two bytes!"), tok, now); err != errImageToken {
		t.Fatalf("another key: %v", err)
	}
	for _, bad := range []string{"", ".", "a.b", tok + "x", "x" + tok, strings.Repeat("a", 3000)} {
		if _, err := openImage(key, bad, now); err == nil {
			t.Errorf("%q opened", bad)
		}
	}
	if !wireOK(imagePath("ext", tok)) {
		t.Fatalf("a path the schema refuses: %s", imagePath("ext", tok))
	}
}

// wireOK says whether path holds to the schema's ImageSource.path.
func wireOK(path string) bool {
	return wire.Validate("ImageSource", map[string]any{"ref": "r", "path": path}) == nil
}
