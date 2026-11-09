package ext

import (
	"encoding/base64"
	"net/http"
	"os"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Image is the answer to ui.image: the picture's bytes (a png, jpeg, webp or gif, at most 4 MB and 8192 pixels a
// side), and how long the hub may keep it in seconds (a camera 5 to 10, a map 30, a still 86400; 0 not at all).
// The hub reads the type from the bytes, so mime is only a hint; empty, it is read here the same way.
func Image(mime string, data []byte, maxAge int) *wire.UIImage {
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	age := min(max(maxAge, 0), wire.ImageMaxAgeMost)
	return &wire.UIImage{Mime: mime, Data: base64.StdEncoding.EncodeToString(data), MaxAge: &age}
}

// ImageFile is Image of a file the extension ships (its package directory: Env().Dir), the type read from its bytes.
func ImageFile(path string, maxAge int) (*wire.UIImage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Image("", data, maxAge), nil
}
