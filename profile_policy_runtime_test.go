package main

import (
	"context"
	"testing"
	"time"
)

func TestProfilePolicyExplicitModes(t *testing.T) {
	for _, name := range proxyNames {
		for _, mode := range []string{"off", "exclusions"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				s, o := policyHarness(t, name)
				s.Proxies[name].PolicyMode = mode
				getCheck(name).geoProbe = func(context.Context, string, int) (geoResult, error) {
					t.Fatal("explicit off/exclusions must not request independent strict geography")
					return geoResult{}, nil
				}
				checkProxyWith(context.Background(), s.Snapshot(), name, o, func(context.Context, string, int) (exitParse, error) {
					return exitParse{node: "DME", nodeCountry: "RU", country: "RU"}, nil
				})
				st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)[name]
				if !st.TransportReady || st.PolicyReady != (mode == "off") {
					t.Fatalf("mode %s: %+v", mode, st)
				}
				if len(o.scanCh) != 0 {
					t.Fatal("explicit geography mismatch must not create rescan churn")
				}
			})
		}
	}
}

func TestProfilePolicyExclusionsPublicCountryUnknown(t *testing.T) {
	for _, country := range []string{"DE", "FR", "RU", "XX", ""} {
		t.Run(country, func(t *testing.T) {
			s, o := policyHarness(t, "warp")
			s.Proxies["warp"].PolicyMode = "exclusions"
			s.ExcludeCountries = "FR"
			checkProxyWith(context.Background(), s.Snapshot(), "warp", o, func(context.Context, string, int) (exitParse, error) {
				return exitParse{node: "AMS", nodeCountry: "NL", country: country}, nil
			})
			st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"]
			if !st.TransportReady || st.PolicyReady != (country == "DE") {
				t.Fatalf("country=%q %+v", country, st)
			}
			if !st.PolicyReady && st.PolicyError == "" {
				t.Fatal("fail closed requires explicit reason")
			}
		})
	}
}

func TestProfilePolicyOffChainDependencyScannerHasNoGeoFilters(t *testing.T) {
	s, o := policyHarness(t, "m2w")
	s.Proxies["m2w"].PolicyMode = "off"
	o.scanner = func(args []string, _ time.Duration) (string, error) {
		for _, arg := range args {
			if arg == "-exclude-node" || arg == "-exclude-country" {
				t.Errorf("off-only chain dependency retained geo filters: %v", args)
			}
		}
		return "192.0.2.1:2408", nil
	}
	o.scanWarpProfile(s.Snapshot(), "warp")
}

func TestProfilePolicyOffScannerHasNoGeoFilters(t *testing.T) {
	s, o := policyHarness(t, "warp")
	s.Proxies["warp"].PolicyMode = "off"
	s.Proxies["warp"].Endpoint = ""
	s.ExcludeNodes = "AMS"
	s.ExcludeCountries = "DE"
	o.scanner = func(args []string, _ time.Duration) (string, error) {
		for _, arg := range args {
			if arg == "-exclude-node" || arg == "-exclude-country" {
				t.Errorf("off retained geography scan filter: %v", args)
			}
		}
		return "192.0.2.1:2408", nil
	}
	o.scanWarpProfile(s.Snapshot(), "warp")
}
