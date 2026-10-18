package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRelayUDPPinnedMihomoActualAWG(t *testing.T) {
	if os.Getenv("RELAY_UDP_MIHOMO") != "1" {
		t.Skip("explicit offline integration gate")
	}
	t.Chdir(os.Getenv("RELAY_UDP_EVIDENCE"))
	t.Cleanup(func() {
		if t.Failed() {
			data, _ := os.ReadFile("mihomo-runtime.log")
			name := fmt.Sprintf("failed-runtime-%d.log", time.Now().UnixNano())
			os.WriteFile(name, data, 0600)
			t.Logf("failure runtime evidence %s:\n%s", name, data)
		}
	})
	// Refuse to launch Mihomo on the host network, even if invoked incorrectly.
	if os.Getenv("OFFLINE_NETNS") != "1" {
		t.Fatal("run using run.sh isolated namespace")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("offline namespace must contain only lo")
	}
	routes, err := exec.Command("ip", "-j", "route", "show", "table", "main").Output()
	if err != nil || strings.TrimSpace(string(routes)) != "[]" {
		t.Fatalf("unexpected routes: %s error=%v", routes, err)
	}
	selfns, _ := os.Readlink("/proc/self/ns/net")
	if selfns == os.Getenv("HOST_NETNS") {
		t.Fatal("not isolated from host")
	}
	t.Logf("isolation=%s only lo; no main routes; host namespace differs", selfns)
	baseline := runtime.NumGoroutine()
	fixture, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, err := newRelayUDPFixture(ctx, RelayUDPConfig{ProfileID: "p_synthetic", Revision: 1, Country: "DE", Access: RelayUDPPath{SourceIP: "127.0.0.2", Interface: "lo", PathConfirmed: true}, Freshness: time.Minute}, fixture.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// All addresses and keys here are invented, not imported account material.
	privateBytes := bytes.Repeat([]byte{0x31}, 32)
	remotePrivate, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	private := base64.StdEncoding.EncodeToString(privateBytes)
	public := base64.StdEncoding.EncodeToString(remotePrivate.PublicKey().Bytes())
	free, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socksPort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	endpointPort := b.ingress.LocalAddr().(*net.UDPAddr).Port
	cfg := fmt.Sprintf(`port: 0
socks-port: 0
mixed-port: 0
allow-lan: false
bind-address: 127.0.0.1
mode: rule
log-level: debug
ipv6: false
external-controller: ''
find-process-mode: off
profile: {store-selected: false, store-fake-ip: false}
geodata-mode: false
geo-auto-update: false
tun: {enable: false}
sniffer: {enable: false}
dns: {enable: false}
proxies:
 - name: SYNTHETIC-AWG
   type: wireguard
   private-key: %s
   ip: 10.77.0.2
   server: 127.0.0.1
   port: %d
   public-key: %s
   allowed-ips: ['0.0.0.0/0']
   udp: true
   remote-dns-resolve: false
   mtu: 1280
   amnezia-wg-option:
     jc: 1
     jmin: 8
     jmax: 8
     s1: 0
     s2: 0
     h1: 1
     h2: 2
     h3: 3
     h4: 4
     i1: '<b 0x5350494b452d49312d4f4e4c59><r 4>'
listeners:
 - name: synthetic-socks
   type: socks
   listen: 127.0.0.1
   port: %d
   proxy: SYNTHETIC-AWG
rules: ['MATCH,SYNTHETIC-AWG']
`, private, endpointPort, public, socksPort)
	if err = os.MkdirAll("mihomo-home", 0700); err != nil {
		t.Fatal(err)
	}
	path, _ := filepath.Abs("synthetic.yaml")
	if err = os.WriteFile(path, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	binaryPath, _ := filepath.Abs("/home/lirikdev/.hermes/cache/scratch/warpd-relay-udp-spike/mihomo")
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer checkCancel()
	check := exec.CommandContext(checkCtx, binaryPath, "-d", "mihomo-home", "-f", path, "-t")
	output, err := check.CombinedOutput()
	os.WriteFile("mihomo-parse.log", output, 0600)
	if err != nil {
		t.Fatalf("parse: %v %s", err, output)
	}
	buf := make([]byte, 2048)
	fixture.SetReadDeadline(time.Now().Add(120 * time.Millisecond))
	if _, _, err = fixture.ReadFromUDP(buf); err == nil {
		t.Fatal("-t unexpectedly sent traffic")
	}
	t.Log("-t YAML check succeeded and fixture received zero datagrams; not runtime initialization proof")
	log, err := os.OpenFile("mihomo-runtime.log", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	runCtx, runCancel := context.WithTimeout(context.Background(), 16*time.Second)
	defer runCancel()
	cmd := exec.CommandContext(runCtx, binaryPath, "-d", "mihomo-home", "-f", path)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.WaitDelay = time.Second
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	var c net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// The custom listener starts before executor installs the inbound IP allowlist.
		// Do not interpret an early accept (which can be immediately closed) as ready.
		startup, _ := os.ReadFile("mihomo-runtime.log")
		if !bytes.Contains(startup, []byte("Start initial compatible provider default")) {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		c, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", socksPort), 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c == nil {
		t.Fatal("SOCKS did not start")
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	fixture.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err = fixture.ReadFromUDP(buf); err == nil {
		t.Fatal("unexpected datagrams before SOCKS dial")
	}
	c.Write([]byte{5, 1, 0})
	response := make([]byte, 2)
	if _, err = io.ReadFull(c, response); err != nil || !bytes.Equal(response, []byte{5, 0}) {
		t.Fatalf("SOCKS negotiation %v %v", response, err)
	}
	// This is an in-tunnel documentation-only IP, not an outer destination.
	if _, err = c.Write([]byte{5, 1, 0, 1, 192, 0, 2, 44, 1, 187}); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	// The exact cmd/PID generation is attached only after Start; no PID=0 trust.
	if err = b.Authorize(runCtx, RelayUDPChild{PID: cmd.Process.Pid, Generation: 1, Exited: exited, Current: func() bool { return cmd.ProcessState == nil }}); err != nil {
		for _, name := range []string{"udp", "udp6"} {
			data, _ := os.ReadFile("/proc/net/" + name)
			t.Logf("%s: %s", name, data)
		}
		entries, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", cmd.Process.Pid))
		for _, entry := range entries {
			link, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", cmd.Process.Pid, entry.Name()))
			if strings.HasPrefix(link, "socket:") {
				t.Logf("child fd=%s %s", entry.Name(), link)
			}
		}
		t.Fatal("exact pinned Mihomo ownership:", err)
	}
	t.Log("authorized exact Mihomo socket from /proc; pidfd lease retained; queued pre-lease packets discarded")
	observedI1, observedHandshake := false, false
	var events []string
	fixture.SetReadDeadline(time.Now().Add(9 * time.Second))
	for !observedI1 || !observedHandshake {
		n, peer, err := fixture.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("actual AWG packet missing i1=%v handshake=%v: %v", observedI1, observedHandshake, err)
		}
		if peer.IP.String() != "127.0.0.2" || b.cfg.Access.Interface != "lo" {
			t.Fatalf("wrong path source=%s device=%s", peer, b.cfg.Access.Interface)
		}
		kind := "junk"
		if n == len("SPIKE-I1-ONLY")+4 && bytes.HasPrefix(buf[:n], []byte("SPIKE-I1-ONLY")) {
			observedI1 = true
			kind = "synthetic-I1"
		}
		if n == 148 && binary.LittleEndian.Uint32(buf[:4]) == 1 {
			observedHandshake = true
			kind = "WG-handshake-initiation"
		}
		events = append(events, fmt.Sprintf("kind=%s bytes=%d fixture_source=%s socket_device=%s", kind, n, peer, b.cfg.Access.Interface))
	}
	os.WriteFile("fixture-observations.txt", []byte(strings.Join(events, "\n")+"\n"), 0600)
	for _, event := range events {
		t.Log(event)
	}
	start := time.Now()
	cancel()
	b.Close()
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("slow bridge cleanup %s", elapsed)
	}
	c.Close()
	// Keep Mihomo alive beyond one WG handshake retry interval; the bridge is stopped.
	fixture.SetReadDeadline(time.Now().Add(5700 * time.Millisecond))
	if n, _, err := fixture.ReadFromUDP(buf); err == nil {
		t.Fatalf("fixture traffic after cancellation: %d", n)
	}
	stopStart := time.Now()
	cmd.Process.Signal(syscall.SIGTERM)
	waitErr := cmd.Wait()
	waited = true
	close(exited)
	if time.Since(stopStart) > 2*time.Second {
		t.Fatal("Mihomo cleanup exceeded two seconds")
	}
	if !cmd.ProcessState.Exited() {
		t.Fatal("Mihomo did not exit")
	}
	time.Sleep(80 * time.Millisecond)
	if count := runtime.NumGoroutine(); count > baseline {
		t.Fatalf("goroutines before=%d after=%d", baseline, count)
	}
	t.Logf("bridge cancel-to-join=%s; no fixture traffic for 5.7s with Mihomo alive; Mihomo reaped=%v wait=%v stop=%s; goroutines before=%d after=%d", elapsed, cmd.ProcessState.Exited(), waitErr, time.Since(stopStart), baseline, runtime.NumGoroutine())
}
