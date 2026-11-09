package hub

import "github.com/nwverse/persona/server/internal/textfmt"

// The text formatting the hub writes with, shared with the assistant (internal/textfmt).
var (
	clip      = textfmt.Clip
	oneLine   = textfmt.OneLine
	planTitle = textfmt.PlanTitle
)
