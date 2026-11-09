package hub

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
)

// The network and access page: how the hub is reached (public URL, TLS, candidate addresses: written to hub.json,
// used after a restart), the security settings (read only here) and the admin UI's own scope (narrowed here, widened
// only on the hub's machine).

func (h *Hub) adminNetworkView(r *http.Request) map[string]any {
	scope := h.adminScope()
	h.adminInit()
	h.admin.mu.Lock()
	file := h.admin.file
	h.admin.mu.Unlock()
	cidrs := make([]string, 0, len(scope.cidrs))
	for _, p := range scope.cidrs {
		cidrs = append(cidrs, p.String())
	}
	trusted := h.cfg.Security.TrustedCIDRs
	if trusted == nil {
		trusted = []string{}
	}
	disk, _ := h.diskConfig()
	public := h.cfg.Public
	publicURL := h.cfg.PublicURL
	if disk != nil { // what the page edits: the file, which may be ahead of the running hub
		public, publicURL = disk.Public, disk.PublicURL
	}
	advertise := public.Advertise
	if advertise == nil {
		advertise = []string{}
	}
	tls := map[string]any{"listen": public.TLSListen, "port": 0, "fp": ""}
	if port, fp := h.tlsPin(); port > 0 {
		tls["port"], tls["fp"] = port, fp
	}
	allowed := map[string]bool{}
	for _, access := range []string{accessLoopback, accessLAN, accessCIDRs, accessAny} {
		s := adminScope{access: access, cidrs: scope.cidrs, trusted: scope.trusted}
		allowed[access] = h.adminWouldAllow(r, s)
	}
	return map[string]any{
		"listen": h.cfg.Listen, "public_url": publicURL, "public_url_from_env": identity.Getenv("PERSONA_PUBLIC_URL") != "",
		"tls": tls, "advertise": advertise,
		"advertise_ipv6": public.AdvertiseIPv6 == nil || *public.AdvertiseIPv6,
		"advertise_lan":  public.AdvertiseLAN == nil || *public.AdvertiseLAN,
		"endpoints":      h.endpointList(),
		"security": map[string]any{
			"trusted_cidrs": trusted, "allow_public_plaintext": h.cfg.Security.AllowPublicPlaintext,
		},
		"admin": map[string]any{
			"access": scope.access, "cidrs": cidrs, "session_hours": file.sessionLifetime(), "from_env": scope.fromEnv,
			"container": h.adminContainer(),
		},
		"you":     map[string]any{"address": remoteIP(r).String(), "tls": r.TLS != nil, "allowed": allowed},
		"restart": h.pendingRestart(h.requestLang(r)),
	}
}

func (h *Hub) adminNetworkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/network", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.adminNetworkView(r))
	})
	mux.HandleFunc("PUT /api/v1/admin/network/access", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Access string   `json:"access"`
			CIDRs  []string `json:"cidrs"`
			Force  bool     `json:"force"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		current := h.adminScope()
		if current.fromEnv {
			writeErrCode(w, http.StatusConflict, "from_env",
				errors.New("the scope comes from PERSONA_ADMIN_ACCESS: change it where the hub is deployed"))
			return
		}
		cfg := AdminConfig{Access: strings.TrimSpace(in.Access)}
		if cfg.Access == accessCIDRs {
			cfg.CIDRs = in.CIDRs
		}
		next, err := scopeFrom(cfg)
		if err != nil || cfg.Access == "" {
			writeErrCode(w, http.StatusBadRequest, "invalid_access", errors.New("access: loopback, lan, cidrs or any"))
			return
		}
		if cfg.Access == accessCIDRs {
			if _, err := parseCIDRs(in.CIDRs); err != nil {
				writeErrCode(w, http.StatusBadRequest, "invalid_cidr", err)
				return
			}
			if len(next.cidrs) == 0 {
				writeErrCode(w, http.StatusBadRequest, "invalid_cidr", errors.New("cidrs needs at least one network"))
				return
			}
			cfg.CIDRs = nil
			for _, p := range next.cidrs {
				cfg.CIDRs = append(cfg.CIDRs, p.String())
			}
		}
		next.trusted = current.trusted
		if !narrowerAccess(current, next) {
			writeErrCode(w, http.StatusForbidden, "widen_cli_only",
				errors.New("the admin UI only narrows its scope: widen it on the hub's machine (personad hub admin access)"))
			return
		}
		if !in.Force && !h.adminWouldAllow(r, next) {
			writeErrCode(w, http.StatusConflict, "would_lock_you_out", errors.New("this browser would be outside the new scope"))
			return
		}
		err = editHubJSON(h.cfg.dir, func(o *jsonObject) error {
			admin, err := o.Object("admin")
			if err != nil {
				return err
			}
			if err := admin.Set("access", cfg.Access); err != nil {
				return err
			}
			if cfg.Access == accessCIDRs {
				if err := admin.Set("cidrs", cfg.CIDRs); err != nil {
					return err
				}
			}
			o.SetObject("admin", admin)
			return nil
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.admin.mu.Lock()
		h.admin.file.Access = cfg.Access
		if cfg.Access == accessCIDRs {
			h.admin.file.CIDRs = cfg.CIDRs
		}
		h.admin.mu.Unlock()
		h.logf("admin: access narrowed to %s from the admin UI", cfg.Access)
		writeJSON(w, h.adminNetworkView(r))
	})
	mux.HandleFunc("PUT /api/v1/admin/network/public", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PublicURL *string   `json:"public_url"`
			TLSListen *string   `json:"tls_listen"`
			Advertise *[]string `json:"advertise"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		// PERSONA_PUBLIC_URL (Docker's .env) wins over the file and is never written into it: the page shows its value,
		// sends it back unchanged with the other fields, and a change of it belongs in the deployment.
		if env := identity.Getenv("PERSONA_PUBLIC_URL"); env != "" && in.PublicURL != nil {
			if strings.TrimSpace(*in.PublicURL) != env {
				writeErrCode(w, http.StatusConflict, "from_env", errors.New("public_url comes from PERSONA_PUBLIC_URL: change it where the hub is deployed"))
				return
			}
			in.PublicURL = nil
		}
		if in.PublicURL != nil {
			v := strings.TrimSpace(*in.PublicURL)
			if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				writeErrCode(w, http.StatusBadRequest, "invalid_public_url", i18n.Err("hub-admin_network.invalid_public_url"))
				return
			}
			*in.PublicURL = v
		}
		if in.TLSListen != nil {
			v := strings.TrimSpace(*in.TLSListen)
			if v != "" && !validListen(v) {
				writeErrCode(w, http.StatusBadRequest, "invalid_tls_listen", i18n.Err("hub-admin_network.invalid_tls_listen"))
				return
			}
			if v != "" && sameListenPort(v, h.cfg.Listen) {
				writeErrCode(w, http.StatusBadRequest, "invalid_tls_listen", i18n.Err("hub-admin_network.tls_listen_same_port"))
				return
			}
			*in.TLSListen = v
		}
		var advertise []string
		if in.Advertise != nil {
			for _, a := range *in.Advertise {
				if a = strings.TrimSpace(a); a == "" {
					continue
				}
				if strings.ContainsAny(a, " \t\n") || len(a) > 300 {
					writeErrCode(w, http.StatusBadRequest, "invalid_advertise", i18n.Err("hub-admin_network.invalid_advertise"))
					return
				}
				advertise = append(advertise, a)
			}
		}
		err := editHubJSON(h.cfg.dir, func(o *jsonObject) error {
			if in.PublicURL != nil {
				if err := o.Set("public_url", *in.PublicURL); err != nil {
					return err
				}
			}
			if in.TLSListen == nil && in.Advertise == nil {
				return nil
			}
			public, err := o.Object("public")
			if err != nil {
				return err
			}
			if in.TLSListen != nil {
				if *in.TLSListen == "" {
					public.Del("tls_listen")
				} else if err := public.Set("tls_listen", *in.TLSListen); err != nil {
					return err
				}
			}
			if in.Advertise != nil {
				if len(advertise) == 0 {
					public.Del("advertise")
				} else if err := public.Set("advertise", advertise); err != nil {
					return err
				}
			}
			o.SetObject("public", public)
			return nil
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.logf("admin: network settings written to hub.json (used after a restart)")
		writeJSON(w, h.adminNetworkView(r))
	})
}

func validListen(s string) bool {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return false
	}
	if host == "" {
		return true
	}
	_, err = netip.ParseAddr(host)
	return err == nil
}

func sameListenPort(a, b string) bool {
	_, pa, errA := net.SplitHostPort(a)
	_, pb, errB := net.SplitHostPort(b)
	return errA == nil && errB == nil && pa == pb
}
