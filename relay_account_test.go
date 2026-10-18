package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func credentialFixtureBody() string {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32))
	peer := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{29}, 32))
	return `{"mode":"manual","private_key":"` + key + `","peer_public_key":"` + peer + `","tunnel_ipv4":"192.0.2.17","dns":"192.0.2.53","mtu":1280}`
}
func credentialAPIFixture(t *testing.T) (*Server, string) {
	t.Helper()
	srv := profileAPIFixture(t)
	r := profileAPIRequest(srv, "POST", "/api/relays", `{"name":"Synthetic","url":"https://relay.tribukvy.ltd/activate/0123456789abcdef","enabled":false}`)
	rid := profileResultID(t, r)
	r = profileAPIRequest(srv, "POST", "/api/profiles", `{"name":"Synthetic child","type":"relay-warp","relay_id":"`+rid+`","relay_country":"DE","enabled":false,"port":4890}`)
	return srv, profileResultID(t, r)
}
func TestRelayAccountManualPrivatePersistence(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	path := srv.cfg.Snapshot().ProfilePath
	shared := filepath.Join(filepath.Dir(path), "warp-account.json")
	sentinel := []byte("synthetic shared account sentinel: preserve every byte\n")
	if err := os.WriteFile(shared, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	before := srv.cfg.Snapshot().Revision
	w := profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", credentialFixtureBody())
	if w.Code != 200 {
		t.Fatalf("manual credential save unavailable: HTTP=%d", w.Code)
	}
	if srv.cfg.Snapshot().Revision != before+1 {
		t.Fatal("credential update did not invalidate revision")
	}
	data, _ := os.ReadFile(path)
	var body map[string]any
	_ = json.Unmarshal([]byte(credentialFixtureBody()), &body)
	if !bytes.Contains(data, []byte(body["private_key"].(string))) {
		t.Fatal("profile credential not persisted")
	}
	loaded := statusTestSettings()
	if err := loaded.InitProfiles(path); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(publicProfiles(loaded.Snapshot()))
	if bytes.Contains(encoded, []byte(body["private_key"].(string))) || bytes.Contains(encoded, []byte(body["peer_public_key"].(string))) || bytes.Contains(encoded, []byte("192.0.2.17")) {
		t.Fatal("public profile leaks credential")
	}
	if !bytes.Contains(encoded, []byte(`"credential_mode":"manual"`)) || !bytes.Contains(encoded, []byte(`"credential_configured":true`)) {
		t.Fatal("missing safe credential flags")
	}
	st, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if st.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
		t.Fatal("private modes not protected")
	}
	after, _ := os.ReadFile(shared)
	if !bytes.Equal(after, sentinel) {
		t.Fatal("shared account modified")
	}
}

func TestRelayAccountUnknownPersistedMetadataFailsClosed(t *testing.T) {
	for _, atCredential := range []bool{false, true} {
		srv, id := credentialAPIFixture(t)
		w := profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", credentialFixtureBody())
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		path := srv.cfg.Snapshot().ProfilePath
		raw, _ := os.ReadFile(path)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		target := doc
		if atCredential {
			target = doc["profiles"].(map[string]any)[id].(map[string]any)["credential"].(map[string]any)
		}
		target["unsupported_hook"] = "synthetic-secret-marker"
		raw, _ = json.Marshal(doc)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		cfg := statusTestSettings()
		err := cfg.InitProfiles(path)
		if err == nil {
			t.Fatal("unsupported persisted metadata was silently discarded")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(after, raw) {
			t.Fatal("invalid private bytes replaced")
		}
	}
}

func TestRelayAccountGenerationRequiresExplicitConfirmation(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	before, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
	for _, body := range []string{`{}`, `{"confirmed":false,"revision":1}`, `{"confirmed":true}`} {
		w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body)
		if w.Code != 400 {
			t.Fatalf("unconfirmed generation not strictly rejected: HTTP=%d", w.Code)
		}
	}
	after, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
	if !bytes.Equal(before, after) {
		t.Fatal("confirmation failure wrote store")
	}
}
