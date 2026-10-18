package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

func TestRelayAccountCannotRegisterAgainstUninitializedStore(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	var calls atomic.Int32
	srv.accounts.generate = func(context.Context) (RelayCredential, error) {
		calls.Add(1)
		return RelayCredential{}, fmt.Errorf("must not run")
	}
	t.Cleanup(srv.accounts.Close)
	mu := srv.cfg.mutex()
	mu.Lock()
	srv.cfg.ProfileSchema = 0
	mu.Unlock()
	revision := srv.cfg.Snapshot().Revision
	w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", fmt.Sprintf(`{"confirmed":true,"revision":%d}`, revision))
	if w.Code != 409 {
		t.Fatalf("generation accepted without writable initialized store: HTTP=%d", w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("uninitialized store registered an account")
	}
}
