package main

import (
	"context"
	"testing"
	"time"
)

func TestProbeMarksPendingAndRecordsIdentity(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	o := NewOrchestrator(s)
	c := getCheck("warp")
	c.set(func(st *ProxyState) {
		st.Checks = 1
		st.OKChecks = 1
		st.LastCheck = time.Now().Format(time.RFC3339)
		st.CheckedEndpoint = "192.0.2.1:2408"
		st.CheckedPort = 4001
		st.CheckedListen = "0.0.0.0"
	})
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, func(ctx context.Context, listen string, port int) (exitParse, error) {
		st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"]
		if !st.Checking || st.Ready {
			t.Error("in-flight probe is not marked checking or still reports ready")
		}
		return exitParse{info: "US via fixture (TEST node)", node: "TEST", country: "US"}, nil
	})
	st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"]
	if st.Checking || !st.Ready || st.Checks != 2 || st.OKChecks != 2 {
		t.Fatalf("completed current probe not reflected accurately: %+v", st)
	}
}

func TestProbeDiscardsPreviousGenerationResult(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	o := NewOrchestrator(s)
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, func(ctx context.Context, listen string, port int) (exitParse, error) {
		mihomo.mu.Lock()
		mihomo.generation++
		mihomo.mu.Unlock()
		return exitParse{info: "US via fixture", node: "TEST", country: "US"}, nil
	})
	st := getCheck("warp").snapshot()
	if st.Checks != 0 || st.OKChecks != 0 || st.Checking {
		t.Fatalf("obsolete probe updated current health state: %+v", st)
	}
}

func TestProbeDiscardsChangedEndpointResult(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	o := NewOrchestrator(s)
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, func(ctx context.Context, listen string, port int) (exitParse, error) {
		stateSetEndpoints(endpoints{warp: "192.0.2.9:2408", masque: map[string]string{}})
		return exitParse{info: "US via fixture", node: "TEST", country: "US"}, nil
	})
	if st := getCheck("warp").snapshot(); st.Checks != 0 {
		t.Fatalf("probe of previous endpoint was accepted: %+v", st)
	}
}
