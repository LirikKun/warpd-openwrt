package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Review-only helper: a synthetic fixture, never a provider executable.
func TestReviewFixtureProcess(t *testing.T) {
	if os.Getenv("RELAY_REVIEW_CHILD") != "1" {
		return
	}
	var c RelayCredential
	if json.Unmarshal([]byte(credentialFixtureBody()), &c) != nil {
		os.Exit(20)
	}
	raw, _ := json.Marshal(map[string]string{"private_key": c.PrivateKey, "peer_public_key": c.PeerPublicKey, "ipv4": c.TunnelIPv4})
	if os.WriteFile("account.json", raw, 0600) != nil {
		os.Exit(21)
	}
	control := os.Getenv("RELAY_REVIEW_CONTROL")
	if os.WriteFile(filepath.Join(control, "ready"), []byte("synthetic fixture ready"), 0600) != nil {
		os.Exit(22)
	}
	f, e := os.Open(filepath.Join(control, "release"))
	if e != nil {
		os.Exit(23)
	}
	b := make([]byte, 1)
	_, e = f.Read(b)
	f.Close()
	if e != nil {
		os.Exit(24)
	}
	os.Exit(0)
}

func reviewReplaceDuringGeneration(t *testing.T, symlink bool) (RelayCredential, error, string) {
	t.Helper()
	control := t.TempDir()
	root := filepath.Join(control, "root")
	moved := filepath.Join(control, "renamed-root")
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e := syscall.Mkfifo(filepath.Join(control, "release"), 0600); e != nil {
		t.Fatal(e)
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	script := filepath.Join(control, "fixture-command")
	if e := os.WriteFile(script, []byte("#!/bin/sh\nexec '"+exe+"' -test.run '^TestReviewFixtureProcess$' -- \"$@\"\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("RELAY_REVIEW_CHILD", "1")
	t.Setenv("RELAY_REVIEW_CONTROL", control)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type answer struct {
		c   RelayCredential
		err error
	}
	result := make(chan answer, 1)
	go func() { c, e := generateRelayAccount(ctx, script, root); result <- answer{c, e} }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(control, "ready")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synthetic child did not reach synchronization point")
		}
		time.Sleep(time.Millisecond)
	}
	// All replacements and fixtures are confined to this test's owned directory.
	if e := os.Rename(root, moved); e != nil {
		t.Fatal(e)
	}
	if symlink {
		e = os.Symlink(moved, root)
	} else {
		e = os.Mkdir(root, 0700)
	}
	if e != nil {
		t.Fatal(e)
	}
	release, e := os.OpenFile(filepath.Join(control, "release"), os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = release.Write([]byte{1})
	release.Close()
	if e != nil {
		t.Fatal(e)
	}
	out := <-result
	return out.c, out.err, moved
}

func TestReviewGenerationSurvivesReplacedAncestor(t *testing.T) {
	c, e, moved := reviewReplaceDuringGeneration(t, true)
	if e != nil || c.Mode != "generated" {
		t.Fatalf("descriptor-anchored generation failed after ancestor replacement: err=%v mode=%q", e, c.Mode)
	}
	assertNoRelayStageDirs(t, moved)
}
func TestReviewGenerationCleansRenamedAncestor(t *testing.T) {
	c, e, moved := reviewReplaceDuringGeneration(t, false)
	if e != nil || c.Mode != "generated" {
		t.Fatalf("descriptor-anchored generation failed after ancestor rename: err=%v mode=%q", e, c.Mode)
	}
	assertNoRelayStageDirs(t, moved)
}
func assertNoRelayStageDirs(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), relayStagePrefix) {
			t.Fatalf("staging directory retained after cleanup: %s", entry.Name())
		}
	}
}
func TestReviewGenerationAllowsTrustedStickyRoot(t *testing.T) {
	h, err := openRelayStageRoot(os.TempDir())
	if err != nil {
		t.Fatalf("trusted sticky staging root rejected: %v", err)
	}
	h.dir.Close()
}

func TestReviewGenerationRejectsWritableNonstickyRoot(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0777); e != nil {
		t.Fatal(e)
	}
	if rootHandle, err := openRelayStageRoot(root); err == nil {
		rootHandle.dir.Close()
		t.Fatal("staging trust check accepts a world-writable nonsticky directory")
	}
}

func TestReviewCancelReservationUntilLateWorkerCleanup(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	before, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
	var c RelayCredential
	_ = json.Unmarshal([]byte(credentialFixtureBody()), &c)
	c.Mode = "generated"
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls, live atomic.Int32
	var cleaned atomic.Bool
	srv.accounts.generate = func(ctx context.Context) (RelayCredential, error) {
		n := calls.Add(1)
		if live.Add(1) != 1 {
			t.Error("overlapping workers")
		}
		defer live.Add(-1)
		if n == 1 {
			close(started)
			<-release
			cleaned.Store(true)
			return c, nil
		}
		if !cleaned.Load() {
			t.Error("new worker started before old cleanup")
		}
		return RelayCredential{}, fmt.Errorf("synthetic failure")
	}
	t.Cleanup(srv.accounts.Close)
	body := fmt.Sprintf(`{"confirmed":true,"revision":%d}`, srv.cfg.Snapshot().Revision)
	if w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body); w.Code != 202 {
		t.Fatal(w.Code)
	}
	<-started
	if w := profileAPIRequest(srv, "DELETE", "/api/profiles/"+id+"/account/generate", `{}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body); w.Code != 409 {
		t.Fatalf("replacement while cancelled worker remains alive: %d", w.Code)
	}
	if calls.Load() != 1 {
		t.Fatal("cancellation released reservation too early")
	}
	once.Do(func() { close(release) })
	srv.accounts.work.Wait()
	after, _ := os.ReadFile(srv.cfg.Snapshot().ProfilePath)
	if !bytes.Equal(before, after) {
		t.Fatal("late successful worker committed after cancellation")
	}
	if waitAccountState(t, srv, id) != "cancelled" {
		t.Fatal("late worker replaced cancellation status")
	}
	if w := profileAPIRequest(srv, "POST", "/api/profiles/"+id+"/account/generate", body); w.Code != 202 {
		t.Fatalf("reservation not released after reaping: %d", w.Code)
	}
	srv.accounts.work.Wait()
	if calls.Load() != 2 || !cleaned.Load() {
		t.Fatal("second worker did not execute after cleanup")
	}
	t.Log("cancelled worker reservation held; late success discarded; next worker entered only after cleanup")
}
