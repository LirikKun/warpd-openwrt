package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRelayAccountExplicitGenerationFromIsolatedCommand(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	root := t.TempDir()
	var manual RelayCredential
	if err := json.Unmarshal([]byte(credentialFixtureBody()), &manual); err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal(map[string]any{"id": "synthetic-id", "token": "synthetic-token", "private_key": manual.PrivateKey, "peer_public_key": manual.PeerPublicKey, "ipv4": manual.TunnelIPv4})
	fixture := filepath.Join(root, "fixture.json")
	if err := os.WriteFile(fixture, output, 0600); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "fake-warpscout")
	script := `#!/bin/sh
set -eu
test "$#" = 5
test "$1" = register
test "$2" = -a
test "$4" = -fresh
test "$5" = -plain
test "$(stat -c %a .)" = 700
test "$(stat -c %a "$3")" = 600
printf 'synthetic secret output must be discarded\n'
cp '` + fixture + `' "$3"
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	srv.accounts.generate = func(ctx context.Context) (RelayCredential, error) { return generateRelayAccount(ctx, command, root) }
	rev := srv.cfg.Snapshot().Revision
	w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", fmt.Sprintf(`{"confirmed":true,"revision":%d}`, rev))
	if w.Code != 202 {
		t.Fatalf("explicit fixture generation not queued: HTTP=%d", w.Code)
	}
	state := waitAccountState(t, srv, id)
	if state != "succeeded" {
		t.Fatalf("fixture did not commit: %s", state)
	}
	g := srv.cfg.Snapshot()
	c := g.Proxies[id].Credential
	if c.Mode != "generated" || c.PrivateKey != manual.PrivateKey || c.TunnelIPv4 != manual.TunnelIPv4 || c.TunnelIPv6 != "" || c.Reserved != "" || c.DNS != "" || c.MTU != 0 {
		t.Fatal("generated credential invented or lost data")
	}
	if g.Revision != rev+1 {
		t.Fatal("generated commit missing revision invalidation")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			t.Fatal("secret staging directory retained")
		}
	}
}
func waitAccountState(t *testing.T, srv *Server, id string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w := profileAPIRequest(srv, "GET", "/api/profiles/"+id+"/account", "")
		var s struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 {
			t.Fatalf("job read=%d", w.Code)
		}
		if s.State != "pending" {
			return s.State
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
	return ""
}
