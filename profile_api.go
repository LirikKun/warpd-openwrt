package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"sort"
	"strings"
)

// Only these explicit DTOs cross the HTTP boundary. Settings and RelaySettings
// contain credentials and must never be used as response payloads.
type publicProfile struct {
	ID                        string      `json:"id"`
	Name                      string      `json:"name"`
	Type                      string      `json:"type"`
	Transport                 string      `json:"transport"`
	Enabled                   bool        `json:"enabled"`
	EffectiveEnabled          bool        `json:"effective_enabled"`
	Listen                    string      `json:"listen"`
	Port                      int         `json:"port"`
	Endpoint                  string      `json:"endpoint"`
	Outer                     string      `json:"outer,omitempty"`
	Inner                     string      `json:"inner,omitempty"`
	RelayID                   string      `json:"relay_id"`
	RelayCountry              string      `json:"relay_country"`
	ExcludeNodes              string      `json:"exclude_nodes"`
	ExcludeCountries          string      `json:"exclude_countries"`
	PolicyMode                string      `json:"policy_mode"`
	PolicyCountry             string      `json:"policy_country"`
	EffectiveExcludeNodes     string      `json:"effective_exclude_nodes"`
	EffectiveExcludeCountries string      `json:"effective_exclude_countries"`
	AWG                       *AWGOptions `json:"awg,omitempty"`
	CredentialMode            *string     `json:"credential_mode,omitempty"`
	CredentialConfigured      *bool       `json:"credential_configured,omitempty"`
}
type publicRelay struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Enabled         bool               `json:"enabled"`
	Configured      bool               `json:"configured"`
	SourceIP        string             `json:"source_ip"`
	Interface       string             `json:"interface"`
	PathConfirmed   bool               `json:"path_confirmed"`
	MonitorInterval int                `json:"monitor_interval"`
	LinkedProfiles  int                `json:"linked_profiles"`
	Status          RelayMonitorStatus `json:"status"`
}

func publicProfiles(g Settings) []publicProfile {
	out := make([]publicProfile, 0, len(g.Proxies))
	for _, id := range profileIDs(g) {
		p := g.Proxies[id]
		if p == nil {
			continue
		}
		kind := profileType(id, p)
		nodes, countries := effectiveExclusions(g, p)
		item := publicProfile{ID: id, Name: p.Name, Type: kind, Transport: p.Transport, Enabled: p.Enabled, EffectiveEnabled: effectiveEnabled(g, p), Listen: p.Listen, Port: p.Port, Endpoint: p.Endpoint, Outer: p.Outer, Inner: p.Inner, RelayID: p.RelayID, RelayCountry: p.RelayCountry, ExcludeNodes: p.ExcludeNodes, ExcludeCountries: p.ExcludeCountries, EffectiveExcludeNodes: nodes, EffectiveExcludeCountries: countries}
		if item.Name == "" {
			item.Name = id
		}
		// Legacy remains empty: it is not a selectable mode.
		item.PolicyMode, item.PolicyCountry = p.PolicyMode, p.PolicyCountry
		if kind == "relay-warp" {
			mode := p.Credential.Mode
			configured := relayCredentialConfigured(p)
			item.CredentialMode = &mode
			item.CredentialConfigured = &configured
		}
		if kind != "masque" {
			a := p.AWG
			if a.Preset == "" {
				a = DefaultAWGOptions()
			}
			item.AWG = &a
		}
		if kind == "masque" && item.Transport == "" {
			item.Transport = "quic"
			if id == "masqueh2" {
				item.Transport = "http2"
			}
		}
		out = append(out, item)
	}
	return out
}
func publicRelays(g Settings) []publicRelay {
	ids := make([]string, 0, len(g.Relays))
	for id := range g.Relays {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]publicRelay, 0, len(ids))
	for _, id := range ids {
		r := g.Relays[id]
		if r == nil {
			continue
		}
		item := publicRelay{ID: id, Name: r.Name, Enabled: r.Enabled, Configured: r.ActivationURL != "", SourceIP: r.SourceIP, Interface: r.Interface, PathConfirmed: r.PathConfirmed, MonitorInterval: r.MonitorInterval}
		for _, p := range g.Proxies {
			if p != nil && p.RelayID == id {
				item.LinkedProfiles++
			}
		}
		out = append(out, item)
	}
	return out
}
func (srv *Server) profilePayload(g Settings) map[string]any {
	return map[string]any{"revision": g.Revision, "profiles": publicProfiles(g), "relays": srv.safeRelays(g), "defaults": map[string]any{"awg": DefaultAWGOptions()}}
}

type profilePatch struct {
	Name             *string     `json:"name"`
	Type             *string     `json:"type"`
	Transport        *string     `json:"transport"`
	Enabled          *bool       `json:"enabled"`
	Listen           *string     `json:"listen"`
	Port             *int        `json:"port"`
	Endpoint         *string     `json:"endpoint"`
	RelayID          *string     `json:"relay_id"`
	RelayCountry     *string     `json:"relay_country"`
	ExcludeNodes     *string     `json:"exclude_nodes"`
	ExcludeCountries *string     `json:"exclude_countries"`
	PolicyMode       *string     `json:"policy_mode"`
	PolicyCountry    *string     `json:"policy_country"`
	AWG              *AWGOptions `json:"awg"`
}
type relayPatch struct {
	Name            *string `json:"name"`
	URL             *string `json:"url"`
	Enabled         *bool   `json:"enabled"`
	SourceIP        *string `json:"source_ip"`
	Interface       *string `json:"interface"`
	PathConfirmed   *bool   `json:"path_confirmed"`
	MonitorInterval *int    `json:"monitor_interval"`
}
type profileAPIError struct {
	code    int
	message string
}

func (e profileAPIError) Error() string         { return e.message }
func apiProblem(code int, message string) error { return profileAPIError{code, message} }
func newProfileID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("identifier generation failed")
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func (srv *Server) profileMutation(w http.ResponseWriter, r *http.Request, create bool, edit func(*Settings, string) error) {
	if !srv.authorized(r) {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	srv.orch.applyMu.Lock()
	defer srv.orch.applyMu.Unlock()
	id := r.PathValue("id")
	if create {
		var err error
		prefix := "p_"
		if strings.HasPrefix(r.URL.Path, "/api/relays") {
			prefix = "r_"
		}
		id, err = newProfileID(prefix)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "identifier generation failed"})
			return
		}
	}
	before := srv.cfg.Snapshot()
	if before.ProfileSchema != profileSchema || before.ProfilePath == "" {
		writeJSON(w, 409, map[string]any{"error": "profile store is not initialized", "persisted": false, "applied": false})
		return
	}
	invalid := false
	err := srv.cfg.ChangeProfiles(func(g *Settings) error {
		editErr := edit(g, id)
		if editErr == nil {
			editErr = validateProfiles(*g, &before)
		}
		if editErr != nil {
			invalid = true
		}
		return editErr
	})
	if err != nil {
		code := 500
		if invalid {
			code = 400
		}
		if problem, ok := err.(profileAPIError); ok {
			code = problem.code
		}
		writeJSON(w, code, map[string]any{"error": err.Error(), "persisted": false, "applied": false})
		return
	}
	var relayErr error
	mihomo.ReconcileRelayIntent(srv.cfg.Snapshot())
	if srv.relays != nil {
		relayErr = srv.relays.sync(srv.cfg.Snapshot())
	}
	// Explicit configuration changes schedule their own discovery, including
	// when autoscan-on-start is off or a missing account makes application pending.
	srv.orch.queueMissing("explicit profile configuration changed")
	// Persisted intent is not equivalent to a live tunnel. Read it back through
	// GET /api/profiles even when applying the shared engine fails.
	if err = srv.orch.applyLocked(); err != nil {
		writeJSON(w, 503, map[string]any{"id": id, "persisted": true, "applied": false, "error": err.Error()})
		return
	}
	if relayErr != nil {
		writeJSON(w, 503, map[string]any{"id": id, "persisted": true, "applied": false, "error": relayErr.Error()})
		return
	}
	code := 200
	if create {
		code = 201
	}
	writeJSON(w, code, map[string]any{"id": id, "persisted": true, "applied": true})
}
func decodeProfilePatch(w http.ResponseWriter, r *http.Request, v any) bool {
	reject := func() bool { writeJSON(w, 400, map[string]string{"error": "invalid JSON request"}); return false }
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return reject()
	}
	limit := int64(16 << 10)
	if _, ok := v.(*profilePatch); ok {
		limit = 512 << 10
	} // maximal Unicode-escaped pinned I1 plus DTO overhead
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return reject()
	}
	// encoding/json otherwise accepts repeated properties, case-insensitive aliases
	// and null scalar fields. Validate exact DTO property names before decoding.
	tokens := json.NewDecoder(bytes.NewReader(data))
	first, err := tokens.Token()
	if err != nil || first != json.Delim('{') {
		return reject()
	}
	if err = profileJSONObject(tokens, 0, reflect.TypeOf(v)); err != nil {
		return reject()
	}
	if _, err = tokens.Token(); err != io.EOF {
		return reject()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return reject()
	}
	return true
}
func profileJSONObject(d *json.Decoder, depth int, schema reflect.Type) error {
	if depth > 8 {
		return fmt.Errorf("JSON nesting limit")
	}
	for schema != nil && schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	if schema == nil || schema.Kind() != reflect.Struct {
		return fmt.Errorf("unexpected JSON object")
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < schema.NumField(); i++ {
		field := schema.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.PkgPath == "" && name != "" && name != "-" {
			fields[name] = field.Type
		}
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] || fields[key] == nil {
			return fmt.Errorf("ambiguous JSON key")
		}
		seen[key] = true
		value, err := d.Token()
		if err != nil {
			return err
		}
		if value == nil {
			return fmt.Errorf("null JSON field")
		}
		if delim, ok := value.(json.Delim); ok {
			if delim != '{' {
				return fmt.Errorf("unexpected JSON container")
			}
			if err = profileJSONObject(d, depth+1, fields[key]); err != nil {
				return err
			}
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return fmt.Errorf("invalid JSON object")
	}
	return nil
}
func (srv *Server) mutationAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if !srv.authorized(r) {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}
func applyProfileFields(p *ProxySettings, b profilePatch) error {
	if b.Name != nil {
		p.Name = strings.TrimSpace(*b.Name)
	}
	if b.Transport != nil {
		p.Transport = *b.Transport
	}
	if b.Enabled != nil {
		p.Enabled = *b.Enabled
	}
	if b.Listen != nil {
		p.Listen = *b.Listen
	}
	if b.Port != nil {
		p.Port = *b.Port
	}
	if b.Endpoint != nil {
		p.Endpoint = *b.Endpoint
	}
	if b.RelayID != nil {
		p.RelayID = *b.RelayID
	}
	if b.RelayCountry != nil {
		p.RelayCountry = strings.ToUpper(*b.RelayCountry)
	}
	if b.ExcludeNodes != nil {
		p.ExcludeNodes = *b.ExcludeNodes
	}
	if b.ExcludeCountries != nil {
		p.ExcludeCountries = *b.ExcludeCountries
	}
	if b.AWG != nil {
		p.AWG = *b.AWG
	}
	if b.PolicyMode != nil {
		if *b.PolicyMode != "off" && *b.PolicyMode != "exclusions" && *b.PolicyMode != "strict" {
			return fmt.Errorf("policy_mode must be off, exclusions or strict")
		}
		p.PolicyMode = *b.PolicyMode
	}
	if b.PolicyCountry != nil {
		p.PolicyCountry = *b.PolicyCountry
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 || strings.ContainsAny(p.Name, "\r\n\x00") {
		return fmt.Errorf("name must contain 1 to 128 characters without control characters")
	}
	return nil
}
func nextProfilePort(g Settings) int {
	used := map[int]bool{g.WebPort: true, 9099: true}
	for _, p := range g.Proxies {
		if p != nil {
			used[p.Port] = true
		}
	}
	for port := 4001; port <= 65535; port++ {
		if !used[port] {
			return port
		}
	}
	return 0
}
func applyRelayFields(g *Settings, r *RelaySettings, b relayPatch, create bool) error {
	changedPath := b.SourceIP != nil && *b.SourceIP != r.SourceIP || b.Interface != nil && *b.Interface != r.Interface || b.URL != nil && *b.URL != "" && *b.URL != r.ActivationURL
	if changedPath {
		r.PathConfirmed = false
	}
	if b.Name != nil {
		r.Name = strings.TrimSpace(*b.Name)
	}
	if b.Enabled != nil {
		r.Enabled = *b.Enabled
	}
	if b.SourceIP != nil {
		r.SourceIP = *b.SourceIP
	}
	if b.Interface != nil {
		r.Interface = *b.Interface
	}
	if b.PathConfirmed != nil {
		r.PathConfirmed = *b.PathConfirmed
	}
	if b.MonitorInterval != nil {
		r.MonitorInterval = *b.MonitorInterval
	}
	if b.URL != nil && (*b.URL != "" || create) {
		r.ActivationURL = *b.URL
	}
	if r.Name == "" || len(r.Name) > 128 || strings.ContainsAny(r.Name, "\r\n\x00") {
		return fmt.Errorf("Relay name must contain 1 to 128 characters without control characters")
	}
	for id, other := range g.Relays {
		if id != r.Key && other != nil && other.ActivationURL == r.ActivationURL && r.ActivationURL != "" {
			return fmt.Errorf("this Relay access is already configured")
		}
	}
	return nil
}
func (srv *Server) registerProfileAPI(mux *http.ServeMux) {
	srv.registerRelayAccountAPI(mux)
	mux.HandleFunc("POST /api/relays/{id}/activate", srv.relayActivationHandler)
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		writeJSON(w, 200, srv.profilePayload(srv.cfg.Snapshot()))
	})
	mux.HandleFunc("POST /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var b profilePatch
		if !decodeProfilePatch(w, r, &b) {
			return
		}
		srv.profileMutation(w, r, true, func(g *Settings, id string) error {
			if b.Type == nil || (*b.Type != "warp" && *b.Type != "masque" && *b.Type != "relay-warp") {
				return fmt.Errorf("type must be warp, masque or relay-warp")
			}
			p := NewProxyProfile(id, *b.Type)
			// Constructors and migration preserve legacy defaults. Only an explicit
			// API create introduces the new exclusions policy by default.
			p.PolicyMode = "exclusions"
			p.Port = nextProfilePort(*g)
			if err := applyProfileFields(p, b); err != nil {
				return err
			}
			g.Proxies[id] = p
			return nil
		})
	})
	mux.HandleFunc("PATCH /api/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var b profilePatch
		if !decodeProfilePatch(w, r, &b) {
			return
		}
		srv.profileMutation(w, r, false, func(g *Settings, id string) error {
			p := g.Proxies[id]
			if p == nil {
				return apiProblem(404, "profile not found")
			}
			kind := profileType(id, p)
			if b.Type != nil && *b.Type != kind {
				return fmt.Errorf("profile type is immutable")
			}
			if kind == "legacy-m2w" {
				if b.Transport != nil || b.Endpoint != nil || b.RelayID != nil || b.RelayCountry != nil || b.AWG != nil {
					return fmt.Errorf("legacy chain transport is read-only")
				}
			}
			if b.Enabled != nil && p.RelayID != "" {
				parent := g.Relays[p.RelayID]
				if parent == nil || !parent.Enabled {
					return apiProblem(409, "profile is paused while its Relay is disabled")
				}
			}
			return applyProfileFields(p, b)
		})
	})
	mux.HandleFunc("DELETE /api/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var b struct{}
		if !decodeProfilePatch(w, r, &b) {
			return
		}
		srv.profileMutation(w, r, false, func(g *Settings, id string) error {
			if g.Proxies[id] == nil {
				return apiProblem(404, "profile not found")
			}
			delete(g.Proxies, id)
			return nil
		})
	})
	mux.HandleFunc("POST /api/relays", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var b relayPatch
		if !decodeProfilePatch(w, r, &b) {
			return
		}
		srv.profileMutation(w, r, true, func(g *Settings, id string) error {
			relay := &RelaySettings{Key: id, MonitorInterval: 300}
			if err := applyRelayFields(g, relay, b, true); err != nil {
				return err
			}
			if g.Relays == nil {
				g.Relays = map[string]*RelaySettings{}
			}
			g.Relays[id] = relay
			return nil
		})
	})
	mux.HandleFunc("PATCH /api/relays/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var b relayPatch
		if !decodeProfilePatch(w, r, &b) {
			return
		}
		srv.profileMutation(w, r, false, func(g *Settings, id string) error {
			relay := g.Relays[id]
			if relay == nil {
				return apiProblem(404, "Relay not found")
			}
			return applyRelayFields(g, relay, b, false)
		})
	})
	mux.HandleFunc("DELETE /api/relays/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !srv.mutationAuthorized(w, r) {
			return
		}
		var b struct{}
		if !decodeProfilePatch(w, r, &b) {
			return
		}
		srv.profileMutation(w, r, false, func(g *Settings, id string) error {
			if g.Relays[id] == nil {
				return apiProblem(404, "Relay not found")
			}
			for _, p := range g.Proxies {
				if p != nil && p.RelayID == id {
					return apiProblem(409, "delete every linked profile before deleting Relay")
				}
			}
			delete(g.Relays, id)
			return nil
		})
	})
}
