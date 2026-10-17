//go:build linux

package main

import (
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestMihomoOwnershipAllRenderedListenersAndReload(t *testing.T) {
	m, g, _ := newOwnershipMihomo(t)
	var reservations []net.Listener
	for _, key := range []string{"warp", "masque", "masqueh2", "m2w"} {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		reservations = append(reservations, ln)
		p := g.Proxies[key]
		p.Enabled, p.Listen, p.Port, p.Endpoint = true, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port, "192.0.2.1:443"
	}
	for _, ln := range reservations {
		ln.Close()
	}
	g.Proxies["m2w"].Outer, g.Proxies["m2w"].Inner = "192.0.2.1:443", "192.0.2.2:2408"
	wants, err := mihomoListeners(mihomoConfText(g, endpoints{}, warpAccount{}))
	if err != nil || len(wants) != 5 {
		t.Fatalf("controller plus four rendered listeners: got %d, err=%v", len(wants), err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := m.checkCollisions(0, wants)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	g.Proxies["warp"].Endpoint = "192.0.2.3:2408"
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot(); got.PID != before.PID || got.Generation != before.Generation+1 {
		t.Fatalf("owned reload status before=%+v after=%+v", before, got)
	}
	if _, applied := m.AppliedSnapshot(g, endpoints{}); !applied {
		t.Fatal("owned full listener set not applied after reload")
	}
}

func TestMihomoOwnershipDualStackWildcard(t *testing.T) {
	m, g, _ := newOwnershipMihomo(t)
	g.Proxies["warp"].Listen = "0.0.0.0"
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatalf("Go dual-stack wildcard listener rejected: %v", err)
	}
	if _, applied := m.AppliedSnapshot(g, endpoints{}); !applied {
		t.Fatal("owned wildcard listener not applied")
	}
}

func TestMihomoOwnershipControllerOnlyIsNotApplied(t *testing.T) {
	m, g, _ := newOwnershipMihomo(t)
	command := m.command
	m.command = func(path string) *exec.Cmd {
		cmd := command(path)
		cmd.Env = append(cmd.Env, "WARPD_OWNERSHIP_CONTROLLER_ONLY=1")
		return cmd
	}
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Fatal("controller without expected SOCKS socket was applied")
	}
	if m.lastConf != "" || m.Snapshot() != (MihomoStatus{}) {
		t.Fatal("controller-only startup advanced state")
	}
}

func TestMihomoOwnershipOccupiedSOCKSIsPreserved(t *testing.T) {
	m, g, _ := newOwnershipMihomo(t)
	ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(g.Proxies["warp"].Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	called := false
	m.command = func(string) *exec.Cmd { called = true; return ownershipSleepingCommand() }
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Fatal("occupied SOCKS listener was accepted")
	}
	if called {
		t.Fatal("child launched despite occupied SOCKS listener")
	}
}
