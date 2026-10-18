package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestRelayUDPDiscoverConnectedChildAmongUnrelatedChildSockets(t *testing.T) {
	child := relayUDPSpawn(t, "")
	ingress := relayUDPFixture(t).LocalAddr().(*net.UDPAddr).AddrPort()
	child.command(t, "extra")
	if got := child.command(t, "connect "+ingress.String()); got != "true" {
		t.Fatal(got)
	}
	peer, lease, e := discoverRelayUDPSocket(child.cmd.Process.Pid, ingress)
	if e != nil {
		t.Fatal("exact connected peer not discovered:", e)
	}
	defer lease.Close()
	if peer != child.peer {
		t.Fatal("wrong child socket", peer)
	}
}

func TestRelayUDPDiscoverRejectsAmbiguousChildSockets(t *testing.T) {
	child := relayUDPSpawn(t, "")
	ingress := relayUDPFixture(t).LocalAddr().(*net.UDPAddr).AddrPort()
	child.command(t, "extra")
	if _, lease, err := discoverRelayUDPSocket(child.cmd.Process.Pid, ingress); err == nil {
		lease.Close()
		t.Fatal("ambiguous unconnected child sockets selected")
	}
}

func TestRelayUDPQueuedReboundTupleIsDiscarded(t *testing.T) {
	fixture := relayUDPFixture(t)
	b, err := newRelayUDPFixture(context.Background(), relayUDPCfg(), fixture.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	old := relayUDPSpawn(t, "")
	old.command(t, "send "+b.Target().String()+" 6f6c642d717565756564")
	old.command(t, "release")
	replacement := relayUDPSpawn(t, old.peer.String())
	if err = b.Authorize(context.Background(), RelayUDPChild{PID: replacement.cmd.Process.Pid, Generation: 2, Exited: replacement.exited, Current: func() bool { return true }}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	fixture.SetReadDeadline(time.Now().Add(60 * time.Millisecond))
	if _, _, err = fixture.ReadFromUDP(buf); err == nil {
		t.Fatal("stale queued packet forwarded as replacement PID")
	}
	replacement.command(t, "send "+b.Target().String()+" 6e65772d6f776e6572")
	fixture.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := fixture.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "new-owner" {
		t.Fatal("new exact owner cannot forward", n, err)
	}
}

func TestRelayUDPRejectUnconfirmedPath(t *testing.T) {
	cfg := RelayUDPConfig{ProfileID: "p_test", Revision: 1, Country: "DE"}
	if b, e := NewRelayUDPBridge(context.Background(), cfg); e == nil {
		b.Close()
		t.Fatal("unconfirmed source accepted")
	}
}
