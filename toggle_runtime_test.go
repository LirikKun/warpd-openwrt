package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestToggleQueuesNewDependencyAfterBusyScanCompletes(t *testing.T) {
	srv, _ := toggleRuntime(t)
	for _, key := range []string{"warp", "masque"} {
		if err := uciSet("warpd."+key+".endpoint", ""); err != nil {
			t.Fatal(err)
		}
		srv.refreshProxyPin(key)
	}
	stateSetEndpoints(endpoints{masque: map[string]string{}})
	o := srv.orch
	o.onApply = func(string) { _ = o.applyCurrent() }
	if w := toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":true}`); w.Code < 400 {
		t.Fatal("missing warp reported applied")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	o.scanner = func([]string, time.Duration) (string, error) { close(entered); <-release; return "192.0.2.9:2408", nil }
	done := make(chan struct{})
	req := <-o.scanCh
	go func() { o.runRequest(req); close(done) }()
	<-entered
	if w := toggleRequest(srv, "/api/proxy", `{"proxy":"masque","enabled":true}`); w.Code < 400 {
		t.Fatal("missing masque reported applied")
	}
	close(release)
	<-done
	if len(o.scanCh) != 1 {
		t.Fatal("enable during active scan left new dependency unscheduled")
	}
	next := <-o.scanCh
	if len(next.targets) != 1 || next.targets[0] != "masque" {
		t.Fatalf("wrong follow-up scans: %v", next.targets)
	}
}

func TestToggleScanDiscardsResultWhenNoConsumerRemains(t *testing.T) {
	isolateStatusState(t)
	s := orchestratorSettings()
	o := NewOrchestrator(s)
	before := stateGetEndpoints().warp
	applied := false
	o.onApply = func(string) { applied = true }
	o.scanner = func([]string, time.Duration) (string, error) {
		s.mu.Lock()
		for _, p := range s.Proxies {
			p.Enabled = false
		}
		s.mu.Unlock()
		return "192.0.2.99:2408", nil
	}
	o.runScan("warp", "test")
	if got := stateGetEndpoints().warp; got != before || applied {
		t.Fatalf("obsolete result published: endpoint=%q callback=%v", got, applied)
	}
}

func TestToggleStartupQueuesChainOnlyDependencies(t *testing.T) {
	s := orchestratorSettings()
	for _, p := range s.Proxies {
		p.Enabled = p.Key == "m2w"
	}
	isolateStatusState(t)
	stateSetEndpoints(endpoints{masque: map[string]string{}})
	o := NewOrchestrator(s)
	if !o.queueMissing("startup") {
		t.Fatal("chain-only startup did not queue scans")
	}
	req := <-o.scanCh
	if strings.Join(req.targets, ",") != "warp,masque" {
		t.Fatalf("startup dependencies=%v", req.targets)
	}
}

func TestToggleChainRetainsInternalDependenciesWithStandaloneListenersDisabled(t *testing.T) {
	srv, m := toggleRuntime(t)
	for _, pin := range []string{"inner_endpoint", "outer_endpoint"} {
		if err := uciSet("warpd.m2w."+pin, ""); err != nil {
			t.Fatal(err)
		}
	}
	srv.refreshProxyPin("m2w")
	stateSetEndpoints(endpoints{warp: "192.0.2.1:2408", masque: map[string]string{"masque": "192.0.2.2:443"}})
	if w := toggleRequest(srv, "/api/proxy", `{"proxy":"m2w","enabled":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	m.mu.Lock()
	conf := m.lastConf
	m.mu.Unlock()
	if !strings.Contains(conf, `name: "M2W OUTER"`) || !strings.Contains(conf, `dialer-proxy: "M2W OUTER"`) || !strings.Contains(conf, "  - name: l4005\n") || strings.Contains(conf, "  - name: l4001\n") || strings.Contains(conf, "  - name: l4003\n") {
		t.Fatalf("incorrect chain dependencies/listeners: %s", conf)
	}
	if len(srv.orch.scanCh) != 0 {
		t.Fatal("available dependencies caused unnecessary scans")
	}
}

func TestToggleScanRevalidatesEachDependency(t *testing.T) {
	s := orchestratorSettings()
	for _, p := range s.Proxies {
		p.Enabled = p.Key == "m2w"
	}
	o := NewOrchestrator(s)
	var protocols []string
	o.scanner = func(args []string, _ time.Duration) (string, error) {
		for i, arg := range args {
			if arg == "-p" {
				protocols = append(protocols, args[i+1])
			}
		}
		s.mu.Lock()
		s.Proxies["m2w"].Enabled = false
		s.mu.Unlock()
		return "", errors.New("cancelled configuration")
	}
	o.runScan("m2w", "test")
	if strings.Join(protocols, ",") != "awg" {
		t.Fatalf("disabled chain continued queued dependencies: %v", protocols)
	}
}

func TestToggleScanCallbackUsesLatestDesiredSettings(t *testing.T) {
	srv, m := toggleRuntime(t)
	if w := toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// A callback already waiting for the shared apply lock must not capture an
	// enabled snapshot before a disable owns the lock and updates desired state.
	o := srv.orch
	o.onApply = func(string) {
		if err := o.applyCurrent(); err != nil {
			t.Error(err)
		}
	}
	o.applyMu.Lock()
	done := make(chan struct{})
	go func() { o.applyLive("warp"); close(done) }()
	if _, err := srv.cfg.ApplyConfigPatch(map[string]any{"warp_enabled": false}); err != nil {
		o.applyMu.Unlock()
		t.Fatal(err)
	}
	if err := o.applyLocked(); err != nil {
		o.applyMu.Unlock()
		t.Fatal(err)
	}
	o.applyMu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("scan callback blocked")
	}
	if m.Running() {
		t.Fatal("queued callback resurrected disabled proxy")
	}
}

func TestToggleHealthDiscardsInFlightDisabledResult(t *testing.T) {
	srv, _ := toggleRuntime(t)
	if w := toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		checkProxyWith(context.Background(), srv.cfg.Snapshot(), "warp", srv.orch, func(context.Context, string, int) (exitParse, error) {
			close(entered)
			<-release
			return exitParse{}, errors.New("stale")
		})
	}()
	<-entered
	if w := toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":false}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	close(release)
	<-done
	if st := getCheck("warp").snapshot(); st.Checks != 0 || st.Rescans != 0 || st.Checking {
		t.Fatalf("disabled in-flight result recorded: %+v", st)
	}
}
