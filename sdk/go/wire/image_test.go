package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func encoded(t *testing.T, enc func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := enc(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngHeader is a png's signature and IHDR claiming w × h, enough for DecodeConfig.
func pngHeader(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // 8 bits, RGB
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

// webpLossless is a VP8L webp header of w × h.
func webpLossless(w, h int) []byte {
	body := make([]byte, 5)
	body[0] = 0x2f
	binary.LittleEndian.PutUint32(body[1:], uint32(w-1)|uint32(h-1)<<14)
	body = append(body, make([]byte, 16)...)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(body)))
	b.WriteString("WEBPVP8L")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(body)))
	b.Write(body)
	return b.Bytes()
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestCheckImage(t *testing.T) {
	pngs := encoded(t, func(b *bytes.Buffer, i image.Image) error { return png.Encode(b, i) })
	jpgs := encoded(t, func(b *bytes.Buffer, i image.Image) error { return jpeg.Encode(b, i, nil) })
	gifs := encoded(t, func(b *bytes.Buffer, i image.Image) error { return gif.Encode(b, i, nil) })
	age := 10
	for name, c := range map[string]struct {
		data []byte
		mime string
	}{
		"png":  {pngs, "image/png"},
		"jpeg": {jpgs, "image/jpeg"},
		"gif":  {gifs, "image/gif"},
		"webp": {webpLossless(640, 480), "image/webp"},
	} {
		got, err := CheckImage(&UIImage{Mime: "application/octet-stream", Data: b64(c.data), MaxAge: &age})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Mime != c.mime || got.MaxAge != 10 || got.Width <= 0 {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if got, _ := CheckImage(&UIImage{Mime: "image/webp", Data: b64(webpLossless(640, 480))}); got.Width != 640 || got.Height != 480 || got.MaxAge != ImageMaxAgeDefault {
		t.Fatalf("webp size: %+v", got)
	}
	big := 999999
	if got, _ := CheckImage(&UIImage{Mime: "image/png", Data: b64(pngs), MaxAge: &big}); got.MaxAge != ImageMaxAgeMost {
		t.Fatalf("max_age held to %d: %d", ImageMaxAgeMost, got.MaxAge)
	}
	for name, img := range map[string]*UIImage{
		"nothing":            nil,
		"no data":            {Mime: "image/png"},
		"not base64":         {Mime: "image/png", Data: "%%%"},
		"svg":                {Mime: "image/svg+xml", Data: b64([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))},
		"svg said png":       {Mime: "image/png", Data: b64([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`))},
		"html said png":      {Mime: "image/png", Data: b64([]byte("<!DOCTYPE html><html><script>alert(1)</script></html>"))},
		"bmp":                {Mime: "image/bmp", Data: b64(append([]byte("BM"), make([]byte, 64)...))},
		"png cut":            {Mime: "image/png", Data: b64(pngs[:12])},
		"too wide":           {Mime: "image/png", Data: b64(pngHeader(ImageSideMost+1, 10))},
		"too many pixels":    {Mime: "image/png", Data: b64(pngHeader(8000, 8000))},
		"webp too wide":      {Mime: "image/webp", Data: b64(webpLossless(9000, 10))},
		"more than 4 MB":     {Mime: "image/png", Data: b64(append(pngs, make([]byte, ImageBytesMost)...))},
		"webp not a picture": {Mime: "image/webp", Data: b64([]byte("RIFF\x10\x00\x00\x00WEBPJUNK" + strings.Repeat("x", 20)))},
	} {
		if _, err := CheckImage(img); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	if got, err := CheckImage(&UIImage{Data: b64(pngHeader(ImageSideMost, 10))}); err != nil || got.Width != ImageSideMost {
		t.Fatalf("at the limit: %+v %v", got, err)
	}
}

func TestImageWidth(t *testing.T) {
	for in, want := range map[int]int{0: 0, -1: 0, 1: 320, 320: 320, 321: 640, 1000: 1280, 2048: 2048, 5000: 2048} {
		if got := ImageWidth(in); got != want {
			t.Errorf("ImageWidth(%d) = %d, want %d", in, got, want)
		}
	}
}
