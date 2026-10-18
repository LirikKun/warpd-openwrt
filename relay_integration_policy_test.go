package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestRelayIntegrationPolicyAndBridgeExpiryCloseGate(t *testing.T) {
	g := integrationSettings()
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	c := getCheck("relay")
	c.geoProbe = func(context.Context, string, int) (geoResult, error) {
		return geoResult{}, fmt.Errorf("offline independent geo failure")
	}
	meta := func(context.Context, string, int) (exitParse, error) {
		return exitParse{node: "AMS", nodeCountry: "NL", country: "NL"}, nil
	}
	checkProxyWith(context.Background(), g.Snapshot(), "relay", o, meta)
	if o.profileGateOpen("relay") {
		t.Fatal("Relay public gate opened with transport/meta but without existing independent geography policy")
	}
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "NL"}, nil }
	c.clearGeo()
	checkProxyWith(context.Background(), g.Snapshot(), "relay", o, meta)
	if !o.profileGateOpen("relay") {
		t.Fatal("current owned transport and independent policy did not open gate")
	}
	srv := NewServer(&g, o)
	st := srv.status()["proxies"].(map[string]ProxyState)["relay"]
	if !st.Ready || !st.PolicyReady || !st.TransportReady {
		t.Fatalf("status still uses unsupported-stage hard block: %+v", st)
	}
	(*bridges)[0].Close() // same event as freshness/path loss: joined bridge, still-live child
	deadline := time.Now().Add(time.Second)
	for o.profileGateOpen("relay") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if o.profileGateOpen("relay") {
		t.Fatal("public gate survived bridge expiry/Close with child alive")
	}
	st = srv.status()["proxies"].(map[string]ProxyState)["relay"]
	if st.Ready || st.TransportReady {
		t.Fatal("expired bridge remained ready")
	}
}

func TestRelayIntegrationGeneratedCredentialRevokesBeforeSuccess(t *testing.T) {
	g := integrationSettings()
	g.ProfileSchema = 0
	if err := g.InitProfiles(filepath.Join(t.TempDir(), "private", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(&g, o)
	t.Cleanup(srv.accounts.Close)
	srv.accounts.generate = func(context.Context) (RelayCredential, error) {
		c := integrationCredential(81)
		c.Mode = "generated"
		return c, nil
	}
	w := profileAPIRequest(srv, "POST", "/api/profiles/relay/account/generate", fmt.Sprintf(`{"confirmed":true,"revision":%d}`, g.Snapshot().Revision))
	if w.Code != 202 {
		t.Fatalf("generation request=%d", w.Code)
	}
	srv.accounts.work.Wait()
	st := srv.accounts.status("relay", g.Snapshot().Proxies["relay"])
	if st.State != "succeeded" {
		t.Fatalf("synthetic generated result=%+v", st)
	}
	select {
	case <-(*bridges)[0].Done():
	default:
		t.Fatal("generated credential reported succeeded before old bridge joined")
	}
}

func TestRelayIntegrationCredentialSaveRevokesGlobalRevisionTokens(t *testing.T) {
	g := integrationSettings()
	second := *g.Proxies["relay"]
	second.Key = "second"
	second.Port = 4812
	second.Credential = integrationCredential(51)
	g.Proxies["second"] = &second
	g.ProfileSchema = 0
	if err := g.InitProfiles(filepath.Join(t.TempDir(), "private", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	old := (*bridges)[1]
	old.mu.Lock()
	owner := old.owner
	old.mu.Unlock()
	srv := NewServer(&g, o)
	t.Cleanup(srv.accounts.Close)
	body, _ := json.Marshal(integrationCredential(71))
	w := profileAPIRequest(srv, "PUT", "/api/profiles/relay/account", string(body))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if owner.Current() {
		t.Fatal("credential save left sibling token current for an obsolete global configuration revision")
	}
	select {
	case <-old.Done():
	default:
		t.Fatal("obsolete revision bridge not joined before save acknowledgment")
	}
}

func TestRelayIntegrationCredentialSaveRevokesBeforeAck(t *testing.T) {
	g := integrationSettings()
	g.ProfileSchema = 0
	if err := g.InitProfiles(filepath.Join(t.TempDir(), "private", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	o, fixture, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	b := (*bridges)[0]
	b.mu.Lock()
	owner := b.owner
	b.mu.Unlock()
	srv := NewServer(&g, o)
	t.Cleanup(srv.accounts.Close)
	body, _ := json.Marshal(integrationCredential(71))
	result := profileAPIRequest(srv, "PUT", "/api/profiles/relay/account", string(body))
	if result.Code != 200 {
		t.Fatalf("credential save=%d %s", result.Code, result.Body.String())
	}
	select {
	case <-b.Done():
	default:
		t.Fatal("credential save acknowledged before cancel/Close/join")
	}
	if owner.Current() {
		t.Fatal("credential save left old token current")
	}
	// The exact child may stay alive, but can no longer send through the old bridge.
	fixture.SetReadDeadline(time.Now().Add(5700 * time.Millisecond))
	buf := make([]byte, 65536)
	if n, _, err := fixture.ReadFromUDP(buf); err == nil {
		t.Fatalf("old credential traffic after save acknowledgment: %d", n)
	}
	if got := g.Snapshot().Proxies["relay"].Credential; got != integrationCredential(71) {
		t.Fatal("saved credential not authoritative")
	}
	if c, e := net.DialTimeout("tcp", "127.0.0.1:4811", 50*time.Millisecond); e == nil {
		c.Close()
		t.Fatal("credential save opened public gate")
	}
}
