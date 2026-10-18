package main

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// RelayCredential is profile-owned and value-only, so Settings.Snapshot cannot
// alias its private data. Never use it as an HTTP response or log argument.
// Zero value keeps old Relay profiles loadable, but never makes them usable.
type RelayCredential struct {
	Mode          string `json:"mode"`
	PrivateKey    string `json:"private_key"`
	PeerPublicKey string `json:"peer_public_key"`
	TunnelIPv4    string `json:"tunnel_ipv4"`
	TunnelIPv6    string `json:"tunnel_ipv6,omitempty"`
	Reserved      string `json:"reserved,omitempty"`
	DNS           string `json:"dns,omitempty"`
	MTU           int    `json:"mtu,omitempty"`
}

func validRelayKey(s string) ([]byte, bool) {
	b, e := base64.StdEncoding.Strict().DecodeString(s)
	return b, e == nil && len(s) == 44 && len(b) == 32 && base64.StdEncoding.EncodeToString(b) == s && !bytes.Equal(b, make([]byte, 32))
}
func validTunnelAddress(s string, v4 bool) bool {
	a, e := netip.ParseAddr(s)
	if e != nil {
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Bits() != p.Addr().BitLen() {
			return false
		}
		a = p.Addr()
	}
	return a.Is4() == v4 && !a.Is4In6() && a.Zone() == "" && a.IsGlobalUnicast()
}
func validateRelayCredential(c RelayCredential) error {
	invalid := func() error { return fmt.Errorf("invalid Relay tunnel credential") }
	if c == (RelayCredential{}) {
		return nil
	}
	if c.Mode != "manual" && c.Mode != "generated" {
		return invalid()
	}
	priv, ok := validRelayKey(c.PrivateKey)
	if !ok {
		return invalid()
	}
	peer, ok := validRelayKey(c.PeerPublicKey)
	if !ok {
		return invalid()
	}
	pk, e := ecdh.X25519().NewPrivateKey(priv)
	if e != nil {
		return invalid()
	}
	pub, e := ecdh.X25519().NewPublicKey(peer)
	if e != nil {
		return invalid()
	}
	if _, e = pk.ECDH(pub); e != nil {
		return invalid()
	}
	if !validTunnelAddress(c.TunnelIPv4, true) || c.TunnelIPv6 != "" && !validTunnelAddress(c.TunnelIPv6, false) {
		return invalid()
	}
	if c.Reserved != "" {
		b, e := base64.StdEncoding.Strict().DecodeString(c.Reserved)
		if e != nil || len(b) != 3 || base64.StdEncoding.EncodeToString(b) != c.Reserved {
			return invalid()
		}
	}
	if c.DNS != "" {
		ips := strings.Split(c.DNS, ",")
		if len(ips) > 3 {
			return invalid()
		}
		for _, s := range ips {
			a, e := netip.ParseAddr(s)
			if e != nil || a.Zone() != "" || !a.IsGlobalUnicast() || a.Is4In6() {
				return invalid()
			}
		}
	}
	if c.MTU != 0 && (c.MTU < 1280 || c.MTU > 1500) {
		return invalid()
	}
	return nil
}
func relayCredentialConfigured(p *ProxySettings) bool {
	return p != nil && p.Credential.Mode != "" && validateRelayCredential(p.Credential) == nil
}
func (srv *Server) registerRelayAccountAPI(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/profiles/{id}/account/generate", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var b struct {
			Confirmed bool   `json:"confirmed"`
			Revision  uint64 `json:"revision"`
		}
		if !decodeProfilePatch(w, r, &b) {
			return
		}
		if !b.Confirmed || b.Revision == 0 {
			writeJSON(w, 400, map[string]string{"error": "explicit confirmation and current revision required"})
			return
		}
		srv.orch.applyMu.Lock()
		defer srv.orch.applyMu.Unlock()
		queued, err := srv.startRelayAccount(r.PathValue("id"), b.Revision)
		if err != nil {
			code := 409
			if p, ok := err.(profileAPIError); ok {
				code = p.code
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 202, map[string]any{"queued": queued, "state": "pending", "runtime_available": false})
	})
	mux.HandleFunc("GET /api/profiles/{id}/account", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		id := r.PathValue("id")
		p := srv.cfg.Snapshot().Proxies[id]
		if p == nil {
			writeJSON(w, 404, map[string]string{"error": "profile not found"})
			return
		}
		if profileType(id, p) != "relay-warp" {
			writeJSON(w, 409, map[string]string{"error": "credentials require Relay-WARP"})
			return
		}
		writeJSON(w, 200, srv.accounts.status(id, p))
	})
	mux.HandleFunc("DELETE /api/profiles/{id}/account/generate", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var b struct{}
		if !decodeProfilePatch(w, r, &b) {
			return
		}
		srv.orch.applyMu.Lock()
		defer srv.orch.applyMu.Unlock()
		id := r.PathValue("id")
		p := srv.cfg.Snapshot().Proxies[id]
		if p == nil {
			writeJSON(w, 404, map[string]string{"error": "profile not found"})
			return
		}
		if profileType(id, p) != "relay-warp" {
			writeJSON(w, 409, map[string]string{"error": "credentials require Relay-WARP"})
			return
		}
		srv.accounts.cancel(id)
		writeJSON(w, 200, srv.accounts.status(id, p))
	})
	mux.HandleFunc("PUT /api/profiles/{id}/account", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var c RelayCredential
		if !decodeProfilePatch(w, r, &c) {
			return
		}
		if c.Mode != "manual" || validateRelayCredential(c) != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid Relay tunnel credential"})
			return
		}
		srv.orch.applyMu.Lock()
		defer srv.orch.applyMu.Unlock()
		id := r.PathValue("id")
		err := srv.cfg.ChangeProfiles(func(g *Settings) error {
			p := g.Proxies[id]
			if p == nil {
				return apiProblem(404, "profile not found")
			}
			if profileType(id, p) != "relay-warp" {
				return apiProblem(409, "credentials require Relay-WARP")
			}
			p.Credential = c
			return nil
		})
		if err != nil {
			code := 500
			if e, ok := err.(profileAPIError); ok {
				code = e.code
			}
			writeJSON(w, code, map[string]any{"error": "Relay credential save failed", "persisted": false, "applied": false})
			return
		}
		// Persisted identity is new. Revoke existing authorizations immediately;
		// do not activate, scan, register, or apply a tunnel as a side effect of save.
		srv.orch.closeProfileGates()
		mihomo.ReconcileRelayIntent(srv.cfg.Snapshot())
		getCheck(id).clearGeo()
		srv.accounts.cancel(id)
		writeJSON(w, 200, map[string]any{"persisted": true, "applied": false, "runtime_available": false})
	})
}
