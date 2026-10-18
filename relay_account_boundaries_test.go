package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRelayAccountCommandFailuresAreBoundedAndRedacted(t *testing.T) {
	for _, mode := range []string{"exit", "cancel", "oversize", "unknown", "duplicate", "symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "fake-warpscout")
			var c RelayCredential
			_ = json.Unmarshal([]byte(credentialFixtureBody()), &c)
			raw, _ := json.Marshal(map[string]string{"private_key": c.PrivateKey, "peer_public_key": c.PeerPublicKey, "ipv4": c.TunnelIPv4})
			if mode == "unknown" {
				raw = append(raw[:len(raw)-1], []byte(`,"unsupported_hook":"synthetic"}`)...)
			}
			if mode == "duplicate" {
				raw = append(raw[:len(raw)-1], []byte(`,"private_key":"synthetic-duplicate"}`)...)
			}
			fixture := filepath.Join(root, "fixture")
			if err := os.WriteFile(fixture, raw, 0600); err != nil {
				t.Fatal(err)
			}
			action := "exit 1\n"
			switch mode {
			case "cancel":
				action = "exec sleep 30\n"
			case "unknown", "duplicate":
				action = "cp '" + fixture + "' \"$3\"\n"
			case "oversize":
				action = "dd if=/dev/zero of=\"$3\" bs=1024 count=65 2>/dev/null\n"
			case "symlink":
				action = "rm \"$3\"; ln -s '" + fixture + "' \"$3\"\n"
			case "hardlink":
				action = "rm \"$3\"; ln '" + fixture + "' \"$3\"\n"
			}
			script := "#!/bin/sh\nprintf 'synthetic-secret-stdout\\n'; printf 'synthetic-secret-stderr\\n' >&2\n" + action
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			started := time.Now()
			result, err := generateRelayAccount(ctx, binary, root)
			if err == nil || err.Error() != "Relay account generation failed" || result != (RelayCredential{}) {
				t.Fatal("unsafe process error/result")
			}
			if time.Since(started) > time.Second {
				t.Fatal("cancel did not reap registration process promptly")
			}
			entries, _ := os.ReadDir(root)
			for _, e := range entries {
				if e.IsDir() {
					t.Fatal("failed process leaked private staging")
				}
			}
			after, _ := os.ReadFile(fixture)
			if !bytes.Equal(after, raw) {
				t.Fatal("unsafe staged output modified unrelated fixture bytes")
			}
		})
	}
}

func TestRelayAccountRejectsCrossSiteUnauthorizedAndMalformedGeneration(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	var calls atomic.Int32
	srv.accounts.generate = func(context.Context) (RelayCredential, error) {
		calls.Add(1)
		return RelayCredential{}, fmt.Errorf("offline")
	}
	rev := srv.cfg.Snapshot().Revision
	valid := fmt.Sprintf(`{"confirmed":true,"revision":%d}`, rev)
	before, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
	for _, test := range []struct {
		body, media, origin, site string
		want                      int
	}{
		{valid, "text/plain", "", "", 400},
		{valid, "application/json", "https://synthetic.invalid", "cross-site", 403},
		{strings.Replace(valid, `"confirmed":true`, `"confirmed":true,"Confirmed":true`, 1), "application/json", "", "", 400},
		{valid + `{}`, "application/json", "", "", 400},
		{`{"confirmed":true,"revision":null}`, "application/json", "", "", 400},
		{fmt.Sprintf(`{"confirmed":true,"revision":%d}`, rev-1), "application/json", "", "", 409},
	} {
		r := httptest.NewRequest("POST", "/api/profiles/"+id+"/account/generate", strings.NewReader(test.body))
		r.Header.Set("Content-Type", test.media)
		if test.origin != "" {
			r.Header.Set("Origin", test.origin)
		}
		if test.site != "" {
			r.Header.Set("Sec-Fetch-Site", test.site)
		}
		w := httptest.NewRecorder()
		srv.handler().ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatalf("rejected generation status=%d want=%d", w.Code, test.want)
		}
	}
	mu := srv.cfg.mutex()
	mu.Lock()
	srv.cfg.WebSecret = "synthetic-admin-bearer"
	mu.Unlock()
	w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", valid)
	if w.Code != 401 {
		t.Fatal("unauthorized action accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("rejected requests caused registration")
	}
	after, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected requests changed store")
	}
}

func TestRelayAccountTwoProfilesOwnDistinctCredentials(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	if w := profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", credentialFixtureBody()); w.Code != 200 {
		t.Fatal(w.Code)
	}
	p := srv.cfg.Snapshot().Proxies[id]
	w := profileAPIRequest(srv, "POST", "/api/profiles", fmt.Sprintf(`{"name":"second","type":"relay-warp","relay_id":%q,"relay_country":"NL","port":4891}`, p.RelayID))
	second := profileResultID(t, w)
	var c RelayCredential
	_ = json.Unmarshal([]byte(credentialFixtureBody()), &c)
	c.PrivateKey = "ICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICA=" // synthetic 32-byte fixture
	c.TunnelIPv4 = "192.0.2.18"
	c.Mode = "generated"
	srv.accounts.generate = func(context.Context) (RelayCredential, error) { return c, nil }
	t.Cleanup(srv.accounts.Close)
	before := srv.cfg.Snapshot()
	shared := filepath.Join(filepath.Dir(before.ProfilePath), "warp-account.json")
	sentinel := []byte("synthetic opaque shared bytes\n")
	if err := os.WriteFile(shared, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	w = profileAPIRequest(srv, "POST", "/api/profiles/"+second+"/account/generate", fmt.Sprintf(`{"confirmed":true,"revision":%d}`, before.Revision))
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	if waitAccountState(t, srv, second) != "succeeded" {
		t.Fatal("second account generation failed")
	}
	g := srv.cfg.Snapshot()
	if g.Proxies[id].Credential != p.Credential || g.Proxies[second].Credential != c {
		t.Fatal("profiles share or overwrite credentials")
	}
	after, _ := os.ReadFile(shared)
	if !bytes.Equal(sentinel, after) {
		t.Fatal("generated profile replaced shared account")
	}
	loaded := statusTestSettings()
	if err := loaded.InitProfiles(g.ProfilePath); err != nil {
		t.Fatal(err)
	}
	if loaded.Proxies[id].Credential != p.Credential || loaded.Proxies[second].Credential != c {
		t.Fatal("profile ownership not persistent")
	}
	restarted := NewServer(loaded, NewOrchestrator(loaded))
	t.Cleanup(restarted.accounts.Close)
	status := profileAPIRequest(restarted, "GET", "/api/profiles/"+second+"/account", "")
	if !strings.Contains(status.Body.String(), `"state":"idle"`) {
		t.Fatal("restart replayed registration job")
	}
}

func TestRelayAccountSingleGlobalRegistrationReservation(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	p := srv.cfg.Snapshot().Proxies[id]
	second := profileResultID(t, profileAPIRequest(srv, "POST", "/api/profiles", fmt.Sprintf(`{"name":"second","type":"relay-warp","relay_id":%q,"relay_country":"DE","port":4891}`, p.RelayID)))
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	srv.accounts.generate = func(ctx context.Context) (RelayCredential, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return RelayCredential{}, fmt.Errorf("offline")
	}
	t.Cleanup(srv.accounts.Close)
	body := fmt.Sprintf(`{"confirmed":true,"revision":%d}`, srv.cfg.Snapshot().Revision)
	if w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body); w.Code != 202 {
		t.Fatal(w.Code)
	}
	if w := profileAPIRequest(srv, "POST", "/api/profiles/"+second+"/account/generate", body); w.Code != 409 {
		t.Fatal("multiple concurrent registration processes accepted")
	}
	once.Do(func() { close(release) })
}
