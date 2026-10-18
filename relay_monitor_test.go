package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func relayMonitorFixture(t *testing.T) (*RelayMonitor, *time.Time, *string, *int) {
	t.Helper()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ip := "198.51.100.1"
	calls := 0
	m := NewRelayMonitor(&RelayClient{}, RelayMonitorOptions{Debounce: 10 * time.Second, Cooldown: time.Minute, RetryBase: time.Second, MaxAttempts: 3})
	m.now = func() time.Time { return clock }
	m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
		return RelayObservation{PublicIP: ip, ObservedAt: clock}, nil
	}
	m.activate = func(context.Context, RelayAccess) (RelayActivation, error) {
		calls++
		return RelayActivation{Outcome: "unverified", HTTPStatus: 200}, nil
	}
	if err := m.Configure("relay1", RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true}, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, &clock, &ip, &calls
}

func TestRelayMonitorOnlyDebouncesStableIPAndNeverClaimsBinding(t *testing.T) {
	m, clock, ip, calls := relayMonitorFixture(t)
	ctx := context.Background()
	s := m.Reconcile(ctx, "relay1", false)
	if *calls != 0 || s.State != "debouncing" {
		t.Fatalf("first observation not debounced: %+v calls=%d", s, *calls)
	}
	*clock = clock.Add(11 * time.Second)
	s = m.Reconcile(ctx, "relay1", false)
	if *calls != 1 || s.ActivationOutcome != "unverified" || s.BindingVerified || s.TunnelVerified {
		t.Fatalf("wrong result: %+v calls=%d", s, *calls)
	}
	for i := 0; i < 5; i++ {
		*clock = clock.Add(2 * time.Minute)
		m.Reconcile(ctx, "relay1", false)
	}
	if *calls != 1 {
		t.Fatal("stable IP repeatedly reactivated")
	}
	*ip = "198.51.100.2"
	if s = m.Reconcile(ctx, "relay1", false); s.State != "debouncing" || *calls != 1 {
		t.Fatal("IP change bypassed debounce")
	}
	*clock = clock.Add(11 * time.Second)
	s = m.Reconcile(ctx, "relay1", false)
	if *calls != 2 || s.LastAttemptIP != *ip {
		t.Fatalf("new stable IP not activated: %+v", s)
	}
}

func TestRelayMonitorUnknownIPNeverActivatesAndRestartsDebounce(t *testing.T) {
	m, clock, _, calls := relayMonitorFixture(t)
	original := m.observe
	m.Reconcile(context.Background(), "relay1", false)
	*clock = clock.Add(20 * time.Second)
	for _, bad := range []string{"", "not-an-IP", "127.0.0.1", "192.168.0.1"} {
		m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
			return RelayObservation{PublicIP: bad}, nil
		}
		s := m.Reconcile(context.Background(), "relay1", true)
		if *calls != 0 || s.State != "ip_unknown" || s.ObservedIP != "" {
			t.Errorf("unknown IP triggered activation: %+v calls=%d", s, *calls)
		}
	}
	m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
		return RelayObservation{}, errors.New(relayFixtureURL)
	}
	s := m.Reconcile(context.Background(), "relay1", false)
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "SYNTHETIC") {
		t.Error("observation error leaked credential")
	}
	m.observe = original
	s = m.Reconcile(context.Background(), "relay1", false)
	if *calls != 0 || s.State != "debouncing" {
		t.Fatal("unknown interval counted toward debounce")
	}
	*clock = clock.Add(11 * time.Second)
	m.Reconcile(context.Background(), "relay1", false)
	if *calls != 1 {
		t.Fatal("recovered stable observation did not activate")
	}
}

func TestRelayMonitorFailuresHaveBoundedRetriesAndCooldown(t *testing.T) {
	m, clock, ip, calls := relayMonitorFixture(t)
	m.activate = func(context.Context, RelayAccess) (RelayActivation, error) {
		*calls++
		return RelayActivation{}, errors.New(relayFixtureURL)
	}
	ctx := context.Background()
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(11 * time.Second)
	s := m.Reconcile(ctx, "relay1", false)
	if *calls != 1 || s.ActivationOutcome != "error" || s.NextAttemptAt.IsZero() {
		t.Fatalf("missing failure/backoff: %+v calls=%d", s, *calls)
	}
	for i := 0; i < 10; i++ {
		m.Reconcile(ctx, "relay1", true)
	}
	if *calls != 1 {
		t.Fatal("manual requests bypassed cooldown")
	}
	for i := 0; i < 8; i++ {
		*clock = clock.Add(time.Minute)
		s = m.Reconcile(ctx, "relay1", false)
	}
	if *calls != 3 || s.State != "retry_exhausted" {
		t.Fatalf("retries unbounded or missing: %+v calls=%d", s, *calls)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "SYNTHETIC") {
		t.Fatal("activation error leaked credential")
	}
	*ip = "198.51.100.2"
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(11 * time.Second)
	m.Reconcile(ctx, "relay1", false)
	if *calls != 4 {
		t.Fatal("genuine new IP could not reactivate")
	}
}

func TestRelayMonitorCooldownAlsoAppliesToAmbiguousSuccessAndChangingIP(t *testing.T) {
	m, clock, ip, calls := relayMonitorFixture(t)
	ctx := context.Background()
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(11 * time.Second)
	m.Reconcile(ctx, "relay1", false)
	m.Reconcile(ctx, "relay1", true)
	if *calls != 1 {
		t.Fatal("ambiguous 200 did not start cooldown")
	}
	*ip = "198.51.100.2"
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(11 * time.Second)
	m.Reconcile(ctx, "relay1", false)
	if *calls != 1 {
		t.Fatal("IP change bypassed resource cooldown")
	}
	*clock = clock.Add(time.Minute)
	m.Reconcile(ctx, "relay1", false)
	if *calls != 2 {
		t.Fatal("new IP not activated after cooldown")
	}
}

func TestRelayMonitorCoalescesConcurrentConsumersWithoutBlockingOtherResources(t *testing.T) {
	m, _, _, _ := relayMonitorFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var observations atomic.Int32
	m.observe = func(ctx context.Context, a RelayAccess) (RelayObservation, error) {
		if a.SourceIP == "192.0.2.2" {
			if observations.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return RelayObservation{PublicIP: "198.51.100.1"}, nil
	}
	if err := m.Configure("relay2", RelayAccess{URL: relayFixtureURL + "OTHER", SourceIP: "192.0.2.3", PathConfirmed: true}, true); err != nil {
		t.Fatal(err)
	}
	first := make(chan RelayMonitorStatus, 1)
	go func() { first <- m.Reconcile(context.Background(), "relay1", false) }()
	<-entered
	second := make(chan RelayMonitorStatus, 1)
	go func() { second <- m.Reconcile(context.Background(), "relay1", true) }()
	select {
	case s := <-second:
		if !s.InFlight {
			t.Error("coalesced caller not told work is in flight")
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("coalesced caller blocked behind network request")
	}
	other := make(chan RelayMonitorStatus, 1)
	go func() { other <- m.Reconcile(context.Background(), "relay2", false) }()
	select {
	case <-other:
	case <-time.After(150 * time.Millisecond):
		t.Error("unrelated access resource blocked")
	}
	if observations.Load() != 1 {
		t.Error("concurrent consumers duplicated observation")
	}
}

func TestRelayMonitorDisableCancelsAndDiscardsLateActivation(t *testing.T) {
	m, clock, _, _ := relayMonitorFixture(t)
	ctx := context.Background()
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(11 * time.Second)
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	m.activate = func(ctx context.Context, a RelayAccess) (RelayActivation, error) {
		entered <- ctx
		<-release
		return RelayActivation{Outcome: "confirmed", BindingVerified: true, TunnelVerified: true}, nil
	}
	result := make(chan RelayMonitorStatus, 1)
	go func() { result <- m.Reconcile(ctx, "relay1", false) }()
	workCtx := <-entered
	disabled := make(chan error, 1)
	go func() {
		disabled <- m.Configure("relay1", RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true}, false)
	}()
	select {
	case err := <-disabled:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("disable blocked on activation")
		return
	}
	select {
	case <-workCtx.Done():
	case <-time.After(150 * time.Millisecond):
		t.Error("disable failed to cancel activation")
		return
	}
	s := m.Status("relay1")
	if s.Enabled || s.State != "disabled" || s.InFlight || s.BindingVerified {
		t.Fatalf("disabled status stale: %+v", s)
	}
	released.Store(true)
	close(release)
	<-result
	s = m.Status("relay1")
	if s.State != "disabled" || s.ActivationOutcome == "confirmed" || s.BindingVerified || s.TunnelVerified {
		t.Fatalf("late result restored disabled readiness: %+v", s)
	}
}

func TestRelayMonitorCanceledContextDoesNoWork(t *testing.T) {
	m, _, _, calls := relayMonitorFixture(t)
	observed := 0
	m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
		observed++
		return RelayObservation{PublicIP: "198.51.100.1"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Reconcile(ctx, "relay1", true)
	if observed != 0 || *calls != 0 {
		t.Fatal("already-canceled reconciliation still did work")
	}
}

func TestRelayMonitorRejectsObservationFromDifferentAddressFamily(t *testing.T) {
	m, clock, _, calls := relayMonitorFixture(t)
	m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
		return RelayObservation{PublicIP: "2001:db8::7"}, nil
	}
	m.Reconcile(context.Background(), "relay1", false)
	*clock = clock.Add(time.Minute)
	s := m.Reconcile(context.Background(), "relay1", true)
	if s.State != "ip_unknown" || *calls != 0 {
		t.Fatalf("different-family observation triggered activation: %+v", s)
	}
}

func TestRelayMonitorManualRetryFailureDoesNotRestoreEarlierUnverifiedOutcome(t *testing.T) {
	m, clock, _, calls := relayMonitorFixture(t)
	ctx := context.Background()
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(11 * time.Second)
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(time.Minute)
	m.activate = func(context.Context, RelayAccess) (RelayActivation, error) {
		*calls++
		return RelayActivation{}, errors.New("synthetic failure")
	}
	m.Reconcile(ctx, "relay1", true)
	*clock = clock.Add(time.Minute)
	s := m.Reconcile(ctx, "relay1", false)
	if *calls != 3 || s.ActivationOutcome != "error" {
		t.Fatalf("older unverified result masked newer failure: %+v calls=%d", s, *calls)
	}
}

func TestRelayMonitorIdempotentConfigPreservesDebounceAndDisablePreservesCooldown(t *testing.T) {
	m, clock, _, calls := relayMonitorFixture(t)
	ctx := context.Background()
	a := RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true}
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(11 * time.Second)
	if err := m.Configure("relay1", a, true); err != nil {
		t.Fatal(err)
	}
	m.Reconcile(ctx, "relay1", false)
	if *calls != 1 {
		t.Fatal("idempotent configuration reset debounce")
	}
	m.Configure("relay1", a, false)
	m.Reconcile(ctx, "relay1", true)
	m.Configure("relay1", a, true)
	m.Reconcile(ctx, "relay1", false)
	*clock = clock.Add(11 * time.Second)
	s := m.Reconcile(ctx, "relay1", true)
	if *calls != 1 || s.State != "cooldown" {
		t.Fatalf("disable/re-enable bypassed cooldown: %+v calls=%d", s, *calls)
	}
}

func TestRelayMonitorRemoveAndCloseCancelAndRejectLateObservation(t *testing.T) {
	for _, operation := range []string{"remove", "close", "edit"} {
		t.Run(operation, func(t *testing.T) {
			m, _, _, calls := relayMonitorFixture(t)
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			var released atomic.Bool
			defer func() {
				if released.CompareAndSwap(false, true) {
					close(release)
				}
			}()
			m.observe = func(ctx context.Context, a RelayAccess) (RelayObservation, error) {
				started <- ctx
				<-release
				return RelayObservation{PublicIP: "198.51.100.1"}, nil
			}
			done := make(chan RelayMonitorStatus, 1)
			go func() { done <- m.Reconcile(context.Background(), "relay1", false) }()
			workCtx := <-started
			switch operation {
			case "remove":
				m.Remove("relay1")
			case "close":
				m.Close()
			case "edit":
				if err := m.Configure("relay1", RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.3", PathConfirmed: true}, true); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-workCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("lifecycle operation did not cancel work")
			}
			if operation == "edit" {
				s := m.Reconcile(context.Background(), "relay1", false)
				if !s.InFlight {
					t.Error("replacement lost old work coalescing gate")
				}
			}
			released.Store(true)
			close(release)
			<-done
			s := m.Status("relay1")
			if s.ObservedIP != "" || s.InFlight || *calls != 0 {
				t.Fatalf("late result survived lifecycle change: %+v", s)
			}
			if operation == "close" && m.Configure("new", RelayAccess{}, false) == nil {
				t.Error("closed monitor accepted resource")
			}
		})
	}
}

func TestRelayMonitorMissingLinkOrUnconfirmedPathNeverObserves(t *testing.T) {
	m, _, _, _ := relayMonitorFixture(t)
	var count atomic.Int32
	m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
		count.Add(1)
		return RelayObservation{}, nil
	}
	for _, a := range []RelayAccess{{}, {URL: relayFixtureURL, SourceIP: "192.0.2.2"}, {URL: relayFixtureURL, PathConfirmed: true}} {
		if err := m.Configure("relay1", a, true); err != nil {
			t.Fatal(err)
		}
		m.Reconcile(context.Background(), "relay1", true)
	}
	if count.Load() != 0 {
		t.Fatal("monitor observed unconfigured or unconfirmed resource")
	}
	if m.Configure("relay1", RelayAccess{URL: "https://evil.example/activate/test"}, false) == nil {
		t.Fatal("invalid saved URL accepted")
	}
}

func TestRelayMonitorRejectsDuplicateCapabilityResources(t *testing.T) {
	m, _, _, _ := relayMonitorFixture(t)
	err := m.Configure("relay2", RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.3", PathConfirmed: true}, true)
	if err == nil {
		t.Fatal("duplicate capability admitted independent reconciliation loops")
	}
	if strings.Contains(err.Error(), "SYNTHETIC") || strings.Contains(err.Error(), "https:") {
		t.Fatal("duplicate validation leaked credential")
	}
}

func TestRelayMonitorRemovedInFlightCapabilityCannotBeRecreatedInParallel(t *testing.T) {
	m, _, _, _ := relayMonitorFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	m.observe = func(ctx context.Context, a RelayAccess) (RelayObservation, error) {
		close(entered)
		<-release
		return RelayObservation{}, ctx.Err()
	}
	done := make(chan RelayMonitorStatus, 1)
	go func() { done <- m.Reconcile(context.Background(), "relay1", false) }()
	<-entered
	m.Remove("relay1")
	if err := m.Configure("replacement", RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true}, true); err == nil {
		t.Fatal("removed capability admitted a second in-flight operation")
	}
}

func TestRelayMonitorCancellationAtActivationReservationCannotLeaveActivatingStatus(t *testing.T) {
	m, clock, _, calls := relayMonitorFixture(t)
	m.Reconcile(context.Background(), "relay1", false)
	*clock = clock.Add(11 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.now = func() time.Time { cancel(); return *clock }
	s := m.Reconcile(ctx, "relay1", true)
	if *calls != 0 || s.InFlight || s.State != "canceled" {
		t.Fatalf("canceled reservation left misleading state: %+v calls=%d", s, *calls)
	}
}
