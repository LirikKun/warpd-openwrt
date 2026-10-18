package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

// Model scheduler preemption between Snapshot and sync: the captured snapshot
// must not restore network work after a later disable has returned success.
func TestRelayReviewLateSnapshotCannotObserveAfterCommittedDisable(t *testing.T) {
	srv, m, id := relayRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := newRelayRuntime(ctx, srv.cfg, m)
	srv.relays = rt
	defer m.Close()
	var calls atomic.Int32
	m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
		calls.Add(1)
		return RelayObservation{PublicIP: "1.1.1.1"}, nil
	}
	captured := make(chan struct{})
	resume := make(chan struct{})
	synced := make(chan error, 1)
	go func() {
		old := srv.cfg.Snapshot()
		close(captured)
		<-resume
		synced <- rt.sync(old)
	}()
	<-captured
	w := profileAPIRequest(srv, "PATCH", "/api/relays/"+id, `{"enabled":false}`)
	close(resume)
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || srv.cfg.Snapshot().Relays[id].Enabled {
		t.Fatalf("disable did not commit: HTTP=%d", w.Code)
	}
	rt.dispatch()
	rt.work.Wait()
	if st := m.Status(id); calls.Load() != 0 || st.Enabled {
		t.Fatalf("late snapshot restarted disabled resource: observations_after_disable=%d runtime_enabled=%t state=%s", calls.Load(), st.Enabled, st.State)
	}
}

func TestRelayReviewStatusNeverMixesConfigurationGenerations(t *testing.T) {
	for _, field := range []string{"source_ip", "interface", "url", "path_confirmed"} {
		t.Run(field, func(t *testing.T) {
			srv, m, id := relayRuntimeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt := newRelayRuntime(ctx, srv.cfg, m)
			srv.relays = rt
			defer m.Close()
			m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
				return RelayObservation{PublicIP: "1.1.1.1"}, nil
			}
			m.Reconcile(ctx, id, false)
			old := srv.cfg.Snapshot()
			if st := srv.safeRelays(old)[0].Status; st.ObservedIP != "1.1.1.1" {
				t.Fatal("matching initial configuration lost its observation")
			}
			err := srv.cfg.ChangeProfiles(func(g *Settings) error {
				r := g.Relays[id]
				switch field {
				case "source_ip":
					r.SourceIP = "192.0.2.11"
				case "interface":
					r.Interface = "test-egress"
				case "url":
					r.ActivationURL = "https://relay.tribukvy.ltd/activate/NEW_TEST_ONLY_CAPABILITY"
				case "path_confirmed":
					r.PathConfirmed = false
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			current := srv.cfg.Snapshot()
			assertApplying := func(g Settings, direction string) {
				t.Helper()
				row := srv.safeRelays(g)[0]
				st := row.Status
				if st.State != "applying" || st.ObservedIP != "" || st.LastAttemptIP != "" || !st.LastObservationAt.IsZero() || st.BindingVerified || st.TunnelVerified || st.ActivationOutcome != "unknown" {
					t.Errorf("%s: metadata attached to another configuration's observation: source=%s state=%s observed=%s", direction, row.SourceIP, st.State, st.ObservedIP)
				}
				data, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "CAPABILITY") || strings.Contains(string(data), "https://") {
					t.Error("configuration identity exposed capability URL")
				}
			}
			// Settings committed before runtime catches up: old observation/new metadata.
			assertApplying(current, "new metadata with old runtime")
			if err := rt.sync(current); err != nil {
				t.Fatal(err)
			}
			m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
				return RelayObservation{PublicIP: "8.8.8.8"}, nil
			}
			m.Reconcile(ctx, id, false)
			// A GET snapshot captured before the edit resumes after new observation.
			assertApplying(old, "old metadata with new runtime")
			want := "8.8.8.8"
			if field == "path_confirmed" {
				want = ""
			}
			if st := srv.safeRelays(current)[0].Status; st.State == "applying" || st.ObservedIP != want {
				t.Errorf("matching current configuration status lost: state=%s observed=%s", st.State, st.ObservedIP)
			}
		})
	}
}
