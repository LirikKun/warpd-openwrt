package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelayIntegrationReloadFallbackReauthorizesReplacement(t *testing.T) {
	g := integrationSettings()
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	old := (*bridges)[0]
	before := mihomo.Snapshot()
	mihomo.reload = func() error { return fmt.Errorf("synthetic controller failure") }
	g.Proxies["relay"].AWG.JC++
	g.Revision++
	if err := o.applyCurrent(); err != nil {
		t.Fatalf("reload fallback replacement was not prepared/authorized: %v", err)
	}
	after := mihomo.Snapshot()
	if after.PID == before.PID || after.Generation <= before.Generation || !mihomo.RelayReady(g.Snapshot(), "relay") {
		t.Fatal("replacement child not current")
	}
	select {
	case <-old.Done():
	default:
		t.Fatal("fallback left prior transport unjoined")
	}
}
func TestRelayIntegrationPrepareFailureBackoff(t *testing.T) {
	g := integrationSettings()
	o, _, _ := integrationEngine(t, &g)
	calls := 0
	mihomo.relayPrepare = func(context.Context, RelayUDPConfig) (*RelayUDPBridge, error) {
		calls++
		return nil, fmt.Errorf("synthetic offline setup failure")
	}
	for i := 0; i < 4; i++ {
		if err := o.applyCurrent(); err == nil {
			t.Fatal("failed prepare reported applied")
		}
	}
	if calls != 1 {
		t.Fatalf("supervision busy retries without backoff: constructor calls=%d", calls)
	}
	if mihomo.Running() {
		t.Fatal("failed Relay-only setup started engine")
	}
}
func TestRelayIntegrationFailedPersistedApplyRevokesAndExposesPending(t *testing.T) {
	g := integrationSettings()
	o, fixture, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	old := (*bridges)[0]
	old.mu.Lock()
	owner := old.owner
	old.mu.Unlock()
	before := mihomo.Snapshot()
	g.Proxies["relay"].Credential = integrationCredential(91)
	g.Revision++
	mihomo.configPath = filepath.Join(t.TempDir(), "absent", "config.yaml")
	err := o.applyCurrent()
	if err == nil {
		t.Fatal("failed config write reported applied")
	}
	select {
	case <-old.Done():
	default:
		t.Fatal("failed apply retained stale transport")
	}
	if owner.Current() {
		t.Fatal("failed apply retained stale token")
	}
	st := NewServer(&g, o).status()["proxies"].(map[string]ProxyState)["relay"]
	if st.Ready || st.TransportReady || !strings.Contains(st.Error, "pending:") {
		t.Fatalf("persisted but failed runtime not pending: ready=%v transport=%v error=%q", st.Ready, st.TransportReady, st.Error)
	}
	if after := mihomo.Snapshot(); after.Generation != before.Generation {
		t.Fatal("failed apply advanced generation")
	}
	fixture.SetReadDeadline(time.Now().Add(5700 * time.Millisecond))
	buf := make([]byte, 65536)
	if n, _, err := fixture.ReadFromUDP(buf); err == nil {
		t.Fatalf("stale credential transport after failed apply: %d", n)
	}
}
func TestRelayIntegrationMonitorOnlyNeedsNoEngineAccount(t *testing.T) {
	isolateStatusState(t)
	g := integrationSettings()
	g.Proxies = map[string]*ProxySettings{}
	o := NewOrchestrator(&g)
	o.accountLoader = func() (warpAccount, error) { t.Fatal("monitor-only loaded account"); return warpAccount{}, nil }
	mihomo.command = func(string) *exec.Cmd { t.Fatal("monitor-only launched engine"); return nil }
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	if mihomo.Running() {
		t.Fatal("monitor-only running engine")
	}
}
func TestRelayIntegrationRevokeBeforeAccountLoad(t *testing.T) {
	g := integrationSettings()
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	old := (*bridges)[0]
	g.Proxies["relay"].Credential = integrationCredential(121)
	g.Revision++
	warp := NewProxyProfile("warp", "warp")
	warp.Enabled = true
	warp.Endpoint = "192.0.2.1:2408"
	g.Proxies["warp"] = warp
	entered, release := make(chan struct{}), make(chan struct{})
	o.accountLoader = func() (warpAccount, error) {
		close(entered)
		<-release
		return warpAccount{}, fmt.Errorf("offline loader unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- o.applyCurrent() }()
	<-entered
	revoked := false
	select {
	case <-old.Done():
		revoked = true
	default:
	}
	close(release)
	<-done
	if !revoked {
		t.Fatal("changed intent retained old credential transport while account loader was blocked")
	}
}

func TestRelayIntegrationFreshnessIsImmediateNotWatcherSample(t *testing.T) {
	g := integrationSettings()
	o, _, bridges := integrationEngine(t, &g)
	prepare := mihomo.relayPrepare
	mihomo.relayPrepare = func(ctx context.Context, cfg RelayUDPConfig) (*RelayUDPBridge, error) {
		cfg.Freshness = time.Second
		return prepare(ctx, cfg)
	}
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	b := (*bridges)[0]
	b.mu.Lock()
	time.Sleep(time.Until(b.expires) + 5*time.Millisecond)
	ready := mihomo.RelayReady(g.Snapshot(), "relay")
	b.mu.Unlock()
	if ready {
		t.Fatal("expired immutable lifetime remained runtime-ready until watcher acquired bridge lock")
	}
}

func TestRelayIntegrationSerializedShutdownCannotResume(t *testing.T) {
	g := integrationSettings()
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	closer, ok := any(o).(interface{ Close() })
	if !ok {
		t.Fatal("orchestrator lacks serialized joined runtime shutdown")
	}
	closer.Close()
	select {
	case <-(*bridges)[0].Done():
	default:
		t.Fatal("shutdown returned before join")
	}
	if err := o.applyCurrent(); err == nil {
		t.Fatal("late supervision apply resumed shutdown runtime")
	}
	if err := o.restartCurrent(); err == nil {
		t.Fatal("late restart resumed shutdown runtime")
	}
	if mihomo.Running() || len(*bridges) != 1 {
		t.Fatal("shutdown recreated engine/transport")
	}
}

func TestRelayIntegrationChildLossRecovery(t *testing.T) {
	g := integrationSettings()
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	b := (*bridges)[0]
	b.mu.Lock()
	owner := b.owner
	b.mu.Unlock()
	before := mihomo.Snapshot()
	mihomo.mu.Lock()
	cmd, done := mihomo.cmd, mihomo.done
	mihomo.mu.Unlock()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	select {
	case <-b.Done():
	case <-time.After(time.Second):
		t.Fatal("child exit did not revoke/join bridge")
	}
	if owner.Current() {
		t.Fatal("exited child token still current")
	}
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	after := mihomo.Snapshot()
	if after.PID == before.PID || after.Generation <= before.Generation {
		t.Fatal("child loss did not recover exact generation")
	}
	if _, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/0", after.PID)); err != nil {
		t.Fatal(err)
	}
}
