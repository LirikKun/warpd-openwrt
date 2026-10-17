package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func offlineGeo(context.Context, string, int) (geoResult, error) {
	return geoResult{}, errors.New("offline geo fixture")
}

func geoMeta(context.Context, string, int) (exitParse, error) {
	return exitParse{node: "FRA", nodeCountry: "DE", country: "DE"}, nil
}

func TestGeoTransportFailureInvalidatesPriorObservation(t *testing.T) {
	s, o := policyHarness(t, "m2w")
	c := getCheck("m2w")
	calls := 0
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	recordProxyResult(s.Snapshot(), "m2w", o, exitParse{}, errors.New("fixture transport loss"), time.Now())
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	if calls != 2 {
		t.Fatalf("transport failure retained positive geo cache: requests=%d", calls)
	}
}

func TestGeoPolicyChangeCannotResurrectOldPositive(t *testing.T) {
	s, o := policyHarness(t, "m2w")
	c := getCheck("m2w")
	calls := 0
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	s.ExcludeCountries = "PL"
	if st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["m2w"]; st.Ready || st.ForeignIPVerified {
		t.Fatal("old policy observation remained ready")
	}
	s.ExcludeCountries = ""
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	if calls != 2 {
		t.Fatalf("policy reversion resurrected old positive: requests=%d", calls)
	}
}

func TestGeoCachesObservationForSixtySeconds(t *testing.T) {
	s, o := policyHarness(t, "m2w")
	c := getCheck("m2w")
	calls := 0
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	if calls != 1 {
		t.Fatalf("geo requests=%d, want one within cache TTL", calls)
	}
	c.set(func(st *ProxyState) { st.GeoCheck = time.Now().Add(-60 * time.Second).Format(time.RFC3339Nano) })
	c.geoProbe = func(context.Context, string, int) (geoResult, error) {
		calls++
		return geoResult{}, errors.New("fixture 429")
	}
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["m2w"]
	if calls != 2 || st.Ready || st.ForeignIPVerified || !st.TransportReady || st.FailStreak != 0 || st.GeoError == "" || st.Error != "" {
		t.Fatalf("failed renewal reused success or damaged health: calls=%d %+v", calls, st)
	}
	if len(o.scanCh) != 0 || healthRoundRestartDue(s.Snapshot(), []string{"m2w"}, 7, time.Now().Add(-time.Second), time.Time{}, time.Now()) {
		t.Fatal("geo failure caused recovery")
	}
}

func TestGeoCacheNeverCertifiesInvalidCurrentState(t *testing.T) {
	cases := []string{"CF RU", "CF unknown", "node RU", "node excluded", "disabled", "checking", "stale", "geo expired", "generation", "endpoint", "outer", "listen", "port", "pending config", "ownership"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			s, o := policyHarness(t, "m2w")
			c := getCheck("m2w")
			c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "DE"}, nil }
			checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
			switch name {
			case "CF RU":
				c.set(func(st *ProxyState) { st.ExitCountry = "RU" })
			case "CF unknown":
				c.set(func(st *ProxyState) { st.ExitCountry = "XX" })
			case "node RU":
				c.set(func(st *ProxyState) { st.ExitNodeCountry = "RU" })
			case "node excluded":
				s.ExcludeNodes = "FRA"
			case "disabled":
				s.Proxies["m2w"].Enabled = false
			case "checking":
				c.set(func(st *ProxyState) { st.Checking = true })
			case "stale":
				c.set(func(st *ProxyState) { st.LastCheck = time.Now().Add(-time.Hour).Format(time.RFC3339Nano) })
			case "geo expired":
				c.set(func(st *ProxyState) { st.GeoCheck = time.Now().Add(-60 * time.Second).Format(time.RFC3339Nano) })
			case "generation":
				mihomo.mu.Lock()
				mihomo.generation++
				mihomo.mu.Unlock()
			case "endpoint":
				e := stateGetEndpoints()
				e.warp = "192.0.2.99:2408"
				stateSetEndpoints(e)
			case "outer":
				e := stateGetEndpoints()
				e.masque["masque"] = "192.0.2.99:8443"
				stateSetEndpoints(e)
			case "listen":
				s.Proxies["m2w"].Listen = "127.0.0.2"
			case "port":
				s.Proxies["m2w"].Port++
			case "pending config":
				s.Proxies["warp"].Enabled = true
			case "ownership":
				mihomo.mu.Lock()
				mihomo.ownership = func(int, []mihomoListener) error { return errors.New("fixture ownership lost") }
				mihomo.mu.Unlock()
			}
			st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["m2w"]
			if st.Ready || st.PolicyReady || st.ForeignIPVerified {
				t.Fatalf("cached positive certified %s: %+v", name, st)
			}
		})
	}
}

func TestGeoDoesNotQueryIneligibleMeta(t *testing.T) {
	for _, res := range []exitParse{{node: "FRA", nodeCountry: "DE", country: "RU"}, {node: "FRA", nodeCountry: "DE", country: "XX"}, {node: "DME", nodeCountry: "DE", country: "DE"}, {node: "LED", nodeCountry: "RU", country: "DE"}, {node: "FRA", country: "DE"}} {
		t.Run(res.node+res.nodeCountry+res.country, func(t *testing.T) {
			s, o := policyHarness(t, "m2w")
			c := getCheck("m2w")
			calls := 0
			c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
			checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
			checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, func(context.Context, string, int) (exitParse, error) { return res, nil })
			if calls != 1 || c.snapshot().GeoCheck != "" {
				t.Fatalf("unsuitable meta queried geo or retained cache: calls=%d %+v", calls, c.snapshot())
			}
		})
	}
}

func TestGeoCacheKeyRequiresEveryConfigurationIdentity(t *testing.T) {
	for _, field := range []string{"generation", "endpoint", "outer", "listen", "port", "exclude nodes", "exclude countries"} {
		t.Run(field, func(t *testing.T) {
			s, o := policyHarness(t, "m2w")
			c := getCheck("m2w")
			calls := 0
			c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
			checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
			c.set(func(st *ProxyState) {
				switch field {
				case "generation":
					st.geoKey.generation++
				case "endpoint":
					st.geoKey.endpoint += "old"
				case "outer":
					st.geoKey.outer += "old"
				case "listen":
					st.geoKey.listen += "old"
				case "port":
					st.geoKey.port++
				case "exclude nodes":
					st.geoKey.excludedNodes = "AMS"
				case "exclude countries":
					st.geoKey.excludedCountries = "PL"
				}
			})
			checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
			if calls != 2 {
				t.Fatalf("cache reused despite %s change: %d", field, calls)
			}
		})
	}
}

func TestGeoEndpointMutationInvalidatesImmediately(t *testing.T) {
	s, o := policyHarness(t, "m2w")
	c := getCheck("m2w")
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "DE"}, nil }
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uci"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	NewServer(s, o).refreshProxyPin("m2w")
	if st := c.snapshot(); st.GeoCheck != "" {
		t.Fatalf("endpoint mutation retained independent cache: %+v", st)
	}
}

func TestGeoService429IsPolicyFailureOnly(t *testing.T) {
	s, o := policyHarness(t, "m2w")
	host, port, roots, done := geoFixture(t, func(c *tls.Conn) { fmt.Fprint(c, "HTTP/1.1 429 Too Many Requests\r\nContent-Length: 0\r\n\r\n") })
	getCheck("m2w").geoProbe = func(ctx context.Context, stringIgnored string, intIgnored int) (geoResult, error) {
		return socksGeoCheckWithRoots(ctx, host, port, roots)
	}
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, geoMeta)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["m2w"]
	if st.Ready || !st.TransportReady || st.FailStreak != 0 || st.OKChecks != 1 || st.Error != "" || st.GeoError != "geo status 429" {
		t.Fatalf("service quota failure damaged health or lost safe reason: %+v", st)
	}
}

func TestGeoM2WReadyRequiresActualIndependentObservation(t *testing.T) {
	s, o := policyHarness(t, "m2w")
	host, port, roots, done := geoFixture(t, func(c *tls.Conn) {
		fmt.Fprint(c, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n"+`{"ip":"8.8.8.8","country_iso":"DE"}`)
	})
	// The probe seam injects trust roots and a loopback SOCKS listener only.
	getCheck("m2w").geoProbe = func(ctx context.Context, listen string, p int) (geoResult, error) {
		if listen != s.Proxies["m2w"].Listen || p != s.Proxies["m2w"].Port {
			t.Fatal("wrong configured SOCKS listener")
		}
		return socksGeoCheckWithRoots(ctx, host, port, roots)
	}
	checkProxyWith(context.Background(), s.Snapshot(), "m2w", o, func(context.Context, string, int) (exitParse, error) {
		return exitParse{node: "FRA", nodeCountry: "DE", country: "DE"}, nil
	})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["m2w"]
	if !st.Ready || !st.ForeignIPVerified {
		t.Fatalf("actual independent non-RU plus /meta not ready: %+v", st)
	}
	if st.GeoCountry != "DE" || st.GeoCheck == "" || st.GeoError != "" {
		t.Fatalf("missing safe geo status: %+v", st)
	}
}
