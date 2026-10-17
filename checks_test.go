package main

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func statusTestSettings() *Settings {
	s := &Settings{CheckPeriod: 10, FailRestart: 3, FailRescan: 5, DMERescanGap: 600, Proxies: map[string]*ProxySettings{}}
	for _, name := range proxyNames {
		p := (&ProxySettings{}).defaults(name)
		p.Enabled = name == "warp"
		s.Proxies[name] = p
	}
	return s
}

func isolateStatusState(t *testing.T) {
	t.Helper()
	oldM := mihomo
	mihomo = &MihomoMan{running: true, pid: 12345}
	checksMu.Lock()
	oldChecks := checks
	checks = map[string]*ProxyCheck{}
	checksMu.Unlock()
	oldEp := stateGetEndpoints()
	stateSetEndpoints(endpoints{warp: "192.0.2.1:2408", masque: map[string]string{}})
	mihomo.lastConf = mihomoConfText(statusTestSettings().Snapshot(), stateGetEndpoints(), warpAccount{})
	mihomo.desiredConf = mihomo.lastConf
	t.Cleanup(func() {
		mihomo = oldM
		checksMu.Lock()
		checks = oldChecks
		checksMu.Unlock()
		stateSetEndpoints(oldEp)
	})
}

func TestStatusRejectsExpiredHealth(t *testing.T) {
	isolateStatusState(t)
	c := getCheck("warp")
	c.set(func(s *ProxyState) {
		s.Checks = 1
		s.OKChecks = 1
		s.LastCheck = time.Now().Add(-time.Hour).Format(time.RFC3339)
	})
	s := statusTestSettings()
	st := NewServer(s, NewOrchestrator(s)).status()["proxies"].(map[string]ProxyState)["warp"]
	if st.Ready {
		t.Fatal("expired health success still reports ready=true")
	}
}

func TestStatusRejectsMissingEndpoint(t *testing.T) {
	isolateStatusState(t)
	stateSetEndpoints(endpoints{masque: map[string]string{}})
	c := getCheck("warp")
	c.set(func(s *ProxyState) { s.Checks = 1; s.OKChecks = 1; s.LastCheck = time.Now().Format(time.RFC3339) })
	s := statusTestSettings()
	st := NewServer(s, NewOrchestrator(s)).status()["proxies"].(map[string]ProxyState)["warp"]
	if st.Ready {
		t.Fatal("listener without endpoint still reports ready=true")
	}
}

func TestRunChecksHonorsAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := statusTestSettings()
	done := make(chan struct{})
	go func() { runChecks(ctx, s, NewOrchestrator(s)); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("cancelled health loop waits for mihomo startup instead of returning")
	}
}

func TestRunChecksCancelsInFlightProbe(t *testing.T) {
	isolateStatusState(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	s := statusTestSettings()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	s.Proxies["warp"].Port, _ = strconv.Atoi(port)
	mihomo.lastConf = mihomoConfText(s.Snapshot(), stateGetEndpoints(), warpAccount{})
	mihomo.desiredConf = mihomo.lastConf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { runChecks(ctx, s, NewOrchestrator(s)); close(done) }()
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(time.Second):
		t.Fatal("health loop never reached the local SOCKS probe")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancelling health loop does not cancel an in-flight SOCKS probe")
	}
}
