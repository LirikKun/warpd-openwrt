package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Read-only UCI stand-in: any write is an error, never a system UCI invocation.
func storeReviewUCI(t *testing.T, fixture map[string]string) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "uci.json")
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(db, raw, 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import json,os,sys
a=[v for v in sys.argv[1:] if v!='-q']
if len(a)!=2 or a[0]!='get': sys.exit(2)
with open(os.environ['WARPD_STORE_REVIEW_UCI']) as f: d=json.load(f)
if a[1] not in d: sys.exit(1)
print(d[a[1]])
`
	if err = os.WriteFile(filepath.Join(dir, "uci"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WARPD_STORE_REVIEW_UCI", db)
	return db, raw
}

func TestStoreReviewMigrationSectionOnlyOptions(t *testing.T) {
	for _, sectionToken := range []bool{false, true} {
		t.Run(fmt.Sprint(sectionToken), func(t *testing.T) {
			fixture := map[string]string{
				"warpd.warp.listen":     "127.0.0.2",
				"warpd.masque.endpoint": "192.0.2.9:443", "warpd.masque.listen": "127.0.0.1",
				"warpd.masqueh2.endpoint": "192.0.2.6:443", "warpd.masqueh2.listen": "127.0.0.3",
				"warpd.m2w.inner_endpoint": "192.0.2.8:2408", "warpd.m2w.outer_endpoint": "192.0.2.7:443", "warpd.m2w.listen": "127.0.0.4",
				"warpd.custom.keep": "sentinel",
			}
			if sectionToken {
				for _, id := range []string{"warp", "masque", "masqueh2", "m2w"} {
					fixture["warpd."+id] = id
				}
			}
			db, raw := storeReviewUCI(t, fixture)
			s := LoadSettings()
			path := filepath.Join(t.TempDir(), "profiles.json")
			if err := s.InitProfiles(path); err != nil {
				t.Fatal(err)
			}
			g := s.Snapshot()
			if g.Proxies["warp"].Enabled {
				t.Error("bind-only WARP unexpectedly enabled")
			}
			for _, id := range []string{"warp", "masque", "masqueh2", "m2w"} {
				p := g.Proxies[id]
				if p == nil {
					t.Fatalf("section %s missing", id)
				}
				if p.Listen != fixture["warpd."+id+".listen"] {
					t.Errorf("%s bind lost: %q", id, p.Listen)
				}
			}
			if g.Proxies["masque"].Endpoint != fixture["warpd.masque.endpoint"] || g.Proxies["masqueh2"].Endpoint != fixture["warpd.masqueh2.endpoint"] {
				t.Error("MASQUE pin-only section lost endpoint")
			}
			c := g.Proxies["m2w"]
			if c.Enabled || c.Inner != fixture["warpd.m2w.inner_endpoint"] || c.Outer != fixture["warpd.m2w.outer_endpoint"] {
				t.Errorf("disabled chain options lost: enabled=%v inner=%q outer=%q", c.Enabled, c.Inner, c.Outer)
			}
			var restarted Settings
			if err := restarted.InitProfiles(path); err != nil {
				t.Fatal(err)
			}
			if restarted.Snapshot().Proxies["m2w"].Inner != c.Inner {
				t.Fatal("chain pin lost on restart")
			}
			if after, _ := os.ReadFile(db); !bytes.Equal(raw, after) {
				t.Fatal("migration modified UCI bytes")
			}
		})
	}
}

func TestStoreReviewAncestorSwap(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.Mkdir(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "profiles.json")
	sentinel := filepath.Join(outside, "profiles.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"profiles":{}}`+strings.Repeat(" ", (1<<20)-128)), 0600); err != nil {
		t.Fatal(err)
	}
	original := []byte("outside-sentinel")
	if err := os.WriteFile(sentinel, original, 0644); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if _, err = syscall.InotifyAddWatch(fd, path, syscall.IN_OPEN); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var buf [4096]byte
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err := syscall.Read(fd, buf[:])
			if err == syscall.EAGAIN && time.Now().Before(deadline) {
				time.Sleep(50 * time.Microsecond)
				continue
			}
			if err != nil {
				done <- err
				return
			}
			break
		}
		err := os.Rename(dir, dir+"-held")
		if err == nil {
			err = os.Symlink(outside, dir)
		}
		done <- err
	}()
	var s Settings
	initErr := s.InitProfiles(path)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 || !bytes.Equal(original, after) {
		t.Fatalf("raced ancestor traversed: init=%v outside mode=%04o bytesChanged=%v", initErr, info.Mode().Perm(), !bytes.Equal(original, after))
	}
	// A subsequent transaction must reject the substituted symlink too.
	if initErr == nil && s.ChangeProfiles(func(g *Settings) error { g.Proxies = map[string]*ProxySettings{}; return nil }) == nil {
		t.Fatal("transaction accepted replaced symlink ancestor")
	}
}

func TestStoreReviewAnchoredReadAndAtomicWrite(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "private")
	outside := filepath.Join(base, "outside")
	for _, p := range []string{dir, outside} {
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	original := []byte(`{"schema":1,"profiles":{}}`)
	sentinel := filepath.Join(outside, "profiles.json")
	sentinelBytes := []byte("outside-unchanged")
	if err := os.WriteFile(sentinel, sentinelBytes, 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "profiles.json")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	d, err := openProfileStoreDir(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.file.Close()
	f, err := d.open(d.name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	held := dir + "-held"
	if err = os.Rename(dir, held); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	read, err := boundedProfileRead(f)
	if err != nil || !bytes.Equal(read, original) {
		t.Fatalf("read escaped opened FD: %v", err)
	}
	if err = f.Chmod(0600); err != nil {
		t.Fatal(err)
	}
	replacement := []byte(`{"schema":1,"revision":2,"profiles":{}}`)
	before := (&Settings{ProfileSchema: 1, Proxies: map[string]*ProxySettings{}}).Snapshot()
	if err = d.write(replacement, &before); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, sentinelBytes) || st.Mode().Perm() != 0644 {
		t.Fatal("anchored write/chmod traversed replacement ancestor")
	}
	got, err := os.ReadFile(filepath.Join(held, "profiles.json"))
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("atomic rename not relative to opened directory: %v", err)
	}
	st, err = os.Stat(held)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatal("opened parent was not secured")
	}
	entries, err := os.ReadDir(held)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v", err)
	}
}

func TestStoreReviewUnsafeAncestorsAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"group-writable-ancestor", "world-writable-ancestor", "fifo", "directory", "account-symlink", "account-hardlink"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			parent := filepath.Join(base, "parent")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "profiles.json")
			outside := filepath.Join(base, "account")
			raw := []byte(`{"gen_i1":"<r 2>"}`)
			if err := os.WriteFile(outside, raw, 0644); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "group-writable-ancestor":
				if err := os.Chmod(parent, 0770); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(parent, "nested", "profiles.json")
			case "world-writable-ancestor":
				if err := os.Chmod(parent, 0777); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(parent, "nested", "profiles.json")
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "account-symlink":
				if err := os.Symlink(outside, filepath.Join(parent, "warp-account.json")); err != nil {
					t.Fatal(err)
				}
			case "account-hardlink":
				if err := os.Link(outside, filepath.Join(parent, "warp-account.json")); err != nil {
					t.Fatal(err)
				}
			}
			s := Settings{Proxies: map[string]*ProxySettings{"warp": (&ProxySettings{}).defaults("warp")}}
			if err := s.InitProfiles(path); err == nil {
				t.Fatal("unsafe ancestor or object accepted")
			}
			after, err := os.ReadFile(outside)
			if err != nil || !bytes.Equal(after, raw) {
				t.Fatal("outside account bytes altered")
			}
			st, err := os.Stat(outside)
			if err != nil || st.Mode().Perm() != 0644 {
				t.Fatal("outside account chmodded")
			}
		})
	}
}

func storeReviewI1Packets() map[string]string {
	b := func(s string) string { return "<b 0x" + hex.EncodeToString([]byte(s)) + ">" }
	return map[string]string{
		"stun":            "<b 0x000100142112a442><r 12><b 0x80220010><rc 16>",
		"sip":             b("OPTIONS sip:example.invalid SIP/2.0\r\nVia: SIP/2.0/UDP 192.168.0.2:5060;branch=z9hG4bK") + "<rc 10>" + b("\r\nFrom: <sip:") + "<rc 8>" + b("@example.invalid>;tag=") + "<rd 6>" + b("\r\nCall-ID: ") + "<rc 16>" + b("\r\nCSeq: ") + "<rd 4>" + b(" OPTIONS\r\nContent-Length: 0\r\n\r\n"),
		"long-byte":       "<b 0x" + strings.Repeat("ab", 1280) + ">",
		"max-single-byte": "<b 0x" + strings.Repeat("ab", 32763) + ">",
		"max-packet":      strings.Repeat("<r 1000>", 65) + "<rc 500><rd 7>",
	}
}

func TestStoreReviewI1CapturedMigration(t *testing.T) {
	for name, packet := range storeReviewI1Packets() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			account, _ := json.Marshal(map[string]string{"gen_i1": packet})
			ap := filepath.Join(dir, "warp-account.json")
			if err := os.WriteFile(ap, account, 0600); err != nil {
				t.Fatal(err)
			}
			p := (&ProxySettings{}).defaults("warp")
			p.GenI1 = "stun"
			p.Enabled = true
			s := &Settings{Proxies: map[string]*ProxySettings{"warp": p}}
			path := filepath.Join(dir, "profiles.json")
			if err := s.InitProfiles(path); err != nil {
				t.Fatalf("scanner/engine-compatible I1 cannot migrate: %v", err)
			}
			if s.Snapshot().Proxies["warp"].AWG.I1 != packet {
				t.Fatal("captured packet altered")
			}
			var next Settings
			if err := next.InitProfiles(path); err != nil || next.Snapshot().Proxies["warp"].AWG.I1 != packet {
				t.Fatalf("captured packet cannot restart: %v", err)
			}
			if after, _ := os.ReadFile(ap); !bytes.Equal(account, after) {
				t.Fatal("account bytes modified")
			}
		})
	}
}

func TestStoreReviewI1BoundsAndInjection(t *testing.T) {
	for name, packet := range map[string]string{
		"packet-overflow": strings.Repeat("<r 1000>", 65) + "<r 508>",
		"uapi-overflow":   "<b 0x" + strings.Repeat("ab", 32764) + ">",
		"random-overflow": "<r 1001>", "ascii-overflow": "<rc 1001>", "negative": "<rd -1>", "zero": "<rc 0>",
		"unknown": "<wt 1>", "yaml": "<rc 2>\nproxies: []", "quote": "<b 0xab>'", "odd-hex": "<b 0xabc>",
		"giant-number": "<r 999999999999999999999999999999>",
	} {
		t.Run(name, func(t *testing.T) {
			a := DefaultAWGOptions()
			a.I1 = packet
			if validateAWG(a) == nil {
				t.Fatal("unsafe or incompatible chain accepted")
			}
		})
	}
}

func TestStoreReviewI1PinnedEngine(t *testing.T) {
	binary := os.Getenv("WARPD_TEST_PINNED_MIHOMO")
	if binary == "" {
		t.Skip("set WARPD_TEST_PINNED_MIHOMO for offline pinned parser test")
	}
	for name, packet := range storeReviewI1Packets() {
		t.Run(name, func(t *testing.T) {
			p := NewProxyProfile("warp", "warp")
			p.Enabled = true
			p.Endpoint = "192.0.2.1:2408"
			p.AWG.I1 = packet
			g := Settings{ProfileSchema: 1, Proxies: map[string]*ProxySettings{"warp": p}}
			key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			conf := mihomoConfText(g, endpoints{}, warpAccount{PrivateKey: key, PeerPub: key, IPv4: "172.16.0.2"})
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(conf), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(binary, "-t", "-d", dir, "-f", path).CombinedOutput()
			if err != nil {
				t.Fatalf("pinned parser: %v %s", err, out)
			}
			t.Logf("pinned parser accepted %s, text bytes=%d", name, len(packet))
		})
	}
}

func TestStoreReviewSizeRejectPreservesRoundTrip(t *testing.T) {
	s := &Settings{}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := s.InitProfiles(path); err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 3000)
	for i := range codes {
		codes[i] = string([]byte{byte('A' + i/676), byte('A' + i/26%26), byte('A' + i%26)})
	}
	nodes := strings.Join(codes, ",")
	rejected := false
	for i := 0; i < 100; i++ {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		revision := s.Snapshot().Revision
		err = s.ChangeProfiles(func(g *Settings) error {
			id := fmt.Sprintf("large%d", i)
			p := NewProxyProfile(id, "warp")
			p.Port = 5000 + i
			p.ExcludeNodes = nodes
			g.Proxies[id] = p
			return nil
		})
		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err != nil {
			rejected = true
			if !bytes.Equal(before, after) || s.Snapshot().Revision != revision {
				t.Fatal("rejected size transaction changed bytes or revision")
			}
			t.Logf("rejected transaction %d at %d durable bytes, revision %d: %v", i, len(after), revision, err)
			break
		}
		if len(after) > 1<<20 {
			t.Fatalf("writer committed %d bytes beyond reader limit at revision %d", len(after), s.Snapshot().Revision)
		}
	}
	if !rejected {
		t.Fatal("oversize candidate was not rejected")
	}
	var restarted Settings
	if err := restarted.InitProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeProfiles(func(g *Settings) error { g.Proxies = map[string]*ProxySettings{}; return nil }); err != nil {
		t.Fatalf("shrink from valid near-limit store failed: %v", err)
	}
	var shrunk Settings
	if err := shrunk.InitProfiles(path); err != nil || len(shrunk.Snapshot().Proxies) != 0 {
		t.Fatalf("shrink cannot restart: %v", err)
	}
	// The exact reader boundary remains accepted; a byte beyond is rejected.
	raw, _ := json.Marshal(profileDocument{Schema: 1, Profiles: map[string]*ProxySettings{}})
	for _, n := range []int{1 << 20, (1 << 20) + 1} {
		p := filepath.Join(t.TempDir(), "profiles.json")
		b := append(raw, bytes.Repeat([]byte(" "), n-len(raw))...)
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
		var next Settings
		err := next.InitProfiles(p)
		if (err == nil) != (n == 1<<20) {
			t.Fatalf("reader boundary %d: %v", n, err)
		}
	}
}
