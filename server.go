package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is the minimal JSON API + static UI of warpd.
// Endpoints (all under /api):
//   GET  /api/status         -> { proxies: {...}, scan: {...}, config: {...} }
//   POST /api/scan           { for: "warp"|"masque"|"both", reason? }
//   POST /api/endpoint       { proxy: "warp", endpoint: "ip:port" }
//                              { proxy: "m2w", outer, inner }
//   POST /api/endpoint/clear { proxy: "warp" }   -> back to auto
//   POST /api/restart        { proxy }
//   POST /api/register       { fresh: bool }
//   POST /api/config         { key: value, ... }   -> persisted to UCI
//   GET  /api/log            -> { lines: [...] }
//
// auth: if warpd.global.web_secret is set, require "Authorization: Bearer <secret>".
type Server struct {
	orch *Orchestrator
	cfg  *Settings
	mu   sync.Mutex
}

func NewServer(cfg *Settings, orch *Orchestrator) *Server {
	return &Server{orch: orch, cfg: cfg}
}

func (srv *Server) authorized(r *http.Request) bool {
	s := srv.cfg.Snapshot()
	if s.WebSecret == "" {
		return true
	}
	h := r.Header.Get("Authorization")
	return h == "Bearer "+s.WebSecret
}

// refreshProxyPin reloads the endpoint pins of one proxy from UCI into the
// live settings, so /api/status reflects a pin set through /api/endpoint.
func (srv *Server) refreshProxyPin(name string) {
	fresh := LoadSettings().Proxies[name]
	srv.cfg.mu.Lock()
	if p, ok := srv.cfg.Proxies[name]; ok {
		p.Endpoint = fresh.Endpoint
		p.Outer = fresh.Outer
		p.Inner = fresh.Inner
	}
	srv.cfg.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (srv *Server) status() map[string]any {
	g := srv.cfg.Snapshot()
	e := stateGetEndpoints()
	proxies := map[string]ProxyState{}
	for _, name := range proxyNames {
		p := g.Proxies[name]
		st := getCheck(name).snapshot()
		st.Name = name
		st.Enabled = p.Enabled
		st.Listen = p.Listen
		st.Port = p.Port
		st.Pid = mihomo.pid
		st.Running = mihomo.Running()
		st.Ready = mihomo.Running() && p.Enabled && st.FailStreak == 0 && st.Checks > 0
		st.Endpoint, st.Outer = liveEndpoint(name, p, e)
		if name == "m2w" {
			st.ManualEP = p.Outer != "" || p.Inner != ""
		} else {
			st.ManualEP = p.Endpoint != ""
		}
		proxies[name] = st
	}
	cfg := map[string]any{
		"check_period":    g.CheckPeriod,
		"scan_sample":     g.ScanSample,
		"scan_jobs":       g.ScanJobs,
		"autoscan_start":  g.AutoscanStart,
		"fail_restart":    g.FailRestart,
		"fail_rescan":     g.FailRescan,
		"dme_rescan_gap":  g.DMERescanGap,
		"max_ping":        g.MaxPing,
		"exclude_nodes":     g.ExcludeNodes,
		"exclude_countries": g.ExcludeCountries,
		"web_secret_set":  g.WebSecret != "",
	}
	return map[string]any{
		"proxies": proxies,
		"scan":    srv.orch.State(),
		"config":  cfg,
		"endpoints": map[string]any{
			"warp":     e.warp,
			"masque":   e.masque["masque"],
			"masqueh2": e.masque["masqueh2"],
		},
		"mihomo": map[string]any{
			"running": mihomo.Running(),
			"pid":     mihomo.pid,
		},
		"time": time.Now().Format(time.RFC3339),
	}
}

func (srv *Server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		if !srv.authorized(r) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		writeJSON(w, 200, srv.status())
	})

	mux.HandleFunc("GET /api/log", func(w http.ResponseWriter, r *http.Request) {
		if !srv.authorized(r) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		writeJSON(w, 200, map[string]any{"lines": logg.Lines()})
	})

	mux.HandleFunc("POST /api/scan", func(w http.ResponseWriter, r *http.Request) {
		if !srv.authorized(r) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		var body struct {
			For    string `json:"for"`
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		if body.For == "" {
			body.For = "both"
		}
		if body.For != "warp" && body.For != "masque" && body.For != "both" {
			writeJSON(w, 400, map[string]string{"error": "for must be warp|masque|both"})
			return
		}
		if body.Reason == "" {
			body.Reason = "manual"
		}
		ok := srv.orch.ForceScan(body.For, "manual: "+body.Reason)
		writeJSON(w, 200, map[string]any{"queued": ok})
	})

	mux.HandleFunc("POST /api/endpoint", func(w http.ResponseWriter, r *http.Request) {
		if !srv.authorized(r) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		var body struct {
			Proxy    string `json:"proxy"`
			Endpoint string `json:"endpoint"`
			Outer    string `json:"outer"`
			Inner    string `json:"inner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		switch body.Proxy {
		case "warp":
			if !validEndpoint(body.Endpoint) {
				writeJSON(w, 400, map[string]string{"error": "endpoint must be ip:port"})
				return
			}
			if err := uciSet("warpd.warp.endpoint", body.Endpoint); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			srv.refreshProxyPin("warp")
			go srv.reapply()
		case "masque", "masqueh2":
			if !validEndpoint(body.Endpoint) {
				writeJSON(w, 400, map[string]string{"error": "endpoint must be ip:port"})
				return
			}
			if err := uciSet("warpd."+body.Proxy+".endpoint", body.Endpoint); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			srv.refreshProxyPin(body.Proxy)
			go srv.reapply()
		case "m2w":
			if !validEndpoint(body.Outer) || !validEndpoint(body.Inner) {
				writeJSON(w, 400, map[string]string{"error": "outer and inner must be ip:port"})
				return
			}
			_ = uciSet("warpd."+body.Proxy+".outer_endpoint", body.Outer)
			_ = uciSet("warpd."+body.Proxy+".inner_endpoint", body.Inner)
			srv.refreshProxyPin(body.Proxy)
			go srv.reapply()
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/endpoint/clear", func(w http.ResponseWriter, r *http.Request) {
		if !srv.authorized(r) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		var body struct {
			Proxy string `json:"proxy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		switch body.Proxy {
		case "warp", "masque", "masqueh2":
			_ = uciSet("warpd."+body.Proxy+".endpoint", "")
			srv.refreshProxyPin(body.Proxy)
			go srv.reapply()
		case "m2w":
			_ = uciSet("warpd.m2w.outer_endpoint", "")
			_ = uciSet("warpd.m2w.inner_endpoint", "")
			srv.refreshProxyPin("m2w")
			go srv.reapply()
		default:
			writeJSON(w, 400, map[string]string{"error": "proxy must be warp|masque|masqueh2|m2w"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/restart", func(w http.ResponseWriter, r *http.Request) {
		if !srv.authorized(r) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		var body struct {
			Proxy string `json:"proxy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		go func() {
			mihomo.Stop()
			g := srv.cfg.Snapshot()
			acc, err := loadAccount(account)
			if err != nil {
				logg.Printf("restart: %v", err)
				return
			}
			if err := mihomo.Ensure(g, stateGetEndpoints(), acc); err != nil {
				logg.Printf("restart: mihomo %v", err)
			} else {
				logg.Printf("mihomo restarted")
			}
		}()
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/register", func(w http.ResponseWriter, r *http.Request) {
		if !srv.authorized(r) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		var body struct {
			Fresh bool `json:"fresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		go func() {
			msg, err := srv.orch.RegisterAccount(body.Fresh)
			if err != nil {
				logg.Printf("register failed: %v (%s)", err, msg)
			} else {
				logg.Printf("register ok: %s", msg)
			}
		}()
		writeJSON(w, 200, map[string]any{"started": true})
	})

	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		if !srv.authorized(r) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		applied, err := srv.cfg.ApplyConfigPatch(patch)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"applied": applied})
	})

	// static UI
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})

	return mux
}

func (srv *Server) ListenAndServe() error {
	g := srv.cfg.Snapshot()
	addr := net.JoinHostPort(g.WebListen, strconv.Itoa(g.WebPort))
	srvCfg := &http.Server{
		Addr:              addr,
		Handler:           srv.handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logg.Printf("web UI listening on %s", addr)
	return srvCfg.ListenAndServe()
}

func validEndpoint(s string) bool {
	if s == "" {
		return false
	}
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return false
	}
	host, port := s[:i], s[i+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}
	if strings.Count(host, ":") > 1 && !strings.HasPrefix(host, "[") {
		// bare IPv6 without brackets: warpscout prints bare IPv6 endpoints
		// like "2606:4700:...:port"; accept if it parses as an IP
		return isIPOrHost(strings.TrimSuffix(s, ":"+port))
	}
	return isIPOrHost(host)
}

func isIPOrHost(s string) bool {
	if s == "" {
		return false
	}
	if net.ParseIP(s) != nil {
		return true
	}
	// hostname: allow letters, digits, dots, hyphens
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':') {
			return false
		}
	}
	return true
}

// reapply re-renders the mihomo config with current settings/endpoints.
func (srv *Server) reapply() {
	g := srv.cfg.Snapshot()
	acc, err := loadAccount(account)
	if err != nil {
		logg.Printf("reapply: %v", err)
		return
	}
	if err := mihomo.Ensure(g, stateGetEndpoints(), acc); err != nil {
		logg.Printf("reapply: mihomo %v", err)
	} else {
		logg.Printf("mihomo config re-applied")
	}
}

// liveEndpoint returns the endpoint (and outer, for chains) currently served.
func liveEndpoint(name string, p *ProxySettings, e endpoints) (ep, outer string) {
	switch name {
	case "warp":
		return firstNonEmpty(p.Endpoint, e.warp), ""
	case "masque":
		return firstNonEmpty(p.Endpoint, e.masque["masque"]), ""
	case "masqueh2":
		return firstNonEmpty(p.Endpoint, e.masque["masqueh2"]), ""
	case "m2w":
		return firstNonEmpty(p.Inner, e.warp), firstNonEmpty(p.Outer, e.masque["masque"], e.warp)
	}
	return "", ""
}

var _ = fmt.Sprintf
