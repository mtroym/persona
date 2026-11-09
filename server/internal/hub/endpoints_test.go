package hub

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netaddr"
)

// Construct synthetic addresses for scope tests; no deployment address is a fixture.
func endpointTestAddress(kind string) netip.Addr {
	switch kind {
	case "lan4":
		return netip.AddrFrom4([4]byte{0x0a, 0, 0, 1})
	case "lan6":
		return netip.AddrFrom16([16]byte{0xfd, 15: 1})
	case "ipv6":
		return netip.AddrFrom16([16]byte{0x20, 15: 1})
	case "ipv4":
		return netip.AddrFrom4([4]byte{0xc0, 0, 0, 9})
	default:
		panic("unknown address scope")
	}
}

func testLocalAddress(kind string) localAddress {
	return localAddress{Addr: endpointTestAddress(kind), Interface: "eth0", Flags: net.FlagUp}
}

func TestLocalAddressFiltering(t *testing.T) {
	for _, kind := range []string{"lan4", "lan6", "ipv6", "ipv4"} {
		want := kind
		if kind == "lan4" || kind == "lan6" {
			want = "lan"
		}
		if kind == "ipv4" {
			want = ""
		}
		if got := localAddressKind(testLocalAddress(kind)); got != want {
			t.Fatalf("scope %s classified as %q, want %q", kind, got, want)
		}
	}
	for _, raw := range []string{"::1", "fe80::1", "ff02::1", "::", "2001:db8::1", "3fff::1", "2002::1", "2001::1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "127.0.0.1"} {
		if got := localAddressKind(localAddress{Addr: netip.MustParseAddr(raw), Interface: "eth0", Flags: net.FlagUp}); got != "" {
			t.Fatalf("excluded address classified as %q", got)
		}
	}
	for _, name := range []string{"lo0", "docker0", "veth0", "virbr0", "br-test", "tun0", "utun0", "tap0", "tailscale0", "wg0", "dummy0", "vxlan0", "cni0", "flannel0", "podman0"} {
		a := testLocalAddress("ipv6")
		a.Interface = name
		if localAddressKind(a) != "" {
			t.Fatalf("virtual interface %q was published", name)
		}
	}
	for _, flags := range []net.Flags{0, net.FlagUp | net.FlagLoopback} {
		a := testLocalAddress("ipv6")
		a.Flags = flags
		if localAddressKind(a) != "" {
			t.Fatal("down or loopback interface was published")
		}
	}
	benchmark := localAddress{Addr: netip.AddrFrom4([4]byte{198, 18, 0, 1}), Interface: "eth0", Flags: net.FlagUp}
	if localAddressKind(benchmark) != "" {
		t.Fatal("benchmark address was published")
	}
	for _, name := range []string{"eth0", "en0", "bond0", "br0", "vlan0"} {
		if virtualInterface(name) {
			t.Fatalf("physical or potentially routed interface %q was excluded", name)
		}
	}
}

func TestEndpointSortingAndConfiguration(t *testing.T) {
	cfg := &Config{Listen: ":18660", PublicURL: "http://example.com:18660", Public: PublicConfig{
		TLSListen: ":19443", Advertise: []string{"hub.example.com", "https://hub.example.com:19443/", "https://198.51.100.1:19443"},
	}}
	now := time.Now().UTC()
	addrs := []localAddress{testLocalAddress("lan4"), testLocalAddress("ipv6"), testLocalAddress("lan6"), testLocalAddress("ipv4")}
	list := collectEndpoints(cfg, 19443, addrs, now)
	kinds := []string{}
	for _, e := range list {
		kinds = append(kinds, e.Kind)
		if !e.SeenAt.Equal(now) || e.Port == 0 || (e.Scheme != "https" && e.Scheme != "http") {
			t.Fatalf("missing candidate metadata: %+v", e)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"ipv6", "name", "name", "lan", "lan", "ipv4"}) {
		t.Fatalf("candidate ordering or deduplication: %v", kinds)
	}
	if list[0].URL != "https://"+net.JoinHostPort(endpointTestAddress("ipv6").String(), "19443") || list[1].URL != "https://hub.example.com:19443" {
		t.Fatal("IPv6 URL or configured domain was not normalized")
	}
	withoutIPv4 := *cfg
	withoutIPv4.Public = PublicConfig{TLSListen: ":19443"}
	for _, e := range collectEndpoints(&withoutIPv4, 19443, addrs, now) {
		if e.Kind == "ipv4" {
			t.Fatal("public IPv4 was automatically published")
		}
	}
	disabled := false
	cfg.Public.AdvertiseIPv6, cfg.Public.AdvertiseLAN = &disabled, &disabled
	list = collectEndpoints(cfg, 19443, addrs, now)
	if len(list) != 3 {
		t.Fatal("disabling discovery must retain explicitly configured candidates")
	}
	cfg.Public.Advertise = nil
	cfg.PublicURL = ""
	cfg.Public.AdvertiseIPv6, cfg.Public.AdvertiseLAN = nil, nil
	cfg.Public.TLSListen = "127.0.0.1:19443"
	if len(collectEndpoints(cfg, 19443, addrs, now)) != 0 {
		t.Fatal("loopback listener advertised inaccessible interface addresses")
	}
	cfg.Public.TLSListen = net.JoinHostPort(endpointTestAddress("lan4").String(), "19443")
	if list := collectEndpoints(cfg, 19443, addrs, now); len(list) != 1 || list[0].Kind != "lan" {
		t.Fatal("bound listener advertised addresses from another interface")
	}
}

func TestConfiguredEndpoints(t *testing.T) {
	for _, tc := range []struct {
		raw, want, kind, scheme string
		port                    int
	}{
		{"example.com", "https://example.com:19443", "name", "https", 19443},
		{"example.com:19555", "https://example.com:19555", "name", "https", 19555},
		{"2001:db8::1", "https://[2001:db8::1]:19443", "ipv6", "https", 19443},
		{"https://[2001:db8::1]:19555", "https://[2001:db8::1]:19555", "ipv6", "https", 19555},
		{"https://EXAMPLE.com/", "https://example.com:443", "name", "https", 443},
		{"http://192.0.2.1", "http://192.0.2.1:80", "ipv4", "http", 80},
		{"https://example.com/hub/", "https://example.com:443/hub", "name", "https", 443},
	} {
		e, ok := configuredEndpoint(tc.raw, "https", 19443, time.Now())
		if !ok || e.URL != tc.want || e.Kind != tc.kind || e.Port != tc.port || e.Scheme != tc.scheme {
			t.Fatalf("configured %q: %+v, accepted=%v", tc.raw, e, ok)
		}
	}
	for _, raw := range []string{"", "https://", "ftp://example.com", "https://example.com:0", "https://example.com:65536", "https://example.com:bad", "https://example.com:", "https://me:secret@example.com", "https://example.com?token=test", "https://example.com#test", "https://[fe80::1%25eth0]", "https://[::]", "http://127.0.0.1", "example.com/path"} {
		if _, ok := configuredEndpoint(raw, "https", 19443, time.Now()); ok {
			t.Fatalf("invalid candidate accepted: %q", raw)
		}
	}
	for _, kind := range []string{"lan4", "lan6"} {
		raw := "https://" + net.JoinHostPort(endpointTestAddress(kind).String(), "19443")
		if e, ok := configuredEndpoint(raw, "https", 19443, time.Now()); !ok || e.Kind != "lan" {
			t.Fatal("configured LAN address was not classified as LAN")
		}
	}
}

func TestEndpointEventsRequireCapability(t *testing.T) {
	h := newPublicTestHub(t)
	h.cfg.Listen, h.cfg.PublicURL = ":18660", ""
	current := []localAddress{testLocalAddress("lan4")}
	var scanErr error
	h.public.addresses = func() ([]localAddress, error) { return current, scanErr }
	capable := &client{out: make(chan []byte, 4), meta: clientMeta{features: map[string]bool{"endpoints": true}}}
	legacy := &client{out: make(chan []byte, 4)}
	other := &client{out: make(chan []byte, 4), meta: clientMeta{features: map[string]bool{"other": true}}}
	h.clients[capable], h.clients[legacy], h.clients[other] = struct{}{}, struct{}{}, struct{}{}
	now := time.Now().UTC()
	if err := h.refreshEndpoints(now); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type      string     `json:"type"`
		Endpoints []Endpoint `json:"endpoints"`
	}
	if len(capable.out) != 1 {
		t.Fatal("capable client did not receive the change")
	}
	if err := json.Unmarshal(<-capable.out, &event); err != nil || event.Type != "endpoints" || len(event.Endpoints) != 1 {
		t.Fatalf("invalid endpoints event: %v", err)
	}
	if err := h.refreshEndpoints(now.Add(time.Minute)); err != nil || len(capable.out) != 0 {
		t.Fatal("timestamp-only change sent an event")
	}
	if !h.endpointList()[0].SeenAt.Equal(now.Add(time.Minute)) {
		t.Fatal("seen_at did not reflect the latest successful scan")
	}
	snapshot := h.endpointList()
	snapshot[0].URL = "modified"
	if h.endpointList()[0].URL == "modified" {
		t.Fatal("snapshot mutates the cached endpoints")
	}
	scanErr = errors.New("scan failed")
	if err := h.refreshEndpoints(now); err == nil || len(h.endpointList()) != 1 || len(capable.out) != 0 {
		t.Fatal("failed scan discarded working candidates")
	}
	scanErr, current = nil, nil
	if err := h.refreshEndpoints(now); err != nil || len(capable.out) != 1 {
		t.Fatal("address removal did not send an update")
	}
	if err := json.Unmarshal(<-capable.out, &event); err != nil || event.Endpoints == nil || len(event.Endpoints) != 0 {
		t.Fatal("empty candidate list was not encoded as []")
	}
	if len(legacy.out) != 0 || len(other.out) != 0 {
		t.Fatal("endpoints event was sent without a declared capability")
	}
}

func TestPairCandidates(t *testing.T) {
	h := newPublicTestHub(t)
	h.public.endpoints = []Endpoint{
		{URL: "http://[2001:db8::1]:18660", Kind: "ipv6", Scheme: "http"},
		{URL: "https://[2001:db8::1]:19443", Kind: "ipv6", Scheme: "https"},
		{URL: "https://example.com:19443", Kind: "name", Scheme: "https"},
		{URL: "https://192.0.2.1:19443", Kind: "lan", Scheme: "https"},
		{URL: "https://198.51.100.1:19443", Kind: "ipv4", Scheme: "https"},
	}
	if got := h.pairCandidates(); !reflect.DeepEqual(got, []string{"https://[2001:db8::1]:19443", "https://example.com:19443"}) {
		t.Fatalf("pair candidates = %v", got)
	}
	h.public.endpoints = []Endpoint{{URL: "https://192.0.2.1:19443", Kind: "lan", Scheme: "https"}}
	if len(h.pairCandidates()) != 0 {
		t.Fatal("pair candidates included a LAN address")
	}
}

func TestEndpointsAuthenticatedRoute(t *testing.T) {
	h := newPublicTestHub(t)
	h.public.port, h.public.fp = 19443, "test-pin"
	h.public.endpoints = []Endpoint{{URL: "https://example.com:19443", Kind: "name", Port: 19443, Scheme: "https", SeenAt: time.Now().UTC()}}
	for _, token := range []string{"", "wrong", "test-token"} {
		req := httptest.NewRequest("GET", "/api/v1/endpoints", nil)
		req.RemoteAddr = "127.0.0.1:1234" // from the LAN: plaintext from the internet stops at the gate first
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.routes().ServeHTTP(rec, req)
		if token != "test-token" {
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("endpoint discovery without authentication: %d", rec.Code)
			}
			continue
		}
		var response struct {
			HubID     string     `json:"hub_id"`
			Port      int        `json:"tls_port"`
			Pin       string     `json:"fp"`
			Endpoints []Endpoint `json:"endpoints"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || response.HubID != h.hubID() || response.Port != 19443 || response.Pin != "test-pin" || !reflect.DeepEqual(response.Endpoints, h.endpointList()) {
			t.Fatalf("endpoints response: %+v, %v", response, err)
		}
	}
}

// TestLANEndpointsReachAnAuthenticatedClient: public.advertise_lan says whether LAN addresses are published to the
// world, not whether one of the hub's own paired clients may hear about the network it is already on (#2170). A phone
// alone at home pairing through a public URL otherwise learns only that URL and keeps going out and back in.
func TestLANEndpointsReachAnAuthenticatedClient(t *testing.T) {
	off := false
	cfg := &Config{Listen: ":18660", PublicURL: "https://hub.example.com"}
	cfg.Public.AdvertiseLAN = &off
	addrs := []localAddress{testLocalAddress("lan4"), testLocalAddress("ipv6")}
	now := time.Now().UTC()

	public := collectEndpointsFor(cfg, 0, addrs, now, scopePublic)
	for _, e := range public {
		if e.Kind == "lan" {
			t.Fatalf("a public answer listed a LAN address with advertise_lan off: %v", e.URL)
		}
	}
	client := collectEndpointsFor(cfg, 0, addrs, now, scopeAuthenticated)
	var lan, publicURL bool
	for _, e := range client {
		switch e.Kind {
		case "lan":
			lan = true
		case "name":
			publicURL = true
		}
	}
	if !lan {
		t.Fatal("an authenticated client was not told about the LAN address")
	}
	if !publicURL {
		t.Fatal("making LAN addresses visible dropped the public one")
	}
	// advertise_lan still removes them from the public list; the authenticated one is unaffected by it.
	if got := collectEndpointsFor(cfg, 0, nil, now, scopeAuthenticated); len(got) != 1 || got[0].Kind != "name" {
		t.Fatalf("authenticated endpoints without local addresses = %v", got)
	}
}

// TestEndpointsRouteListsLANForAClient: the same over HTTP, through the real route, so a change to the mounted handler
// cannot quietly stop using the authenticated list.
func TestEndpointsRouteListsLANForAClient(t *testing.T) {
	h := newPublicTestHub(t)
	off := false
	h.cfg.Public.AdvertiseLAN = &off
	h.cfg.Listen = ":18660"
	h.public.addresses = func() ([]localAddress, error) {
		return []localAddress{testLocalAddress("lan4")}, nil
	}
	if err := h.refreshEndpoints(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/endpoints", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, req)
	var response struct {
		Endpoints []Endpoint `json:"endpoints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("endpoints: %d %v", rec.Code, err)
	}
	if !slices.ContainsFunc(response.Endpoints, func(e Endpoint) bool { return e.Kind == "lan" }) {
		t.Fatalf("authenticated endpoints answer has no LAN candidate: %+v", response.Endpoints)
	}
	// The pairing QR still does not carry one.
	lanURL := endpointTestAddress("lan4").String()
	if slices.ContainsFunc(h.pairCandidates(), func(u string) bool { return strings.Contains(u, lanURL) }) {
		t.Fatal("pair candidates included a LAN address")
	}
}

func TestPublicConfigurationDefaults(t *testing.T) {
	for _, body := range []string{`{}`, `{"public":{}}`, `{"public":{"advertise_ipv6":true,"advertise_lan":true}}`} {
		var cfg Config
		if err := json.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.Listen = ":18660"
		if cfg.Public.TLSListen != "" {
			t.Fatal("old configuration enables TLS")
		}
		list := collectEndpoints(&cfg, 0, []localAddress{testLocalAddress("lan4"), testLocalAddress("ipv6")}, time.Now())
		if len(list) != 2 || !slices.ContainsFunc(list, func(e Endpoint) bool { return e.Scheme == "http" && e.Port == 18660 }) {
			t.Fatal("address discovery is not enabled by default")
		}
	}
	var cfg Config
	if err := json.Unmarshal([]byte(`{"public":{"tls_listen":":19443","advertise":["example.com"],"advertise_ipv6":false,"advertise_lan":false}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	list := collectEndpoints(&cfg, 19443, []localAddress{testLocalAddress("lan4"), testLocalAddress("ipv6")}, time.Now())
	if len(list) != 1 || list[0].URL != "https://example.com:"+strconv.Itoa(19443) {
		t.Fatal("public JSON configuration was not applied")
	}
}

func TestPublishableDropsUnstableIPv6(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast
	stable := netaddr.Addr{IP: netip.MustParseAddr("2400::1"), Interface: "eth0", InterfaceFlags: up, FlagsKnown: true, ScopeKnown: true,
		Flags: netaddr.FlagManageTempAddr, Class: netaddr.GlobalIPv6}
	temporary := stable
	temporary.IP, temporary.Flags = netip.MustParseAddr("2400::2"), netaddr.FlagTemporary
	deprecated := stable
	deprecated.IP, deprecated.Flags = netip.MustParseAddr("2400::3"), netaddr.FlagDeprecated
	unknown := stable // not Linux: flags unknown, published as before
	unknown.IP, unknown.FlagsKnown = netip.MustParseAddr("2400::4"), false
	virtual := stable
	virtual.IP, virtual.Virtual = netip.MustParseAddr("2400::5"), true
	lan := netaddr.Addr{IP: netip.AddrFrom4([4]byte{10, 0, 0, 2}), Interface: "eth0", InterfaceFlags: up, FlagsKnown: true}
	var got []string
	for _, a := range publishable([]netaddr.Addr{stable, temporary, deprecated, unknown, virtual, lan}) {
		got = append(got, a.Addr.String())
	}
	if want := []string{"2400::1", "2400::4", lan.IP.String()}; !slices.Equal(got, want) {
		t.Fatalf("publishable = %v, want %v", got, want)
	}
}
