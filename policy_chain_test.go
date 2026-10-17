package main

import "testing"

func TestPolicyM2WForeignIPFailsClosedWithoutIndependentVerification(t *testing.T) {
	for _, tc := range []struct{ country, want string }{
		{"RU", "public IP country RU is excluded; foreign IP not verified"},
		{"DE", "foreign IP not verified"},
	} {
		t.Run(tc.country, func(t *testing.T) {
			s, o := policyHarness(t, "m2w")
			p := policyProbe(t, s, o, "m2w", `{"country":"`+tc.country+`","colo":{"iata":"FRA","cca2":"DE"}}`)
			if p["transport_ready"] != true || p["policy_ready"] != false || p["ready"] != false || p["policy_error"] != tc.want {
				t.Fatalf("unverified foreign egress falsely ready: %+v", p)
			}
			if p["foreign_ip_verified"] != false {
				t.Fatalf("verification must be explicitly false: %+v", p)
			}
			if len(o.scanCh) != 0 || p["rescans"] != float64(0) {
				t.Fatal("M2W policy created automatic MASQUE scan churn")
			}
			if p["ok_checks"] != float64(1) || p["fail_streak"] != float64(0) || p["error"] != "" {
				t.Fatalf("policy failure counted as transport failure: %+v", p)
			}
		})
	}
}

func TestPolicyStandaloneMasqueRejectsRussianColoWithoutChurn(t *testing.T) {
	for _, name := range []string{"masque", "masqueh2"} {
		t.Run(name, func(t *testing.T) {
			s, o := policyHarness(t, name)
			s.ExcludeNodes = "DME"
			s.ExcludeCountries = "RU"
			p := policyProbe(t, s, o, name, `{"country":"RU","colo":{"iata":"DME","cca2":"RU"}}`)
			if p["transport_ready"] != true || p["policy_ready"] != false || p["ready"] != false || p["policy_error"] != "excluded node DME" {
				t.Fatalf("standalone MASQUE falsely satisfies node policy: %+v", p)
			}
			if len(o.scanCh) != 0 {
				t.Fatal("standalone MASQUE policy churn")
			}
		})
	}
}
