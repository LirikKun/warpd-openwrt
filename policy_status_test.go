package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPolicyMetaSeparatesPublicAndNodeCountries(t *testing.T) {
	// Same /meta shape as the observed RU-public-IP/foreign-colo response.
	wire := "HTTP/1.1 200 OK\r\n\r\n" + `{"country":"RU","colo":{"iata":"FRA","city":"Frankfurt","cca2":"DE"}}`
	got, err := readMetaResponse(bufio.NewReader(strings.NewReader(wire)))
	if err != nil {
		t.Fatal(err)
	}
	if got.country != "RU" {
		t.Fatalf("public IP country=%q, want RU", got.country)
	}
	if got.nodeCountry != "DE" {
		t.Fatalf("lost colo.cca2 node country: %+v", got)
	}
}

// Synthetic applied-child seam only: this certifies policy logic, not a live tunnel.
func policyHarness(t *testing.T, name string) (*Settings, *Orchestrator) {
	t.Helper()
	oldM, oldE := mihomo, stateGetEndpoints()
	checksMu.Lock()
	oldChecks := checks
	checks = map[string]*ProxyCheck{}
	checksMu.Unlock()
	getCheck("m2w").geoProbe = offlineGeo
	s := &Settings{CheckPeriod: 10, FailRestart: 3, FailRescan: 5, DMERescanGap: 600, Proxies: map[string]*ProxySettings{}}
	for _, n := range proxyNames {
		p := (&ProxySettings{}).defaults(n)
		p.Enabled = n == name
		s.Proxies[n] = p
	}
	stateSetEndpoints(endpoints{warp: "192.0.2.1:2408", masque: map[string]string{"masque": "192.0.2.2:8443", "masqueh2": "192.0.2.3:443"}})
	mihomo = &MihomoMan{running: true, pid: 12345, generation: 7, ownership: func(int, []mihomoListener) error { return nil }}
	mihomo.lastConf = mihomoConfText(s.Snapshot(), stateGetEndpoints(), warpAccount{})
	mihomo.desiredConf = mihomo.lastConf
	t.Cleanup(func() { mihomo = oldM; stateSetEndpoints(oldE); checksMu.Lock(); checks = oldChecks; checksMu.Unlock() })
	return s, NewOrchestrator(s)
}

func policyProbe(t *testing.T, s *Settings, o *Orchestrator, name, body string) map[string]any {
	t.Helper()
	checkProxyWith(context.Background(), s.Snapshot(), name, o, func(context.Context, string, int) (exitParse, error) {
		return readMetaResponse(bufio.NewReader(strings.NewReader("HTTP/1.1 200 OK\r\n\r\n" + body)))
	})
	recorder := httptest.NewRecorder()
	NewServer(s, o).handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/status", nil))
	if recorder.Code != 200 {
		t.Fatalf("status API returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Proxies map[string]map[string]any `json:"proxies"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Proxies[name]
}

func TestPolicyWarpReadyWithForeignColoAndRUPublicIP(t *testing.T) {
	s, o := policyHarness(t, "warp")
	p := policyProbe(t, s, o, "warp", `{"country":"RU","colo":{"iata":"FRA","city":"Frankfurt","cca2":"DE"}}`)
	if p["transport_ready"] != true || p["policy_ready"] != true || p["ready"] != true {
		t.Fatalf("foreign colo must satisfy WARP policy despite RU public IP: %+v", p)
	}
	if p["exit_node_country"] != "DE" || p["exit_country"] != "RU" {
		t.Fatalf("countries conflated: %+v", p)
	}
	if p["checks"] != float64(1) || p["ok_checks"] != float64(1) || p["fail_streak"] != float64(0) {
		t.Fatalf("transport counters changed: %+v", p)
	}
	if len(o.scanCh) != 0 {
		t.Fatal("suitable node triggered scan")
	}
}

func TestPolicyWarpRejectsExcludedOrUnknownColo(t *testing.T) {
	for _, tc := range []struct{ name, node, country, excludedNodes, excludedCountries, wantError string }{
		{"default RU despite LED", "LED", "RU", "", "", "excluded node country RU"},
		{"unknown country", "FRA", "", "", "", "node country unknown"},
		{"malformed country", "FRA", "???", "", "", "node country unknown"},
		{"unknown two letter code", "FRA", "QQ", "", "", "node country unknown"},
		{"user PL", "WAW", "PL", "", " PL , kz ", "excluded node country PL"},
		{"user node", "AMS", "NL", " ams , svo ", "", "excluded node AMS"},
		{"built-in node", "DME", "DE", "", "", "excluded node DME"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, o := policyHarness(t, "warp")
			s.ExcludeNodes = tc.excludedNodes
			s.ExcludeCountries = tc.excludedCountries
			body := `{"country":"US","colo":{"iata":"` + tc.node + `","cca2":"` + tc.country + `"}}`
			started := time.Now().Add(-time.Second)
			p := policyProbe(t, s, o, "warp", body)
			if p["transport_ready"] != true || p["policy_ready"] != false || p["ready"] != false || p["policy_error"] != tc.wantError {
				t.Fatalf("bad colo falsely suitable: %+v", p)
			}
			if p["ok_checks"] != float64(1) || p["fail_streak"] != float64(0) || p["error"] != "" {
				t.Fatalf("policy failure must not count as transport failure: %+v", p)
			}
			if healthRoundRestartDue(s.Snapshot(), []string{"warp"}, 7, started, time.Time{}, time.Now()) {
				t.Fatal("healthy unsuitable transport scheduled restart")
			}
			if len(o.scanCh) != 1 || p["rescans"] != float64(1) {
				t.Fatalf("expected one WARP rescan, state=%+v queued=%d", p, len(o.scanCh))
			}
			policyProbe(t, s, o, "warp", body)
			if len(o.scanCh) != 1 || getCheck("warp").snapshot().Rescans != 1 {
				t.Fatal("policy checks flood reserved scan queue")
			}
		})
	}
}
