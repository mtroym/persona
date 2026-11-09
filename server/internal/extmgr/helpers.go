package extmgr

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Small helpers shared by this package's handlers.

var (
	clip    = textfmt.Clip
	oneLine = textfmt.OneLine
)

func writeJSON(w http.ResponseWriter, v any) { apierr.WriteJSON(w, v) }

func writeErr(w http.ResponseWriter, status int, err error) { apierr.Write(w, status, err) }

func writeErrCode(w http.ResponseWriter, status int, code string, err error) {
	apierr.WriteCode(w, status, code, err)
}

func writeErrorBody(w http.ResponseWriter, status int, body map[string]any) {
	apierr.WriteBody(w, status, body)
}

func writeCode(w http.ResponseWriter, status int, code, msg string, extra map[string]any) {
	body := map[string]any{"error": msg, "code": code}
	for k, v := range extra {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// readAdminJSON reads an admin page's JSON body into v (an empty body leaves v as it is).
var readAdminJSON = apierr.ReadAdminJSON

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func retryAfter(w http.ResponseWriter, retry time.Duration) {
	seconds := max(1, int((retry+time.Second-1)/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func mcpTool(name string) proto.Tool {
	t := prompts.Tool(name)
	return proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
}

// compactJSON drops media / avatar noise and cuts long answers so they fit in the model's context.
func compactJSON(raw json.RawMessage, max int) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return clip(string(raw), max)
	}
	v = prune(v)
	b, _ := json.Marshal(v)
	if len(b) > max {
		return string(b[:max]) + "…(cut; narrow the query or page)"
	}
	return string(b)
}

// prune drops what a tool result does not need: avatars, thumbnails, highlights, emoji keys and empty values.
func prune(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "avatar") || strings.HasSuffix(lk, "thumbnail") || lk == "highlights" || lk == "emojikey" {
				delete(t, k)
				continue
			}
			if val == nil || val == "" || val == false {
				delete(t, k)
				continue
			}
			t[k] = prune(val)
		}
	case []any:
		for i := range t {
			t[i] = prune(t[i])
		}
	}
	return v
}

func qrRows(text string) ([]string, int, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]string, code.Size)
	var b strings.Builder
	for y := range code.Size {
		b.Reset()
		for x := range code.Size {
			if code.Black(x, y) {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[y] = b.String()
	}
	return rows, code.Size, nil
}

func noDevice(id string) error { return fmt.Errorf("%w %q", assistantwire.ErrNoDevice, id) }

// ---- who asked: the principal the hub found and passed on with a relayed request (extwire.Header*) ----

const (
	roleFull   = "full"
	roleAnswer = "answer"
)

// principal is the caller of a relayed request as the hub's auth found it; the hub replaces whatever the client sent
// in these headers.
type principal struct {
	Hub       string
	ID        string
	Name      string
	Role      string
	Paired    bool // a pairing token (not one of the user's own signed-in apps)
	Extension string
	Scopes    []string
	// Owner: the master token over loopback (installing runs code on this machine); Admin: a signed-in admin session.
	Owner, Admin bool
	// Remote is the caller's address as the hub saw it.
	Remote netip.Addr
	Lang   i18n.Lang
}

type principalKey struct{}

func principalOf(ctx context.Context) principal {
	if p, ok := ctx.Value(principalKey{}).(principal); ok {
		return p
	}
	return principal{Role: roleFull} // handlers called directly (tests)
}

func withPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom reads the hub's headers off a relayed request.
func principalFrom(r *http.Request) principal {
	h := r.Header
	p := principal{Hub: h.Get(extwire.HeaderHub), ID: h.Get(extwire.HeaderDevice), Name: h.Get(extwire.HeaderDeviceName), Role: h.Get(extwire.HeaderRole),
		Paired: h.Get(extwire.HeaderPaired) == "1", Extension: h.Get(extwire.HeaderExtension),
		Owner: h.Get(extwire.HeaderOwner) == "1", Admin: h.Get(extwire.HeaderAdmin) == "1", Lang: i18n.Lang(h.Get(extwire.HeaderLang))}
	if s := h.Get(extwire.HeaderScopes); s != "" {
		p.Scopes = strings.Split(s, ",")
	}
	p.Remote, _ = netip.ParseAddr(h.Get(extwire.HeaderRemote))
	if p.Role == "" {
		p.Role = roleFull
	}
	return p
}

// byOwnDevice says whether the request came from one of the user's own signed-in apps (a full device token, never a
// pairing one), as the hub's auth found it.
func byOwnDevice(ctx context.Context) bool {
	p, ok := ctx.Value(principalKey{}).(principal)
	return ok && p.Role == roleFull && p.ID != "" && !p.Paired
}

// remoteIP is the caller's address the hub saw.
func remoteIP(r *http.Request) netip.Addr {
	if a := principalOf(r.Context()).Remote; a.IsValid() {
		return a
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr()
}
