package main

import "testing"

func TestPolicyEveryProxyRequiresKnownNonExcludedObservedNode(t *testing.T) {
	for _, name := range proxyNames {
		for _, tc := range []struct{ node, country, want string }{
			{"LED", "RU", "excluded node country RU"},
			{"FRA", "", "node country unknown"},
			{"WAW", "PL", "excluded node country PL"},
		} {
			t.Run(name+"/"+tc.node+"/"+tc.country, func(t *testing.T) {
				s, o := policyHarness(t, name)
				s.ExcludeCountries = "PL"
				p := policyProbe(t, s, o, name, `{"country":"RU","colo":{"iata":"`+tc.node+`","cca2":"`+tc.country+`"}}`)
				want := tc.want
				if name == "m2w" {
					want += "; public IP country RU is excluded; foreign IP not verified"
				}
				if p["transport_ready"] != true || p["policy_ready"] != false || p["ready"] != false || p["policy_error"] != want {
					t.Fatalf("node policy bypassed: %+v", p)
				}
				if name != "warp" && len(o.scanCh) != 0 {
					t.Fatal("non-WARP policy triggered scan churn")
				}
			})
		}
	}
}

func TestPolicyStandaloneMasqueForeignNodeDoesNotRequireForeignPublicIP(t *testing.T) {
	for _, name := range []string{"masque", "masqueh2"} {
		t.Run(name, func(t *testing.T) {
			s, o := policyHarness(t, name)
			p := policyProbe(t, s, o, name, `{"country":"RU","colo":{"iata":"FRA","cca2":"DE"}}`)
			if p["transport_ready"] != true || p["policy_ready"] != true || p["ready"] != true {
				t.Fatalf("standalone requires foreign IP beyond non-RU node requirement: %+v", p)
			}
			if len(o.scanCh) != 0 {
				t.Fatal("suitable standalone triggered scan")
			}
		})
	}
}
