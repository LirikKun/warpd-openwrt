package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture never invokes the system UCI or opens the router's config.
func fakeToggleUCI(t *testing.T) (string, func() map[string]string) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "uci.json")
	script := `#!/usr/bin/env python3
import json,os,sys
p=os.environ['WARPD_TEST_UCI_DB']
try:
 with open(p) as f: d=json.load(f)
except FileNotFoundError: d={}
a=[x for x in sys.argv[1:] if x!='-q']
if a[0]=='get':
 if a[1] not in d: sys.exit(1)
 print(d[a[1]])
elif a[0]=='set':
 if os.environ.get('WARPD_TEST_UCI_NOOP')=='1': sys.exit(0)
 k,v=a[1].split('=',1); d[k]=v
 with open(p,'w') as f: json.dump(d,f)
elif a[0]=='commit': pass
else: sys.exit(2)
`
	if err := os.WriteFile(filepath.Join(dir, "uci"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WARPD_TEST_UCI_DB", db)
	read := func() map[string]string {
		t.Helper()
		d := map[string]string{}
		b, err := os.ReadFile(db)
		if err == nil {
			if err = json.Unmarshal(b, &d); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	return db, read
}

func TestToggleConfigVerifiesPersistedDesiredState(t *testing.T) {
	fakeToggleUCI(t)
	t.Setenv("WARPD_TEST_UCI_NOOP", "1")
	s := statusTestSettings()
	if _, err := s.ApplyConfigPatch(map[string]any{"warp_enabled": false}); err == nil {
		t.Fatal("successful UCI exit without persisted desired state reported success")
	}
	if !s.Snapshot().Proxies["warp"].Enabled {
		t.Fatal("failed persistence changed live desired state")
	}
}

func TestToggleConfigValidatesWholePatchBeforeWrites(t *testing.T) {
	_, read := fakeToggleUCI(t)
	s := statusTestSettings()
	for i := 0; i < 32; i++ {
		if _, err := s.ApplyConfigPatch(map[string]any{"masque_enabled": true, "warp_port": float64(1)}); err == nil {
			t.Fatal("invalid port accepted")
		}
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("partially persisted invalid patch: %v", got)
	}
}

func TestToggleConfigAllFourPreservePinsAndUnknownSettings(t *testing.T) {
	db, read := fakeToggleUCI(t)
	initial := map[string]string{"warpd.warp.endpoint": "192.0.2.9:2408", "warpd.m2w.inner_endpoint": "192.0.2.8:2408", "warpd.custom.keep": "sentinel"}
	b, _ := json.Marshal(initial)
	if err := os.WriteFile(db, b, 0600); err != nil {
		t.Fatal(err)
	}
	s := LoadSettings()
	for _, key := range proxyNames {
		if _, err := s.ApplyConfigPatch(map[string]any{key + "_enabled": true}); err != nil {
			t.Fatal(err)
		}
		if !s.Snapshot().Proxies[key].Enabled {
			t.Fatalf("%s not enabled", key)
		}
	}
	for key, value := range initial {
		if read()[key] != value {
			t.Fatalf("erased %s", key)
		}
	}
}

func TestToggleConfigRejectsNonBooleanWithoutWrites(t *testing.T) {
	_, read := fakeToggleUCI(t)
	for _, key := range []string{"warp_enabled", "masque_enabled", "masqueh2_enabled", "m2w_enabled", "autoscan_start"} {
		for _, value := range []any{"false", nil, float64(0), []any{}} {
			s := statusTestSettings()
			_, err := s.ApplyConfigPatch(map[string]any{key: value})
			if err == nil || !strings.Contains(err.Error(), "expected boolean") {
				t.Errorf("%s=%#v: want boolean validation, got %v", key, value, err)
			}
		}
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("invalid boolean persisted: %v", got)
	}
}
