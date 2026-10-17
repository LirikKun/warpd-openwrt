//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeOwnershipFixture(t *testing.T, root, table, rows string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", table), []byte("  sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"+rows), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixtureOwnedFD(t *testing.T, root, fd, inode string) {
	t.Helper()
	dir := filepath.Join(root, "8830", "fd")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:["+inode+"]", filepath.Join(dir, fd)); err != nil {
		t.Fatal(err)
	}
}

func TestMihomoOwnershipMalformedProcFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeOwnershipFixture(t, root, "tcp", "truncated listener row\n")
	m := &MihomoMan{procRoot: root}
	if err := m.checkCollisions(0, []mihomoListener{{"127.0.0.1", 9099}}); err == nil {
		t.Fatal("malformed proc listener table was treated as free")
	}
}

func TestMihomoOwnershipProcPIDAndAllListenerInodes(t *testing.T) {
	root := t.TempDir()
	writeOwnershipFixture(t, root, "tcp", "0: 0100007F:238B 00000000:0000 0A 0:0 00:0 0 0 0 100\n1: 00000000:0FA1 00000000:0000 0A 0:0 00:0 0 0 0 101\n")
	fixtureOwnedFD(t, root, "3", "100")
	m := &MihomoMan{procRoot: root}
	wants := []mihomoListener{{"127.0.0.1", 9099}, {"0.0.0.0", 4001}}
	if err := m.checkOwnership(8830, wants); err == nil {
		t.Fatal("controller-only child accepted another process's SOCKS socket")
	}
	fixtureOwnedFD(t, root, "4", "101")
	if err := m.checkOwnership(8830, wants); err != nil {
		t.Fatalf("all exact owned listeners rejected: %v", err)
	}
	if err := m.checkOwnership(8716, wants); err == nil {
		t.Fatal("different PID accepted owned listener inodes")
	}
	if err := os.Remove(filepath.Join(root, "8830", "fd", "3")); err != nil {
		t.Fatal(err)
	}
	if err := m.checkOwnership(8830, wants); err == nil {
		t.Fatal("SOCKS-only child accepted foreign controller")
	}
}

func TestMihomoOwnershipProcIPv6AndListenState(t *testing.T) {
	root := t.TempDir()
	writeOwnershipFixture(t, root, "tcp", "")
	writeOwnershipFixture(t, root, "tcp6", "0: 00000000000000000000000001000000:0FA3 00000000000000000000000000000000:0000 0A 0:0 00:0 0 0 0 201\n")
	fixtureOwnedFD(t, root, "3", "201")
	m := &MihomoMan{procRoot: root}
	if err := m.checkOwnership(8830, []mihomoListener{{"::1", 4003}}); err != nil {
		t.Fatal(err)
	}
	if err := m.checkOwnership(8830, []mihomoListener{{"::", 4003}}); err == nil {
		t.Fatal("loopback-only listener satisfied wildcard bind")
	}
	writeOwnershipFixture(t, root, "tcp6", "0: 00000000000000000000000001000000:0FA3 00000000000000000000000000000000:0000 01 0:0 00:0 0 0 0 201\n")
	if err := m.checkOwnership(8830, []mihomoListener{{"::1", 4003}}); err == nil {
		t.Fatal("established connection satisfied listening socket")
	}
}
