package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func profileAPIFixture(t *testing.T) *Server {
	t.Helper()
	cfg := statusTestSettings()
	for _, p := range cfg.Proxies {
		p.Enabled = false
	}
	if err := cfg.InitProfiles(filepath.Join(t.TempDir(), "profiles.json")); err != nil {
		t.Fatal(err)
	}
	return NewServer(cfg, NewOrchestrator(cfg))
}
func profileAPIRequest(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, r)
	return w
}
func profileResultID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var result struct {
		ID        string `json:"id"`
		Persisted bool   `json:"persisted"`
		Applied   bool   `json:"applied"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID == "" || !result.Persisted || !result.Applied {
		t.Fatalf("missing persisted/applied identity: %d %s", w.Code, w.Body.String())
	}
	return result.ID
}
func TestProfileCRUDPreservesIdentityAndPrivatePersistedValues(t *testing.T) {
	srv := profileAPIFixture(t)
	w := profileAPIRequest(srv, "POST", "/api/profiles", `{"name":"Second WARP","type":"warp","enabled":false,"port":4811}`)
	if w.Code != 201 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	id := profileResultID(t, w)
	if id == "warp" || srv.cfg.Snapshot().Proxies[id].Name != "Second WARP" {
		t.Fatal("create replaced existing WARP")
	}
	w = profileAPIRequest(srv, "PATCH", "/api/profiles/"+id, `{"name":"Renamed","port":4812,"exclude_nodes":"FRA"}`)
	if w.Code != 200 {
		t.Fatalf("edit=%d %s", w.Code, w.Body.String())
	}
	if profileResultID(t, w) != id {
		t.Fatal("rename changed identity")
	}
	loaded := statusTestSettings()
	if err := loaded.InitProfiles(srv.cfg.Snapshot().ProfilePath); err != nil {
		t.Fatal(err)
	}
	p := loaded.Snapshot().Proxies[id]
	if p == nil || p.Name != "Renamed" || p.Port != 4812 || p.ExcludeNodes != "FRA" {
		t.Fatalf("persistent settings differ: %+v", p)
	}
	info, err := os.Stat(srv.cfg.Snapshot().ProfilePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("secret-bearing store permissions=%o", info.Mode().Perm())
	}
	w = profileAPIRequest(srv, "DELETE", "/api/profiles/"+id, `{}`)
	if w.Code != 200 {
		t.Fatalf("delete=%d %s", w.Code, w.Body.String())
	}
	loaded = statusTestSettings()
	if err := loaded.InitProfiles(srv.cfg.Snapshot().ProfilePath); err != nil {
		t.Fatal(err)
	}
	if loaded.Proxies[id] != nil {
		t.Fatal("deleted profile restored after reload")
	}
}

func TestProfileAPIRelayDependencyIncludesDisabledChildrenAndDoesNotExposeCapability(t *testing.T) {
	srv := profileAPIFixture(t)
	const capability = "https://relay.tribukvy.ltd/activate/0123456789abcdef"
	w := profileAPIRequest(srv, "POST", "/api/relays", `{"name":"Test Relay","url":"`+capability+`","enabled":false}`)
	if w.Code != 201 {
		t.Fatalf("Relay create=%d %s", w.Code, w.Body.String())
	}
	relayID := profileResultID(t, w)
	var ids []string
	for i, enabled := range []bool{true, false} {
		body := fmt.Sprintf(`{"name":"Child %d","type":"relay-warp","relay_id":%q,"relay_country":"DE","enabled":%t,"port":%d}`, i, relayID, enabled, 4820+i)
		w = profileAPIRequest(srv, "POST", "/api/profiles", body)
		if w.Code != 201 {
			t.Fatalf("child create=%d %s", w.Code, w.Body.String())
		}
		ids = append(ids, profileResultID(t, w))
	}
	for _, path := range []string{"/api/profiles", "/api/status"} {
		w = profileAPIRequest(srv, "GET", path, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), capability) || strings.Contains(w.Body.String(), "0123456789abcdef") {
			t.Fatalf("public read leaks secret or fails: status=%d", w.Code)
		}
	}
	w = profileAPIRequest(srv, "DELETE", "/api/relays/"+relayID, `{}`)
	if w.Code != 409 {
		t.Fatalf("Relay deleted with disabled children: %d", w.Code)
	}
	w = profileAPIRequest(srv, "PATCH", "/api/profiles/"+ids[0], `{"enabled":false}`)
	if w.Code != 409 {
		t.Fatalf("paused child's desired switch was editable: %d", w.Code)
	}
	// The apply failure is injected at the account boundary; no network/scanner
	// subprocess may be needed to prove saved intent and parent gating.
	srv.orch.accountLoader = func() (warpAccount, error) { return warpAccount{}, fmt.Errorf("test account unavailable") }
	w = profileAPIRequest(srv, "PATCH", "/api/relays/"+relayID, `{"enabled":true}`)
	if w.Code != 503 {
		t.Fatalf("expected honest persisted/apply failure: %d %s", w.Code, w.Body.String())
	}
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result["persisted"] != true || result["applied"] != false {
		t.Fatal("apply failure lost persistence outcome")
	}
	g := srv.cfg.Snapshot()
	if !g.Proxies[ids[0]].Enabled || g.Proxies[ids[1]].Enabled || !effectiveEnabled(g, g.Proxies[ids[0]]) || effectiveEnabled(g, g.Proxies[ids[1]]) {
		t.Fatal("parent enable did not preserve child switches")
	}
	w = profileAPIRequest(srv, "PATCH", "/api/relays/"+relayID, `{"enabled":false}`)
	if w.Code != 200 {
		t.Fatalf("parent disable=%d %s", w.Code, w.Body.String())
	}
	loaded := statusTestSettings()
	if err := loaded.InitProfiles(g.ProfilePath); err != nil {
		t.Fatal(err)
	}
	g = loaded.Snapshot()
	if !g.Proxies[ids[0]].Enabled || g.Proxies[ids[1]].Enabled || effectiveEnabled(g, g.Proxies[ids[0]]) {
		t.Fatal("disable/restart lost saved switches or kept active child")
	}
	for _, id := range ids {
		w = profileAPIRequest(srv, "DELETE", "/api/profiles/"+id, `{}`)
		if w.Code != 200 {
			t.Fatalf("child delete=%d %s", w.Code, w.Body.String())
		}
	}
	w = profileAPIRequest(srv, "DELETE", "/api/relays/"+relayID, `{}`)
	if w.Code != 200 {
		t.Fatalf("empty Relay delete=%d %s", w.Code, w.Body.String())
	}
}
func TestProfileAPIRejectsInvalidSiblingBeforePersistence(t *testing.T) {
	srv := profileAPIFixture(t)
	g := srv.cfg.Snapshot()
	before, err := os.ReadFile(g.ProfilePath)
	if err != nil {
		t.Fatal(err)
	}
	w := profileAPIRequest(srv, "PATCH", "/api/profiles/warp", `{"name":"changed","port":4003}`)
	if w.Code < 400 {
		t.Fatalf("conflicting port accepted: %d", w.Code)
	}
	after, err := os.ReadFile(g.ProfilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || srv.cfg.Snapshot().Proxies["warp"].Name != g.Proxies["warp"].Name {
		t.Fatal("invalid patch partially persisted")
	}
}

func TestProfileAPIPersistenceFailureDoesNotPublishOrApply(t *testing.T) {
	srv := profileAPIFixture(t)
	before := srv.cfg.Snapshot()
	data, err := os.ReadFile(before.ProfilePath)
	if err != nil {
		t.Fatal(err)
	}
	// A directory cannot be atomically replaced by the store's regular file.
	// This fails after validation without depending on chmod/root semantics.
	srv.cfg.ProfilePath = t.TempDir()
	calls := 0
	srv.orch.accountLoader = func() (warpAccount, error) { calls++; return warpAccount{}, fmt.Errorf("must not be called") }
	w := profileAPIRequest(srv, "PATCH", "/api/profiles/warp", `{"name":"not committed"}`)
	if w.Code != 500 {
		t.Fatalf("filesystem failure must be a server error: %d %s", w.Code, w.Body.String())
	}
	after := srv.cfg.Snapshot()
	if calls != 0 || after.Revision != before.Revision || after.Proxies["warp"].Name != before.Proxies["warp"].Name {
		t.Fatal("failed persistence published or applied desired state")
	}
	actual, err := os.ReadFile(before.ProfilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, data) {
		t.Fatal("failed transaction changed previous store")
	}
}

func TestExplicitProfileEnableQueuesDiscoveryEvenWhenApplyFailsAndAutoscanIsOff(t *testing.T) {
	srv := profileAPIFixture(t)
	srv.cfg.AutoscanStart = false
	srv.orch.accountLoader = func() (warpAccount, error) { return warpAccount{}, fmt.Errorf("test account unavailable") }
	w := profileAPIRequest(srv, "POST", "/api/profiles", `{"name":"New enabled WARP","type":"warp","enabled":true,"port":4891}`)
	if w.Code != 503 {
		t.Fatalf("expected pending apply, got %d %s", w.Code, w.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	select {
	case req := <-srv.orch.scanCh:
		found := false
		for _, id := range req.targets {
			if id == result.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("explicit profile not scheduled: %v", req.targets)
		}
	default:
		t.Fatal("explicit enable never queued discovery after apply failure")
	}
}

func TestRelayPathConfirmationDoesNotSurviveAnUnconfirmedSourceChange(t *testing.T) {
	srv := profileAPIFixture(t)
	w := profileAPIRequest(srv, "POST", "/api/relays", `{"name":"Relay","url":"https://relay.tribukvy.ltd/activate/0123456789abcdef","enabled":false,"source_ip":"192.0.2.10","path_confirmed":true}`)
	if w.Code != 201 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	id := profileResultID(t, w)
	w = profileAPIRequest(srv, "PATCH", "/api/relays/"+id, `{"name":"Renamed"}`)
	if w.Code != 200 || !srv.cfg.Snapshot().Relays[id].PathConfirmed {
		t.Fatal("rename unexpectedly revoked source confirmation")
	}
	w = profileAPIRequest(srv, "PATCH", "/api/relays/"+id, `{"source_ip":"192.0.2.11"}`)
	if w.Code != 200 {
		t.Fatalf("source edit=%d %s", w.Code, w.Body.String())
	}
	if srv.cfg.Snapshot().Relays[id].PathConfirmed {
		t.Fatal("old path confirmation authorized a different source path")
	}
}

func TestDynamicProfileScanIDReachesScheduler(t *testing.T) {
	cfg := statusTestSettings()
	clone := *cfg.Proxies["warp"]
	clone.Key = "p_second"
	cfg.Proxies["p_second"] = &clone
	srv := NewServer(cfg, NewOrchestrator(cfg))
	r := httptest.NewRequest(http.MethodPost, "/api/scan", strings.NewReader(`{"for":"p_second"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("dynamic scan ID rejected before scheduling: %d %s", w.Code, w.Body.String())
	}
}

func TestProfileCreateRequiresAuthorizationBeforeBodyAndPersistence(t *testing.T) {
	cfg := statusTestSettings()
	cfg.WebSecret = "test-only-secret"
	srv := NewServer(cfg, NewOrchestrator(cfg))
	r := httptest.NewRequest(http.MethodPost, "/api/profiles", strings.NewReader(`{"name":"second","type":"warp","enabled":false}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("create without bearer=%d, want 401", w.Code)
	}
	if len(cfg.Proxies) != 4 {
		t.Fatal("unauthorized create changed profiles")
	}
}

func TestProfileListUsesStableIDsAndDoesNotRequireEngine(t *testing.T) {
	cfg := statusTestSettings()
	srv := NewServer(cfg, NewOrchestrator(cfg))
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/profiles", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("profile list status=%d, want 200", w.Code)
	}
	var body struct {
		Profiles []struct {
			ID   string `json:"id"`
			Port int    `json:"port"`
		} `json:"profiles"`
		Relays []any `json:"relays"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Profiles == nil || body.Relays == nil {
		t.Fatalf("missing collections: %s", w.Body.String())
	}
	found := false
	for _, p := range body.Profiles {
		if p.ID == "warp" && p.Port == cfg.Proxies["warp"].Port {
			found = true
		}
	}
	if !found {
		t.Fatalf("stable profile ID/bind missing: %s", w.Body.String())
	}
}
