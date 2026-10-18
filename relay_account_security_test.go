package main

import (
	"bytes"
	"context"
	"encoding/base64"
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

func TestRelayAccountConcurrentCoalescingAndStaleDiscard(t *testing.T) {
	for _, action := range []string{"edit", "delete", "manual", "cancel", "close", "error"} {
		t.Run(action, func(t *testing.T) {
			srv, id := credentialAPIFixture(t)
			started := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32
			var c RelayCredential
			_ = json.Unmarshal([]byte(credentialFixtureBody()), &c)
			c.Mode = "generated"
			srv.accounts.generate = func(ctx context.Context) (RelayCredential, error) {
				if calls.Add(1) == 1 {
					close(started)
				}
				<-release
				if action == "error" {
					return c, fmt.Errorf("synthetic-private-error %s", c.PrivateKey)
				}
				return c, nil
			}
			t.Cleanup(srv.accounts.Close)
			rev := srv.cfg.Snapshot().Revision
			body := fmt.Sprintf(`{"confirmed":true,"revision":%d}`, rev)
			w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body)
			if w.Code != 202 {
				t.Fatal(w.Code)
			}
			<-started
			var group sync.WaitGroup
			responses := make(chan *httptest.ResponseRecorder, 12)
			for i := 0; i < 12; i++ {
				group.Add(1)
				go func() {
					defer group.Done()
					responses <- profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body)
				}()
			}
			group.Wait()
			close(responses)
			for w := range responses {
				if w.Code != 202 || !strings.Contains(w.Body.String(), `"queued":false`) {
					t.Fatal("duplicate request not coalesced")
				}
			}
			if calls.Load() != 1 {
				t.Fatal("concurrent duplicate registration")
			}
			pending := profileAPIRequest(srv, "GET", "/api/profiles/"+id+"/account", "")
			if !strings.Contains(pending.Body.String(), `"pending":true`) {
				t.Fatal("no pending observation")
			}
			old, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
			want := "stale"
			switch action {
			case "edit":
				w = profileAPIRequest(srv, "PATCH", "/api/profiles/"+id, `{"name":"edited while registering"}`)
			case "delete":
				w = profileAPIRequest(srv, "DELETE", "/api/profiles/"+id, `{}`)
			case "manual":
				w = profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", credentialFixtureBody())
				want = "cancelled"
			case "cancel":
				w = profileAPIRequest(srv, "DELETE", "/api/profiles/"+id+"/account/generate", `{}`)
				want = "cancelled"
			case "close":
				srv.accounts.mu.Lock()
				srv.accounts.closed = true
				srv.accounts.active.cancel()
				srv.accounts.mu.Unlock()
				want = "cancelled"
			case "error":
				want = "error"
			}
			if w.Code >= 400 {
				t.Fatalf("conflicting action failed: HTTP=%d", w.Code)
			}
			committed, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
			close(release)
			srv.accounts.work.Wait()
			if action != "delete" {
				state := waitAccountState(t, srv, id)
				if state != want {
					t.Fatalf("state=%s want=%s", state, want)
				}
			}
			after, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
			if !bytes.Equal(committed, after) {
				t.Fatal("cancelled/stale/error job committed credentials")
			}
			if (action == "cancel" || action == "close" || action == "error") && !bytes.Equal(old, after) {
				t.Fatal("failed job changed store bytes")
			}
			for _, path := range []string{"/api/status", "/api/profiles", "/api/profiles/" + id + "/account", "/api/log"} {
				r := profileAPIRequest(srv, "GET", path, "")
				for _, secret := range []string{c.PrivateKey, c.PeerPublicKey, c.TunnelIPv4, "synthetic-private-error"} {
					if strings.Contains(r.Body.String(), secret) {
						t.Fatal("public response exposed credential or child error")
					}
				}
			}
		})
	}
}

func TestRelayAccountStrictRequestsPreserveBytesAndIdentity(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	valid := credentialFixtureBody()
	path := srv.cfg.Snapshot().ProfilePath
	before, _ := os.ReadFile(path)
	rev := srv.cfg.Snapshot().Revision
	var c RelayCredential
	_ = json.Unmarshal([]byte(valid), &c)
	lowOrder := base64.StdEncoding.EncodeToString(append([]byte{1}, make([]byte, 31)...))
	invalid := []string{
		`{}`, `null`, valid + `{}`, strings.Replace(valid, `"mode":"manual"`, `"mode":"generated"`, 1),
		strings.Replace(valid, `"mode":"manual"`, `"mode":"manual","Mode":"manual"`, 1),
		strings.Replace(valid, `"mode":"manual"`, `"mode":"manual","mode":"manual"`, 1),
		strings.Replace(valid, `"mode":"manual"`, `"mode":"manual","post_up":"synthetic"`, 1),
		strings.Replace(valid, `"mtu":1280`, `"mtu":null`, 1),
		strings.Replace(valid, `"mtu":1280`, `"mtu":1279`, 1),
		strings.Replace(valid, c.PrivateKey, "invalid-key-marker", 1),
		strings.Replace(valid, c.PeerPublicKey, lowOrder, 1),
		strings.Replace(valid, c.TunnelIPv4, "0.0.0.0", 1),
		strings.Replace(valid, c.TunnelIPv4, "192.0.2.0/24", 1),
		strings.Replace(valid, `"dns":"192.0.2.53"`, `"dns":"https://synthetic.invalid"`, 1),
		strings.Replace(valid, `"mtu":1280`, `"mtu":1280,"reserved":"AA=="`, 1),
		strings.Repeat(" ", 17<<10) + valid,
	}
	for i, body := range invalid {
		w := profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", body)
		if w.Code != 400 {
			t.Fatalf("invalid request %d accepted/status=%d", i, w.Code)
		}
		if strings.Contains(w.Body.String(), "invalid-key-marker") {
			t.Fatal("invalid credential echoed")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) || srv.cfg.Snapshot().Revision != rev {
			t.Fatal("invalid request mutated store or revision")
		}
	}
	w := profileAPIRequest(srv, "PUT", "/api/profiles/warp/account", valid)
	if w.Code != 409 {
		t.Fatal("nonRelay credential write accepted")
	}
	w = profileAPIRequest(srv, "PUT", "/api/profiles/missing/account", valid)
	if w.Code != 404 {
		t.Fatal("missing profile accepted")
	}
}

func TestRelayAccountSnapshotIsolationFailureAndLegacyUnavailable(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	p := srv.cfg.Snapshot().Proxies[id]
	if relayCredentialConfigured(p) {
		t.Fatal("legacy unconfigured profile synthesized credential")
	}
	// A broken persistence boundary cannot publish an unpersisted identity.
	path := srv.cfg.Snapshot().ProfilePath
	original, _ := os.ReadFile(path)
	before := srv.cfg.Snapshot()
	backup := path + ".backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, path); err != nil {
		t.Fatal(err)
	}
	w := profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", credentialFixtureBody())
	if w.Code != 500 || srv.cfg.Snapshot().Revision != before.Revision || srv.cfg.Snapshot().Proxies[id].Credential != (RelayCredential{}) {
		t.Fatal("failed save changed in-memory identity")
	}
	after, _ := os.ReadFile(backup)
	if !bytes.Equal(original, after) {
		t.Fatal("failed save altered original bytes")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	w = profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", credentialFixtureBody())
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	snapshot := srv.cfg.Snapshot()
	snapshot.Proxies[id].Credential.PrivateKey = "snapshot-mutated"
	if srv.cfg.Snapshot().Proxies[id].Credential.PrivateKey == "snapshot-mutated" {
		t.Fatal("snapshot aliases live credentials")
	}
	// Loading the store and toggling desired flags do not invoke registration.
	var calls atomic.Int32
	srv.accounts.generate = func(context.Context) (RelayCredential, error) {
		calls.Add(1)
		return RelayCredential{}, fmt.Errorf("must not run")
	}
	loaded := statusTestSettings()
	if err := loaded.InitProfiles(path); err != nil {
		t.Fatal(err)
	}
	_ = profileAPIRequest(srv, "PATCH", "/api/profiles/"+id, `{"name":"no implicit registration"}`)
	time.Sleep(time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("save/reload implicitly registered")
	}
	// Credentials alone cannot authorize a path or imply registration/readiness.
	g := srv.cfg.Snapshot()
	g.Proxies[id].Enabled = true
	g.Relays[g.Proxies[id].RelayID].Enabled = true
	if err := srv.cfg.ChangeProfiles(func(next *Settings) error {
		next.Proxies[id].Enabled = true
		next.Relays[next.Proxies[id].RelayID].Enabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := srv.orch.applyCurrent()
	if err == nil || !strings.Contains(err.Error(), "pending:") || srv.orch.profileGateOpen(id) || mihomo.RelayReady(srv.cfg.Snapshot(), id) {
		t.Fatal("credentials without a usable bound path escaped the closed runtime gate")
	}
	if calls.Load() != 0 {
		t.Fatal("enable implicitly registered")
	}
	shared := filepath.Join(filepath.Dir(path), "warp-account.json")
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Fatal("save created shared account")
	}
}
