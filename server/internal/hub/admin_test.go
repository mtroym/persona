package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// failingDeleteStore is adminMemStore whose session delete fails, as when Postgres is down.
type failingDeleteStore struct{ *adminMemStore }

func (failingDeleteStore) DeleteAdminSession(context.Context, string) error {
	return errors.New("connection refused")
}

// ip4 builds private addresses from their numbers, so fixtures do not look like someone's real hosts (like
// ingress_test.go; scripts/privacy_check.py flags private literals).
func ip4(a, b, c, d byte) string { return netip.AddrFrom4([4]byte{a, b, c, d}).String() }

// adminMemStore keeps the admin password and sessions (and device / pairing tokens) in memory.
type adminMemStore struct {
	sharedStore
	mu       sync.Mutex
	hash     string
	sessions map[string]db.AdminSession
	pairs    map[string]string // pairing token hash -> machine
	devices  []db.DeviceToken
	revoked  []string // the machines whose credentials were revoked, in order
}

func newAdminMemStore() *adminMemStore {
	return &adminMemStore{sessions: map[string]db.AdminSession{}, pairs: map[string]string{}}
}

func (s *adminMemStore) RevokeMachineCredentials(_ context.Context, machine string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = append(s.revoked, machine)
	return nil, nil
}

func (s *adminMemStore) AdminPasswordHash(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hash, nil
}

func (s *adminMemStore) SetAdminPassword(_ context.Context, hash string, ifUnset bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ifUnset && s.hash != "" {
		return false, nil
	}
	s.hash = hash
	return true, nil
}

func (s *adminMemStore) SetAdminPasswordSignOut(_ context.Context, hash, keep string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hash = hash
	for k := range s.sessions {
		if k != keep {
			delete(s.sessions, k)
		}
	}
	return nil
}

func (s *adminMemStore) CreateAdminSession(_ context.Context, a db.AdminSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[a.IDHash] = a
	return nil
}

func (s *adminMemStore) AdminSessionByHash(_ context.Context, hash string) (*db.AdminSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.sessions[hash]
	if !ok || !time.Now().Before(a.ExpiresAt) {
		return nil, nil
	}
	return &a, nil
}

func (s *adminMemStore) DeleteAdminSession(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hash)
	return nil
}

func (s *adminMemStore) CreatePairToken(_ context.Context, hash string, _ time.Time, machine string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairs[hash] = machine
	return nil
}

func (s *adminMemStore) DeviceTokens(context.Context) ([]db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.DeviceToken{}, s.devices...), nil
}

func (s *adminMemStore) RevokeDeviceToken(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.devices {
		if d.ID == id && d.RevokedAt == nil {
			now := time.Now()
			s.devices[i].RevokedAt = &now
			return true, nil
		}
	}
	return false, nil
}

func (s *adminMemStore) DeviceTokenByHash(context.Context, string) (*db.DeviceToken, error) {
	return nil, nil
}

// A private LAN (ip4(192, 168, 1, 0)/24) and a global /64 2001:db8:1::/64 on this machine; a VPN /64 that must not count.
func adminTestHub(t *testing.T) (*Hub, *adminMemStore, http.Handler) {
	t.Helper()
	h := newTestHub(t)
	store := newAdminMemStore()
	h.store = store
	h.admin.container = func() bool { return false }
	h.admin.prefixes = func() []ifacePrefix {
		return []ifacePrefix{
			{prefix: netip.MustParsePrefix(ip4(192, 168, 1, 0) + "/24")},
			{prefix: netip.MustParsePrefix("2001:db8:1::/64")},
			{prefix: netip.MustParsePrefix("2001:db8:9::/64"), vpn: true},
		}
	}
	h.admin.restart = func() {}
	return h, store, h.routes()
}

// setAdmin replaces the admin section as if hub.json had it from the start.
func setAdmin(h *Hub, cfg AdminConfig) {
	h.adminInit()
	h.admin.mu.Lock()
	h.admin.file = cfg
	h.admin.mu.Unlock()
}

type adminClient struct {
	t      *testing.T
	h      http.Handler
	remote string
	tls    bool
	csrf   string
	cookie *http.Cookie
	header http.Header
}

func (c *adminClient) do(method, path, body string) (int, map[string]any, *httptest.ResponseRecorder) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = c.remote
	if c.tls {
		req.TLS = &tls.ConnectionState{}
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		if c.csrf != "" {
			req.Header.Set("X-Persona-CSRF", c.csrf)
		}
	}
	for k, v := range c.header {
		req.Header[k] = v
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		if strings.HasPrefix(ck.Name, "persona_admin") {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if s, ok := out["csrf"].(string); ok && s != "" {
		c.csrf = s
	}
	return w.Code, out, w
}

func TestAdminScopeAddresses(t *testing.T) {
	h, _, _ := adminTestHub(t)
	lan := adminScope{access: accessLAN, trusted: []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10")}}
	cases := []struct {
		scope adminScope
		ip    string
		want  bool
	}{
		{lan, "127.0.0.1", true},
		{lan, "::1", true},
		{lan, ip4(192, 168, 1, 20), true},  // same network
		{lan, ip4(192, 168, 2, 20), false}, // another private network: not this LAN
		{lan, ip4(10, 0, 0, 5), false},     // private, not on any of this machine's networks
		{lan, "fe80::1", true},             // link-local is on-link by definition
		{lan, "2001:db8:1::42", true},      // global IPv6 on this machine's /64
		{lan, "2001:db8:9::42", false},     // a VPN's /64 does not count
		{lan, "203.0.113.9", false},        // the internet
		{lan, "100.100.1.1", true},         // security.trusted_cidrs
		{adminScope{access: accessLoopback}, ip4(192, 168, 1, 20), false},
		{adminScope{access: accessLoopback}, "127.0.0.1", true},
		{adminScope{access: accessCIDRs, cidrs: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}}, "198.51.100.7", true},
		{adminScope{access: accessCIDRs, cidrs: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}}, ip4(192, 168, 1, 20), false},
		{adminScope{access: accessAny}, "203.0.113.9", false}, // over plaintext "any" is "lan"
		{adminScope{access: accessAny}, ip4(192, 168, 1, 20), true},
	}
	for _, c := range cases {
		if got := h.addrInScope(c.scope, netip.MustParseAddr(c.ip)); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.scope.access, c.ip, got, c.want)
		}
	}
	// In a container the host's LAN is not visible: any private address counts, the internet still does not, and
	// neither does the container's gateway: docker-proxy relays everyone from there, the internet included.
	h.admin.container = func() bool { return true }
	gateway := ip4(172, 18, 0, 1)
	h.admin.gateways = func() []netip.Addr {
		return []netip.Addr{netip.MustParseAddr(gateway), netip.MustParseAddr("fd00:1::1")}
	}
	for ip, want := range map[string]bool{
		ip4(10, 0, 0, 5): true, ip4(172, 18, 0, 7): true, ip4(192, 168, 7, 7): true, "fe80::5": true, "fd00:1::7": true,
		"203.0.113.9": false, "2001:db8:1::42": false, gateway: false, "fd00:1::1": false, "127.0.0.1": true,
	} {
		if got := h.addrInScope(lan, netip.MustParseAddr(ip)); got != want {
			t.Errorf("container %s: %v, want %v", ip, got, want)
		}
	}
	// Only a deliberate choice lets the gateway in: security.trusted_cidrs, or admin.cidrs.
	trusting := adminScope{access: accessLAN, trusted: []netip.Prefix{netip.MustParsePrefix(gateway + "/32")}}
	listed := adminScope{access: accessCIDRs, cidrs: []netip.Prefix{netip.MustParsePrefix(gateway + "/32")}}
	if !h.addrInScope(trusting, netip.MustParseAddr(gateway)) || !h.addrInScope(listed, netip.MustParseAddr(gateway)) {
		t.Error("a gateway the user listed is still refused")
	}
	if h.addrInScope(adminScope{access: accessAny}, netip.MustParseAddr(gateway)) {
		t.Error("any over plaintext lets the gateway in")
	}
}

func TestContainerGateways(t *testing.T) {
	// A bridge network's /proc/net/route: the default route through the bridge's .1 (little-endian hex), and the network.
	route := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	route6 := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 fd000000000000010000000000000001 00000400 00000001 00000000 00000003     eth0\n" +
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo\n"
	second := netip.MustParsePrefix(ip4(172, 20, 0, 9) + "/16") // a second network: its .1 is a gateway docker-proxy may use
	got := containerGateways(route, route6, []netip.Prefix{netip.MustParsePrefix(ip4(172, 17, 0, 2) + "/16"), second,
		netip.MustParsePrefix("127.0.0.1/8"), netip.MustParsePrefix("fe80::42/64")})
	want := []string{ip4(172, 17, 0, 1), "fd00:0:0:1::1", ip4(172, 20, 0, 1)}
	if len(got) != len(want) {
		t.Fatalf("gateways %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("gateways %v, want %v", got, want)
		}
	}
	// No /proc (not Linux, or hidden): the networks' first addresses still count.
	if got := containerGateways("", "", []netip.Prefix{second}); len(got) != 1 || got[0].String() != ip4(172, 20, 0, 1) {
		t.Fatalf("fallback %v", got)
	}
	if inContainer() {
		t.Skip("running in a container")
	}
	if containerGateway(netip.MustParseAddr(ip4(172, 17, 0, 1))) {
		t.Fatal("a gateway outside a container")
	}
}

// Through docker-proxy everything comes from the gateway: there the admin UI does not exist, first-run setup included.
func TestAdminContainerGatewayLooksAbsent(t *testing.T) {
	h, _, mux := adminTestHub(t)
	h.admin.container = func() bool { return true }
	gateway := ip4(172, 18, 0, 1)
	h.admin.gateways = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr(gateway)} }
	proxied := &adminClient{t: t, h: mux, remote: gateway + ":40000"}
	// The gateway is a public source (sourceOf): admin paths answer exactly like any app API path from there.
	appCode, appOut, _ := proxied.do("GET", "/api/v1/machines", "")
	if code, out, _ := proxied.do("GET", "/api/v1/admin/state", ""); code != appCode || out["code"] != appOut["code"] || code == 200 {
		t.Fatalf("gateway state: %d %v, app API %d %v", code, out, appCode, appOut)
	}
	if code, _, _ := proxied.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`); code == 200 || code == http.StatusNoContent {
		t.Fatalf("gateway setup: %d", code)
	}
	lan := &adminClient{t: t, h: mux, remote: ip4(192, 168, 50, 9) + ":40000"} // DNAT keeps a LAN client's address
	if code, st, _ := lan.do("GET", "/api/v1/admin/state", ""); code != 200 || st["can_setup"] != true {
		t.Fatalf("LAN through DNAT: %d %v", code, st)
	}
	// Docker exec on the hub's machine: loopback inside the container.
	if code, _, _ := (&adminClient{t: t, h: mux, remote: "127.0.0.1:1"}).do("GET", "/api/v1/admin/state", ""); code != 200 {
		t.Fatalf("loopback in the container: %d", code)
	}
}

func TestAdminForwardedHeadersOnlyNarrow(t *testing.T) {
	h, _, _ := adminTestHub(t)
	req := func(remote string, headers map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/admin", nil)
		r.RemoteAddr = remote
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		remote  string
		headers map[string]string
		want    bool
	}{
		{"127.0.0.1:1", nil, true},
		// A proxy on this machine passing on someone from the internet: the proxy's loopback must not let them in.
		{"127.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.9"}, false},
		{"127.0.0.1:1", map[string]string{"X-Real-IP": "203.0.113.9"}, false},
		{"127.0.0.1:1", map[string]string{"Forwarded": `for="[2001:db8:7::1]:443";proto=https`}, false},
		{"127.0.0.1:1", map[string]string{"Forwarded": "for=unknown"}, false},
		{"127.0.0.1:1", map[string]string{"X-Forwarded-For": "garbage"}, false},
		// A LAN proxy passing on a LAN client is fine.
		{ip4(192, 168, 1, 2) + ":1", map[string]string{"X-Forwarded-For": ip4(192, 168, 1, 30) + ", 127.0.0.1"}, true},
		{ip4(192, 168, 1, 2) + ":1", map[string]string{"Forwarded": "for=" + ip4(192, 168, 1, 30) + ";by=" + ip4(192, 168, 1, 2)}, true},
		// A proxy that does not say for whom: unknown, so out.
		{"127.0.0.1:1", map[string]string{"Via": "1.1 proxy"}, false},
		{"127.0.0.1:1", map[string]string{"X-Forwarded-Proto": "https"}, false},
		{"127.0.0.1:1", map[string]string{"X-Forwarded-Host": "hub.example.com"}, false},
		// Headers never widen: an internet source claiming a LAN address stays out.
		{"203.0.113.9:1", map[string]string{"X-Forwarded-For": ip4(192, 168, 1, 30)}, false},
	}
	for i, c := range cases {
		if got := h.adminInScope(req(c.remote, c.headers)); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
}

func TestAdminOutOfScopeLooksAbsent(t *testing.T) {
	h, _, mux := adminTestHub(t)
	web := filepath.Join(h.cfg.dir, "web")
	if err := os.MkdirAll(filepath.Join(web, "admin", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("CLIENT"), 0o644)
	_ = os.WriteFile(filepath.Join(web, "admin", "index.html"), []byte("ADMIN"), 0o644)
	_ = os.WriteFile(filepath.Join(web, "admin", "assets", "admin-1.js"), []byte("ADMINJS"), 0o644)
	get := func(remote, path string, tlsOn bool) (int, string) {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = remote
		if tlsOn {
			r.TLS = &tls.ConnectionState{}
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	// In scope: the admin page, its client-side routes and its own files.
	for path, want := range map[string]string{"/admin": "ADMIN", "/admin/machines": "ADMIN", "/admin/assets/admin-1.js": "ADMINJS"} {
		if code, body := get(ip4(192, 168, 1, 20)+":5", path, false); code != 200 || body != want {
			t.Errorf("LAN %s: %d %q", path, code, body)
		}
	}
	// Out of scope: exactly what a hub without an admin UI answers.
	for _, path := range []string{"/admin", "/admin/machines", "/admin/assets/admin-1.js", "/ADMIN/index.html", "/admin/index.html"} {
		if code, body := get(ip4(10, 9, 9, 9)+":5", path, false); code != 200 || body != "CLIENT" {
			t.Errorf("outside %s: %d %q", path, code, body)
		}
	}
	code, out, _ := (&adminClient{t: t, h: mux, remote: ip4(10, 9, 9, 9) + ":5"}).do("GET", "/api/v1/admin/state", "")
	if code != http.StatusUnauthorized || out["code"] != "invalid_token" {
		t.Fatalf("outside state: %d %v (want the app API's 401)", code, out)
	}
	code, _, _ = (&adminClient{t: t, h: mux, remote: ip4(10, 9, 9, 9) + ":5"}).do("POST", "/api/v1/admin/login", `{"password":"x"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("outside login: %d", code)
	}
	// With an app token the app API answers its own 404: still no admin UI to be seen.
	r := httptest.NewRequest("GET", "/api/v1/admin/state", nil)
	r.RemoteAddr = ip4(10, 9, 9, 9) + ":5"
	r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("outside with a token: %d", w.Code)
	}
	// "any" lets the internet in, over TLS only; plaintext from there sees nothing.
	setAdmin(h, AdminConfig{Access: accessAny})
	if code, body := get("203.0.113.9:5", "/admin", true); code != 200 || body != "ADMIN" {
		t.Fatalf("any over TLS: %d %q", code, body)
	}
	if code, body := get("203.0.113.9:5", "/admin", false); body != "CLIENT" {
		t.Fatalf("any over plaintext from the internet: %d %q", code, body)
	}
}

func TestAdminPasswordHash(t *testing.T) {
	hash, err := hashAdminPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash %q", hash)
	}
	if !checkAdminPassword(hash, "correct horse") || checkAdminPassword(hash, "correct horsE") || checkAdminPassword(hash, "") {
		t.Fatal("check")
	}
	if other, _ := hashAdminPassword("correct horse"); other == hash {
		t.Fatal("no salt")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=0,t=2,p=1$AAAA$AAAA", "$argon2i$v=19$m=19456,t=2,p=1$AAAA$AAAA"} {
		if checkAdminPassword(bad, "x") {
			t.Fatalf("accepted %q", bad)
		}
	}
	if adminPasswordProblem("short") == nil || adminPasswordProblem("        ") == nil || adminPasswordProblem(strings.Repeat("a", 300)) == nil {
		t.Fatal("weak passwords pass")
	}
	if adminPasswordProblem("long enough") != nil || adminPasswordProblem("中文密码也可以的") != nil {
		t.Fatal("good passwords refused")
	}
}

func TestAdminSetupLoginSessionCSRF(t *testing.T) {
	_, store, mux := adminTestHub(t)
	c := &adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 20) + ":5000"}
	code, st, _ := c.do("GET", "/api/v1/admin/state", "")
	if code != 200 || st["password_set"] != false || st["can_setup"] != true || st["logged_in"] != false {
		t.Fatalf("first state: %d %v", code, st)
	}
	if code, out, _ := c.do("GET", "/api/v1/admin/overview", ""); code != http.StatusUnauthorized || out["code"] != "login_required" {
		t.Fatalf("overview before signing in: %d %v", code, out)
	}
	if code, out, _ := c.do("POST", "/api/v1/admin/setup", `{"password":"short"}`); code != 400 || out["code"] != "weak_password" {
		t.Fatalf("weak: %d %v", code, out)
	}
	code, st, w := c.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`)
	if code != 200 || st["logged_in"] != true || c.csrf == "" || c.cookie == nil {
		t.Fatalf("setup: %d %v", code, st)
	}
	ck := w.Result().Cookies()[0]
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != adminCookiePath || ck.Secure || ck.Name != "persona_admin" {
		t.Fatalf("cookie %+v", ck)
	}
	if store.hash == "" || strings.Contains(store.hash, "a good password") {
		t.Fatal("password not hashed")
	}
	for hash := range store.sessions {
		if hash == ck.Value {
			t.Fatal("the cookie value is stored as it is")
		}
	}
	// A second setup loses: the password is there.
	other := &adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 21) + ":5000"}
	if code, out, _ := other.do("POST", "/api/v1/admin/setup", `{"password":"another password"}`); code != http.StatusConflict || out["code"] != "already_set" {
		t.Fatalf("second setup: %d %v", code, out)
	}
	if code, _, _ := c.do("GET", "/api/v1/admin/overview", ""); code != 200 {
		t.Fatalf("overview: %d", code)
	}
	// Writes need the session's CSRF token, JSON, and a same-origin browser.
	csrf := c.csrf
	c.csrf = "wrong"
	if code, out, _ := c.do("PUT", "/api/v1/admin/hub", `{"name":"x"}`); code != 403 || out["code"] != "csrf" {
		t.Fatalf("bad csrf: %d %v", code, out)
	}
	c.csrf = csrf
	c.header = http.Header{"Sec-Fetch-Site": {"cross-site"}}
	if code, _, _ := c.do("PUT", "/api/v1/admin/hub", `{"name":"x"}`); code != 403 {
		t.Fatalf("cross-site: %d", code)
	}
	c.header = http.Header{"Origin": {"http://evil.example.com"}}
	if code, _, _ := c.do("PUT", "/api/v1/admin/hub", `{"name":"x"}`); code != 403 {
		t.Fatalf("other origin: %d", code)
	}
	c.header = http.Header{"Origin": {"http://example.com"}, "Sec-Fetch-Site": {"same-origin"}}
	if code, out, _ := c.do("PUT", "/api/v1/admin/hub", `{"name":"家里"}`); code != 200 || out["name"] != "家里" {
		t.Fatalf("same origin: %d %v", code, out)
	}
	c.header = nil
	req := httptest.NewRequest("PUT", "/api/v1/admin/hub", strings.NewReader(`{"name":"x"}`))
	req.RemoteAddr, req.Header = c.remote, http.Header{"Content-Type": {"text/plain"}, "X-Persona-CSRF": {csrf}}
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("form post: %d", rec.Code)
	}
	// Signing out ends the session for good.
	saved := c.cookie
	if code, _, _ := c.do("POST", "/api/v1/admin/logout", `{}`); code != 200 || c.cookie != nil {
		t.Fatalf("logout: %d", code)
	}
	c.cookie = saved
	if code, _, _ := c.do("GET", "/api/v1/admin/overview", ""); code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", code)
	}
	c.cookie, c.csrf = nil, ""
	if code, out, _ := c.do("POST", "/api/v1/admin/login", `{"password":"nope nope"}`); code != 401 || out["code"] != "bad_password" {
		t.Fatalf("wrong: %d %v", code, out)
	}
	if code, st, _ := c.do("POST", "/api/v1/admin/login", `{"password":"a good password"}`); code != 200 || st["logged_in"] != true {
		t.Fatalf("login: %d %v", code, st)
	}
	// A password change signs out the other browsers, not this one.
	b := &adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 22) + ":5000"}
	b.do("POST", "/api/v1/admin/login", `{"password":"a good password"}`)
	if code, out, _ := c.do("POST", "/api/v1/admin/password", `{"current":"a good password","new":"a better password"}`); code != 200 {
		t.Fatalf("change: %d %v", code, out)
	}
	if code, _, _ := c.do("GET", "/api/v1/admin/overview", ""); code != 200 {
		t.Fatalf("this browser after the change: %d", code)
	}
	if code, _, _ := b.do("GET", "/api/v1/admin/overview", ""); code != http.StatusUnauthorized {
		t.Fatalf("other browser after the change: %d", code)
	}
}

// A sign-out that could not delete the session says so (and logs it) instead of reporting ok.
func TestAdminLogoutDeleteFails(t *testing.T) {
	h, store, mux := adminTestHub(t)
	var mu sync.Mutex
	var logs strings.Builder
	h.logger = log.New(lockedWriter{&mu, &logs}, "", 0)
	c := &adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 20) + ":5000"}
	if code, _, _ := c.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`); code != 200 || c.cookie == nil {
		t.Fatalf("setup: %d", code)
	}
	value := c.cookie.Value
	h.store = failingDeleteStore{store}
	code, out, w := c.do("POST", "/api/v1/admin/logout", `{}`)
	if code != http.StatusInternalServerError || out["code"] != "logout_failed" || out["ok"] == true {
		t.Fatalf("logout with a failing delete: %d %v", code, out)
	}
	cleared := false
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "persona_admin" && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared || c.cookie != nil {
		t.Fatalf("cookie not cleared: %v", w.Header().Values("Set-Cookie"))
	}
	if len(store.sessions) != 1 {
		t.Fatalf("sessions: %d", len(store.sessions))
	}
	mu.Lock()
	logged := logs.String()
	mu.Unlock()
	if !strings.Contains(logged, "could not end the session") || !strings.Contains(logged, "connection refused") || strings.Contains(logged, value) {
		t.Fatalf("log: %q", logged)
	}
}

func TestAdminTLSCookieAndPublicPlaintext(t *testing.T) {
	h, _, mux := adminTestHub(t)
	setAdmin(h, AdminConfig{Access: accessCIDRs, CIDRs: []string{"198.51.100.0/24"}})
	plain := &adminClient{t: t, h: mux, remote: "198.51.100.7:1"}
	if code, out, _ := plain.do("GET", "/api/v1/admin/state", ""); code != 403 || out["code"] != "tls_required" {
		t.Fatalf("public cidr over plaintext: %d %v", code, out)
	}
	secure := &adminClient{t: t, h: mux, remote: "198.51.100.7:1", tls: true}
	code, out, w := secure.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`)
	if code != 200 {
		t.Fatalf("setup over TLS: %d %v", code, out)
	}
	ck := w.Result().Cookies()[0]
	if ck.Name != "persona_admin_tls" || !ck.Secure {
		t.Fatalf("TLS cookie %+v", ck)
	}
}

func TestAdminSetupUnderAnyIsLocalOnly(t *testing.T) {
	h, _, mux := adminTestHub(t)
	setAdmin(h, AdminConfig{Access: accessAny})
	remote := &adminClient{t: t, h: mux, remote: "203.0.113.9:1", tls: true}
	if code, st, _ := remote.do("GET", "/api/v1/admin/state", ""); code != 200 || st["can_setup"] != false {
		t.Fatalf("internet state: %d %v", code, st)
	}
	if code, out, _ := remote.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`); code != 403 || out["code"] != "setup_local_only" {
		t.Fatalf("internet setup: %d %v", code, out)
	}
	local := &adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 20) + ":1"}
	if code, _, _ := local.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`); code != 200 {
		t.Fatalf("LAN setup: %d", code)
	}
	if code, st, _ := remote.do("POST", "/api/v1/admin/login", `{"password":"a good password"}`); code != 200 || st["logged_in"] != true {
		t.Fatalf("internet login over TLS: %d %v", code, st)
	}
}

func TestAdminLoginRateLimit(t *testing.T) {
	h, _, mux := adminTestHub(t)
	h.cfg.Security.AuthFailures = 3
	(&adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 20) + ":1"}).do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`)
	lan := &adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 30) + ":1"}
	for i := range 3 {
		if code, _, _ := lan.do("POST", "/api/v1/admin/login", `{"password":"guess guess"}`); code != 401 {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	// The LAN counts too (a password is guessable); even the right password waits now.
	code, out, w := lan.do("POST", "/api/v1/admin/login", `{"password":"a good password"}`)
	if code != 429 || out["code"] != "rate_limited" || w.Header().Get("Retry-After") == "" || out["retry_after"] == nil {
		t.Fatalf("blocked: %d %v", code, out)
	}
	// Another source and this machine are not affected; this machine is never counted.
	if code, _, _ := (&adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 31) + ":1"}).do("POST", "/api/v1/admin/login", `{"password":"a good password"}`); code != 200 {
		t.Fatalf("other source: %d", code)
	}
	// Loopback counts too: it may be a reverse proxy on this machine relaying the internet.
	local := &adminClient{t: t, h: mux, remote: "127.0.0.1:1"}
	for range 3 {
		local.do("POST", "/api/v1/admin/login", `{"password":"guess guess"}`)
	}
	if code, _, _ := local.do("POST", "/api/v1/admin/login", `{"password":"a good password"}`); code != 429 {
		t.Fatalf("loopback guessing: %d", code)
	}
	// The app's token limiter is a different table.
	if retry := h.devtok.failures.retry(ip4(192, 168, 1, 30), time.Now(), h.cfg.Security); retry != 0 {
		t.Fatalf("app limiter touched: %v", retry)
	}
}

func signedIn(t *testing.T, mux http.Handler, remote string) *adminClient {
	t.Helper()
	c := &adminClient{t: t, h: mux, remote: remote}
	if code, _, _ := c.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`); code != 200 {
		if code, _, _ := c.do("POST", "/api/v1/admin/login", `{"password":"a good password"}`); code != 200 {
			t.Fatalf("sign in: %d", code)
		}
	}
	return c
}

func TestAdminAccessOnlyNarrows(t *testing.T) {
	h, _, mux := adminTestHub(t)
	_ = os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"token":"t","future_field":{"x":1},"admin":{"access":"any","note":"keep"}}`), 0o600)
	setAdmin(h, AdminConfig{Access: accessAny})
	c := signedIn(t, mux, ip4(192, 168, 1, 20)+":1")
	if code, out, _ := c.do("PUT", "/api/v1/admin/network/access", `{"access":"cidrs","cidrs":["198.51.100.0/24"]}`); code != 409 || out["code"] != "would_lock_you_out" {
		t.Fatalf("lock out: %d %v", code, out)
	}
	if code, out, _ := c.do("PUT", "/api/v1/admin/network/access", `{"access":"lan"}`); code != 200 {
		t.Fatalf("any → lan: %d %v", code, out)
	}
	if h.adminScope().access != accessLAN {
		t.Fatal("not live")
	}
	if code, out, _ := c.do("PUT", "/api/v1/admin/network/access", `{"access":"any"}`); code != 403 || out["code"] != "widen_cli_only" {
		t.Fatalf("lan → any: %d %v", code, out)
	}
	if code, out, _ := c.do("PUT", "/api/v1/admin/network/access", `{"access":"cidrs","cidrs":["`+ip4(192, 168, 1, 0)+`/24"]}`); code != 403 {
		t.Fatalf("lan → cidrs: %d %v", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json"))
	var file map[string]any
	_ = json.Unmarshal(b, &file)
	admin := file["admin"].(map[string]any)
	if admin["access"] != "lan" || admin["note"] != "keep" || file["future_field"] == nil || file["token"] != "t" {
		t.Fatalf("hub.json: %s", b)
	}
	if code, out, _ := c.do("PUT", "/api/v1/admin/network/access", `{"access":"loopback","force":true}`); code != 200 {
		t.Fatalf("lan → loopback (forced): %d %v", code, out)
	}
	// The browser is outside now: the admin UI is gone for it.
	if code, _, _ := c.do("GET", "/api/v1/admin/overview", ""); code != http.StatusUnauthorized {
		t.Fatalf("after locking out: %d", code)
	}
	// A scope from the environment is the deployment's: the page cannot change it.
	t.Setenv("PERSONA_ADMIN_ACCESS", "lan")
	if err := h.adminReload(); err != nil {
		t.Fatal(err)
	}
	c2 := signedIn(t, mux, ip4(192, 168, 1, 20)+":1")
	if code, out, _ := c2.do("PUT", "/api/v1/admin/network/access", `{"access":"loopback","force":true}`); code != 409 || out["code"] != "from_env" {
		t.Fatalf("from env: %d %v", code, out)
	}
}

func TestNarrowerAccess(t *testing.T) {
	p := func(s ...string) []netip.Prefix {
		out, _ := parseCIDRs(s)
		return out
	}
	any, lan, lo := adminScope{access: accessAny}, adminScope{access: accessLAN}, adminScope{access: accessLoopback}
	wide := adminScope{access: accessCIDRs, cidrs: p(ip4(10, 0, 0, 0) + "/8")}
	cases := []struct {
		from, to adminScope
		want     bool
	}{
		{any, lan, true}, {any, lo, true}, {any, wide, true}, {any, any, true},
		{lan, lo, true}, {lan, lan, true}, {lan, any, false}, {lan, wide, false},
		{wide, adminScope{access: accessCIDRs, cidrs: p(ip4(10, 1, 0, 0)+"/16", ip4(10, 2, 3, 4))}, true},
		{wide, adminScope{access: accessCIDRs, cidrs: p(ip4(10, 0, 0, 0) + "/7")}, false},
		{wide, adminScope{access: accessCIDRs, cidrs: p("192.0.2.0/24")}, false},
		{wide, lan, false}, {wide, lo, true}, {lo, lan, false},
	}
	for i, c := range cases {
		if got := narrowerAccess(c.from, c.to); got != c.want {
			t.Errorf("case %d %s→%s: %v", i, c.from.access, c.to.access, got)
		}
	}
}

func TestEditHubJSONKeepsWhatItDoesNotKnow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.json")
	orig := `{
    "listen": ":18660",
    "token": "abc",
    "zzz_future": {"nested": [1, 2, {"a": "<b>"}]},
    "machines": [{"id": "a", "kind": "ssh", "ssh_host": "a", "x_extra": true}],
    "public": {"advertise_ipv6": false, "other": 1}
}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	err := editHubJSON(dir, func(o *jsonObject) error {
		if err := o.Set("name", "家里"); err != nil {
			return err
		}
		public, err := o.Object("public")
		if err != nil {
			return err
		}
		if err := public.Set("tls_listen", ":18662"); err != nil {
			return err
		}
		o.SetObject("public", public)
		admin, _ := o.Object("admin")
		_ = admin.Set("access", "loopback")
		o.SetObject("admin", admin)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	want := `{
  "listen": ":18660",
  "token": "abc",
  "zzz_future": {
    "nested": [
      1,
      2,
      {
        "a": "<b>"
      }
    ]
  },
  "machines": [
    {
      "id": "a",
      "kind": "ssh",
      "ssh_host": "a",
      "x_extra": true
    }
  ],
  "public": {
    "advertise_ipv6": false,
    "other": 1,
    "tls_listen": ":18662"
  },
  "name": "家里",
  "admin": {
    "access": "loopback"
  }
}
`
	if string(b) != want {
		t.Fatalf("got\n%s", b)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hub.json.") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
	// Not an object: refused, the file stays as it was.
	_ = os.WriteFile(path, []byte(`[1,2]`), 0o600)
	if err := editHubJSON(dir, func(o *jsonObject) error { return o.Set("x", 1) }); err == nil {
		t.Fatal("edited a non-object")
	}
	if b, _ := os.ReadFile(path); string(b) != `[1,2]` {
		t.Fatalf("changed: %s", b)
	}
}

func TestAdminMachinesWriteHubJSON(t *testing.T) {
	h, store, mux := adminTestHub(t)
	_ = os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"token":"t","machines":[{"id":"old","kind":"ssh","ssh_host":"old","x":1}]}`), 0o600)
	h.cfg.Machines = []MachineConfig{{ID: "old", Kind: "ssh", SSHHost: "old"}}
	c := signedIn(t, mux, ip4(192, 168, 1, 20)+":1")
	for body, code := range map[string]string{
		`{"kind":"link","id":"Bad ID"}`:                         "invalid_id",
		`{"kind":"ssh","id":"box","ssh_host":"me@box"}`:         "ssh_unsupported", // not added any more (plan-hub-center.md 4)
		`{"kind":"tailcat","id":"box"}`:                         "ssh_unsupported",
		`{"kind":"carrier-pigeon","id":"box"}`:                  "invalid_kind",
		`{"kind":"link","id":"old"}`:                            "exists",
		`{"kind":"service","id":"svc","pair_link":"https://x"}`: "pair_failed",
	} {
		if _, out, _ := c.do("POST", "/api/v1/admin/machines", body); out["code"] != code {
			t.Errorf("%s: %v", body, out)
		}
	}
	code, out, _ := c.do("POST", "/api/v1/admin/machines", `{"kind":"link","id":"box","name":"盒子"}`)
	if code != 200 || out["machine"].(map[string]any)["state"] != "pending_add" || out["restart"].(map[string]any)["required"] != true {
		t.Fatalf("add: %d %v", code, out)
	}
	if code, _, _ := c.do("POST", "/api/v1/admin/machines", `{"kind":"link","id":"mac"}`); code != 200 {
		t.Fatalf("add link: %d", code)
	}
	if code, _, _ := c.do("DELETE", "/api/v1/admin/machines/old", `{}`); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	// A new link computer's id loses what was left under it before it is added; "old" only when it is deleted, not
	// when an add of its id is refused.
	if got := strings.Join(store.revoked, ","); got != "box,mac,old" {
		t.Fatalf("credentials revoked for %q", got)
	}
	b, _ := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json"))
	var file struct {
		Token    string           `json:"token"`
		Machines []map[string]any `json:"machines"`
	}
	_ = json.Unmarshal(b, &file)
	if file.Token != "t" || len(file.Machines) != 2 || file.Machines[0]["id"] != "box" || file.Machines[0]["name"] != "盒子" ||
		file.Machines[1]["kind"] != "link" {
		t.Fatalf("hub.json: %s", b)
	}
	_, out, _ = c.do("GET", "/api/v1/admin/machines", "")
	reasons := out["restart"].(map[string]any)["reasons"].([]any)
	if len(reasons) != 3 {
		t.Fatalf("reasons %v", reasons)
	}
	if code, out, _ := c.do("POST", "/api/v1/admin/machines/box/link-pair", `{}`); code != 409 || out["code"] != "not_active" {
		t.Fatalf("pair before restart: %d %v", code, out)
	}
}

func TestAdminHubNameAndDevices(t *testing.T) {
	h, store, mux := adminTestHub(t)
	store.devices = []db.DeviceToken{{ID: "dev_1", Name: "iPhone", Kind: "phone", Role: "full"}}
	c := signedIn(t, mux, ip4(192, 168, 1, 20)+":1")
	if _, out, _ := c.do("PUT", "/api/v1/admin/hub", `{"name":"书房"}`); out["name"] != "书房" || h.hubName() != "书房" {
		t.Fatalf("name %v %q", out, h.hubName())
	}
	if _, out, _ := c.do("PUT", "/api/v1/admin/hub", `{"name":""}`); out["name_set"] != false || h.hubName() == "" {
		t.Fatalf("cleared %v", out)
	}
	if code, out, _ := c.do("GET", "/api/v1/admin/overview", ""); code != 200 || out["devices"] != float64(1) {
		t.Fatalf("overview %d %v", code, out)
	}
	if code, _, _ := c.do("DELETE", "/api/v1/admin/devices/dev_1", `{}`); code != 200 {
		t.Fatalf("revoke %d", code)
	}
	if code, _, _ := c.do("DELETE", "/api/v1/admin/devices/dev_1", `{}`); code != 404 {
		t.Fatalf("revoke twice %d", code)
	}
	code, out, _ := c.do("POST", "/api/v1/admin/devices/pair", `{}`)
	qr, _ := out["qr"].(map[string]any)
	if code != 200 || !strings.HasPrefix(out["pair_url"].(string), "persona://pair?") || qr == nil {
		t.Fatalf("pair %d %v", code, out)
	}
	rows := qr["rows"].([]any)
	if size := int(qr["size"].(float64)); size < 21 || len(rows) != size || len(rows[0].(string)) != size {
		t.Fatalf("qr %v", qr["size"])
	}
	if len(store.pairs) != 1 {
		t.Fatal("pairing token not stored")
	}
	// The mesh's routes (peer_admin.go): no peers yet.
	if code, out, _ := c.do("GET", "/api/v1/admin/peers", ""); code != 200 || out["peers"] == nil || out["discovered"] == nil {
		t.Fatalf("peers %d %v", code, out)
	}
}

func TestAdminRestartOnlyWhenSupervised(t *testing.T) {
	h, _, mux := adminTestHub(t)
	restarted := make(chan struct{}, 1)
	h.admin.restart = func() { restarted <- struct{}{} }
	c := signedIn(t, mux, ip4(192, 168, 1, 20)+":1")
	t.Setenv("INVOCATION_ID", "")
	t.Setenv("PERSONA_SUPERVISED", "")
	if code, out, _ := c.do("POST", "/api/v1/admin/restart", `{}`); code != 409 || out["code"] != "not_supervised" {
		t.Fatalf("unsupervised: %d %v", code, out)
	}
	t.Setenv("INVOCATION_ID", "abc")
	if code, _, _ := c.do("POST", "/api/v1/admin/restart", `{}`); code != 200 {
		t.Fatalf("supervised: %d", code)
	}
	select {
	case <-restarted:
	case <-time.After(3 * time.Second):
		t.Fatal("no restart")
	}
}

func TestAdminReloadNeedsMasterTokenOverLoopback(t *testing.T) {
	h, _, mux := adminTestHub(t)
	_ = os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"admin":{"access":"loopback"},"name":"新名字"}`), 0o600)
	try := func(remote, token string) int {
		r := httptest.NewRequest("POST", "/api/v1/admin/reload", strings.NewReader("{}"))
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if code := try("127.0.0.1:1", "wrong"); code != 403 {
		t.Fatalf("wrong token: %d", code)
	}
	if code := try(ip4(192, 168, 1, 20)+":1", h.cfg.Token); code != 403 {
		t.Fatalf("from the LAN: %d", code)
	}
	if code := try("127.0.0.1:1", h.cfg.Token); code != 200 {
		t.Fatalf("reload: %d", code)
	}
	if h.adminScope().access != accessLoopback || h.hubName() != "新名字" {
		t.Fatalf("not reloaded: %s %s", h.adminScope().access, h.hubName())
	}
}

func TestAdminBadScopeFailsClosed(t *testing.T) {
	h, _, _ := adminTestHub(t)
	setAdmin(h, AdminConfig{Access: "everyone"})
	if s := h.adminScope(); s.access != accessLoopback {
		t.Fatalf("bad access: %s", s.access)
	}
	setAdmin(h, AdminConfig{Access: accessCIDRs, CIDRs: []string{"not a network"}})
	if s := h.adminScope(); s.access != accessLoopback {
		t.Fatalf("bad cidrs: %s", s.access)
	}
}

func TestParseDoctor(t *testing.T) {
	text := "hub 自检（只读）\n正常｜明文 listen：:18660；仅供内网或 VPN 使用\n有问题｜public.tls_listen：未设置有效 TLS 监听端口\n" +
		"正常｜候选地址：已读取\n  http://hub.example.com:18660（name，端口 18660）\n外部可达性：未验证。\n\n下一步：\n- 放行 TLS 端口\n- 用手机测一次\n"
	items, next := parseDoctor(text)
	if len(items) != 5 || items[1].Level != "ok" || items[1].Item != "明文 listen" || items[2].Level != "problem" ||
		!strings.Contains(items[3].Detail, "hub.example.com") || items[4].Level != "info" {
		t.Fatalf("items %+v", items)
	}
	if len(next) != 2 || next[0] != "放行 TLS 端口" {
		t.Fatalf("next %v", next)
	}
}

func TestAdminAccessCommand(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"token":"","listen":"127.0.0.1:1","other":true}`), 0o600)
	var out, errOut strings.Builder
	ctx := context.Background()
	if err := adminAccessCommand(ctx, dir, []string{"--get"}, &out, &errOut); err != nil || strings.TrimSpace(out.String()) != "" {
		t.Fatalf("get unset: %v %q", err, out.String())
	}
	if err := adminAccessCommand(ctx, dir, []string{"cidrs"}, &out, &errOut); err == nil {
		t.Fatal("cidrs without --cidrs")
	}
	if err := adminAccessCommand(ctx, dir, []string{"everyone"}, &out, &errOut); err == nil {
		t.Fatal("bad scope")
	}
	out.Reset()
	if err := adminAccessCommand(ctx, dir, []string{"cidrs", "--cidrs", "192.0.2.7, 198.51.100.0/24"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hub 没在运行") {
		t.Fatalf("out %q", out.String())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "hub.json"))
	var file map[string]any
	_ = json.Unmarshal(b, &file)
	admin := file["admin"].(map[string]any)
	if admin["access"] != "cidrs" || len(admin["cidrs"].([]any)) != 2 || admin["cidrs"].([]any)[0] != "192.0.2.7/32" || file["other"] != true {
		t.Fatalf("hub.json %s", b)
	}
	out.Reset()
	if err := adminAccessCommand(ctx, dir, []string{"--get"}, &out, &errOut); err != nil || strings.TrimSpace(out.String()) != "cidrs" {
		t.Fatalf("get: %v %q", err, out.String())
	}
}

// From loopback (maybe a reverse proxy on this machine relaying the internet) the first password needs the one-time
// code only someone on the hub's machine sees; the LAN does not.
func TestAdminSetupCodeFromLoopback(t *testing.T) {
	h, store, mux := adminTestHub(t)
	h.cfg.Security.AuthFailures = 3
	local := &adminClient{t: t, h: mux, remote: "127.0.0.1:1"}
	code, st, _ := local.do("GET", "/api/v1/admin/state", "")
	if code != 200 || st["setup_code_required"] != true || st["can_setup"] != true || !h.setupCodeLive() {
		t.Fatalf("state %d %v (and a code logged: %v)", code, st, h.setupCodeLive())
	}
	if _, st, _ := (&adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 20) + ":1"}).do("GET", "/api/v1/admin/state", ""); st["setup_code_required"] != false {
		t.Fatalf("LAN state %v", st)
	}
	if code, out, _ := local.do("POST", "/api/v1/admin/setup", `{"password":"a good password"}`); code != 403 || out["code"] != "setup_code_required" {
		t.Fatalf("no code: %d %v", code, out)
	}
	setup, _ := h.newSetupCode()
	if code, out, _ := local.do("POST", "/api/v1/admin/setup", `{"password":"a good password","setup_code":"ABCD-EFGH"}`); code != 401 || out["code"] != "bad_setup_code" {
		t.Fatalf("wrong code: %d %v", code, out)
	}
	// A weak password does not spend the code.
	if code, out, _ := local.do("POST", "/api/v1/admin/setup", `{"password":"short","setup_code":"`+setup+`"}`); code != 400 || out["code"] != "weak_password" {
		t.Fatalf("weak: %d %v", code, out)
	}
	// Lower case and without the dash: the same code.
	relaxed := strings.ToLower(strings.ReplaceAll(setup, "-", ""))
	if code, st, _ := local.do("POST", "/api/v1/admin/setup", `{"password":"a good password","setup_code":"`+relaxed+`"}`); code != 200 || st["logged_in"] != true {
		t.Fatalf("right code: %d %v", code, st)
	}
	if h.setupCodeLive() || store.hash == "" {
		t.Fatal("the code was not spent")
	}
	// The setup-code command: master token over loopback only; nothing to give once a password is set.
	post := func(remote, token string) (int, map[string]any) {
		r := httptest.NewRequest("POST", "/api/v1/admin/setup-code", strings.NewReader("{}"))
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := post("127.0.0.1:1", h.cfg.Token); code != 409 {
		t.Fatalf("after setup: %d", code)
	}
	store.hash = ""
	if code, _ := post(ip4(192, 168, 1, 20)+":1", h.cfg.Token); code != 403 {
		t.Fatalf("from the LAN: %d", code)
	}
	if code, _ := post("127.0.0.1:1", "nope"); code != 403 {
		t.Fatalf("wrong token: %d", code)
	}
	code, out := post("127.0.0.1:1", h.cfg.Token)
	fresh, _ := out["code"].(string)
	if code != 200 || len(fresh) != 9 || fresh[4] != '-' {
		t.Fatalf("fresh code: %d %v", code, out)
	}
	// Ten minutes later it is gone.
	later := time.Now().Add(setupCodeLifetime + time.Second)
	h.admin.now = func() time.Time { return later }
	if h.useSetupCode(fresh) {
		t.Fatal("an expired code worked")
	}
}

func TestAdminSetupCodeGuessesAreLimited(t *testing.T) {
	h, _, mux := adminTestHub(t)
	h.cfg.Security.AuthFailures = 3
	h.newSetupCode()
	local := &adminClient{t: t, h: mux, remote: "127.0.0.1:1"}
	for i := range 3 {
		if code, _, _ := local.do("POST", "/api/v1/admin/setup", `{"password":"a good password","setup_code":"AAAA-AAAA"}`); code != 401 {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	if code, out, _ := local.do("POST", "/api/v1/admin/setup", `{"password":"a good password","setup_code":"AAAA-AAAA"}`); code != 429 {
		t.Fatalf("after guessing: %d %v", code, out)
	}
}

func TestAdminNetworkPublicKeepsEnvOut(t *testing.T) {
	h, _, mux := adminTestHub(t)
	_ = os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"token":"t","public":{"advertise_ipv6":false}}`), 0o600)
	t.Setenv("PERSONA_PUBLIC_URL", "http://nas.example.com:18660")
	c := signedIn(t, mux, ip4(192, 168, 1, 20)+":1")
	_, view, _ := c.do("GET", "/api/v1/admin/network", "")
	if view["public_url"] != "http://nas.example.com:18660" || view["public_url_from_env"] != true {
		t.Fatalf("view %v", view)
	}
	// The page sends every field back: the environment's URL is not written into the file.
	if code, out, _ := c.do("PUT", "/api/v1/admin/network/public", `{"public_url":"http://nas.example.com:18660","tls_listen":":18662","advertise":["https://hub.example.com:18662"]}`); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	if code, out, _ := c.do("PUT", "/api/v1/admin/network/public", `{"public_url":"http://other.example.com:18660"}`); code != 409 || out["code"] != "from_env" {
		t.Fatalf("change env url: %d %v", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json"))
	if strings.Contains(string(b), "public_url") || !strings.Contains(string(b), `"tls_listen": ":18662"`) || !strings.Contains(string(b), `"advertise_ipv6": false`) {
		t.Fatalf("hub.json %s", b)
	}
	if code, out, _ := c.do("PUT", "/api/v1/admin/network/public", `{"tls_listen":":18660"}`); code != 400 {
		t.Fatalf("tls on the plaintext port: %d %v", code, out)
	}
	_, out, _ := c.do("GET", "/api/v1/admin/network", "")
	if out["restart"].(map[string]any)["required"] != true {
		t.Fatalf("restart %v", out["restart"])
	}
}

// Setting is the test database's when there is one, else nothing (the hello's answer reads the mutes).
func (s *adminMemStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if s.sharedStore != nil {
		return s.sharedStore.Setting(ctx, key, out)
	}
	return false, nil
}
