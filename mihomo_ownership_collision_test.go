//go:build linux

package main

import (
	"net"
	"os"
	"testing"
)

func TestMihomoOwnershipRechecksImmediatelyBeforeReload(t *testing.T) {
	m, g, marker := newOwnershipMihomo(t)
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.ownership = func(pid int, wants []mihomoListener) error {
		err := (&MihomoMan{}).checkOwnership(pid, wants)
		calls++
		if calls == 2 {
			closeOwnershipSockets(t, m, marker)
		}
		return err
	}
	called := false
	m.reload = func() error { called = true; return nil }
	g.Proxies["warp"].Endpoint = "192.0.2.2:2408"
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Error("lost controller ownership accepted")
	}
	if called {
		t.Fatal("reload sent after ownership disappeared during configuration write boundary")
	}
}

func TestMihomoOwnershipReloadRejectsNewListenerCollisionBeforeWrite(t *testing.T) {
	m, g, _ := newOwnershipMihomo(t)
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	conf, err := os.ReadFile(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	external, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	g.Proxies["warp"].Port = external.Addr().(*net.TCPAddr).Port
	called := false
	m.reload = func() error { called = true; return nil }
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Error("new occupied listener returned success")
	}
	if called {
		t.Error("reload attempted despite known external listener collision")
	}
	if got, err := os.ReadFile(m.configPath); err != nil || string(got) != string(conf) {
		t.Error("known collision replaced config file")
	}
	if got := m.Snapshot(); got != before {
		t.Errorf("external listener collision disturbed managed child: before=%+v after=%+v", before, got)
	}
}
