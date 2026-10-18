package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUnconfiguredRelayOnlyDoesNotLoadAccountOrStartEngine(t *testing.T) {
	isolateStatusState(t)
	p := NewProxyProfile("child", "relay-warp")
	p.Enabled = true
	p.RelayID = "r"
	p.RelayCountry = "DE"
	s := &Settings{ProfileSchema: 1, Proxies: map[string]*ProxySettings{"child": p}, Relays: map[string]*RelaySettings{"r": {Key: "r", Enabled: true}}}
	o := NewOrchestrator(s)
	called := false
	o.accountLoader = func() (warpAccount, error) { called = true; return warpAccount{}, nil }
	err := o.applyCurrent()
	if called || mihomo.Running() || err == nil || !strings.Contains(err.Error(), "credential required") {
		t.Fatalf("unconfigured Relay started unrelated engine/account: called=%v running=%v err=%v", called, mihomo.Running(), err)
	}
}

func TestAWGPinnedEngineParsesRenderedProfiles(t *testing.T) {
	binary := os.Getenv("WARPD_TEST_PINNED_MIHOMO")
	if binary == "" {
		t.Skip("set WARPD_TEST_PINNED_MIHOMO for offline pinned-engine parser acceptance")
	}
	g := Settings{ProfileSchema: 1, Proxies: map[string]*ProxySettings{}}
	for i, id := range []string{"first", "second"} {
		p := NewProxyProfile(id, "warp")
		p.Port = 4600 + i
		p.Enabled = true
		p.Endpoint = "192.0.2.1:2408"
		p.AWG.JC = 6 + i
		p.AWG.I1 = "<r 2><b 0x01020304>"
		g.Proxies[id] = p
	}
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	conf := mihomoConfText(g, endpoints{}, warpAccount{PrivateKey: key, PeerPub: key, IPv4: "172.16.0.2"})
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(conf), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "-t", "-d", dir, "-f", path).CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigratedChainWithoutStandaloneProfilesScansPrivateDependencies(t *testing.T) {
	stateSetEndpoints(endpoints{})
	t.Cleanup(func() { stateSetEndpoints(endpoints{}) })
	p := NewProxyProfile("m2w", "legacy-m2w")
	p.Enabled = true
	p.Port = 4500
	s := &Settings{ProfileSchema: 1, Proxies: map[string]*ProxySettings{"m2w": p}}
	o := NewOrchestrator(s)
	calls := 0
	o.scanner = func([]string, time.Duration) (string, error) {
		calls++
		if calls == 1 {
			return "192.0.2.1:2408", nil
		}
		return "192.0.2.2:443", nil
	}
	o.runScan("m2w", "test")
	ep, outer := profileLiveEndpoint("m2w", p, stateGetEndpoints())
	if ep != "192.0.2.1:2408" || outer != "192.0.2.2:443" {
		t.Fatalf("migrated dependencies missing: %s %s", ep, outer)
	}
}

func TestDynamicHealthUsesPrivateProbeAndClosesOnPolicyChange(t *testing.T) {
	isolateStatusState(t)
	reserved, _ := net.Listen("tcp", "127.0.0.1:0")
	publicPort := reserved.Addr().(*net.TCPAddr).Port
	reserved.Close()
	p := NewProxyProfile("unique", "warp")
	p.Enabled = true
	p.Listen = "127.0.0.1"
	p.Port = publicPort
	p.Endpoint = "192.0.2.1:2408"
	s := &Settings{ProfileSchema: 1, CheckPeriod: 300, Proxies: map[string]*ProxySettings{"unique": p}}
	o := NewOrchestrator(s)
	defer o.closeProfileGates()
	m, ready := newTestMihomo(t)
	mihomo = m
	if err := m.Ensure(s.Snapshot(), stateGetEndpoints(), warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	probe := func(_ context.Context, listen string, port int) (exitParse, error) {
		if listen != "127.0.0.1" || port == publicPort {
			t.Errorf("candidate probe reached public listener %s:%d", listen, port)
		}
		return exitParse{node: "AMS", nodeCountry: "NL", country: "RU"}, nil
	}
	checkProxyWith(context.Background(), s.Snapshot(), "unique", o, probe)
	if !o.profileGateOpen("unique") {
		t.Fatal("verified profile public listener not opened")
	}
	// A non-rendered global filter edit must invalidate health and close streams.
	s.ExcludeCountries = "NL"
	o.accountLoader = func() (warpAccount, error) { return warpAccount{}, nil }
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	if o.profileGateOpen("unique") {
		t.Fatal("changed policy left public listener open")
	}
	before := getCheck("unique").snapshot().Checks
	checkProxyWith(context.Background(), s.Snapshot(), "unique", o, func(context.Context, string, int) (exitParse, error) {
		s.ExcludeNodes = "FRA"
		return exitParse{node: "AMS", nodeCountry: "NL"}, nil
	})
	if getCheck("unique").snapshot().Checks != before {
		t.Fatal("late health from stale effective policy recorded")
	}
}

func TestDynamicScanIndependentAndRejectsStalePolicy(t *testing.T) {
	stateSetEndpoints(endpoints{})
	t.Cleanup(func() { stateSetEndpoints(endpoints{}) })
	s := &Settings{ScanSample: 2, ScanJobs: 1, Proxies: map[string]*ProxySettings{}}
	for i, id := range []string{"one", "two"} {
		p := NewProxyProfile(id, "warp")
		p.Enabled = true
		p.Port = 4300 + i
		s.Proxies[id] = p
	}
	if got := scanTargets(s.Snapshot(), "both", false); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("dynamic scans not resolved: %v", got)
	}
	o := NewOrchestrator(s)
	calls := 0
	o.scanner = func(args []string, _ time.Duration) (string, error) {
		calls++
		if calls == 1 {
			s.mutex().Lock()
			s.Proxies["one"].ExcludeCountries = "BY"
			s.mutex().Unlock()
			return "192.0.2.1:2408", nil
		}
		return "192.0.2.2:2408", nil
	}
	o.runScan("both", "test")
	if ep, _ := profileLiveEndpoint("one", s.Proxies["one"], stateGetEndpoints()); ep != "" {
		t.Fatal("scan from stale exclusion revision published")
	}
	if ep, _ := profileLiveEndpoint("two", s.Proxies["two"], stateGetEndpoints()); ep != "192.0.2.2:2408" {
		t.Fatalf("independent profile lost result: %q", ep)
	}
}

func TestDynamicRendererTwoWarpProfilesIndependent(t *testing.T) {
	g := mihomoSettings()
	g.Proxies["warp"].Enabled = true
	g.Proxies["warp"].Endpoint = "192.0.2.1:2408"
	p := NewProxyProfile("other", "warp")
	p.Enabled = true
	p.Port = 4100
	p.Endpoint = "192.0.2.2:2408"
	p.AWG.JC = 9
	g.Proxies["other"] = p
	conf := mihomoConfText(g, endpoints{}, warpAccount{})
	if !strings.Contains(conf, "192.0.2.2") || !strings.Contains(conf, "port: 4100") || !strings.Contains(conf, "jc: 9") {
		t.Fatalf("second profile/settings missing:\n%s", conf)
	}
	if !strings.Contains(conf, "192.0.2.1") {
		t.Fatal("first sibling lost")
	}
}
func TestDynamicPolicyLocalUnion(t *testing.T) {
	g := mihomoSettings()
	g.Proxies["warp"].ExcludeNodes = "FRA"
	g.Proxies["warp"].ExcludeCountries = "BY"
	g.ExcludeCountries = "DE"
	if exitPolicyError(g, "warp", "FRA", "FR", "RU") == "" {
		t.Fatal("local banned node accepted")
	}
	if exitPolicyError(g, "warp", "MSQ", "BY", "RU") == "" {
		t.Fatal("local banned country accepted")
	}
	if exitPolicyError(g, "warp", "BER", "DE", "RU") == "" {
		t.Fatal("global banned country erased")
	}
}
