package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func integrationEngine(t *testing.T, g *Settings) (*Orchestrator, *net.UDPConn, *[]*RelayUDPBridge) {
	t.Helper()
	if os.Getenv("OFFLINE_NETNS") != "1" {
		t.Fatal("requires fresh isolated namespace")
	}
	isolateStatusState(t)
	fixture, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { fixture.Close() })
	dir := t.TempDir()
	log, e := os.Create(filepath.Join(os.Getenv("RELAY_UDP_EVIDENCE"), fmt.Sprintf("%s-%d-engine.log", strings.ReplaceAll(t.Name(), "/", "-"), time.Now().UnixNano())))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { log.Close() })
	m := &MihomoMan{configPath: filepath.Join(dir, "config.yaml"), command: func(path string) *exec.Cmd {
		cmd := exec.Command("/home/lirikdev/.hermes/cache/scratch/warpd-relay-udp-spike/mihomo", "-d", dir, "-f", path)
		cmd.Stdout = log
		cmd.Stderr = log
		return cmd
	}}
	m.reload = func() error {
		req, err := http.NewRequest("PUT", "http://"+mihomoAPI+"/configs?force=true", bytes.NewBufferString(fmt.Sprintf(`{"path":%q}`, m.configPath)))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 204 {
			return fmt.Errorf("actual reload status=%d", resp.StatusCode)
		}
		return nil
	}
	var bridges []*RelayUDPBridge
	m.relayPrepare = func(ctx context.Context, cfg RelayUDPConfig) (*RelayUDPBridge, error) {
		cfg.Access.SourceIP = "127.0.0.2"
		b, err := newRelayUDPFixture(ctx, cfg, fixture.LocalAddr().(*net.UDPAddr).AddrPort())
		if err == nil {
			bridges = append(bridges, b)
		}
		return b, err
	}
	mihomo = m
	t.Cleanup(m.Stop)
	o := NewOrchestrator(g)
	t.Cleanup(o.closeProfileGates)
	o.accountLoader = func() (warpAccount, error) {
		t.Error("Relay-only startup loaded shared standalone account")
		return warpAccount{}, nil
	}
	return o, fixture, &bridges
}

func TestRelayIntegrationManagedLazyAuthorizeRevoke(t *testing.T) {
	g := integrationSettings()
	o, fixture, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatalf("single child prepare/render/apply/lazy bootstrap/authorize: %v", err)
	}
	if len(*bridges) != 1 {
		t.Fatalf("bridge preparation count=%d", len(*bridges))
	}
	b := (*bridges)[0]
	process, ok := mihomo.AppliedSnapshot(g.Snapshot(), stateGetEndpoints())
	if !ok || !process.Running || process.Generation == 0 {
		t.Fatal("exact child not successfully applied")
	}
	if o.profileGateOpen("relay") {
		t.Fatal("bootstrap opened public gate before health/policy")
	}
	b.mu.Lock()
	owner := b.owner
	lease := b.lease
	b.mu.Unlock()
	if owner == nil || lease == nil || owner.PID != process.PID {
		t.Fatal("no real exact-child pidfd lease")
	}
	buf := make([]byte, 4096)
	fixture.SetReadDeadline(time.Now().Add(8 * time.Second))
	i1, handshake := false, false
	for !i1 || !handshake {
		n, peer, err := fixture.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("actual engine initialized i1=%v handshake=%v: %v", i1, handshake, err)
		}
		if peer.IP.String() != "127.0.0.2" {
			t.Fatalf("wrong source %s", peer)
		}
		i1 = i1 || bytes.HasPrefix(buf[:n], []byte("INTEGRATION"))
		handshake = handshake || n == 148 && binary.LittleEndian.Uint32(buf[:4]) == 1
	}
	t.Logf("real child PID=%d generation=%d proc/fd/pidfd authorized; synthetic I1+handshake initialization only", process.PID, process.Generation)
	// Parent disable must preserve child's desired flag, revoke and join before returning.
	g.Relays["r"].Enabled = false
	g.Revision++
	done := make(chan error, 1)
	go func() { done <- o.applyCurrent() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disable deadlocked while joining bridge under apply/manager lock")
	}
	select {
	case <-b.Done():
	default:
		t.Fatal("disable acknowledged before bridge joined")
	}
	if owner.Current() {
		t.Fatal("stale immutable token still current")
	}
	if !g.Proxies["relay"].Enabled {
		t.Fatal("parent disable overwrote desired child choice")
	}
	fixture.SetReadDeadline(time.Now().Add(5700 * time.Millisecond))
	if n, _, err := fixture.ReadFromUDP(buf); err == nil {
		t.Fatalf("traffic after successful disable acknowledgment: %d", n)
	}
	if mihomo.Running() {
		t.Fatal("Relay-only engine survived last disabled child")
	}
}

func TestRelayIntegrationActualReloadIsNotTransportRevocation(t *testing.T) {
	g := integrationSettings()
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	b := (*bridges)[0]
	before := mihomo.Snapshot()
	empty := g.Snapshot()
	empty.Proxies = map[string]*ProxySettings{}
	if err := os.WriteFile(mihomo.configPath, []byte(mihomoConfText(empty, endpoints{}, warpAccount{})), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mihomo.reload(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
		t.Log("actual engine released old adapter; mandatory proc/fd watcher joined")
	case <-time.After(150 * time.Millisecond):
		t.Log("actual engine retained old UDP adapter after reload; reload alone is not revocation proof")
	}
	mihomo.RevokeRelays()
	select {
	case <-b.Done():
	default:
		t.Fatal("explicit lifetime revocation did not join retained adapter bridge")
	}
	after := mihomo.Snapshot()
	if after.PID != before.PID || !after.Running {
		t.Fatal("test did not retain same live child")
	}
	if mihomo.RelayReady(g.Snapshot(), "relay") {
		t.Fatal("revoked transport still runtime-ready")
	}
}

func TestRelayIntegrationTwoRelaysOneActualChild(t *testing.T) {
	g := integrationSettings()
	second := *g.Proxies["relay"]
	second.Key = "second"
	second.Port = 4812
	second.Credential = integrationCredential(51)
	second.RelayCountry = "NL"
	g.Proxies["second"] = &second
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatalf("two profile-owned transports in one exact child: %v", err)
	}
	if len(*bridges) != 2 {
		t.Fatalf("prepared bridges=%d", len(*bridges))
	}
	process := mihomo.Snapshot()
	for _, b := range *bridges {
		b.mu.Lock()
		owner := b.owner
		b.mu.Unlock()
		if owner == nil || owner.PID != process.PID || !owner.Current() {
			t.Fatal("Relay profile not leased to same current child")
		}
	}
}

func TestRelayIntegrationHotReloadAndRestartGenerations(t *testing.T) {
	g := integrationSettings()
	o, _, bridges := integrationEngine(t, &g)
	if err := o.applyCurrent(); err != nil {
		t.Fatal(err)
	}
	old := (*bridges)[0]
	old.mu.Lock()
	owner := old.owner
	old.mu.Unlock()
	before := mihomo.Snapshot()
	g.Proxies["relay"].RelayCountry = "NL"
	g.Revision++
	if err := o.applyCurrent(); err != nil {
		t.Fatalf("actual same-child hot reload: %v", err)
	}
	after := mihomo.Snapshot()
	if after.PID != before.PID || after.Generation <= before.Generation {
		t.Fatalf("not real hot reload: before=%+v after=%+v", before, after)
	}
	if owner.Current() {
		t.Fatal("old generation token resumed after reload")
	}
	select {
	case <-old.Done():
	default:
		t.Fatal("reload acknowledged before old transport joined")
	}
	if err := o.restartCurrent(); err != nil {
		t.Fatalf("fresh exact child after restart: %v", err)
	}
	restarted := mihomo.Snapshot()
	if restarted.PID == after.PID || restarted.Generation <= after.Generation {
		t.Fatalf("restart identity unchanged: %+v", restarted)
	}
}
