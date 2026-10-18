package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestProfileMigrationPreservesCapturedAccountI1(t *testing.T) {
	dir := t.TempDir()
	packet := "<r 3><b 0x01020304>"
	b, _ := json.Marshal(map[string]string{"gen_i1": packet})
	os.WriteFile(filepath.Join(dir, "warp-account.json"), b, 0600)
	p := (&ProxySettings{}).defaults("warp")
	p.GenI1 = "quic"
	p.Enabled = true
	s := &Settings{Proxies: map[string]*ProxySettings{"warp": p}}
	if err := s.InitProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Proxies["warp"]; got.AWG.I1 != packet || got.GenI1 != "quic" {
		t.Fatalf("migration changed captured engine I1 or erased legacy generator: %+v", got.AWG)
	}
}

func TestProfileStoreRejectsUnsafeFilesystemObjects(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	os.WriteFile(outside, []byte(`{"schema":1,"profiles":{}}`), 0600)
	for _, kind := range []string{"symlink", "hardlink", "ancestor"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(base, kind)
			os.Mkdir(dir, 0755)
			path := filepath.Join(dir, "profiles")
			switch kind {
			case "symlink":
				os.Symlink(outside, path)
			case "hardlink":
				os.Link(outside, path)
			case "ancestor":
				os.Remove(dir)
				os.Symlink(base, dir)
			}
			s := &Settings{}
			if s.InitProfiles(path) == nil {
				t.Fatal("unsafe store path accepted")
			}
			b, _ := os.ReadFile(outside)
			if string(b) != `{"schema":1,"profiles":{}}` {
				t.Fatal("unsafe target altered")
			}
		})
	}
	dir := filepath.Join(base, "public")
	os.Mkdir(dir, 0755)
	s := &Settings{}
	if err := s.InitProfiles(filepath.Join(dir, "p")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("existing store directory not made private")
	}
}

func TestProfileDuplicateRelayAndUnsupportedScannerAWGRejected(t *testing.T) {
	g := Settings{Proxies: map[string]*ProxySettings{}, Relays: map[string]*RelaySettings{
		"a": {Key: "a", Name: "a", MonitorInterval: 300, ActivationURL: "https://relay.tribukvy.ltd/activate/shared"},
		"b": {Key: "b", Name: "b", MonitorInterval: 300, ActivationURL: "https://relay.tribukvy.ltd/activate/shared"},
	}}
	if validateProfiles(g, nil) == nil {
		t.Fatal("duplicate access resources can race the same capability")
	}
	delete(g.Relays, "b")
	p := NewProxyProfile("warp", "warp")
	p.AWG.S1 = 1
	g.Proxies["warp"] = p
	if validateProfiles(g, nil) == nil {
		t.Fatal("accepted AWG S1 unsupported by pinned scanner")
	}
	p.AWG = DefaultAWGOptions()
	p.AWG.H1 = 10
	if validateProfiles(g, nil) == nil {
		t.Fatal("accepted header incompatible with scanner fixed peer protocol")
	}
}

func TestDynamicGlobalPatchPreservesProfilesAndRejectsFixedSlots(t *testing.T) {
	fakeToggleUCI(t)
	s := &Settings{}
	if err := s.InitProfiles(filepath.Join(t.TempDir(), "p")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyConfigPatch(map[string]any{"warp_enabled": false}); err == nil {
		t.Fatal("obsolete fixed-slot mutation accepted")
	}
	if _, err := s.ApplyConfigPatch(map[string]any{"exclude_countries": " by,DE,by "}); err != nil {
		t.Fatal(err)
	}
	if !s.Snapshot().Proxies["warp"].Enabled {
		t.Fatal("global update overwrote profile desired enabled")
	}
	if s.Snapshot().ExcludeCountries != "BY,DE" {
		t.Fatal("global exclusions not normalized")
	}
	if _, err := s.ApplyConfigPatch(map[string]any{"exclude_nodes": "not-a-code"}); err == nil {
		t.Fatal("invalid global exclusion accepted")
	}
}
func TestLegacyChainAllowsDisableButNoNewChainOrEndpointEdit(t *testing.T) {
	p := NewProxyProfile("old", "legacy-m2w")
	p.Enabled = true
	p.Port = 4400
	g := Settings{Proxies: map[string]*ProxySettings{"old": p}}
	before := g.Snapshot()
	g.Proxies["old"].Enabled = false
	if err := validateProfiles(g, &before); err != nil {
		t.Fatalf("cannot safely disable migrated chain: %v", err)
	}
	g.Proxies["old"].Inner = "192.0.2.1:2408"
	if validateProfiles(g, &before) == nil {
		t.Fatal("edited read-only chain endpoint")
	}
}

func TestProfileMigrationPreservesOnlyExistingSections(t *testing.T) {
	db, _ := fakeToggleUCI(t)
	raw := map[string]string{"warpd.warp.enabled": "0", "warpd.warp.port": "4101", "warpd.warp.endpoint": "192.0.2.5:2408", "warpd.masqueh2.enabled": "1", "warpd.masqueh2.port": "4102", "warpd.m2w.enabled": "0", "warpd.m2w.inner_endpoint": "192.0.2.7:2408", "warpd.global.web_language": "en"}
	b, _ := json.Marshal(raw)
	os.WriteFile(db, b, 0600)
	s := LoadSettings()
	if err := s.InitProfiles(filepath.Join(t.TempDir(), "p")); err != nil {
		t.Fatal(err)
	}
	g := s.Snapshot()
	if len(g.Proxies) != 3 || g.Proxies["masque"] != nil {
		t.Fatalf("migration added missing legacy slots: %v", profileIDs(g))
	}
	if g.Proxies["warp"].Enabled || g.Proxies["warp"].Port != 4101 || g.Proxies["warp"].Endpoint != "192.0.2.5:2408" || g.Language != "en" {
		t.Fatal("migration lost legacy choices")
	}
	if profileType("m2w", g.Proxies["m2w"]) != "legacy-m2w" || g.Proxies["m2w"].Inner != "192.0.2.7:2408" {
		t.Fatal("lost archived legacy chain")
	}
	if g.Proxies["masqueh2"].Transport != "http2" {
		t.Fatal("lost H2 transport")
	}
}
func TestProfileMigrationFreshMarkerOnlyWarp(t *testing.T) {
	db, _ := fakeToggleUCI(t)
	b, _ := json.Marshal(map[string]string{"warpd.global.profile_schema": "1", "warpd.warp.port": "4001", "warpd.warp.enabled": "1", "warpd.masque.port": "4003", "warpd.masque.enabled": "0"})
	os.WriteFile(db, b, 0600)
	s := LoadSettings()
	if err := s.InitProfiles(filepath.Join(t.TempDir(), "p")); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().Proxies) != 1 {
		t.Fatal("fresh marker created extra profiles")
	}
}
func TestProfileStoreCorruptNeverResets(t *testing.T) {
	for _, raw := range []string{`{`, `{"schema":999,"profiles":{}}`, `{"schema":1,"profiles":{"warp":{"Key":"warp"}}}`} {
		path := filepath.Join(t.TempDir(), "p")
		os.WriteFile(path, []byte(raw), 0600)
		s := &Settings{}
		if s.InitProfiles(path) == nil {
			t.Fatal("invalid store loaded")
		}
		b, _ := os.ReadFile(path)
		if string(b) != raw {
			t.Fatal("invalid store overwritten")
		}
	}
}

type profileStorage interface {
	InitProfiles(string) error
	ChangeProfiles(func(*Settings) error) error
}

func storageFor(t *testing.T, s *Settings) profileStorage {
	t.Helper()
	v, ok := any(s).(profileStorage)
	if !ok {
		t.Fatal("Settings lacks persistent dynamic profile lifecycle")
	}
	return v
}
func TestProfileStoreEmptySurvivesRestart(t *testing.T) {
	s := &Settings{Proxies: map[string]*ProxySettings{}}
	path := filepath.Join(t.TempDir(), "profiles.json")
	store := storageFor(t, s)
	if err := store.InitProfiles(path); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().Proxies) != 1 {
		t.Fatal("fresh installation must create only WARP")
	}
	if err := store.ChangeProfiles(func(g *Settings) error { g.Proxies = map[string]*ProxySettings{}; return nil }); err != nil {
		t.Fatal(err)
	}
	next := &Settings{}
	if err := storageFor(t, next).InitProfiles(path); err != nil {
		t.Fatal(err)
	}
	if len(next.Snapshot().Proxies) != 0 {
		t.Fatal("intentionally empty store repopulated")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private store mode: %v %v", info, err)
	}
	var raw map[string]any
	b, _ := os.ReadFile(path)
	if json.Unmarshal(b, &raw) != nil {
		t.Fatal("not JSON")
	}
}

func TestProfileStoreValidationAndIsolation(t *testing.T) {
	s := &Settings{}
	path := filepath.Join(t.TempDir(), "p.json")
	if err := s.InitProfiles(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	cases := []func(*Settings){
		func(g *Settings) { g.Proxies["bad id"] = NewProxyProfile("bad id", "warp") },
		func(g *Settings) { g.Proxies["warp"].Port = 9099 },
		func(g *Settings) { g.Proxies["warp"].ExcludeCountries = "ZZ" },
		func(g *Settings) {
			p := NewProxyProfile("relay", "relay-warp")
			p.RelayID = "absent"
			g.Proxies["relay"] = p
		},
		func(g *Settings) { g.Proxies["chain"] = NewProxyProfile("chain", "legacy-m2w") },
		func(g *Settings) {
			p := NewProxyProfile("mq", "masque")
			p.Port = 4700
			p.AWG.JC = 999
			g.Proxies["mq"] = p
		},
		func(g *Settings) { g.Proxies["warp"].Outer = "192.0.2.1:2408" },
		func(g *Settings) { g.Proxies["warp"].AWG.JC = 0 },
		func(g *Settings) { g.Proxies["warp"].AWG.JMax = 1 },
		func(g *Settings) { g.Proxies["warp"].AWG.I1 = "x\nproxies: []" },
		func(g *Settings) { g.Proxies["warp"].Type = "masque" },
	}
	for i, edit := range cases {
		err := s.ChangeProfiles(func(g *Settings) error { edit(g); return nil })
		if err == nil {
			t.Errorf("invalid case %d accepted", i)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rejected transaction changed durable bytes")
	}
	var retained *ProxySettings
	if err := s.ChangeProfiles(func(g *Settings) error { retained = g.Proxies["warp"]; retained.Name = "renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	retained.Name = "outside"
	if s.Snapshot().Proxies["warp"].Name != "renamed" {
		t.Fatal("callback aliases live state")
	}
	s.ProfilePath = filepath.Join(path, "cannot-write")
	revision := s.Snapshot().Revision
	if err := s.ChangeProfiles(func(g *Settings) error { g.Proxies["warp"].Name = "lost"; return nil }); err == nil {
		t.Fatal("write failure accepted")
	}
	if s.Snapshot().Revision != revision || s.Snapshot().Proxies["warp"].Name != "renamed" {
		t.Fatal("failed persistence published")
	}
}
func TestProfileStoreDependencyIncludesDisabledChildren(t *testing.T) {
	s := &Settings{}
	if err := s.InitProfiles(filepath.Join(t.TempDir(), "p")); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeProfiles(func(g *Settings) error {
		g.Relays["r"] = &RelaySettings{Key: "r", Name: "access", ActivationURL: "https://relay.tribukvy.ltd/activate/test", MonitorInterval: 300}
		p := NewProxyProfile("child", "relay-warp")
		p.Port = 4100
		p.RelayID = "r"
		p.RelayCountry = "DE"
		g.Proxies["child"] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeProfiles(func(g *Settings) error { delete(g.Relays, "r"); return nil }); err == nil {
		t.Fatal("removed referenced Relay with disabled child")
	}
	if err := s.ChangeProfiles(func(g *Settings) error { g.Relays["r"].ActivationURL = ""; return nil }); err == nil {
		t.Fatal("cleared referenced capability")
	}
	snap := s.Snapshot()
	snap.Relays["r"].Name = "changed"
	if s.Snapshot().Relays["r"].Name == "changed" {
		t.Fatal("relay snapshot aliases live settings")
	}
}
func TestProfileStoreConcurrentTransactions(t *testing.T) {
	s := &Settings{}
	if err := s.InitProfiles(filepath.Join(t.TempDir(), "p")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.ChangeProfiles(func(g *Settings) error {
				p := NewProxyProfile(fmt.Sprintf("p%d", i), "warp")
				p.Port = 4200 + i
				g.Proxies[p.Key] = p
				return nil
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(s.Snapshot().Proxies) != 13 {
		t.Fatalf("lost transactions: %d", len(s.Snapshot().Proxies))
	}
	b, _ := os.ReadFile(s.ProfilePath)
	if strings.Count(string(b), "\"Key\"") != 13 {
		t.Fatal("durable snapshot lost transaction")
	}
}
