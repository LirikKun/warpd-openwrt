package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Both initialization and replacement must use the exact private-document
// decoder. Equal-value aliases/duplicates still lose unsupported source data.
func TestRelayCredentialIntegrationStrictPrivateDocument(t *testing.T) {
	for _, operation := range []string{"initialize", "edit"} {
		for _, extra := range []string{`"SCHEMA":1,`, `"schema":1,`} {
			t.Run(operation+extra, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "profiles.json")
				s := &Settings{}
				if err := s.InitProfiles(path); err != nil {
					t.Fatal(err)
				}
				before := s.Snapshot()
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = append([]byte("{"+extra), raw[1:]...)
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if operation == "initialize" {
					var restarted Settings
					err = restarted.InitProfiles(path)
					if restarted.Snapshot().ProfileSchema != 0 {
						t.Error("invalid document published")
					}
				} else {
					err = s.ChangeProfiles(func(g *Settings) error { g.Proxies["warp"].Name = "local rename"; return nil })
					if !reflect.DeepEqual(before, s.Snapshot()) {
						t.Error("rejection changed memory or revision")
					}
				}
				if err == nil {
					t.Error("ambiguous private document accepted")
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(raw, after) {
					t.Error("rejection changed bytes")
				}
				st, statErr := os.Stat(path)
				if statErr != nil || st.Mode().Perm() != 0644 {
					t.Error("rejection changed file mode")
				}
				dir, statErr := os.Stat(filepath.Dir(path))
				if statErr != nil || dir.Mode().Perm() != 0755 {
					t.Error("rejection changed directory mode")
				}
			})
		}
	}
}
