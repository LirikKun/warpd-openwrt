package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Actual pinned engine and loopback Relay bridge; meta/independent geography are
// deterministic fixtures. The inherited offline netns prevents provider access.
func TestProfilePolicyRelayRuntimeModesAndPrerequisites(t *testing.T) {
	for _, mode := range []string{"off", "exclusions", "strict"} {
		t.Run(mode, func(t *testing.T) {
			g := integrationSettings()
			p := g.Proxies["relay"]
			p.PolicyMode = mode
			p.PolicyCountry = "DE"
			o, _, bridges := integrationEngine(t, &g)
			if err := o.applyCurrent(); err != nil {
				t.Fatal(err)
			}
			if o.profileGateOpen("relay") {
				t.Fatal("mode bypassed bootstrap health requirement")
			}
			calls := 0
			c := getCheck("relay")
			c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
			checkProxyWith(context.Background(), g.Snapshot(), "relay", o, geoMeta)
			if !o.profileGateOpen("relay") {
				t.Fatal("healthy verified Relay gate did not open")
			}
			if (calls > 0) != (mode == "strict") {
				t.Fatalf("unexpected independent geography requests: %d", calls)
			}
			if mode == "strict" {
				c.clearGeo()
				c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "NL"}, nil }
				checkProxyWith(context.Background(), g.Snapshot(), "relay", o, geoMeta)
				if o.profileGateOpen("relay") {
					t.Fatal("Relay selected endpoint country used as egress evidence")
				}
				c.clearGeo()
				c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "DE"}, nil }
				checkProxyWith(context.Background(), g.Snapshot(), "relay", o, geoMeta)
			}
			(*bridges)[0].Close()
			deadline := time.Now().Add(time.Second)
			for o.profileGateOpen("relay") && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if o.profileGateOpen("relay") {
				t.Fatal("mode bypassed Relay source/bridge requirement")
			}
			st := NewServer(&g, o).status()["proxies"].(map[string]ProxyState)["relay"]
			if st.Ready || st.TransportReady {
				t.Fatal("lost bootstrap stayed ready")
			}
		})
	}
}

func TestProfilePolicyOffRelayMissingCredentialsStillFails(t *testing.T) {
	isolateStatusState(t)
	g := integrationSettings()
	p := g.Proxies["relay"]
	p.PolicyMode = "off"
	p.Credential = RelayCredential{}
	o := NewOrchestrator(&g)
	o.accountLoader = func() (warpAccount, error) {
		t.Error("missing Relay credential loaded shared account")
		return warpAccount{}, fmt.Errorf("offline")
	}
	if err := o.applyCurrent(); err == nil {
		t.Fatal("off accepted missing Relay credential")
	}
	if mihomo.Running() || o.profileGateOpen("relay") {
		t.Fatal("off bypassed credential requirement")
	}
}
