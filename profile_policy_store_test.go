package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilePolicyPrivateStoreExactSchemaAndLegacy(t *testing.T) {
	for _, fields := range []string{
		`"policy_mode":"off","policy_mode":"off"`,
		`"policy_mode":"off","POLICY_MODE":"off"`,
		`"PolicyMode":"off"`, `"policy_mode":null`, `"policy_mode":"invalid"`,
		`"policy_country":"DE","policy_country":"DE"`,
		`"policy_country":"DE","POLICY_COUNTRY":"DE"`,
		`"PolicyCountry":"DE"`, `"policy_country":null`, `"policy_country":"de"`,
		`"policy_mode":"strict","policy_country":"XX"`,
	} {
		t.Run(fields, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.json")
			s := &Settings{}
			if err := s.InitProfiles(path); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var compact bytes.Buffer
			if err = json.Compact(&compact, raw); err != nil {
				t.Fatal(err)
			}
			raw = compact.Bytes()
			// Existing serializer's identity field gives an exact insertion site without
			// constructing a second private schema by hand.
			raw = []byte(strings.Replace(string(raw), `"Key":"warp"`, `"Key":"warp",`+fields, 1))
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var restart Settings
			if err = restart.InitProfiles(path); err == nil {
				t.Fatal("invalid policy document accepted at startup")
			}
			before := s.Snapshot().Revision
			if err = s.ChangeProfiles(func(g *Settings) error { g.Proxies["warp"].Name = "edited"; return nil }); err == nil {
				t.Fatal("invalid external policy accepted during replacement")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, after) || s.Snapshot().Revision != before {
				t.Fatal("rejected policy store was rewritten")
			}
		})
	}
	for _, fields := range []string{"", `,"policy_mode":""`, `,"policy_mode":"off"`, `,"policy_mode":"exclusions"`, `,"policy_mode":"strict","policy_country":"DE"`} {
		t.Run("compatible/"+fields, func(t *testing.T) {
			p := NewProxyProfile("warp", "warp")
			raw, err := json.Marshal(profileDocument{Schema: 1, Revision: 1, Profiles: map[string]*ProxySettings{"warp": p}})
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(strings.Replace(string(raw), `"Key":"warp"`, `"Key":"warp"`+fields, 1))
			path := filepath.Join(t.TempDir(), "profiles.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var s Settings
			if err = s.InitProfiles(path); err != nil {
				t.Fatal(err)
			}
			if fields == "" && s.Proxies["warp"].PolicyMode != "" {
				t.Fatal("absent legacy policy silently migrated")
			}
		})
	}
}
