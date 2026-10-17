package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A published desired config is not evidence that the old child applied it.
func TestChecksRejectPendingCredentials(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	o := NewOrchestrator(s)
	m, ready := newTestMihomo(t)
	mihomo = m
	g, e := s.Snapshot(), stateGetEndpoints()
	acc := warpAccount{PrivateKey: "synthetic-old-key"}
	if err := m.Ensure(g, e, acc); err != nil {
		t.Fatal(err)
	}
	ready()
	probe := func(context.Context, string, int) (exitParse, error) {
		return exitParse{node: "AMS", country: "US", nodeCountry: "NL"}, nil
	}
	checkProxyWith(context.Background(), g, "warp", o, probe)
	if !NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"].Ready {
		t.Fatal("initial applied credentials not ready")
	}
	acc.PrivateKey = "synthetic-new-key"
	path := m.configPath
	m.configPath = t.TempDir()
	if err := m.Ensure(g, e, acc); err == nil {
		t.Fatal("expected config write failure")
	}
	status := NewServer(s, o).status()
	if status["proxies"].(map[string]ProxyState)["warp"].Ready {
		t.Error("unapplied credentials still report ready")
	}
	checkProxyWith(context.Background(), g, "warp", o, probe)
	if got := getCheck("warp").snapshot().Checks; got != 1 {
		t.Errorf("probe recorded for failed credential apply: checks=%d", got)
	}
	wire, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "synthetic-") || strings.Contains(string(wire), "private-key") {
		t.Fatal("status leaked config/account")
	}
	m.configPath = path
	if err := m.Ensure(g, e, acc); err != nil {
		t.Fatal(err)
	}
	checkProxyWith(context.Background(), g, "warp", o, probe)
	if !NewServer(s, o).status()["proxies"].(map[string]ProxyState)["warp"].Ready {
		t.Fatal("new applied credentials not ready")
	}
}

func TestProbeDiscardsUnappliedSiblingConfig(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	s.Proxies["masque"].Enabled = true
	e := stateGetEndpoints()
	e.masque["masque"] = "192.0.2.2:8443"
	stateSetEndpoints(e)
	m, ready := newTestMihomo(t)
	mihomo = m
	if err := m.Ensure(s.Snapshot(), e, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	checkProxyWith(context.Background(), s.Snapshot(), "warp", NewOrchestrator(s), func(context.Context, string, int) (exitParse, error) {
		mu := s.mutex()
		mu.Lock()
		s.Proxies["masque"].Port++
		mu.Unlock()
		return exitParse{node: "AMS", country: "US", nodeCountry: "NL"}, nil
	})
	if st := getCheck("warp").snapshot(); st.Checks != 0 || st.Checking {
		t.Fatalf("probe recorded while full config became unapplied: %+v", st)
	}
}

func TestAppliedConfigIgnoresUnusedScannedEndpoints(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	s.Proxies["warp"].Endpoint = "192.0.2.3:2408"
	s.Proxies["m2w"].Enabled = true
	s.Proxies["m2w"].Inner = "192.0.2.4:2408"
	s.Proxies["m2w"].Outer = "192.0.2.5:8443"
	e := stateGetEndpoints()
	m, ready := newTestMihomo(t)
	mihomo = m
	if err := m.Ensure(s.Snapshot(), e, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	e.warp = "192.0.2.9:2408"
	e.masque["masque"] = "192.0.2.9:8443"
	stateSetEndpoints(e)
	for _, name := range []string{"warp", "m2w"} {
		checkProxyWith(context.Background(), s.Snapshot(), name, NewOrchestrator(s), func(context.Context, string, int) (exitParse, error) {
			return exitParse{node: "AMS", country: "US", nodeCountry: "NL"}, nil
		})
		st := NewServer(s, NewOrchestrator(s)).status()["proxies"].(map[string]ProxyState)[name]
		if !st.TransportReady || st.Ready != (name != "m2w") {
			t.Fatalf("unused auto endpoints invalidated pinned %s or bypassed M2W policy: %+v", name, st)
		}
	}
}

func TestChecksRejectUnappliedConfig(t *testing.T) {
	for _, change := range []string{"warp endpoint", "listener port", "listener address", "masque dependency", "warp dependency", "standalone pin", "chain pin", "other listener"} {
		t.Run(change, func(t *testing.T) {
			isolateStatusState(t)
			s := statusTestSettings()
			s.Proxies["masque"].Enabled = true
			s.Proxies["m2w"].Enabled = true
			e := endpoints{warp: "192.0.2.1:2408", masque: map[string]string{"masque": "192.0.2.2:8443"}}
			stateSetEndpoints(e)
			m, ready := newTestMihomo(t)
			mihomo = m
			if err := m.Ensure(s.Snapshot(), e, warpAccount{}); err != nil {
				t.Fatal(err)
			}
			ready()
			before := m.Snapshot()
			name := "warp"
			switch change {
			case "warp endpoint":
				e.warp = "192.0.2.9:2408"
			case "listener port":
				s.Proxies[name].Port++
			case "listener address":
				s.Proxies[name].Listen = "127.0.0.1"
			case "masque dependency":
				name = "m2w"
				e.masque["masque"] = "192.0.2.9:8443"
			case "warp dependency":
				name = "m2w"
				e.warp = "192.0.2.9:2408"
			case "standalone pin":
				s.Proxies[name].Endpoint = "192.0.2.9:2408"
			case "chain pin":
				name = "m2w"
				s.Proxies[name].Outer = "192.0.2.9:8443"
			case "other listener":
				s.Proxies["masque"].Port++
			}
			stateSetEndpoints(e) // scanner publishes before Ensure
			g := s.Snapshot()
			p := g.Proxies[name]
			ep, outer := liveEndpoint(name, p, e)
			c := getCheck(name)
			c.set(func(st *ProxyState) {
				st.Checks, st.OKChecks = 1, 1
				st.LastCheck = time.Now().Format(time.RFC3339Nano)
				st.CheckedGeneration = before.Generation
				st.CheckedEndpoint, st.CheckedOuter = ep, outer
				st.CheckedPort, st.CheckedListen = p.Port, p.Listen
				st.ExitNode, st.ExitNodeCountry, st.ExitCountry = "AMS", "NL", "US"
			})
			o := NewOrchestrator(s)
			if st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)[name]; st.Ready || st.TransportReady {
				t.Error("pending desired config reported ready/transport_ready before apply")
			}
			workingPath := m.configPath
			m.configPath = t.TempDir() // force write failure without stopping old child
			if err := m.Ensure(g, e, warpAccount{}); err == nil {
				t.Fatal("expected config write failure")
			}
			if got := m.Snapshot(); got != before {
				t.Fatalf("write failure replaced old running child: before=%+v after=%+v", before, got)
			}
			c.set(func(st *ProxyState) { *st = ProxyState{} })
			called := false
			checkProxyWith(context.Background(), g, name, o, func(context.Context, string, int) (exitParse, error) {
				called = true
				return exitParse{node: "AMS", country: "US", nodeCountry: "NL"}, nil
			})
			if st := c.snapshot(); called || st.Checks != 0 || st.Checking {
				t.Errorf("unapplied desired config was probed/recorded: called=%v checks=%d checking=%v", called, st.Checks, st.Checking)
			}
			if st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)[name]; st.Ready || st.TransportReady {
				t.Error("failed apply reported ready/transport_ready through old child")
			}
			m.configPath = workingPath
			if err := m.Ensure(g, e, warpAccount{}); err != nil {
				t.Fatal(err)
			}
			checkProxyWith(context.Background(), g, name, o, func(context.Context, string, int) (exitParse, error) {
				return exitParse{node: "AMS", country: "US", nodeCountry: "NL"}, nil
			})
			st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)[name]
			if !st.TransportReady || st.Ready != (name != "m2w") {
				t.Fatalf("applied config lacks fresh transport success or bypassed M2W policy: %+v", st)
			}
		})
	}
}
