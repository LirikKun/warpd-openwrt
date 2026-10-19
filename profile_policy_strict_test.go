package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProfilePolicyStrictIndependentExactCountryEveryType(t *testing.T) {
	for _, name := range proxyNames {
		for _, country := range []string{"DE", "NL", "RU", "XX", ""} {
			t.Run(name+"/"+country, func(t *testing.T) {
				s, o := policyHarness(t, name)
				p := s.Proxies[name]
				p.PolicyMode = "strict"
				p.PolicyCountry = "DE"
				calls := 0
				getCheck(name).geoProbe = func(context.Context, string, int) (geoResult, error) {
					calls++
					return geoResult{country: country}, nil
				}
				checkProxyWith(context.Background(), s.Snapshot(), name, o, geoMeta)
				st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)[name]
				if calls != 1 || !st.TransportReady || st.PolicyReady != (country == "DE") {
					t.Fatalf("strict country=%q calls=%d state=%+v", country, calls, st)
				}
				if len(o.scanCh) != 0 || st.FailStreak != 0 || st.Rescans != 0 {
					t.Fatal("strict mismatch altered transport or queued scans")
				}
			})
		}
	}
}

func TestProfilePolicyStrictEvidenceExpiryFailureAndIdentity(t *testing.T) {
	s, o := policyHarness(t, "warp")
	p := s.Proxies["warp"]
	p.PolicyMode = "strict"
	p.PolicyCountry = "DE"
	c := getCheck("warp")
	calls := 0
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, geoMeta)
	if calls != 1 || !NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"].Ready {
		t.Fatal("strict fresh evidence did not certify WARP")
	}
	p.PolicyCountry = "NL"
	if NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"].Ready {
		t.Fatal("country edit reused old evidence")
	}
	p.PolicyCountry = "DE"
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, geoMeta)
	if calls != 2 {
		t.Fatal("reverting policy resurrected cached success")
	}
	c.set(func(st *ProxyState) { st.GeoCheck = time.Now().Add(-61 * time.Second).Format(time.RFC3339Nano) })
	if NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"].Ready {
		t.Fatal("expired independent evidence remains ready")
	}
	c.geoProbe = func(context.Context, string, int) (geoResult, error) {
		return geoResult{}, errors.New("offline unavailable")
	}
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, geoMeta)
	st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"]
	if st.Ready || !st.TransportReady || st.GeoError == "" || st.PolicyError == "" {
		t.Fatalf("failed renewal retained success: %+v", st)
	}
}

func TestProfilePolicyStrictConflictingMetaClosesWithoutCachedSuccess(t *testing.T) {
	s, o := policyHarness(t, "warp")
	p := s.Proxies["warp"]
	p.PolicyMode = "strict"
	p.PolicyCountry = "DE"
	c := getCheck("warp")
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "DE"}, nil }
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, geoMeta)
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, func(context.Context, string, int) (exitParse, error) {
		return exitParse{node: "AMS", nodeCountry: "NL", country: "NL"}, nil
	})
	st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"]
	if st.Ready || st.PolicyReady || st.ForeignIPVerified || !st.TransportReady {
		t.Fatal("conflicting current public country retained positive independent cache")
	}
}

func TestProfilePolicyModeChangeClearsIndependentCache(t *testing.T) {
	s, o := policyHarness(t, "warp")
	p := s.Proxies["warp"]
	p.PolicyMode = "strict"
	p.PolicyCountry = "DE"
	c := getCheck("warp")
	calls := 0
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, geoMeta)
	p.PolicyMode = "off"
	NewServer(s, o).status()
	if c.snapshot().GeoCountry != "" {
		t.Fatal("mode off retained strict geography cache")
	}
	p.PolicyMode = "strict"
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, geoMeta)
	if calls != 2 {
		t.Fatal("strict mode resurrected pre-off evidence")
	}
}

func TestProfilePolicyStrictRelayDoesNotInferEgressFromSelection(t *testing.T) {
	p := NewProxyProfile("relay", "relay-warp")
	p.PolicyMode = "strict"
	p.PolicyCountry = "DE"
	p.RelayCountry = "DE"
	g := Settings{Proxies: map[string]*ProxySettings{"relay": p}}
	if !profileNeedsGeo("relay", p) {
		t.Fatal("strict Relay skipped independent verification")
	}
	if exitPolicyError(g, "relay", "AMS", "NL", "DE", false) == "" {
		t.Fatal("Relay endpoint selection or meta was treated as independent evidence")
	}
}
