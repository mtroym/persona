package hub

import (
	"crypto/subtle"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/osproc"
)

// The hub admin UI (docs/operations/admin.md): /admin and /api/v1/admin/, signed in with the administrator's password, reachable
// only from admin.access. Outside that scope neither exists: page paths fall through to the web client and API paths
// to the app's API, exactly as on a hub without an admin UI.

// processStart is when this personad started (the overview's uptime).
var processStart = time.Now()

// adminState is the admin UI's live state; hub.json's "admin" and "name" are read into it on first use and changed
// through it (the rest of the hub reads h.cfg, which never changes while it runs).
type adminState struct {
	once sync.Once
	mu   sync.Mutex
	file AdminConfig  // hub.json's "admin"
	env  *AdminConfig // PERSONA_ADMIN_ACCESS / PERSONA_ADMIN_CIDRS, nil when unset
	name *string      // hub.json's "name" once the admin UI changed or reloaded it; nil: h.cfg.Name
	bad  string       // why the configured scope was refused (it then acts as loopback)
	// personaView is assistant.persona_view once the admin UI changed it; nil: h.cfg's (admin_assistant.go).
	personaView           *bool
	personaViewGeneration uint64

	setup          *setupCode  // the first-run setup code (admin_setupcode.go), nil when none is live
	failures       authLimiter // failed sign-ins per source
	publicFailures authLimiter // failed sign-ins from outside the LAN, all together
	doctorMu       sync.Mutex

	ifCache       []ifacePrefix
	ifCached      time.Time
	gatewayLogged time.Time

	// Seams for tests.
	prefixes  func() []ifacePrefix
	container func() bool
	gateways  func() []netip.Addr
	restart   func()
	now       func() time.Time
}

// adminScope is the effective admin.access with its networks parsed.
type adminScope struct {
	access  string
	cidrs   []netip.Prefix
	trusted []netip.Prefix // security.trusted_cidrs (lan, any)
	fromEnv bool
}

func (h *Hub) adminInit() {
	h.admin.once.Do(func() {
		h.admin.mu.Lock()
		defer h.admin.mu.Unlock()
		h.admin.file = h.cfg.Admin
		h.admin.env = adminEnv()
	})
}

func (h *Hub) adminScope() adminScope {
	h.adminInit()
	h.admin.mu.Lock()
	cfg, fromEnv := h.admin.file, false
	if h.admin.env != nil {
		cfg, fromEnv = *h.admin.env, true
	}
	h.admin.mu.Unlock()
	scope, err := scopeFrom(cfg)
	scope.fromEnv = fromEnv
	if err != nil {
		// A scope that cannot be read must not open anything up: only this machine.
		h.admin.mu.Lock()
		if h.admin.bad != err.Error() {
			h.admin.bad = err.Error()
			h.logf("admin: %v; the admin UI is limited to this machine until it is fixed", err)
		}
		h.admin.mu.Unlock()
		return adminScope{access: accessLoopback, fromEnv: fromEnv}
	}
	scope.trusted, _ = parseCIDRs(h.cfg.Security.TrustedCIDRs)
	return scope
}

func scopeFrom(cfg AdminConfig) (adminScope, error) {
	access := strings.TrimSpace(cfg.Access)
	if !validAccess(access) {
		return adminScope{}, errors.New("admin.access " + `"` + access + `"` + " is not loopback, lan, cidrs or any")
	}
	if access == "" {
		access = accessLAN
	}
	cidrs, err := parseCIDRs(cfg.CIDRs)
	if err != nil {
		return adminScope{}, errors.New("admin.cidrs: " + err.Error())
	}
	return adminScope{access: access, cidrs: cidrs}, nil
}

func (h *Hub) adminNow() time.Time {
	if h.admin.now != nil {
		return h.admin.now()
	}
	return time.Now()
}

// adminRoutes puts the admin API on mux and returns the handler for every other path: the web client, with /admin
// in front of it for sources in scope. api is the app's API, where admin requests from outside the scope go.
func (h *Hub) adminRoutes(mux *http.ServeMux, api, web http.Handler) http.Handler {
	open := http.NewServeMux()   // before signing in
	closed := http.NewServeMux() // signed in
	h.adminAuthRoutes(open, closed)
	h.adminHubRoutes(closed)
	h.adminAssistantRoutes(closed)
	h.adminMachineRoutes(closed)
	h.adminUsageRoutesRelayed(closed) // the usage sources page, the manager's (devices_routes.go, #2595)
	h.adminDeviceRoutes(closed)
	h.adminNetworkRoutes(closed)
	h.adminExtensionRoutes(closed)
	// Hub mesh (docs/archive/operations/plan-hub-mesh.md 2.7): /peers and /routes, from the mesh's own file.
	h.registerPeerAdmin(closed)
	h.peerCLIRoutes(open) // personad hub pair --peer, personad hub peer … (peer_admin.go)
	signedIn := jsonRouteErrors(closed)
	mux.Handle("/api/v1/admin/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.adminInScope(r) {
			api.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// A password must not cross the internet in the clear, whichever scope let the source in.
		if r.TLS == nil && h.sourceOf(r) == sourcePublic {
			writeErrCode(w, http.StatusForbidden, "tls_required", errors.New("the admin UI needs TLS from outside the LAN"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if code, err := adminWriteCheck(r); err != nil {
				writeErrCode(w, http.StatusForbidden, code, err)
				return
			}
		}
		if _, pattern := open.Handler(r); pattern != "" {
			open.ServeHTTP(w, r)
			return
		}
		s, ok := h.adminSession(w, r)
		if !ok {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Persona-CSRF")), []byte(s.CSRF)) != 1 {
			writeErrCode(w, http.StatusForbidden, "csrf", errors.New("missing or stale X-Persona-CSRF"))
			return
		}
		signedIn.ServeHTTP(w, r)
	}))
	page := h.adminPage(filepath.Join(h.cfg.dir, "web"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminPath(r.URL.Path) {
			web.ServeHTTP(w, r)
			return
		}
		if !h.adminInScope(r) {
			// What a hub without an admin UI answers for a path it does not have: the web client's own page.
			r2 := r.Clone(r.Context())
			r2.URL.Path, r2.URL.RawPath = "/", ""
			web.ServeHTTP(w, r2)
			return
		}
		page.ServeHTTP(w, r)
	})
}

// adminPath is /admin or anything under it, in any letter case (a case-insensitive file system would serve
// /ADMIN/index.html from the web directory otherwise).
func adminPath(p string) bool {
	p = strings.ToLower(path.Clean("/" + p))
	return p == "/admin" || strings.HasPrefix(p, "/admin/")
}

// adminWriteCheck refuses writes a browser would only send for another site: JSON only (a cross-site form cannot
// send it without a preflight, which nothing here answers; a plugin upload is application/octet-stream, which needs
// one too), same-origin fetch metadata, an Origin of this host.
func adminWriteCheck(r *http.Request) (string, error) {
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	p := path.Clean(r.URL.Path)
	upload := r.Method == http.MethodPost && (p == adminUploadPath || p == legacyAdminUploadPath) && strings.HasPrefix(ct, "application/octet-stream")
	if !strings.HasPrefix(ct, "application/json") && !upload {
		return "csrf", errors.New("writes must be application/json")
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return "csrf", errors.New("cross-site request")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		host := origin
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		if !strings.EqualFold(host, r.Host) {
			return "csrf", errors.New("cross-origin request")
		}
	}
	return "", nil
}

// adminPage serves web/admin/ (the admin UI's own page, built from web/admin/index.html): files under it as they are,
// every other path below /admin its index.html (client-side routes).
func (h *Hub) adminPage(dir string) http.Handler {
	embedded, _ := fs.Sub(webDist, "webdist")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var site fs.FS
		if st, err := os.Stat(filepath.Join(dir, "admin", "index.html")); err == nil && !st.IsDir() {
			site = os.DirFS(dir)
		} else if embedded != nil {
			if st, err := fs.Stat(embedded, "admin/index.html"); err == nil && !st.IsDir() {
				site = embedded
			}
		}
		if site == nil {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		file := name
		if st, err := fs.Stat(site, name); err != nil || st.IsDir() || strings.HasPrefix(path.Base(name), ".") {
			if strings.HasPrefix(name, "admin/assets/") {
				http.NotFound(w, r)
				return
			}
			file = "admin/index.html"
		}
		if file == "admin/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(file, "admin/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		f, err := site.Open(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		rs, ok := f.(io.ReadSeeker)
		if err != nil || !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, path.Base(file), st.ModTime(), rs)
	})
}

// readAdminJSON decodes a small JSON body into v (an empty body is {}).
var readAdminJSON = apierr.ReadAdminJSON

// supervised is whether something restarts the hub when it exits: systemd (INVOCATION_ID), a container's restart
// policy, or PERSONA_SUPERVISED=1 for anything else that does.
func (h *Hub) supervised() bool {
	return os.Getenv("INVOCATION_ID") != "" || identity.Getenv("PERSONA_SUPERVISED") == "1" || h.adminContainer()
}

// restartHub ends this process the way systemctl stop / docker stop would (SIGTERM: a clean shutdown); systemd's
// Restart=always or the container's restart policy starts it again.
func (h *Hub) restartHub() {
	if h.admin.restart != nil {
		h.admin.restart()
		return
	}
	h.logf("admin: restart requested from the admin UI")
	osproc.TerminateSelf()
}
