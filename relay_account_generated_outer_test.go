package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// WarpScout v0.16.0 register -fresh can emit independent MASQUE and outer WG
// devices as well as the primary device. These are synthetic, distinct values;
// only the primary tunnel credential may be returned or stored by warpd.
func generatedAccountFixture(t *testing.T) (string, RelayCredential) {
	t.Helper()
	var primary RelayCredential
	if err := json.Unmarshal([]byte(credentialFixtureBody()), &primary); err != nil {
		t.Fatal(err)
	}
	primary.Mode = "generated"
	primary.TunnelIPv6 = "2001:db8::17"
	primary.DNS, primary.MTU = "", 0
	raw, err := json.Marshal(map[string]string{
		"id": "synthetic-primary-id", "token": "synthetic-primary-token",
		"private_key": primary.PrivateKey, "peer_public_key": primary.PeerPublicKey,
		"ipv4": primary.TunnelIPv4, "ipv6": primary.TunnelIPv6,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), primary
}

func generateAccountFixture(t *testing.T, raw string) (RelayCredential, error) {
	t.Helper()
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture.json")
	marker := filepath.Join(root, "completed")
	binary := filepath.Join(root, "synthetic-warpscout")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err := os.WriteFile(fixture, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
test "$#" = 5
test "$1" = register
test "$2" = -a
test "$3" = account.json
test "$4" = -fresh
test "$5" = -plain
cp ` + quote(fixture) + ` "$3"
printf 'ok' > ` + quote(marker) + "\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	credential, err := generateRelayAccount(ctx, binary, root)
	if done, e := os.ReadFile(marker); e != nil || string(done) != "ok" {
		t.Fatal("synthetic CLI did not complete its successful file write")
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatal("generation retained private staging output")
		}
	}
	return credential, err
}

func TestRelayGeneratedOptionalOuterUsesOnlyPrimary(t *testing.T) {
	primary, want := generatedAccountFixture(t)
	// Shape of mintOuter -> mintWGAccount in the pinned fresh-generation path.
	// Different keys and addresses make accidental outer/primary substitution fail.
	outerBytes, err := json.Marshal(map[string]string{
		"id": "synthetic-outer-id", "token": "synthetic-outer-token",
		"private_key": want.PeerPublicKey, "peer_public_key": want.PrivateKey,
		"ipv4": "192.0.2.91", "ipv6": "2001:db8::91",
	})
	if err != nil {
		t.Fatal(err)
	}
	outer := string(outerBytes)
	for _, tc := range []struct{ name, suffix string }{
		{"absent", ""},
		{"null", `,"outer":null`},
		{"populated", `,"outer":` + outer},
		{"with-masque", `,"outer":` + outer + `,"masque":` + outer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := generateAccountFixture(t, strings.TrimSuffix(primary, "}")+tc.suffix+"}")
			if err != nil {
				t.Fatalf("supported generated account rejected after successful CLI: %v", err)
			}
			if got != want {
				t.Fatal("generation did not exclusively project primary tunnel credentials")
			}
		})
	}
}

func TestRelayGeneratedOuterRetainsStrictEnvelope(t *testing.T) {
	primary, _ := generatedAccountFixture(t)
	for _, tc := range []struct{ name, suffix string }{
		{"unknown-top-level", `,"unsupported_hook":"synthetic"`},
		{"case-aliased-outer", `,"Outer":null`},
		{"duplicate-outer", `,"outer":null,"outer":null`},
		{"string", `,"outer":"synthetic"`},
		{"number", `,"outer":1`},
		{"boolean", `,"outer":true`},
		{"array", `,"outer":[]`},
		{"unknown-nested", `,"outer":{"unsupported_hook":"synthetic"}`},
		{"case-aliased-nested", `,"outer":{"Private_Key":"synthetic"}`},
		{"duplicate-nested", `,"outer":{"id":"first","id":"second"}`},
		{"wrong-nested-type", `,"outer":{"private_key":1}`},
		{"null-nested-string", `,"outer":{"private_key":null}`},
		{"nested-chain", `,"outer":{"outer":{}}`},
		{"nested-masque", `,"outer":{"masque":{}}`},
		{"oversized", `,"outer":{"token":"` + strings.Repeat("x", 64<<10) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := generateAccountFixture(t, strings.TrimSuffix(primary, "}")+tc.suffix+"}")
			if err == nil || err.Error() != "Relay account generation failed" || got != (RelayCredential{}) {
				t.Fatal("unsupported generated metadata did not fail closed with a sanitized error")
			}
		})
	}
}

func TestRelayGeneratedOuterDoesNotRelaxImportOrStore(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	path := srv.cfg.Snapshot().ProfilePath
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	revision := srv.cfg.Snapshot().Revision
	body := strings.TrimSuffix(credentialFixtureBody(), "}") + `,"outer":{}}`
	w := profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", body)
	if w.Code != 400 || srv.cfg.Snapshot().Revision != revision {
		t.Fatal("manual credential import accepted generated-only metadata")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected manual import modified the private store")
	}
	var doc map[string]any
	if err := json.Unmarshal(before, &doc); err != nil {
		t.Fatal(err)
	}
	var credential map[string]any
	if err := json.Unmarshal([]byte(body), &credential); err != nil {
		t.Fatal(err)
	}
	doc["profiles"].(map[string]any)[id].(map[string]any)["credential"] = credential
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var decoded profileDocument
	if decodePrivateProfileDocument(raw, &decoded) == nil {
		t.Fatal("private-store decoder accepted generated-only metadata")
	}
}
