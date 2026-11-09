package ext

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Page is one ui contribution (docs/extensions/extensions.md "Declarative UI"): what it draws, and what its buttons do.
type Page struct {
	// Render draws the page for r (its state, the slot, the app's context).
	Render func(ctx context.Context, r *UIRequest) (*wire.View, error)
	// Action answers a tap on something with an action (r.Action is its id, r.Values the page's fields when it
	// submits). Nil, or an empty result, draws the page again. UI version 5: a result with Lines, or with Items (an
	// empty but non-nil Items says done, nothing to draw: a viewed mark), is the answer as it is.
	Action func(ctx context.Context, r *UIRequest) (*wire.ActionResult, error)
	// Image serves a picture the page drew by ref (UI version 6: an image's src, a row's thumbnail or avatar, a
	// tile's image), fetched by the hub for the apps: Image and ImageFile make the answer. Nil answers unknown_method.
	Image func(ctx context.Context, r *ImageRequest) (*wire.UIImage, error)
}

// ImageRequest is a ui.image: the ref, the contribution that drew it, the pixel width the app wants (0: any), and
// the hub to call.
type ImageRequest struct {
	wire.UIImageParams
	Hub *Hub
}

// UIRequest is a ui.render or ui.action: wire.UIParams, and the hub to call while drawing (gateway.http …).
type UIRequest struct {
	wire.UIParams
	Hub *Hub
}

// DecodeState reads the page's state, which the program wrote as JSON (EncodeState); an empty state leaves v as is.
func (r *UIRequest) DecodeState(v any) error {
	if r.State == "" {
		return nil
	}
	return json.Unmarshal([]byte(r.State), v)
}

// EncodeState writes v as a page's state (at most 16 KB).
func EncodeState(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *server) ui(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method == "image" {
		return s.image(ctx, raw)
	}
	r := &UIRequest{Hub: s.hub}
	if err := json.Unmarshal(raw, &r.UIParams); err != nil {
		return nil, &Error{Code: "invalid", Message: err.Error()}
	}
	if method != "render" && method != "action" {
		return nil, UnknownMethod("ui." + method)
	}
	page, ok := s.prog.UI[r.Contribution]
	if !ok {
		return nil, NoContribution(r.Contribution)
	}
	switch method {
	case "render":
		if page.Render == nil {
			return nil, UnknownMethod("ui.render")
		}
		v, err := page.Render(ctx, r)
		if err == nil && older(r) {
			s.forOlder(v, r.Contribution)
		}
		return v, err
	case "action":
		if older(r) {
			if res, ok, err := s.olderAction(ctx, r); ok {
				return res, err
			}
		}
		if page.Action != nil {
			res, err := page.Action(ctx, r)
			if err != nil {
				return nil, err
			}
			// UI version 5: lines, or components in place (an empty but non-nil Items: done, nothing to draw again)
			if res != nil && (res.View != nil || res.Toast != "" || res.OpenURL != "" || res.Push != nil || res.Pop || res.Lines != nil || res.Items != nil) {
				if older(r) {
					return s.olderResult(ctx, r, res)
				}
				return res, nil
			}
		}
		if page.Render == nil {
			return wire.ActionResult{}, nil
		}
		v, err := page.Render(ctx, r)
		if err != nil {
			return nil, err
		}
		if older(r) {
			s.forOlder(v, r.Contribution)
		}
		return wire.ActionResult{View: v}, nil
	}
	return nil, UnknownMethod("ui." + method)
}

func (s *server) image(ctx context.Context, raw json.RawMessage) (any, error) {
	r := &ImageRequest{Hub: s.hub}
	if err := json.Unmarshal(raw, &r.UIImageParams); err != nil {
		return nil, &Error{Code: "invalid", Message: err.Error()}
	}
	page, ok := s.prog.UI[r.Contribution]
	if !ok {
		return nil, NoContribution(r.Contribution)
	}
	if page.Image == nil {
		return nil, UnknownMethod("ui.image")
	}
	return page.Image(ctx, r)
}
