package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStoreDiskChangedStatePreserved(t *testing.T) {
	for _, kind := range []string{"corrupt", "corrupt-in-place", "future", "valid-new-revision", "valid-same-revision", "revision-only", "older-revision", "invalid-profile", "null-profiles", "unknown-field", "trailing-document", "oversize", "missing"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.json")
			s := &Settings{}
			if err := s.InitProfiles(path); err != nil {
				t.Fatal(err)
			}
			before := s.Snapshot()
			raw := []byte("{")
			switch kind {
			case "future":
				raw = []byte(`{"schema":2,"revision":99,"profiles":{},"future":"preserve"}`)
			case "valid-new-revision", "valid-same-revision", "revision-only", "older-revision", "invalid-profile":
				d := profileDocument{profileSchema, before.Revision, before.Snapshot().Proxies, before.Relays}
				if kind == "valid-new-revision" || kind == "valid-same-revision" {
					d.Profiles["warp"].Name = "external writer"
				}
				if kind == "valid-new-revision" {
					d.Revision++
				}
				if kind == "revision-only" {
					d.Revision += 99
				}
				if kind == "older-revision" {
					d.Revision--
				}
				if kind == "invalid-profile" {
					d.Profiles["warp"].Port = 0
				}
				var err error
				raw, err = json.Marshal(d)
				if err != nil {
					t.Fatal(err)
				}
			case "null-profiles":
				raw = []byte(`{"schema":1,"revision":1,"profiles":null}`)
			case "unknown-field":
				var err error
				raw, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = append([]byte(`{"future":"preserve",`), raw[1:]...)
			case "trailing-document":
				raw = []byte(`{"schema":1,"revision":1,"profiles":{}} {}`)
			case "oversize":
				raw = bytes.Repeat([]byte(" "), maxProfileBytes+1)
			}
			if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if kind == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else if kind == "corrupt-in-place" {
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				// Replace the initialized inode as an external editor would.
				if err := os.WriteFile(path+".external", raw, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".external", path); err != nil {
					t.Fatal(err)
				}
			}
			err := s.ChangeProfiles(func(g *Settings) error {
				g.Proxies["warp"].Name = "local rename"
				return nil
			})
			if err == nil {
				t.Error("changed disk state accepted")
			}
			if !reflect.DeepEqual(before, s.Snapshot()) {
				t.Error("rejected transaction changed memory or revision")
			}
			parent, statErr := os.Stat(filepath.Dir(path))
			if statErr != nil || parent.Mode().Perm() != 0755 {
				t.Fatalf("rejected transaction changed directory mode: %v", statErr)
			}
			after, readErr := os.ReadFile(path)
			if kind == "missing" {
				if !os.IsNotExist(readErr) {
					t.Fatal("removed initialized store was recreated")
				}
			} else {
				if readErr != nil || !bytes.Equal(raw, after) {
					t.Fatalf("external bytes not preserved: %v", readErr)
				}
				st, statErr := os.Stat(path)
				if statErr != nil || st.Mode().Perm() != 0644 {
					t.Fatalf("external mode not preserved: %v", statErr)
				}
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			wantEntries := 1
			if kind == "missing" {
				wantEntries = 0
			}
			if len(entries) != wantEntries {
				t.Fatal("rejected transaction left temporary files")
			}
		})
	}
}

func TestStoreDiskEquivalentDocumentAllowsEdit(t *testing.T) {
	for _, kind := range []string{"reformatted", "omitted-empty-relays", "normalized-exclusions"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.json")
			s := &Settings{}
			if err := s.InitProfiles(path); err != nil {
				t.Fatal(err)
			}
			if kind == "normalized-exclusions" {
				if err := s.ChangeProfiles(func(g *Settings) error {
					g.Proxies["warp"].ExcludeCountries = "DE,NL"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := s.Snapshot()
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err = json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if kind == "omitted-empty-relays" {
				delete(doc, "relays")
			}
			if kind == "normalized-exclusions" {
				doc["profiles"].(map[string]any)["warp"].(map[string]any)["ExcludeCountries"] = " de,NL,de "
			}
			raw, err = json.MarshalIndent(doc, "", "\t")
			if err != nil {
				t.Fatal(err)
			}
			// Different key order, whitespace and JSON escaping remain valid.
			raw = []byte(strings.Replace(string(raw), `"Name": "warp"`, `"Name": "\u0077arp"`, 1))
			if err = os.WriteFile(path+".external", raw, 0644); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(path+".external", path); err != nil {
				t.Fatal(err)
			}
			if err = s.ChangeProfiles(func(g *Settings) error {
				g.Proxies["warp"].Name = "saved rename"
				return nil
			}); err != nil {
				t.Fatalf("semantically unchanged disk rejected: %v", err)
			}
			var restarted Settings
			if err = restarted.InitProfiles(path); err != nil {
				t.Fatal(err)
			}
			if restarted.Snapshot().Revision != before.Revision+1 || restarted.Snapshot().Proxies["warp"].Name != "saved rename" {
				t.Fatal("successful edit did not round-trip")
			}
			st, err := os.Stat(path)
			if err != nil || st.Mode().Perm() != 0600 {
				t.Fatalf("successful write did not secure file mode: %v", err)
			}
		})
	}
}

func TestStoreDiskCreationDoesNotReplaceAppearedTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	dir, err := openProfileStoreDir(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.file.Close()
	// Model a target appearing after InitProfiles observed absence.
	raw := []byte(`{"schema":2,"revision":99,"profiles":{}}`)
	if err = os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	g := Settings{Revision: 1, Proxies: map[string]*ProxySettings{}}
	if err = persistProfilesInDir(g, dir, nil); err == nil {
		t.Fatal("creation replaced an appeared target")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("appeared target bytes not preserved: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0644 {
		t.Fatalf("appeared target mode not preserved: %v", err)
	}
}
