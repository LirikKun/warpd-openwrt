package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestRelayIntegrationHTTPRevocationMatrix(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		code                     int
	}{
		{"child-disable", "PATCH", "/api/profiles/relay", `{"enabled":false}`, 200},
		{"parent-disable", "PATCH", "/api/relays/r", `{"enabled":false}`, 200},
		{"delete", "DELETE", "/api/profiles/relay", `{}`, 200},
		{"country-edit", "PATCH", "/api/profiles/relay", `{"relay_country":"NL"}`, 200},
		{"source-edit", "PATCH", "/api/relays/r", `{"source_ip":"192.0.2.3"}`, 503},
		{"confirmation-revoke", "PATCH", "/api/relays/r", `{"path_confirmed":false}`, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := integrationSettings()
			g.ProfileSchema = 0
			if err := g.InitProfiles(filepath.Join(t.TempDir(), "private", "profiles.json")); err != nil {
				t.Fatal(err)
			}
			o, _, bridges := integrationEngine(t, &g)
			if err := o.applyCurrent(); err != nil {
				t.Fatal(err)
			}
			old := (*bridges)[0]
			old.mu.Lock()
			owner := old.owner
			old.mu.Unlock()
			srv := NewServer(&g, o)
			t.Cleanup(srv.accounts.Close)
			done := make(chan int, 1)
			go func() { w := profileAPIRequest(srv, tc.method, tc.path, tc.body); done <- w.Code }()
			select {
			case code := <-done:
				if code != tc.code {
					t.Fatalf("mutation response=%d want=%d", code, tc.code)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("API deadlocked joining under apply/manager locks")
			}
			select {
			case <-old.Done():
			default:
				t.Fatal("API acknowledged before old bridge joined")
			}
			if owner.Current() {
				t.Fatal("old token resumed after mutation")
			}
			if tc.name == "parent-disable" && !g.Snapshot().Proxies["relay"].Enabled {
				t.Fatal("parent disable clobbered individual desired flag")
			}
			if tc.name == "source-edit" && g.Snapshot().Relays["r"].PathConfirmed {
				t.Fatal("source edit retained confirmation")
			}
			if o.profileGateOpen("relay") {
				t.Fatal("mutation opened unverified public gate")
			}
		})
	}
}

func TestRelayIntegrationPendingRelayDoesNotCloseHealthyOrdinaryGate(t *testing.T) {
	g := integrationSettings()
	warp := NewProxyProfile("warp", "warp")
	warp.Enabled = true
	warp.Endpoint = "127.0.0.1:24999"
	warp.Port = 4812
	g.Proxies["warp"] = warp
	o, _, _ := integrationEngine(t, &g)
	c := integrationCredential(101)
	o.accountLoader = func() (warpAccount, error) {
		return warpAccount{PrivateKey: c.PrivateKey, PeerPub: c.PeerPublicKey, IPv4: c.TunnelIPv4}, nil
	}
	mihomo.relayPrepare = func(context.Context, RelayUDPConfig) (*RelayUDPBridge, error) {
		return nil, fmt.Errorf("synthetic offline setup failure")
	}
	if err := o.applyCurrent(); err == nil {
		t.Fatal("Relay failure not pending")
	}
	checkProxyWith(context.Background(), g.Snapshot(), "warp", o, func(context.Context, string, int) (exitParse, error) {
		return exitParse{node: "AMS", nodeCountry: "NL", country: "RU"}, nil
	})
	if !o.profileGateOpen("warp") {
		t.Fatal("healthy ordinary sibling gate unavailable")
	}
	before := mihomo.Snapshot()
	if err := o.applyCurrent(); err == nil {
		t.Fatal("Relay retry no longer pending")
	}
	if !o.profileGateOpen("warp") {
		t.Fatal("pending unrelated Relay unnecessarily revoked healthy ordinary gate")
	}
	if after := mihomo.Snapshot(); after != before {
		t.Fatal("pending Relay changed healthy shared child generation")
	}
}

func TestRelayIntegrationExpiryRecoversWithoutRestartingOrdinarySibling(t *testing.T) {
	g := integrationSettings()
	warp := NewProxyProfile("warp", "warp")
	warp.Enabled = true
	warp.Endpoint = "127.0.0.1:24999"
	warp.Port = 4812
	g.Proxies["warp"] = warp
	o, _, bridges := integrationEngine(t, &g)
	c := integrationCredential(101)
	loads := 0
	o.accountLoader = func() (warpAccount, error) {
		loads++
		return warpAccount{PrivateKey: c.PrivateKey, PeerPub: c.PeerPublicKey, IPv4: c.TunnelIPv4}, nil
	}
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	before := mihomo.Snapshot()
	(*bridges)[0].Close()
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	after := mihomo.Snapshot()
	if after.PID != before.PID || after.Generation <= before.Generation || !mihomo.RelayReady(g.Snapshot(), "relay") {
		t.Fatalf("expiry disrupted ordinary shared child or did not recover: before=%+v after=%+v", before, after)
	}
	if loads != 2 {
		t.Fatalf("mixed ordinary account loads=%d", loads)
	}
	if len(*bridges) != 2 {
		t.Fatalf("expired transport not freshly prepared: %d", len(*bridges))
	}
}
