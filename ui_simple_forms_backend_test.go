package main

import (
	"context"
	"testing"
)

// Characterize the unchanged backend contract behind the simplified forms.
// Nothing starts the runtime or makes a provider request: even the monitor's
// observation/activation boundary is replaced with a fatal test guard.
func TestSimpleRelayFormDisabledURLOnlyBackendContract(t *testing.T) {
	srv := profileAPIFixture(t)
	m := NewRelayMonitor(nil, RelayMonitorOptions{})
	defer m.Close()
	m.observe = func(context.Context, RelayAccess) (RelayObservation, error) {
		t.Fatal("unbound path attempted a network observation")
		return RelayObservation{}, nil
	}
	m.activate = func(context.Context, RelayAccess) (RelayActivation, error) {
		t.Fatal("form save or unbound enable attempted activation")
		return RelayActivation{}, nil
	}
	srv.relays = newRelayRuntime(context.Background(), srv.cfg, m)
	w := profileAPIRequest(srv, "POST", "/api/relays", `{"name":"Relay","url":"https://relay.tribukvy.ltd/activate/SYNTHETIC_SIMPLE_FORM","enabled":false,"source_ip":"","interface":"","monitor_interval":300,"path_confirmed":false}`)
	if w.Code != 201 {
		t.Fatalf("disabled URL-only save rejected: HTTP %d", w.Code)
	}
	id := profileResultID(t, w)
	r := srv.cfg.Snapshot().Relays[id]
	if r.Enabled || r.PathConfirmed || r.SourceIP != "" || r.Interface != "" {
		t.Fatal("URL-only save invented an enabled or confirmed source path")
	}
	if st := m.Reconcile(context.Background(), id, false); st.State != "disabled" {
		t.Fatalf("saved resource state=%s", st.State)
	}
	w = profileAPIRequest(srv, "PATCH", "/api/relays/"+id, `{"enabled":true}`)
	if w.Code != 200 {
		t.Fatalf("explicit desired enable rejected: HTTP %d", w.Code)
	}
	if st := m.Reconcile(context.Background(), id, false); st.State != "path_unconfirmed" || st.InFlight {
		t.Fatalf("unbound resource was not fail-closed: state=%s", st.State)
	}
	if validateRelaySource(RelayAccess{PathConfirmed: true}) == nil {
		t.Fatal("confirmation alone bypassed source binding")
	}
	if validateRelaySource(RelayAccess{SourceIP: "192.0.2.2"}) == nil {
		t.Fatal("source alone bypassed explicit confirmation")
	}
	w = profileAPIRequest(srv, "POST", "/api/relays/"+id+"/activate", `{}`)
	if w.Code != 409 {
		t.Fatalf("manual activation accepted without source confirmation: HTTP %d", w.Code)
	}
}
