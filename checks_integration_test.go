package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

func TestChecksStartIndependentProbes(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	s.Proxies["masque"].Enabled = true
	stateSetEndpoints(endpoints{warp: "192.0.2.1:2408", masque: map[string]string{"masque": "192.0.2.2:8443"}})
	accepted := make(chan net.Conn, 2)
	for _, name := range []string{"warp", "masque"} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		s.Proxies[name].Port = ln.Addr().(*net.TCPAddr).Port
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				accepted <- conn
			}
		}()
	}
	mihomo.lastConf = mihomoConfText(s.Snapshot(), stateGetEndpoints(), warpAccount{})
	mihomo.desiredConf = mihomo.lastConf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runChecks(ctx, s, NewOrchestrator(s)); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("health loop did not shut down")
		}
	}()
	for i := 0; i < 2; i++ {
		select {
		case conn := <-accepted:
			defer conn.Close()
		case <-time.After(500 * time.Millisecond):
			t.Fatal("a stalled proxy prevents the next proxy's probe from starting")
		}
	}
}

func TestChecksMasqueColoDoesNotTriggerUnusableGeoRescan(t *testing.T) {
	for _, name := range []string{"masque", "masqueh2", "m2w"} {
		t.Run(name, func(t *testing.T) {
			isolateStatusState(t)
			s := statusTestSettings()
			s.Proxies[name].Enabled = true
			o := NewOrchestrator(s)
			recordProxyResult(s.Snapshot(), name, o, exitParse{info: "RU via Moscow (DME node)", node: "DME", country: "RU"}, nil, time.Now())
			if len(o.scanCh) != 0 {
				t.Fatal("a successful MASQUE/chain probe started a geography-only rescan that cannot satisfy its filter")
			}
		})
	}
}

func TestChecksOneFailedProxyDoesNotRestartHealthySiblings(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	s.Proxies["m2w"].Enabled = true
	getCheck("warp").set(func(st *ProxyState) { st.Checks = 1; st.OKChecks = 1; st.LastCheck = time.Now().Format(time.RFC3339) })
	getCheck("m2w").set(func(st *ProxyState) { st.Checks = 2; st.FailStreak = 2 })
	recordProxyResult(s.Snapshot(), "m2w", NewOrchestrator(s), exitParse{}, errors.New("fixture tunnel timeout"), time.Now())
	if !mihomo.Running() {
		t.Fatal("one failed chained proxy stopped the shared child serving healthy siblings")
	}
}

func TestStatusValidatesProbeIdentity(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  bool
	}{
		{"current success", `{}`, true},
		{"previous generation", `{"checked_generation":1}`, false},
		{"previous endpoint", `{"checked_endpoint":"192.0.2.9:2408"}`, false},
		{"previous port", `{"checked_port":4999}`, false},
		{"in flight", `{"checking":true}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateStatusState(t)
			mihomo.mu.Lock()
			mihomo.generation = 2
			mihomo.mu.Unlock()
			var st ProxyState
			if err := json.Unmarshal([]byte(`{"checks":1,"ok_checks":1,"checked_generation":2,"checked_endpoint":"192.0.2.1:2408","checked_port":4001,"checked_listen":"0.0.0.0"}`), &st); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.patch), &st); err != nil {
				t.Fatal(err)
			}
			st.LastCheck = time.Now().Format(time.RFC3339)
			st.ExitNode, st.ExitNodeCountry, st.ExitCountry = "FRA", "DE", "RU"
			getCheck("warp").set(func(out *ProxyState) { *out = st })
			s := statusTestSettings()
			got := NewServer(s, NewOrchestrator(s)).status()["proxies"].(map[string]ProxyState)["warp"]
			if got.Ready != tc.want {
				t.Fatalf("ready=%v, want %v", got.Ready, tc.want)
			}
		})
	}
}
