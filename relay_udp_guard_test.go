package main

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestRelayUDPAuthorizeCancelledContextCloses(t *testing.T) {
	fixture := relayUDPFixture(t)
	b, err := newRelayUDPFixture(context.Background(), relayUDPCfg(), fixture.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	child := relayUDPSpawn(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = b.Authorize(ctx, RelayUDPChild{PID: child.cmd.Process.Pid, Generation: 1, Exited: child.exited, Current: func() bool { return true }})
	if err == nil {
		t.Fatal("already-cancelled authorization succeeded")
	}
	select {
	case <-b.Done():
	case <-time.After(time.Second):
		t.Fatal("failed authorization retained live sockets/goroutines")
	}
}

func TestRelayUDPAuthorizeCancellationBeforePublishCloses(t *testing.T) {
	fixture := relayUDPFixture(t)
	b, err := newRelayUDPFixture(context.Background(), relayUDPCfg(), fixture.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	child := relayUDPSpawn(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = b.Authorize(ctx, RelayUDPChild{PID: child.cmd.Process.Pid, Generation: 1, Exited: child.exited, Current: func() bool { cancel(); return true }})
	if err == nil {
		t.Fatal("cancelled authorization published active bridge")
	}
	relayUDPWaitClosed(t, b)
}

func TestRelayUDPConfigCannotCarryActivationCredential(t *testing.T) {
	typ := reflect.TypeOf(relayUDPCfg().Access)
	if _, ok := typ.FieldByName("URL"); ok {
		t.Fatal("UDP transport config can retain an activation credential")
	}
}

func TestRelayUDPDescriptorScanBound(t *testing.T) {
	child := relayUDPSpawn(t, "")
	child.command(t, "manyfd 4097")
	ingress := relayUDPFixture(t).LocalAddr().(*net.UDPAddr).AddrPort()
	if f, err := acquireRelayUDPSocket(child.cmd.Process.Pid, child.peer, ingress); err == nil {
		f.Close()
		t.Fatal("oversized child fd table accepted")
	}
}

func TestRelayUDPRevokesMutableReusableSocket(t *testing.T) {
	b, child, _, _ := relayUDPActive(t, relayUDPCfg())
	child.command(t, "reuse")
	relayUDPWaitClosed(t, b)
}
