//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Export exact production serializers. All observations are synthetic loopback
// fixture evidence, not a live provider/tunnel. Browser tests may freeze Date at
// last_check but must not rewrite these DTOs to force a preferred presentation.
func TestProfilePolicyUIFixtures(t *testing.T) {
	dir := os.Getenv("WARPD_POLICY_UI_FIXTURES")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"strict-ready", "strict-mismatch", "strict-missing-geo", "exclusions-rejected", "off-ru-operational", "disabled", "persisted-unapplied"} {
		t.Run(scenario, func(t *testing.T) {
			o, _, s, _, _ := gateReviewFixture(t, "echo")
			p := s.Proxies["review"]
			p.PolicyMode = "strict"
			p.PolicyCountry = "DE"
			c := getCheck("review")
			c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "DE"}, nil }
			meta := geoMeta
			switch scenario {
			case "strict-mismatch":
				c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "NL"}, nil }
			case "strict-missing-geo":
				c.geoProbe = func(context.Context, string, int) (geoResult, error) {
					return geoResult{}, errors.New("offline fixture: geography unavailable")
				}
			case "exclusions-rejected":
				p.PolicyMode = "exclusions"
				p.ExcludeCountries = "DE"
			case "off-ru-operational":
				p.PolicyMode = "off"
				meta = func(context.Context, string, int) (exitParse, error) {
					return exitParse{node: "DME", nodeCountry: "RU", country: "RU"}, nil
				}
			}
			checkProxyWith(context.Background(), s.Snapshot(), "review", o, meta)
			switch scenario {
			case "disabled":
				p.Enabled = false
				o.syncProfileGatesLocked(s.Snapshot())
			case "persisted-unapplied":
				p.Endpoint = "192.0.2.2:2408"
				o.syncProfileGatesLocked(s.Snapshot())
			}
			srv := NewServer(s, o)
			status := srv.status()
			st := status["proxies"].(map[string]ProxyState)["review"]
			wantReady := scenario == "strict-ready" || scenario == "off-ru-operational"
			if st.Ready != wantReady {
				t.Fatalf("fixture state %s ready=%v wanted %v", scenario, st.Ready, wantReady)
			}
			if wantReady {
				if checked, err := time.Parse(time.RFC3339Nano, st.LastCheck); err != nil || time.Since(checked) > time.Second {
					t.Fatal("fixture is not a fresh real production result")
				}
			}
			for suffix, payload := range map[string]any{"status": status, "profiles": srv.profilePayload(s.Snapshot())} {
				raw, err := json.MarshalIndent(payload, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, scenario+"."+suffix+".json"), append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
