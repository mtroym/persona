package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // DecodeConfig reads gif, jpeg and png
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"slices"
)

// Limits of a picture an extension serves (ui.image, UI version 6).
const (
	ImageBytesMost  = 4 << 20
	ImageSideMost   = 8192
	ImagePixelsMost = 40_000_000
	// ImageMaxAgeDefault and ImageMaxAgeMost bound how long the hub keeps one, in seconds.
	ImageMaxAgeDefault = 60
	ImageMaxAgeMost    = 86400
)

// ImageTypes are the pictures the apps are given: no SVG (it can carry script), nothing the type sniffing does not
// recognise as one of these.
var ImageTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

// ImageWidths are the pixel widths an app asks a picture for; ImageWidth picks one.
var ImageWidths = []int{320, 640, 1280, 2048}

// ImageWidth is the least of ImageWidths at least w wide (the largest when w is wider); 0 for none asked.
func ImageWidth(w int) int {
	if w <= 0 {
		return 0
	}
	for _, x := range ImageWidths {
		if w <= x {
			return x
		}
	}
	return ImageWidths[len(ImageWidths)-1]
}

// CheckedImage is a picture CheckImage took: its bytes, its type as read from them, its size.
type CheckedImage struct {
	Data          []byte
	Mime          string
	Width, Height int
	// MaxAge is how long it may be kept, in seconds (ImageMaxAgeDefault when the extension said nothing).
	MaxAge int
}

// CheckImage holds what an extension answered ui.image with to what the apps are given: base64 that decodes to at
// most ImageBytesMost bytes of a png, jpeg, webp or gif (the type read from the bytes, never the one the extension
// declares), at most ImageSideMost pixels a side and ImagePixelsMost in all. The hub's image proxy and the
// conformance tests both use it.
func CheckImage(img *UIImage) (*CheckedImage, error) {
	if img == nil || img.Data == "" {
		return nil, errors.New("ui.image: no data")
	}
	if base64.StdEncoding.DecodedLen(len(img.Data)) > ImageBytesMost+3 {
		return nil, fmt.Errorf("ui.image: at most %d bytes", ImageBytesMost)
	}
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return nil, errors.New("ui.image: data is not base64")
	}
	if len(data) > ImageBytesMost {
		return nil, fmt.Errorf("ui.image: at most %d bytes", ImageBytesMost)
	}
	mime := http.DetectContentType(data)
	if !slices.Contains(ImageTypes, mime) {
		return nil, fmt.Errorf("ui.image: a png, jpeg, webp or gif, not %s", mime)
	}
	var w, h int
	if mime == "image/webp" {
		w, h, err = webpSize(data)
	} else {
		var cfg image.Config
		cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
		w, h = cfg.Width, cfg.Height
	}
	if err != nil || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("ui.image: a %s whose size cannot be read", mime)
	}
	if w > ImageSideMost || h > ImageSideMost || w*h > ImagePixelsMost {
		return nil, fmt.Errorf("ui.image: at most %d pixels a side and %d in all, not %d×%d", ImageSideMost, ImagePixelsMost, w, h)
	}
	age := ImageMaxAgeDefault
	if img.MaxAge != nil {
		age = min(max(*img.MaxAge, 0), ImageMaxAgeMost)
	}
	return &CheckedImage{Data: data, Mime: mime, Width: w, Height: h, MaxAge: age}, nil
}

// webpSize reads a webp's size from its first chunk (VP8, VP8L or VP8X), without decoding it.
func webpSize(b []byte) (int, int, error) {
	bad := errors.New("not a webp")
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, bad
	}
	chunk, body := string(b[12:16]), b[20:]
	switch chunk {
	case "VP8 ":
		// a key frame: 3 bytes of frame tag, the start code 9d 01 2a, then 14-bit width and height
		if len(body) < 10 || body[3] != 0x9d || body[4] != 0x01 || body[5] != 0x2a {
			return 0, 0, bad
		}
		return int(binary.LittleEndian.Uint16(body[6:8]) & 0x3fff), int(binary.LittleEndian.Uint16(body[8:10]) & 0x3fff), nil
	case "VP8L":
		if len(body) < 5 || body[0] != 0x2f {
			return 0, 0, bad
		}
		bits := binary.LittleEndian.Uint32(body[1:5])
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, nil
	case "VP8X":
		if len(body) < 10 {
			return 0, 0, bad
		}
		w := int(body[4]) | int(body[5])<<8 | int(body[6])<<16
		h := int(body[7]) | int(body[8])<<8 | int(body[9])<<16
		return w + 1, h + 1, nil
	}
	return 0, 0, bad
}

// ImageRefs are the refs of a view's pictures (an image's src, a row's thumbnail and avatar, a header's and a
// comment's avatar, a tile's image), each once, in order: what the hub would ask ui.image for.
func ImageRefs(v *View) []string {
	var out []string
	add := func(ref string) {
		if ref != "" && !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	if v == nil {
		return nil
	}
	for _, s := range v.Sections {
		for _, e := range s.Items {
			if e.Src != nil {
				add(e.Src.Ref)
			}
			if e.Thumbnail != nil {
				add(e.Thumbnail.Ref)
			}
			if e.Avatar != nil {
				add(e.Avatar.Ref)
			}
			for _, t := range e.Tiles {
				if t.Image != nil {
					add(t.Image.Ref)
				}
			}
			for _, c := range e.Comments {
				if c.Avatar != nil {
					add(c.Avatar.Ref)
				}
			}
			for _, th := range e.Threads {
				for _, c := range th.Comments {
					if c.Avatar != nil {
						add(c.Avatar.Ref)
					}
				}
			}
		}
	}
	return out
}
