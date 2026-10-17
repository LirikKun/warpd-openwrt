//go:build linux

package main

import "testing"

func TestToggleReloadMustActuallyRemoveDisabledListener(t *testing.T) {
	m, g, _ := newOwnershipMihomo(t)
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	// The local fixture deliberately ignores reload. A controller 200 and
	// still-owned controller alone cannot certify that the listener was removed.
	g.Proxies["warp"].Enabled = false
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Fatal("reload retained disabled listener but returned success")
	}
	if got := m.Snapshot(); got.Generation != before.Generation || got.Running {
		t.Fatalf("failed removal advanced apply or kept stale listener: %+v", got)
	}
}
