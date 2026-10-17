//go:build linux

package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// Exercise Ensure's real /proc ownership and removal paths, not the ownership
// bypass seam. Reload replaces only the synthetic tables for this exact PID.
func bindRemovalFixture(t *testing.T, oldAddress, newAddress string, retainOld bool) (*MihomoMan, Settings) {
	t.Helper()
	root := t.TempDir()
	g := mihomoSettings()
	g.Proxies["warp"].Enabled = true
	g.Proxies["warp"].Endpoint = "192.0.2.1:2408"
	g.Proxies["warp"].Listen = oldAddress
	g.Proxies["warp"].Port = 4001
	controller := mihomoSocket{mihomoListener{"127.0.0.1", 9099}, "100"}
	old := mihomoSocket{mihomoListener{oldAddress, 4001}, "101"}
	current := mihomoSocket{mihomoListener{newAddress, 4001}, "102"}
	writeSockets := func(sockets ...mihomoSocket) {
		t.Helper()
		rows := map[string]string{"tcp": "", "tcp6": ""}
		for i, socket := range sockets {
			ip := net.ParseIP(socket.address)
			table := "tcp6"
			raw := ip.To16()
			if v4 := ip.To4(); v4 != nil {
				table, raw = "tcp", v4
			}
			raw = append([]byte(nil), raw...)
			for word := 0; word < len(raw); word += 4 {
				binary.NativeEndian.PutUint32(raw[word:word+4], binary.BigEndian.Uint32(raw[word:word+4]))
			}
			rows[table] += fmt.Sprintf("%d: %s:%04X 00000000:0000 0A 0:0 00:0 0 0 0 %s\n", i, hex.EncodeToString(raw), socket.port, socket.inode)
		}
		for table, data := range rows {
			writeOwnershipFixture(t, root, table, data)
		}
	}
	fixtureOwnedFD(t, root, "3", controller.inode)
	fixtureOwnedFD(t, root, "4", old.inode)
	fixtureOwnedFD(t, root, "5", current.inode)
	writeSockets(controller, old)
	m := &MihomoMan{
		procRoot: root, configPath: filepath.Join(root, "mihomo.yaml"),
		pid: 8830, running: true, generation: 7, ownershipTimeout: time.Millisecond,
		lastConf: mihomoConfText(g, endpoints{}, warpAccount{}),
	}
	m.reload = func() error {
		if retainOld {
			writeSockets(controller, old, current)
		} else {
			writeSockets(controller, current)
		}
		return nil
	}
	g.Proxies["warp"].Listen = newAddress
	return m, g
}

func TestReloadWildcardBindChangesRejectObsoleteExposure(t *testing.T) {
	for _, tc := range []struct {
		old, current        string
		retained, wantError bool
	}{
		{"0.0.0.0", "127.0.0.1", true, true},
		{"0.0.0.0", "127.0.0.1", false, false},
		{"::", "::1", true, true},
		{"::", "::1", false, false},
		{"::", "0.0.0.0", true, true},
		{"::", "0.0.0.0", false, false},
		// :: may be v6-only; it must not authorize a leftover IPv4 fd.
		{"0.0.0.0", "::", true, true},
		{"0.0.0.0", "::", false, false},
		// Widening within one family intentionally covers the old binding.
		{"127.0.0.1", "0.0.0.0", true, false},
		{"::1", "::", true, false},
	} {
		t.Run(fmt.Sprintf("%s-to-%s-retained=%v", tc.old, tc.current, tc.retained), func(t *testing.T) {
			m, g := bindRemovalFixture(t, tc.old, tc.current, tc.retained)
			before := m.Snapshot()
			err := m.Ensure(g, endpoints{}, warpAccount{})
			if (err != nil) != tc.wantError {
				t.Fatalf("obsolete exposure error=%v wantError=%v", err, tc.wantError)
			}
			if tc.wantError && (m.Snapshot().Generation != before.Generation || m.Running()) {
				t.Fatal("obsolete wildcard exposure advanced applied state")
			}
			if !tc.wantError && m.Snapshot().Generation != before.Generation+1 {
				t.Fatal("valid wildcard move did not advance generation")
			}
		})
	}
}

func TestReloadRejectsObsoleteSamePortBind(t *testing.T) {
	for _, retain := range []bool{true, false} {
		t.Run(fmt.Sprintf("retained=%v", retain), func(t *testing.T) {
			m, g := bindRemovalFixture(t, "127.0.0.1", "127.0.0.2", retain)
			before := m.Snapshot()
			oldConf := m.lastConf
			err := m.Ensure(g, endpoints{}, warpAccount{})
			if retain {
				if err == nil {
					t.Fatal("reload certified both old and new addresses on the same port")
				}
				if got := m.Snapshot(); got.Generation != before.Generation || got.Running || m.lastConf != oldConf {
					t.Fatalf("failed bind removal advanced applied state: %+v", got)
				}
			} else {
				if err != nil {
					t.Fatalf("clean bind move rejected: %v", err)
				}
				if got := m.Snapshot(); got.Generation != before.Generation+1 || !got.Running {
					t.Fatalf("clean move not applied: %+v", got)
				}
				if _, applied := m.AppliedSnapshot(g, endpoints{}); !applied {
					t.Fatal("clean bind move not verified")
				}
			}
		})
	}
}
