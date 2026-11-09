package hub

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/hubedge"
	"github.com/nwverse/persona/server/internal/netaddr"
)

type publicState struct {
	ddns        *ddnsUpdater
	mu          sync.RWMutex
	refreshMu   sync.Mutex
	id          string
	port        int
	fp          string
	certificate *tls.Certificate
	server      *http.Server
	listener    net.Listener
	proxy       *proxyPolicy // security.proxy_protocol, read by startPublic
	proxyLog    proxyLog
	addresses   addressSource
	endpoints   []Endpoint
	// endpointsForClients: the same, plus LAN addresses whatever public.advertise_lan says, for the authenticated
	// /api/v1/endpoints answer (endpoints.go, endpointScope).
	endpointsForClients []Endpoint
}

func (h *Hub) initHubID(ctx context.Context) error {
	h.public.mu.Lock()
	defer h.public.mu.Unlock()
	if h.public.id != "" {
		return nil
	}
	var id string
	if h.store != nil {
		if _, err := h.store.Setting(ctx, "hub_id", &id); err != nil {
			return fmt.Errorf("load hub id: %w", err)
		}
	}
	if id == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
		id = fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		if h.store != nil {
			if err := h.store.PutSetting(ctx, "hub_id", id); err != nil {
				return fmt.Errorf("save hub id: %w", err)
			}
		}
	}
	h.public.id = id
	return nil
}

// startPublic prepares both transports before either one accepts requests.
func (h *Hub) startPublic(ctx context.Context) error {
	if err := h.initHubID(ctx); err != nil {
		return err
	}
	proxy, err := h.cfg.Security.ProxyProtocol.policy()
	if err != nil {
		return err
	}
	h.public.proxy = proxy
	if proxy != nil {
		h.logf("hub: PROXY protocol %s", proxy)
		if proxy.tls && !proxy.http && h.cfg.Public.TLSListen == "" {
			h.logf("hub: security.proxy_protocol is for the TLS listener, and public.tls_listen is not set: no header is read")
		}
	}
	var ln net.Listener
	var activated bool
	if h.cfg.Public.TLSListen != "" {
		if err := h.refreshTLS(time.Now()); err != nil {
			return err
		}
		ln, activated, err = h.listen(h.cfg.Public.TLSListen)
		if err != nil && (tlsAddressGone(h.cfg.Public.TLSListen, err) || errors.Is(err, hubedge.ErrNoSocket)) {
			// A new install binds TLS to the LAN address only (docs/assistant/onboarding.md 4.1), and DHCP may have moved it:
			// start without TLS rather than not at all; the plaintext port still works on the LAN. Behind the edge
			// the edge found it gone (hub_edge.go Serve) and has no socket for that port.
			h.logf("hub TLS: %s is not an address of this machine now (has the LAN address changed?): TLS is off; "+
				"set public.tls_listen to this machine's LAN address, or \":%s\" (every address), and restart",
				h.cfg.Public.TLSListen, portOf(h.cfg.Public.TLSListen))
			ln, err = nil, nil
		}
		if err != nil {
			return fmt.Errorf("listen TLS: %w", err)
		}
	}
	if h.cfg.Public.TLSListen != "" && ln != nil {
		if activated {
			h.logf("hub TLS: listening on the socket systemd passed (%s)", ln.Addr())
			// personad.socket binds with FreeBind, so a LAN address DHCP has moved still "listens", reaching nobody.
			if host, ok := addressNotLocal(h.cfg.Public.TLSListen, net.InterfaceAddrs); ok {
				h.logf("hub TLS: %s is not an address of this machine now (has the LAN address changed?): TLS cannot be "+
					"reached; set public.tls_listen to this machine's LAN address, or \":%s\" (every address), and rerun "+
					"personad hub setup", host, portOf(h.cfg.Public.TLSListen))
			}
		}
		// The PROXY header comes before the TLS handshake: the TLS server reads through it.
		ln = h.proxyListener(ln, func(p *proxyPolicy) bool { return p.tls })
		h.public.listener = ln
		h.public.server = newHubHTTPServer(h.cfg.Public.TLSListen, nil)
		h.public.server.ErrorLog = log.New(&handshakeNoise{logf: h.logf}, "", 0)
		h.public.server.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				h.public.mu.RLock()
				defer h.public.mu.RUnlock()
				return h.public.certificate, nil
			},
		}
		h.public.mu.Lock()
		h.public.port = ln.Addr().(*net.TCPAddr).Port
		h.public.mu.Unlock()
		port, fp := h.tlsPin()
		h.logf("hub TLS listening on port %d, SPKI SHA-256 %s", port, fp)
	}
	port, _ := h.tlsPin()
	h.public.mu.Lock()
	h.public.endpoints = collectEndpoints(h.cfg, port, nil, time.Now())
	h.public.mu.Unlock()
	h.startDDNS(ctx)
	if err := h.refreshEndpoints(time.Now()); err != nil {
		h.logf("discover public endpoints: %v", err)
	}
	go h.publicLoop(ctx)
	return nil
}

func (h *Hub) refreshTLS(now time.Time) error {
	cfg := h.cfg.Public
	if cfg.TLSListen == "" {
		return nil
	}
	var cert tls.Certificate
	var fp string
	var err error
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return fmt.Errorf("public.cert_file and public.key_file must be set together")
		}
		cert, fp, err = loadTLSFiles(cfg.CertFile, cfg.KeyFile)
	} else {
		if h.cfg.dir == "" {
			return fmt.Errorf("hub data directory is required for managed TLS")
		}
		cert, fp, err = loadOrCreateTLS(filepath.Join(h.cfg.dir, "tls"), h.tlsNames(), now)
	}
	if err != nil {
		return fmt.Errorf("load hub TLS identity: %w", err)
	}
	h.public.mu.Lock()
	defer h.public.mu.Unlock()
	// A replacement key requires a restart and pairing again, never an unnoticed live rotation.
	if h.public.fp != "" && h.public.fp != fp {
		return fmt.Errorf("TLS identity key changed; restart the hub and pair devices again")
	}
	h.public.certificate, h.public.fp = &cert, fp
	return nil
}

func (h *Hub) publicLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	// Address changes (a new IPv6 prefix after the line redials) go out at once rather than at the next tick.
	var changed <-chan struct{}
	if h.public.addresses == nil {
		changed = netaddr.Watch(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
			if err := h.refreshEndpoints(time.Now()); err != nil {
				h.logf("refresh public endpoints: %v", err)
			}
		case now := <-ticker.C:
			if err := h.refreshTLS(now); err != nil {
				h.logf("refresh public TLS: %v", err)
			}
			if err := h.refreshEndpoints(now); err != nil {
				h.logf("refresh public endpoints: %v", err)
			}
		}
	}
}

func newHubHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

func (h *Hub) serveHTTP(ctx context.Context) error {
	// Keep request contexts alive until a close frame has been sent. Cancelling a
	// websocket Read context first closes the transport without a close handshake.
	requestCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	if h.shutdown == nil {
		h.shutdown = newShutdownBudget(h.cfg)
		defer h.shutdown.close()
	}
	var servers []*http.Server
	var listeners []net.Listener
	if h.public.listener != nil {
		listeners = append(listeners, h.public.listener)
	}
	stopSessions, stopTests := func() {}, func() {}
	defer func() {
		h.shutdown.start()
		// Close both listeners before draining any clients, including pending upgrades.
		for _, ln := range listeners {
			_ = ln.Close()
		}
		h.shutdown.step(h, "websockets", h.stopWebSockets)
		cancel()
		h.shutdown.step(h, "HTTP handlers", func(sctx context.Context) error {
			var wg sync.WaitGroup
			for _, srv := range servers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := srv.Shutdown(sctx); err != nil {
						h.logf("shutdown HTTP server: %v", err)
						_ = srv.Close()
					}
				}()
			}
			wg.Wait()
			return sctx.Err()
		})
		h.shutdown.step(h, "device sessions", func(context.Context) error { stopSessions(); return nil })
		h.shutdown.step(h, "lock tests", func(context.Context) error { stopTests(); return nil })
	}()
	var err error
	stopSessions, err = h.startDeviceSessions(ctx)
	if err != nil {
		stopSessions = func() {}
		return err
	}
	stopTests, err = h.startLockTests(ctx)
	if err != nil {
		stopTests = func() {}
		return err
	}
	handler := h.routes()
	plain := newHubHTTPServer(h.cfg.Listen, handler)
	plain.BaseContext = func(net.Listener) context.Context { return requestCtx }
	errs := make(chan error, 2)
	if srv := h.public.server; srv != nil {
		srv.Handler = handler
		srv.BaseContext = plain.BaseContext
		servers = append(servers, srv)
		go func() { errs <- srv.ServeTLS(h.public.listener, "", "") }()
	}
	ln, activated, err := h.listen(plain.Addr)
	if err != nil {
		return err
	}
	if activated {
		h.logf("hub: listening on the socket systemd passed (%s): restarts refuse no connection", ln.Addr())
	}
	ln = h.proxyListener(ln, func(p *proxyPolicy) bool { return p.http })
	servers = append(servers, plain)
	listeners = append(listeners, ln)
	go func() { errs <- plain.Serve(ln) }()
	h.logf("personad hub %s listening on %s (%s)", h.version, h.cfg.Listen, h.cfg.PublicURL)
	if h.slot != nil {
		h.logf("hub: slot %s, relayed by the hub's edge: the route moves here now", h.slot.Name)
		h.slot.Ready()
	} else {
		logUnclaimedInherited(h.logf) // behind the edge, the edge took the passed sockets (hub_edge.go Serve)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (h *Hub) publicRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/endpoints", h.serveEndpoints)
}

// hubID also works for hubs built without a database in unit tests.
func (h *Hub) hubID() string {
	if h.store == nil {
		_ = h.initHubID(context.Background())
	}
	h.public.mu.RLock()
	defer h.public.mu.RUnlock()
	return h.public.id
}

// tlsPin uses base64url without padding of SHA-256 over SubjectPublicKeyInfo DER.
func (h *Hub) tlsPin() (port int, fp string) {
	h.public.mu.RLock()
	defer h.public.mu.RUnlock()
	if h.public.port == 0 {
		return 0, ""
	}
	return h.public.port, h.public.fp
}

// pairCandidates keeps the QR small and never includes plaintext or LAN addresses.
func (h *Hub) pairCandidates() []string {
	var urls []string
	for _, endpoint := range h.endpointList() {
		if endpoint.Scheme == "https" && endpoint.Kind != "lan" {
			urls = append(urls, endpoint.URL)
			if len(urls) == 2 {
				break
			}
		}
	}
	return urls
}

// handshakeNoise keeps the TLS server's error log readable on a port the internet can reach: scanners and clients
// with a stale pin fail handshakes all day, one line each. Those lines are summed up at most every 10 minutes;
// anything else the server logs goes through unchanged.
type handshakeNoise struct {
	logf  func(string, ...any)
	mu    sync.Mutex
	count int
	since time.Time
	now   func() time.Time // tests
}

const handshakeSummaryEvery = 10 * time.Minute

func (n *handshakeNoise) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if !strings.Contains(line, "TLS handshake error") {
		n.logf("tls: %s", line)
		return len(p), nil
	}
	now := time.Now()
	if n.now != nil {
		now = n.now()
	}
	n.mu.Lock()
	if n.since.IsZero() {
		n.since = now
	}
	n.count++
	var count int
	var since time.Time
	if now.Sub(n.since) >= handshakeSummaryEvery {
		count, since = n.count, n.since
		n.count, n.since = 0, now
	}
	n.mu.Unlock()
	if count > 0 {
		n.logf("tls: %d failed handshakes since %s (scanners, or a client with an old pin)", count, since.Format(time.TimeOnly))
	}
	return len(p), nil
}

// tlsAddressGone: listening on a given host failed because the machine no longer has that address (EADDRNOTAVAIL).
func tlsAddressGone(addr string, err error) bool {
	host, _, splitErr := net.SplitHostPort(addr)
	return splitErr == nil && host != "" && errors.Is(err, syscall.EADDRNOTAVAIL)
}

func portOf(addr string) string {
	_, port, _ := net.SplitHostPort(addr)
	return port
}

// addressNotLocal: addr names a host (not every address) that none of the machine's interfaces has now.
func addressNotLocal(addr string, interfaceAddrs func() ([]net.Addr, error)) (string, bool) {
	host, _, err := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		return "", false
	}
	addrs, err := interfaceAddrs()
	if err != nil {
		return "", false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return "", false
		}
	}
	return host, true
}
