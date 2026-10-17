package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPolicyWarpRescanKeepsFiltersAndFailureCooldown(t *testing.T) {
	for _, configuredGap := range []int{1, 600} {
		t.Run((time.Duration(configuredGap) * time.Second).String(), func(t *testing.T) {
			s, o := policyHarness(t, "warp")
			s.DMERescanGap = configuredGap
			s.ExcludeNodes = "AMS"
			s.ExcludeCountries = "PL"
			now := time.Now()
			o.now = func() time.Time { return now }
			var args []string
			o.scanner = func(a []string, _ time.Duration) (string, error) {
				args = append([]string(nil), a...)
				return "", errors.New("offline failed scan fixture")
			}
			body := `{"country":"RU","colo":{"iata":"LED","cca2":"RU"}}`
			policyProbe(t, s, o, "warp", body)
			if len(o.scanCh) != 1 {
				t.Fatal("no policy rescan accepted")
			}
			o.runRequest(<-o.scanCh)
			for flag, want := range map[string]string{"-exclude-node": "DME,AMS", "-exclude-country": "RU,PL"} {
				count := 0
				for i, a := range args {
					if a == flag {
						count++
						if i+1 == len(args) || args[i+1] != want {
							t.Fatalf("lost policy filter: %v", args)
						}
					}
				}
				if count != 1 {
					t.Fatalf("%s passed %d times (last-value-wins): %v", flag, count, args)
				}
			}
			gap := time.Duration(max(60, configuredGap)) * time.Second
			now = now.Add(gap - time.Second)
			policyProbe(t, s, o, "warp", body)
			if len(o.scanCh) != 0 || getCheck("warp").snapshot().Rescans != 1 {
				t.Fatal("policy rescan bypassed cooldown after failed scan")
			}
			now = now.Add(time.Second)
			policyProbe(t, s, o, "warp", body)
			if len(o.scanCh) != 1 || getCheck("warp").snapshot().Rescans != 2 {
				t.Fatal("bounded retry not accepted at existing cooldown")
			}
		})
	}
}

func TestPolicyObsoleteExcludedResultCannotRescan(t *testing.T) {
	s, o := policyHarness(t, "warp")
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, func(context.Context, string, int) (exitParse, error) {
		mihomo.mu.Lock()
		mihomo.generation++
		mihomo.mu.Unlock()
		return exitParse{node: "LED", nodeCountry: "RU", country: "RU"}, nil
	})
	if st := getCheck("warp").snapshot(); st.Checks != 0 || st.Rescans != 0 || len(o.scanCh) != 0 {
		t.Fatalf("obsolete observation altered policy/health: %+v", st)
	}
}

func TestPolicyCurrentExclusionsApplyToInFlightObservation(t *testing.T) {
	s, o := policyHarness(t, "warp")
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, func(context.Context, string, int) (exitParse, error) {
		mu := s.mutex()
		mu.Lock()
		s.ExcludeCountries = "PL"
		mu.Unlock()
		return exitParse{node: "WAW", nodeCountry: "PL", country: "RU"}, nil
	})
	if len(o.scanCh) != 1 {
		t.Fatal("new current PL exclusion was ignored when fresh WAW observation completed")
	}
	if req := <-o.scanCh; !strings.Contains(req.reason, "excluded node country PL") {
		t.Fatalf("wrong rescan reason: %+v", req)
	}
}
