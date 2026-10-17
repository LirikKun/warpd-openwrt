//go:build linux

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func newOwnershipMihomo(t *testing.T) (*MihomoMan, Settings, string) {
	t.Helper()
	lockOwnershipController(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "closed")
	m := &MihomoMan{configPath: filepath.Join(dir, "mihomo.yaml"), ownershipTimeout: 200 * time.Millisecond}
	m.command = func(path string) *exec.Cmd {
		cmd := ownershipSleepingCommand()
		cmd.Env = append(cmd.Env, "WARPD_OWNERSHIP_CONFIG="+path, "WARPD_OWNERSHIP_CLOSED="+marker)
		return cmd
	}
	m.reload = func() error { return nil }
	t.Cleanup(m.Stop)
	g := mihomoSettings()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	g.Proxies["warp"].Enabled = true
	g.Proxies["warp"].Endpoint = "192.0.2.1:2408"
	g.Proxies["warp"].Listen = "127.0.0.1"
	g.Proxies["warp"].Port = port
	// WSL can retain a closed LISTEN row briefly; wait for fixture isolation.
	deadline := time.Now().Add(time.Second)
	for {
		err := m.checkOwnership(0, []mihomoListener{{"127.0.0.1", port}})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return m, g, marker
}

func closeOwnershipSockets(t *testing.T, m *MihomoMan, marker string) {
	t.Helper()
	if err := m.cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("child did not close listener sockets")
}

func TestMihomoOwnershipLostSocketsRejectAppliedAndSameConfigEnsure(t *testing.T) {
	m, g, marker := newOwnershipMihomo(t)
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	if _, applied := m.AppliedSnapshot(g, endpoints{}); !applied {
		t.Fatal("owned child not applied")
	}
	closeOwnershipSockets(t, m, marker)
	if _, applied := m.AppliedSnapshot(g, endpoints{}); applied {
		t.Error("living child whose sockets disappeared remains applied")
	}
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Error("same-config Ensure accepted living child without sockets")
	}
	if m.generation != before.Generation {
		t.Error("ownership failure advanced generation")
	}
}

func TestMihomoOwnershipRejectsForeignControllerBeforeReload(t *testing.T) {
	m, g, marker := newOwnershipMihomo(t)
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	closeOwnershipSockets(t, m, marker)
	ln, err := net.Listen("tcp4", mihomoAPI)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	called := false
	m.reload = func() error { called = true; return nil }
	g.Proxies["warp"].Endpoint = "192.0.2.2:2408"
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Error("foreign controller reload returned success")
	}
	if called {
		t.Error("reload reached unrelated controller")
	}
	if m.generation != before.Generation {
		t.Error("foreign controller advanced generation")
	}
	conn, err := net.DialTimeout("tcp", mihomoAPI, time.Second)
	if err != nil {
		t.Fatalf("external listener harmed: %v", err)
	}
	conn.Close()
}

func TestMihomoOwnershipReloadMustRetainSockets(t *testing.T) {
	m, g, marker := newOwnershipMihomo(t)
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	before, conf := m.Snapshot(), m.lastConf
	m.reload = func() error { closeOwnershipSockets(t, m, marker); return nil }
	g.Proxies["warp"].Endpoint = "192.0.2.2:2408"
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Error("reload without owned listeners returned success")
	}
	if m.generation != before.Generation || m.lastConf != conf {
		t.Error("unowned reload advanced applied configuration")
	}
	if _, applied := m.AppliedSnapshot(g, endpoints{}); applied {
		t.Error("unowned reload reported applied")
	}
}
