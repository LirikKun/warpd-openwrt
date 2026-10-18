package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestRelayAccountPersistedNullIsNotSilentlyLost(t *testing.T) {
	for _, field := range []string{"mode", "private_key", "mtu"} {
		srv, id := credentialAPIFixture(t)
		if w := profileAPIRequest(srv, "PUT", "/api/profiles/"+id+"/account", credentialFixtureBody()); w.Code != 200 {
			t.Fatal(w.Code)
		}
		path := srv.cfg.Snapshot().ProfilePath
		raw, _ := os.ReadFile(path)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		doc["profiles"].(map[string]any)[id].(map[string]any)["credential"].(map[string]any)[field] = nil
		changed, _ := json.Marshal(doc)
		if err := os.WriteFile(path, changed, 0600); err != nil {
			t.Fatal(err)
		}
		loaded := statusTestSettings()
		if loaded.InitProfiles(path) == nil {
			t.Fatal("null private metadata accepted and would be overwritten")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(changed, after) {
			t.Fatal("rejected file changed")
		}
	}
}
func TestRelayAccountCloseCancelsReapsAndPreventsRestart(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	start := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	srv.accounts.generate = func(ctx context.Context) (RelayCredential, error) {
		close(start)
		<-ctx.Done()
		close(cancelled)
		<-release
		return RelayCredential{}, fmt.Errorf("synthetic secret")
	}
	body := fmt.Sprintf(`{"confirmed":true,"revision":%d}`, srv.cfg.Snapshot().Revision)
	if w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body); w.Code != 202 {
		t.Fatal(w.Code)
	}
	<-start
	done := make(chan struct{})
	go func() { srv.accounts.Close(); close(done) }()
	<-cancelled
	select {
	case <-done:
		t.Fatal("Close returned without reaping worker")
	default:
	}
	once.Do(func() { close(release) })
	<-done
	if waitAccountState(t, srv, id) != "cancelled" {
		t.Fatal("Close did not cancel job")
	}
	if w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body); w.Code != 503 {
		t.Fatal("closed registration service restarted")
	}
}
