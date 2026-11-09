package hub

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// AdminConfig is hub.json's "admin": who may open the admin UI and how long a sign-in lasts (docs/operations/admin.md).
type AdminConfig struct {
	// Access is loopback | lan (default) | cidrs | any.
	Access string `json:"access,omitempty"`
	// CIDRs are the networks access "cidrs" lets in, besides this machine.
	CIDRs []string `json:"cidrs,omitempty"`
	// SessionHours is how long a sign-in lasts (default 12), counted from signing in.
	SessionHours int `json:"session_hours,omitempty"`
	// FirstSetup "code": setting the first password takes the one-time setup code from every source, not only from
	// loopback (admin_setupcode.go). `personad hub setup` writes it into new hub.json files (docs/assistant/onboarding.md 4.1);
	// files without it keep the old rule: only loopback needs the code.
	FirstSetup string `json:"first_setup,omitempty"`
}

const firstSetupCode = "code"

const (
	accessLoopback = "loopback"
	accessLAN      = "lan"
	accessCIDRs    = "cidrs"
	accessAny      = "any"
)

func (c AdminConfig) sessionLifetime() int { return positiveOr(c.SessionHours, 12) }

// validAccess reports whether s names an access scope ("" is the default, lan).
func validAccess(s string) bool {
	switch s {
	case "", accessLoopback, accessLAN, accessCIDRs, accessAny:
		return true
	}
	return false
}

// parseCIDRs accepts networks ("192.0.2.0/24") and single addresses ("192.0.2.7" is a /32).
func parseCIDRs(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, raw := range list {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, netip.PrefixFrom(p.Addr().Unmap(), min(p.Bits(), p.Addr().Unmap().BitLen())).Masked())
			continue
		}
		if a, err := netip.ParseAddr(s); err == nil {
			a = a.Unmap().WithZone("")
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		return nil, fmt.Errorf("not a network: %q", s)
	}
	return out, nil
}

// splitList splits "a, b,c" (an environment variable or a flag) into its items.
func splitList(s string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// adminEnv is the scope a deployment gave in the environment (PERSONA_ADMIN_ACCESS, PERSONA_ADMIN_CIDRS; Docker's
// .env): it wins over hub.json, like the other PERSONA_* overrides, and is never written back. nil when unset.
func adminEnv() *AdminConfig {
	access := strings.TrimSpace(identity.Getenv("PERSONA_ADMIN_ACCESS"))
	if access == "" {
		return nil
	}
	return &AdminConfig{Access: access, CIDRs: splitList(identity.Getenv("PERSONA_ADMIN_CIDRS"))}
}

// --- hub.json edits ---------------------------------------------------------------------------------------------

// jsonObject is hub.json as an edit sees it: keys in order and every value as it was, so an edit changes the keys it
// means to and nothing else (fields this build does not know, written by a newer one or by hand, stay). The manager's
// editor of manager.json is the same one.
type jsonObject = managerconf.Object

var (
	parseJSONObject = managerconf.ParseObject
	marshalNoEscape = managerconf.MarshalNoEscape
)

// hubJSONMu serializes this process's edits of hub.json; a lock on its directory does the same between processes
// (the hub and `personad hub admin …` on its machine). Not on hub.json itself: the rename replaces that file.
var hubJSONMu sync.Mutex

// editHubJSON reads <dir>/hub.json (missing: an empty object), lets edit change it and writes it back atomically:
// a temporary file in the same directory, fsync, rename over the old one, mode 0600. A file that is not a JSON
// object is left alone (error). Values from the environment (PERSONA_PUBLIC_URL…) never get here: edit sees the file.
func editHubJSON(dir string, edit func(o *jsonObject) error) error {
	hubJSONMu.Lock()
	defer hubJSONMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := managerconf.LockDir(dir)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(dir, "hub.json")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	o, err := parseJSONObject(b)
	if err != nil {
		return fmt.Errorf("%s: %w (not changed)", path, err)
	}
	if err := edit(o); err != nil {
		return err
	}
	out, err := o.Indented()
	if err != nil {
		return err
	}
	return writeFileAtomic(path, out, 0o600)
}

// readHubJSON is <dir>/hub.json parsed (an empty object when there is none).
func readHubJSON(dir string) (*jsonObject, error) {
	b, err := os.ReadFile(filepath.Join(dir, "hub.json"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return parseJSONObject(b)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	// The rename itself must survive a power cut too.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
