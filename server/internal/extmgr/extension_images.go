package extmgr

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
)

// Pictures an extension serves itself (UI version 6, docs/extensions.md "UI version 6", "Pictures through the hub"):
// a view's ref is the extension's own name for a picture (a camera, a map). For an app of UI version 6 the hub writes
// next to each ref a path of its own, GET /api/v1/extensions/{id}/-/image/{token}, the token naming the extension,
// the ui that drew it and the ref, signed and good for imageTokenFor. The app loads that path with its own device
// credentials; the hub checks the token, asks the extension (ui.image), holds what it answers to wire.CheckImage and
// keeps it on disk for as long as the extension says.
//
// The signing key is derived from the hub's secret key (secretkey.Box.Derive, the key that seals extension secrets):
// nothing more is stored, and it holds across restarts and redeploys.

const (
	imageKeyInfo = "persona extension image v1"
	// imageTokenFor is how long a path the hub wrote is good for; a page on screen longer draws again (410 expired).
	imageTokenFor = 15 * time.Minute
	// imageCallTimeout bounds one ui.image; imageCallsMost is how many of one extension's run at once.
	imageCallTimeout = 10 * time.Second
	imageCallsMost   = 4
	// imageCacheMost is how many bytes of pictures the hub keeps; the oldest go first.
	imageCacheMost = 256 << 20
)

var (
	errImageToken   = errors.New("no such picture")
	errImageExpired = errors.New("the picture's address has expired: draw the page again")
	errImageRefused = errors.New("the extension's picture is not one the apps are given")
)

// imageToken is what a picture's token says.
type imageToken struct {
	Ext string `json:"e"`
	UI  string `json:"u"`
	Ref string `json:"r"`
	Exp int64  `json:"x"` // unix seconds
}

// imageProxy is the hub's side of the pictures: signing, the calls in flight and the disk cache.
type imageProxy struct {
	now func() time.Time // time.Now; tests set it

	mu       sync.Mutex
	inflight map[string]*imageFetch
	slots    map[string]chan struct{} // per extension, imageCallsMost each
	writes   int
}

type imageFetch struct {
	done chan struct{}
	img  *wire.CheckedImage
	exp  time.Time
	err  error
}

func (p *imageProxy) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (h *Manager) imageKey() ([]byte, error) { return h.secret.Derive(imageKeyInfo, 32) }

// signImage is the token of ref, drawn by ui of extension ext, good until exp.
func signImage(key []byte, t imageToken) string {
	raw, _ := json.Marshal(t)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// openImage reads a token signed with key: errImageToken when it is not one, errImageExpired when it is past.
func openImage(key []byte, token string, now time.Time) (imageToken, error) {
	var t imageToken
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || len(token) > 2048 {
		return t, errImageToken
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return t, errImageToken
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return t, errImageToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &t) != nil || t.Ext == "" || t.UI == "" || t.Ref == "" {
		return imageToken{}, errImageToken
	}
	if now.Unix() >= t.Exp {
		return t, errImageExpired
	}
	return t, nil
}

// imagePath is the hub's address of a picture.
func imagePath(ext, token string) string { return "/api/v1/extensions/" + ext + "/-/image/" + token }

// signRefs writes the hub's path next to every ref in what ui of extension ext drew (a view, components put in place),
// for an app of UI version 6. The same ref gets the same path.
func (h *Manager) signRefs(ext, ui string, v *extension.View, items []extension.UIElem) error {
	if !hasRefs(v, items) {
		return nil
	}
	key, err := h.imageKey()
	if err != nil {
		return err
	}
	exp := h.images.clock().Add(imageTokenFor).Unix()
	paths := map[string]string{}
	path := func(ref string) string {
		if p, ok := paths[ref]; ok {
			return p
		}
		p := imagePath(ext, signImage(key, imageToken{Ext: ext, UI: ui, Ref: ref, Exp: exp}))
		paths[ref] = p
		return p
	}
	eachPicture(v, items, func(ref string, set func(string)) { set(path(ref)) })
	return nil
}

func hasRefs(v *extension.View, items []extension.UIElem) bool {
	any := false
	eachPicture(v, items, func(string, func(string)) { any = true })
	return any
}

// eachPicture calls f with every ref of a view's (and items') pictures and a way to set its path.
func eachPicture(v *extension.View, items []extension.UIElem, f func(ref string, set func(string))) {
	elem := func(e *extension.UIElem) {
		if s := e.Src; s != nil && s.Ref != "" {
			f(s.Ref, func(p string) { s.Path = p })
		}
		if t := e.Thumbnail; t != nil && t.Ref != "" {
			f(t.Ref, func(p string) { t.Path = p })
		}
		avatar := func(a *wire.Avatar) {
			if a != nil && a.Ref != "" {
				f(a.Ref, func(p string) { a.Path = p })
			}
		}
		avatar(e.Avatar)
		for k := range e.Tiles {
			if s := e.Tiles[k].Image; s != nil && s.Ref != "" {
				f(s.Ref, func(p string) { s.Path = p })
			}
		}
		for k := range e.Comments {
			avatar(e.Comments[k].Avatar)
		}
		for k := range e.Threads {
			for l := range e.Threads[k].Comments {
				avatar(e.Threads[k].Comments[l].Avatar)
			}
		}
	}
	if v != nil {
		for i := range v.Sections {
			for j := range v.Sections[i].Items {
				elem(&v.Sections[i].Items[j])
			}
		}
	}
	for i := range items {
		elem(&items[i])
	}
}

// imagesDir is where the pictures are kept: <hub dir>/cache/extension-images/<extension>/<hash of ui, ref and width>.
func (h *Manager) imagesDir() string { return filepath.Join(h.cfg.dir, "cache", "extension-images") }

func imageFile(dir, ext, ui, ref string, width int) string {
	sum := sha256.Sum256([]byte(ui + "\x00" + ref + "\x00" + strconv.Itoa(width)))
	return filepath.Join(dir, ext, hex.EncodeToString(sum[:16]))
}

// cached is a kept picture still good at now: the file's first line is its type and when it stops being good (unix
// seconds), then the bytes.
func cachedImage(file string, now time.Time) (*wire.CheckedImage, time.Time, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, time.Time{}, false
	}
	head, data, ok := strings.Cut(string(b), "\n")
	mime, at, ok2 := strings.Cut(head, " ")
	exp, err := strconv.ParseInt(at, 10, 64)
	if !ok || !ok2 || err != nil || data == "" || !slices.Contains(wire.ImageTypes, mime) || now.Unix() >= exp {
		return nil, time.Time{}, false
	}
	return &wire.CheckedImage{Data: []byte(data), Mime: mime}, time.Unix(exp, 0), true
}

func (p *imageProxy) keep(dir, file string, img *wire.CheckedImage, exp time.Time) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return
	}
	tmp := file + ".tmp"
	head := fmt.Sprintf("%s %d\n", img.Mime, exp.Unix())
	if os.WriteFile(tmp, append([]byte(head), img.Data...), 0o600) != nil || os.Rename(tmp, file) != nil {
		_ = os.Remove(tmp)
		return
	}
	p.mu.Lock()
	p.writes++
	prune := p.writes%50 == 1
	p.mu.Unlock()
	if prune {
		pruneMedia(dir, imageCacheMost)
	}
}

// forget drops what is kept of extension ext's pictures (switched off, upgraded, removed).
func (h *Manager) forgetImages(ext string) {
	if ext == "" || ext != filepath.Base(ext) || h.cfg == nil || h.cfg.dir == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(h.imagesDir(), ext))
}

// slot takes one of extension ext's imageCallsMost places for a call, waiting for one; release gives it back.
func (p *imageProxy) slot(ctx context.Context, ext string) (release func(), err error) {
	p.mu.Lock()
	if p.slots == nil {
		p.slots = map[string]chan struct{}{}
	}
	ch := p.slots[ext]
	if ch == nil {
		ch = make(chan struct{}, imageCallsMost)
		p.slots[ext] = ch
	}
	p.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// extensionImage is the picture a token names, at width: kept, or asked of the extension once however many ask for it
// at the same time. It says until when it is good.
func (h *Manager) extensionImage(ctx context.Context, t imageToken, width int) (*wire.CheckedImage, time.Time, error) {
	p := h.extensions.reg.Get(t.Ext)
	hp := h.extensions.get(t.Ext)
	if p == nil || !p.Record.Enabled || hp == nil || hp.proc == nil {
		return nil, time.Time{}, errNoExtension
	}
	i := slices.IndexFunc(p.Manifest.UIs(), func(u extension.UIItem) bool { return u.ID == t.UI })
	if i < 0 {
		return nil, time.Time{}, errNoExtension
	}
	item := p.Manifest.UIs()[i]
	dir := h.imagesDir()
	file := imageFile(dir, p.Manifest.ID, t.UI, t.Ref, width)
	px := &h.images
	if img, exp, ok := cachedImage(file, px.clock()); ok {
		return img, exp, nil
	}
	key := t.Ext + "\x00" + file
	px.mu.Lock()
	if f, ok := px.inflight[key]; ok {
		px.mu.Unlock()
		select {
		case <-f.done:
			return f.img, f.exp, f.err
		case <-ctx.Done():
			return nil, time.Time{}, ctx.Err()
		}
	}
	if px.inflight == nil {
		px.inflight = map[string]*imageFetch{}
	}
	f := &imageFetch{done: make(chan struct{})}
	px.inflight[key] = f
	px.mu.Unlock()
	defer func() {
		px.mu.Lock()
		delete(px.inflight, key)
		px.mu.Unlock()
		close(f.done)
	}()
	// the call is the extension's, not this request's: another app asking for the same picture waits on it too
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageCallTimeout)
	defer cancel()
	release, err := px.slot(cctx, t.Ext)
	if err != nil {
		f.err = err
		return nil, time.Time{}, err
	}
	defer release()
	var out wire.UIImage
	params := wire.UIImageParams{Contribution: t.UI, Ref: t.Ref, Width: width, Placement: item.Placement}
	if err := hp.proc.Call(cctx, "ui.image", params, &out); err != nil {
		f.err = fmt.Errorf("ui.image: %w", err)
		return nil, time.Time{}, f.err
	}
	img, err := wire.CheckImage(&out)
	if err != nil {
		h.extensions.logf("plugin %s: ui %s answered a picture the apps are not given (%s): refused", t.Ext, t.UI, err)
		hp.stats.Count("ui.image refused", 1)
		f.err = errImageRefused
		return nil, time.Time{}, f.err
	}
	f.img, f.exp = img, px.clock().Add(time.Duration(img.MaxAge)*time.Second)
	if img.MaxAge > 0 {
		px.keep(dir, file, img, f.exp)
	}
	return f.img, f.exp, nil
}

// extensionImageRoutes serves the pictures: GET /api/v1/extensions/{id}/-/image/{token}?w=<pixels>, for a device
// with full rights (as drawing the page is), the token one this hub signed for that extension.
func (h *Manager) extensionImageRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/extensions/{id}/-/image/{token}", func(w http.ResponseWriter, r *http.Request) {
		// a device with full rights, as drawing the page needs; never an extension's own token
		if p := principalOf(r.Context()); p.Role != roleFull || p.Extension != "" {
			writeErr(w, http.StatusForbidden, errors.New("this device can only read and answer"))
			return
		}
		key, err := h.imageKey()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		now := h.images.clock()
		t, err := openImage(key, r.PathValue("token"), now)
		switch {
		case errors.Is(err, errImageExpired) && t.Ext == r.PathValue("id"):
			writeErrCode(w, http.StatusGone, "expired", err)
			return
		case err != nil || t.Ext != r.PathValue("id"):
			writeErrCode(w, http.StatusNotFound, "not_found", errImageToken)
			return
		}
		width, _ := strconv.Atoi(r.URL.Query().Get("w"))
		img, exp, err := h.extensionImage(r.Context(), t, wire.ImageWidth(width))
		switch {
		case errors.Is(err, errNoExtension):
			writeErrCode(w, http.StatusNotFound, "not_found", errImageToken)
			return
		case err != nil:
			writeErrCode(w, http.StatusBadGateway, "plugin_failed", err)
			return
		}
		age := int(min(exp.Sub(now), time.Unix(t.Exp, 0).Sub(now)) / time.Second)
		hd := w.Header()
		hd.Set("Content-Type", img.Mime)
		hd.Set("Content-Length", strconv.Itoa(len(img.Data)))
		hd.Set("Cache-Control", "private, max-age="+strconv.Itoa(max(age, 0)))
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Content-Security-Policy", "default-src 'none'")
		hd.Set("Content-Disposition", "inline")
		_, _ = w.Write(img.Data)
	})
}

// signPictures writes the hub's paths for an app of UI version 6 (an older one got no refs: Fit drew them as text).
func (h *Manager) signPictures(ext, ui string, uiVersion int, v *extension.View, items []extension.UIElem) error {
	if uiVersion < 6 {
		return nil
	}
	return h.signRefs(ext, ui, v, items)
}
