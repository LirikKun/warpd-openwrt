package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestScanWarpExclusionsAreSingleAdditiveFlags(t *testing.T) {
	for _, tt := range []struct {
		name, nodes, countries, wantNodes, wantCountries string
	}{
		{"defaults", "", "", "DME", "RU"},
		{"normalized-additive", " svo, DME, waw,,svo ", " kz,ru, PL,,kz ", "DME,SVO,WAW", "RU,KZ,PL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := orchestratorSettings()
			s.ExcludeNodes, s.ExcludeCountries = tt.nodes, tt.countries
			o := NewOrchestrator(s)
			var got []string
			o.scanner = func(args []string, _ time.Duration) (string, error) {
				got = append([]string(nil), args...)
				return "", errors.New("offline fixture")
			}
			o.scanWarp(s.Snapshot())
			for flag, want := range map[string]string{"-exclude-node": tt.wantNodes, "-exclude-country": tt.wantCountries} {
				var values []string
				for i, arg := range got {
					if arg == flag && i+1 < len(got) {
						values = append(values, got[i+1])
					}
				}
				if !reflect.DeepEqual(values, []string{want}) {
					t.Errorf("real scan argv %s = %q; want exactly [%q]; argv=%q", flag, values, want, got)
				}
			}
		})
	}
}

// Fixtures mirror writeFullReport/writeRows/writeNodePicks in warpscout
// v0.16.0 db4ac9ebae8d942191b8e8351f2c3a37a375bd66, report.go:305-361,615-620.
// Our scans always use -P, without speed columns.
const scanReportHeader = "# WARP endpoints: 3 working / 4 probed\n# sorted by in-tunnel loss, then in-tunnel ping\n\n"

func scanReportRow(endpoint, ping, tunPing, loss string) string {
	return fmt.Sprintf("%-22s %-13s %s%s%-10s %-6s %s\n", endpoint, ping,
		fmt.Sprintf("%-9s %-6s ", tunPing, loss), "", "DE", "FRA", "Germany, Frankfurt")
}

func scanReportTable(rows ...string) string {
	header := fmt.Sprintf("%-22s %-13s %s%s%-10s %-6s %s\n", "ENDPOINT", "ENDPOINT PING",
		fmt.Sprintf("%-9s %-6s ", "TUN PING", "LOSS"), "", "SEEN AS", "NODE", "NODE LOCATION")
	return header + strings.Join(rows, "")
}

func writeScanReport(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBestUnderPingWorkingSectionOnly(t *testing.T) {
	working := scanReportTable(scanReportRow("192.0.2.1:2408", "200ms", "10ms", "0%"))
	torn := "\n# 1 torn down (handshake ok, data flowed, then cut and never recovered)\n" +
		scanReportTable(scanReportRow("192.0.2.2:2408", "5ms", "20ms", "0%"))
	summary := "\n# Best endpoint per node (lowest in-tunnel loss, then in-tunnel ping)\n" +
		fmt.Sprintf("%-6s %-22s %-13s %s%s%-10s %s\n", "NODE", "ENDPOINT", "ENDPOINT PING", "TUN PING  LOSS   ", "", "SEEN AS", "NODE LOCATION") +
		fmt.Sprintf("%-6s %-22s %-13s %s%s%-10s %s\n", "FRA", "192.0.2.1:2408", "200ms", "10ms      0%     ", "", "DE", "Germany, Frankfurt")
	for _, tt := range []struct{ name, text, want string }{
		{"torn-after-working", scanReportHeader + working + torn + summary, ""},
		{"only-torn", "# WARP endpoints: 0 working / 1 probed\n" + torn, ""},
		{"summary-not-working", scanReportHeader + summary, ""},
		{"truncated-working-row", scanReportHeader + scanReportTable("192.0.2.3:2408 5ms junk\n"), ""},
		{"bad-tunnel-latency", scanReportHeader + scanReportTable(scanReportRow("192.0.2.3:2408", "5ms", "junk", "0%")), ""},
		{"bad-loss", scanReportHeader + scanReportTable(scanReportRow("192.0.2.3:2408", "5ms", "1ms", "junk")), ""},
		{"original-ranking-not-lowest-host-ping", scanReportHeader + scanReportTable(
			scanReportRow("192.0.2.1:2408", "200ms", "1ms", "0%"),
			scanReportRow("192.0.2.2:2408", "90ms", "2ms", "0%"),
			scanReportRow("192.0.2.3:2408", "5ms", "3ms", "0%")), "192.0.2.2:2408"},
		{"submillisecond", scanReportHeader + scanReportTable(scanReportRow("192.0.2.3:2408", "0.35ms", "?", "-")), "192.0.2.3:2408"},
		{"unknown-host-ping", scanReportHeader + scanReportTable(scanReportRow("192.0.2.3:2408", "?", "1ms", "0%")), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := bestUnderPing(writeScanReport(t, tt.text), 100); got != tt.want {
				t.Errorf("bestUnderPing = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestScanReportFreshnessAndCleanup(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	stateMu.Lock()
	old := stateEp
	stateEp = endpoints{masque: map[string]string{}}
	stateMu.Unlock()
	t.Cleanup(func() { stateMu.Lock(); stateEp = old; stateMu.Unlock() })
	for _, key := range []string{"warp", "masque", "masqueh2"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", key, failed), func(t *testing.T) {
				s := orchestratorSettings()
				s.MaxPing = 100
				o := NewOrchestrator(s)
				scan := func() {
					if key == "warp" {
						o.scanWarp(s.Snapshot())
					} else {
						o.scanMasque(s.Snapshot(), key)
					}
				}
				// Discover the actual command's report path, not a parallel test-only builder.
				var report string
				o.scanner = func(args []string, _ time.Duration) (string, error) {
					for i, arg := range args {
						if arg == "-o" {
							report = args[i+1]
						}
					}
					return "", errors.New("offline initial attempt")
				}
				scan()
				if report == "" {
					t.Fatal("scan has no report output argument")
				}
				if filepath.Dir(report) != os.TempDir() {
					t.Fatalf("report %q ignores temporary directory %q; cannot safely isolate scans", report, os.TempDir())
				}
				t.Cleanup(func() { os.Remove(report) })
				stale := scanReportHeader + scanReportTable(scanReportRow("192.0.2.99:2408", "1ms", "1ms", "0%"))
				if err := os.WriteFile(report, []byte(stale), 0600); err != nil {
					t.Fatal(err)
				}
				before := stateGetEndpoints()
				o.scanner = func([]string, time.Duration) (string, error) {
					if _, err := os.Stat(report); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("old report still exists at scanner launch: %v", err)
					}
					if failed {
						if err := os.WriteFile(report, []byte(stale), 0600); err != nil {
							t.Fatal(err)
						}
						return "", errors.New("offline failed scan with partial report")
					}
					// Upstream logs report-write errors but can still return a valid -best.
					return "192.0.2.1:2408\n", nil
				}
				scan()
				after := stateGetEndpoints()
				if failed {
					if !reflect.DeepEqual(after, before) {
						t.Errorf("failed scan changed endpoints: before=%v after=%v", before, after)
					}
				} else {
					got := after.warp
					if key != "warp" {
						got = after.masque[key]
					}
					if got != "192.0.2.1:2408" {
						t.Errorf("stale row overrode current scanner pick: %q", got)
					}
				}
				if _, err := os.Stat(report); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("report not cleaned up after scan: %v", err)
				}
			})
		}
	}
}

func TestIsEndpointLineRequiresIPAddressAndValidPort(t *testing.T) {
	for _, endpoint := range []string{"192.0.2.1:1", "192.0.2.1:65535", "[2001:db8::1]:443"} {
		if !isEndpointLine(endpoint) {
			t.Errorf("rejected valid endpoint %q", endpoint)
		}
	}
	for _, endpoint := range []string{"192.0.2.1:0", "192.0.2.1:-1", "192.0.2.1:65536", "192.0.2.1:+443", "host.example:443", "[not::ip]:443", "2001:db8::1:443", "[192.0.2.1]:443"} {
		t.Run(endpoint, func(t *testing.T) {
			if isEndpointLine(endpoint) {
				t.Errorf("accepted malformed scanner endpoint %q", endpoint)
			}
			if got := lastNonEmpty("192.0.2.5:2408\n" + endpoint + "\n"); got != "192.0.2.5:2408" {
				t.Errorf("malformed candidate replaced valid -best: %q", got)
			}
			text := scanReportHeader + scanReportTable(scanReportRow(endpoint, "5ms", "1ms", "0%"))
			if got := bestUnderPing(writeScanReport(t, text), 100); got != "" {
				t.Errorf("malformed report candidate selected: %q", got)
			}
		})
	}
}

func TestScannerChildDoesNotInheritExplicitProxies(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin", "KEEP_SCANNER_FIXTURE=unchanged", "GOMEMLIMIT=99MiB", "NO_COLOR=0", "TERM=color"}
	proxyKeys := []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}
	for _, key := range proxyKeys {
		base = append(base, key+"=http://proxy.invalid:8080")
	}
	base = append(base, "NO_PROXY=example.invalid", "no_proxy=other.invalid")
	original := append([]string(nil), base...)
	o := NewOrchestrator(orchestratorSettings())
	o.scoutCommand = func(ctx context.Context, _ []string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/usr/bin/env")
		cmd.Env = base
		return cmd
	}
	out, err := o.runWarpScout([]string{"scan"}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}
	for _, key := range proxyKeys {
		if value, ok := values[key]; ok {
			t.Errorf("child inherited %s=%q", key, value)
		}
	}
	for key, want := range map[string]string{"NO_PROXY": "*", "no_proxy": "*", "KEEP_SCANNER_FIXTURE": "unchanged", "GOMEMLIMIT": "48MiB", "NO_COLOR": "1", "TERM": "dumb"} {
		if got := values[key]; got != want {
			t.Errorf("child %s=%q; want %q", key, got, want)
		}
	}
	if !reflect.DeepEqual(base, original) {
		t.Errorf("scanner mutated supplied parent environment")
	}
}

func TestScanMaxPingRemainsSoftCap(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	stateMu.Lock()
	old := stateEp
	stateEp = endpoints{masque: map[string]string{}}
	stateMu.Unlock()
	t.Cleanup(func() { stateMu.Lock(); stateEp = old; stateMu.Unlock() })
	for _, key := range []string{"warp", "masque", "masqueh2"} {
		t.Run(key, func(t *testing.T) {
			s := orchestratorSettings()
			s.MaxPing = 100
			o := NewOrchestrator(s)
			o.scanner = func(args []string, _ time.Duration) (string, error) {
				for i, arg := range args {
					if arg == "-o" {
						text := scanReportHeader + scanReportTable(scanReportRow("192.0.2.1:2408", "200ms", "10ms", "0%")) +
							"\n# 1 torn down (handshake ok, data flowed, then cut and never recovered)\n" +
							scanReportTable(scanReportRow("192.0.2.2:2408", "5ms", "20ms", "0%"))
						if err := os.WriteFile(args[i+1], []byte(text), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				return "192.0.2.1:2408\n", nil
			}
			if key == "warp" {
				o.scanWarp(s.Snapshot())
			} else {
				o.scanMasque(s.Snapshot(), key)
			}
			e := stateGetEndpoints()
			got := e.warp
			if key != "warp" {
				got = e.masque[key]
			}
			if got != "192.0.2.1:2408" {
				t.Errorf("selected %q; want over-cap working scanner pick, not torn-down candidate", got)
			}
		})
	}
}
