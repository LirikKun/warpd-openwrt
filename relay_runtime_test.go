package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitRelayRuntime(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("Relay runtime condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func relayRuntimeFixture(t *testing.T) (*Server, *RelayMonitor, string) {
	t.Helper()
	srv := profileAPIFixture(t)
	srv.orch.applyMu.Lock()
	err := srv.cfg.ChangeProfiles(func(g *Settings) error {
		g.Proxies = map[string]*ProxySettings{}
		g.Relays = map[string]*RelaySettings{"r_test": {Key: "r_test", Name: "Monitor only", Enabled: true, ActivationURL: "https://relay.tribukvy.ltd/activate/TEST_ONLY_CAPABILITY", SourceIP: "192.0.2.10", PathConfirmed: true, MonitorInterval: 300}}
		return nil
	})
	srv.orch.applyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return srv, NewRelayMonitor(nil, RelayMonitorOptions{}), "r_test"
}
func startTestRelayRuntime(t *testing.T, srv *Server, m *RelayMonitor) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rt := newRelayRuntime(ctx, srv.cfg, m)
	srv.relays = rt
	done := make(chan struct{})
	go func() { rt.run(); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Relay runtime did not stop")
		}
	})
	return cancel, done
}
func TestRelayRuntimeMonitorOnlyReconcilesAndActivatesWithoutAccountOrEngine(t *testing.T) {
	srv, m, id := relayRuntimeFixture(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	m.now = func() time.Time { return time.Unix(0, clock.Load()) }
	var observed, activated, accounts atomic.Int32
	m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
		observed.Add(1)
		return RelayObservation{PublicIP: "1.1.1.1"}, nil
	}
	m.activate = func(context.Context, RelayAccess) (RelayActivation, error) {
		activated.Add(1)
		return RelayActivation{Outcome: "unverified"}, nil
	}
	srv.orch.accountLoader = func() (warpAccount, error) {
		accounts.Add(1)
		t.Error("monitor-only loaded account")
		return warpAccount{}, nil
	}
	before := mihomo.Snapshot()
	startTestRelayRuntime(t, srv, m)
	waitRelayRuntime(t, func() bool { st := m.Status(id); return st.State == "debouncing" && !st.InFlight })
	clock.Add(int64(11 * time.Second))
	w := profileAPIRequest(srv, "POST", "/api/relays/"+id+"/activate", `{}`)
	if w.Code != 202 {
		t.Fatalf("manual activation=%d %s", w.Code, w.Body.String())
	}
	waitRelayRuntime(t, func() bool { st := m.Status(id); return st.State == "unverified" && !st.InFlight })
	after := mihomo.Snapshot()
	if accounts.Load() != 0 || activated.Load() != 1 || observed.Load() != 2 || before != after {
		t.Fatal("monitor-only created engine/account or duplicated requests")
	}
	row := srv.safeRelays(srv.cfg.Snapshot())[0]
	if row.Status.BindingVerified || row.Status.TunnelVerified || row.LinkedProfiles != 0 {
		t.Fatal("monitor-only fabricated binding/tunnel proof")
	}
	w = profileAPIRequest(srv, "POST", "/api/relays/"+id+"/activate", `{}`)
	if w.Code != 429 {
		t.Fatalf("manual request bypassed cooldown: %d", w.Code)
	}
	w = profileAPIRequest(srv, "PATCH", "/api/relays/"+id, `{"enabled":false}`)
	if w.Code != 200 {
		t.Fatalf("disable=%d %s", w.Code, w.Body.String())
	}
	if st := m.Status(id); st.Enabled || st.State != "disabled" {
		t.Fatalf("monitor did not pause: %+v", st)
	}
	w = profileAPIRequest(srv, "DELETE", "/api/relays/"+id, `{}`)
	if w.Code != 200 {
		t.Fatalf("delete=%d", w.Code)
	}
	if m.Status(id).State != "not_configured" {
		t.Fatal("deleted Relay remains scheduled")
	}
}
func TestRelayRuntimeDisableCancelsObservationAndRejectsLateResult(t *testing.T) {
	srv, m, id := relayRuntimeFixture(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	m.observe = func(ctx context.Context, _ RelayAccess) (RelayObservation, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return RelayObservation{PublicIP: "1.1.1.1"}, nil
	}
	m.activate = func(context.Context, RelayAccess) (RelayActivation, error) {
		t.Error("disabled Relay activated")
		return RelayActivation{}, nil
	}
	startTestRelayRuntime(t, srv, m)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("observation did not start")
	}
	w := profileAPIRequest(srv, "PATCH", "/api/relays/"+id, `{"enabled":false}`)
	if w.Code != 200 {
		t.Fatalf("disable=%d %s", w.Code, w.Body.String())
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("disable did not cancel in-flight observation")
	}
	waitRelayRuntime(t, func() bool { return !srv.relays.status(id).InFlight })
	st := m.Status(id)
	if st.Enabled || st.State != "disabled" || st.ObservedIP != "" {
		t.Fatalf("late result overwrote disabled state: %+v", st)
	}
}

func TestRelayRuntimeFailedReconfigureNeverKeepsOldAccessActive(t *testing.T) {
	srv, m, id := relayRuntimeFixture(t)
	oldURL := srv.cfg.Snapshot().Relays[id].ActivationURL
	const second = "r_second"
	err := srv.cfg.ChangeProfiles(func(g *Settings) error {
		copy := *g.Relays[id]
		copy.Key = second
		copy.Name = "Second"
		copy.ActivationURL = "https://relay.tribukvy.ltd/activate/SECOND_TEST_CAPABILITY"
		g.Relays[second] = &copy
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var released sync.Once
	finish := func() { released.Do(func() { close(release) }) }
	defer finish()
	var once sync.Once
	var newAccessCalls atomic.Int32
	m.observe = func(_ context.Context, a RelayAccess) (RelayObservation, error) {
		if a.URL == oldURL {
			newAccessCalls.Add(1)
			once.Do(func() { close(entered) })
			<-release
		}
		return RelayObservation{PublicIP: "1.1.1.1"}, nil
	}
	startTestRelayRuntime(t, srv, m)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first resource did not start")
	}
	waitRelayRuntime(t, func() bool { st := m.Status(second); return st.State == "debouncing" && !st.InFlight })
	w := profileAPIRequest(srv, "DELETE", "/api/relays/"+id, `{}`)
	if w.Code != 200 {
		t.Fatalf("delete=%d %s", w.Code, w.Body.String())
	}
	w = profileAPIRequest(srv, "PATCH", "/api/relays/"+second, `{"url":"`+oldURL+`","path_confirmed":true}`)
	if w.Code != 503 {
		t.Fatalf("reconfigure blocked by canceled credential owner must report persisted-but-not-applied, got %d %s", w.Code, w.Body.String())
	}
	w = profileAPIRequest(srv, "POST", "/api/relays/"+second+"/activate", `{}`)
	if w.Code == 202 {
		t.Fatal("failed reconfigure queued activation with old access")
	}
	if st := m.Status(second); st.Enabled {
		t.Fatal("failed configuration left the old access enabled")
	}
	finish()
	waitRelayRuntime(t, func() bool {
		st := m.Status(second)
		return st.Enabled && st.State == "debouncing" && newAccessCalls.Load() >= 2
	})
}

func TestRelayActivationAPIRequiresBearerBeforeLookingUpResource(t *testing.T) {
	srv := profileAPIFixture(t)
	srv.cfg.WebSecret = "test-only-token"
	w := profileAPIRequest(srv, "POST", "/api/relays/missing/activate", `{}`)
	if w.Code != 401 {
		t.Fatalf("activation escaped bearer guard: %d", w.Code)
	}
}
