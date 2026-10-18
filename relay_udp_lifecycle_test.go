package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func relayUDPCfg() RelayUDPConfig {
	return RelayUDPConfig{ProfileID: "p_test", Revision: 1, Country: "DE", Access: RelayUDPPath{SourceIP: "127.0.0.2", Interface: "lo", PathConfirmed: true}, Freshness: time.Minute}
}
func relayUDPActive(t *testing.T, cfg RelayUDPConfig) (*RelayUDPBridge, *relayUDPTestChild, *net.UDPConn, *atomic.Bool) {
	t.Helper()
	fixture := relayUDPFixture(t)
	b, e := newRelayUDPFixture(context.Background(), cfg, fixture.LocalAddr().(*net.UDPAddr).AddrPort())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(b.Close)
	child := relayUDPSpawn(t, "")
	current := &atomic.Bool{}
	current.Store(true)
	e = b.Authorize(context.Background(), RelayUDPChild{PID: child.cmd.Process.Pid, Generation: 1, Exited: child.exited, Current: current.Load})
	if e != nil {
		t.Fatal(e)
	}
	return b, child, fixture, current
}
func relayUDPWaitClosed(t *testing.T, b *RelayUDPBridge) {
	t.Helper()
	select {
	case <-b.Done():
	case <-time.After(time.Second):
		t.Fatal("bridge not closed/joined within bound")
	}
}
func TestRelayUDPLifetime(t *testing.T) {
	for _, mode := range []string{"exit", "release", "cancel", "reconfigure", "expire"} {
		t.Run(mode, func(t *testing.T) {
			cfg := relayUDPCfg()
			b, child, fixture, current := relayUDPActive(t, cfg)
			child.command(t, "send "+b.Target().String()+" 616374697665")
			buf := make([]byte, 65536)
			fixture.SetReadDeadline(time.Now().Add(time.Second))
			if _, _, e := fixture.ReadFromUDP(buf); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "exit":
				child.cmd.Process.Kill()
				<-child.exited
			case "release":
				child.command(t, "release")
			case "cancel":
				b.cancel()
			case "reconfigure":
				current.Store(false)
			case "expire":
				b.mu.Lock()
				b.expires = time.Now().Add(100 * time.Millisecond)
				b.mu.Unlock()
			}
			relayUDPWaitClosed(t, b)
			fixture.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			if _, _, e := fixture.ReadFromUDP(buf); e == nil {
				t.Fatal("traffic after joined close")
			}
			// A local process can reuse a released port only after bridge shutdown;
			// that replacement cannot receive replies or send through the old bridge.
			if mode == "release" || mode == "exit" {
				rebound, e := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(child.peer))
				if e != nil {
					t.Fatal("socket lease leaked", e)
				}
				defer rebound.Close()
				rebound.WriteToUDPAddrPort([]byte("replacement"), b.Target())
				fixture.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				if _, _, e = fixture.ReadFromUDP(buf); e == nil {
					t.Fatal("replacement forwarded")
				}
			}
		})
	}
}
func TestRelayUDPOpaqueBudgetAndPinnedRemote(t *testing.T) {
	b, child, fixture, _ := relayUDPActive(t, relayUDPCfg())
	// The bridge is an opaque UDP transport, not a WG/AWG parser. Short arbitrary
	// nonempty data is valid. Empty/truncated/over-budget payloads are dropped.
	payload := bytes.Repeat([]byte{0xa5}, relayUDPMaxDatagram)
	if child.command(t, "send "+b.Target().String()+" "+hex.EncodeToString(payload)) != "true" {
		t.Fatal("max send")
	}
	buf := make([]byte, 65536)
	fixture.SetReadDeadline(time.Now().Add(time.Second))
	n, peer, e := fixture.ReadFromUDPAddrPort(buf)
	if e != nil || !bytes.Equal(buf[:n], payload) {
		t.Fatal("max payload truncated", n, e)
	}
	stranger := relayUDPFixture(t)
	stranger.WriteToUDPAddrPort([]byte("wrong-remote"), b.outbound.LocalAddr().(*net.UDPAddr).AddrPort())
	fixture.WriteToUDPAddrPort([]byte("known-remote"), peer)
	if got := child.command(t, "read"); got != hex.EncodeToString([]byte("known-remote")) {
		t.Fatal("unpinned remote reply", got)
	}
	// IPv4 kernel rejects oversized payload instead of silently truncating it.
	tooLarge := bytes.Repeat([]byte{0xa5}, relayUDPMaxDatagram+1)
	if child.command(t, "send "+b.Target().String()+" "+hex.EncodeToString(tooLarge)) != "false" {
		t.Fatal("oversized send unexpectedly accepted")
	}
}
func TestRelayUDPWildcardAmbiguity(t *testing.T) {
	child := relayUDPSpawn(t, "")
	child.command(t, "reuse")
	control := func(n, a string, c syscall.RawConn) error {
		var e error
		c.Control(func(fd uintptr) { e = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1) })
		return e
	}
	lc := net.ListenConfig{Control: control}
	other, e := lc.ListenPacket(context.Background(), "udp4", netip.AddrPortFrom(netip.IPv4Unspecified(), child.peer.Port()).String())
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	ingress := relayUDPFixture(t).LocalAddr().(*net.UDPAddr).AddrPort()
	if _, e = relayUDPInode(child.peer, ingress); e == nil {
		t.Fatal("wildcard ambiguity accepted")
	}
	if f, e := acquireRelayUDPSocket(child.cmd.Process.Pid, child.peer, ingress); e == nil {
		f.Close()
		t.Fatal("ambiguous inode leased")
	}
}
func TestRelayUDPInvalidConfigNoFallback(t *testing.T) {
	for _, mode := range []string{"country", "confirmation", "device", "source", "family", "ttl", "revision"} {
		t.Run(mode, func(t *testing.T) {
			cfg := relayUDPCfg()
			endpoint := relayUDPFixture(t).LocalAddr().(*net.UDPAddr).AddrPort()
			switch mode {
			case "country":
				cfg.Country = "RU"
			case "confirmation":
				cfg.Access.PathConfirmed = false
			case "device":
				cfg.Access.Interface = "missing0"
			case "source":
				cfg.Access.SourceIP = "192.0.2.88"
			case "family":
				cfg.Access.SourceIP = "::1"
			case "ttl":
				cfg.Freshness = 6 * time.Minute
			case "revision":
				cfg.Revision = 0
			}
			if b, e := newRelayUDPFixture(context.Background(), cfg, endpoint); e == nil {
				b.Close()
				t.Fatal("invalid config accepted")
			}
		})
	}
	// A nonlocal source fails binding, not merely fixture path validation.
	_, _, dial := relayBoundDialers(RelayUDPPath{SourceIP: "192.0.2.88", Interface: "lo", PathConfirmed: true})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if c, e := dial.DialContext(ctx, "udp4", "127.0.0.1:4500"); e == nil {
		c.Close()
		t.Fatal("missing source fell back")
	}
	family, _, _ := relayBoundDialers(RelayUDPPath{Interface: "lo", PathConfirmed: true})
	if family != "4" {
		t.Fatal("interface-only family changed")
	}
}
func TestRelayUDPIPv6Outbound(t *testing.T) {
	fixture, e := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if e != nil {
		t.Fatal(e)
	}
	defer fixture.Close()
	cfg := relayUDPCfg()
	cfg.Access.SourceIP = "::1"
	b, e := newRelayUDPFixture(context.Background(), cfg, fixture.LocalAddr().(*net.UDPAddr).AddrPort())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	child := relayUDPSpawn(t, "")
	if e = b.Authorize(context.Background(), RelayUDPChild{PID: child.cmd.Process.Pid, Generation: 1, Exited: child.exited, Current: func() bool { return true }}); e != nil {
		t.Fatal(e)
	}
	child.command(t, "send "+b.Target().String()+" 7636")
	fixture.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 64)
	n, peer, e := fixture.ReadFromUDPAddrPort(buf)
	if e != nil || string(buf[:n]) != "v6" || peer.Addr() != netip.IPv6Loopback() {
		t.Fatal(n, peer, e)
	}
}
func TestRelayUDPPathRemovalCloses(t *testing.T) {
	if os.Getenv("OFFLINE_NETNS") != "1" {
		t.Fatal("isolated netns required")
	}
	b, _, _, _ := relayUDPActive(t, relayUDPCfg())
	if out, e := exec.Command("ip", "link", "set", "lo", "down").CombinedOutput(); e != nil {
		t.Fatal(string(out), e)
	}
	defer exec.Command("ip", "link", "set", "lo", "up").Run()
	relayUDPWaitClosed(t, b)
}
func TestRelayUDPNoDescriptorOrGoroutineLeak(t *testing.T) {
	// Child process creation/reaping and timers have settled before measuring.
	baseFD, e := os.ReadDir("/proc/self/fd")
	if e != nil {
		t.Fatal(e)
	}
	baseGo := runtime.NumGoroutine()
	for i := 0; i < 12; i++ {
		cfg := relayUDPCfg()
		fixture := relayUDPFixture(t)
		b, e := newRelayUDPFixture(context.Background(), cfg, fixture.LocalAddr().(*net.UDPAddr).AddrPort())
		if e != nil {
			t.Fatal(e)
		}
		b.Close()
		fixture.Close()
	}
	after, e := os.ReadDir("/proc/self/fd")
	if e != nil {
		t.Fatal(e)
	}
	if len(after) != len(baseFD) || runtime.NumGoroutine() != baseGo {
		t.Fatalf("leak fd %d -> %d; goroutine %d -> %d", len(baseFD), len(after), baseGo, runtime.NumGoroutine())
	}
}

func TestRelayUDPBoundDNSTCPFallback(t *testing.T) {
	tcp, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer tcp.Close()
	udp, e := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(tcp.Addr().(*net.TCPAddr).AddrPort()))
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	errs := make(chan error, 2)
	var udpSeen, tcpSeen atomic.Bool
	go func() {
		buf := make([]byte, 512)
		n, peer, e := udp.ReadFromUDP(buf)
		if e != nil {
			errs <- e
			return
		}
		if peer.IP.String() != "127.0.0.2" {
			errs <- io.ErrUnexpectedEOF
			return
		}
		q := append([]byte(nil), buf[:n]...)
		q[2] = 0x83
		q[3] = 0x80
		_, e = udp.WriteToUDP(q, peer)
		udpSeen.Store(true)
		errs <- e
	}()
	go func() {
		c, e := tcp.Accept()
		if e != nil {
			errs <- e
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		if c.RemoteAddr().(*net.TCPAddr).IP.String() != "127.0.0.2" {
			errs <- io.ErrUnexpectedEOF
			return
		}
		h := make([]byte, 2)
		if _, e = io.ReadFull(c, h); e != nil {
			errs <- e
			return
		}
		q := make([]byte, binary.BigEndian.Uint16(h))
		if _, e = io.ReadFull(c, q); e != nil {
			errs <- e
			return
		} // Keep the question only: the resolver adds an EDNS OPT additional record,
		// which cannot precede the answer in a valid response.
		end := 12
		for end < len(q) && q[end] != 0 {
			end += int(q[end]) + 1
		}
		end += 5
		if end > len(q) {
			errs <- io.ErrUnexpectedEOF
			return
		}
		q = q[:end]
		q[2] = 0x81
		q[3] = 0x80
		q[7] = 1
		q[10] = 0
		q[11] = 0
		q = append(q, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, 192, 0, 2, 8)
		binary.BigEndian.PutUint16(h, uint16(len(q)))
		_, e = c.Write(append(h, q...))
		tcpSeen.Store(true)
		errs <- e
	}()
	_, _, d := relayBoundDialers(relayUDPCfg().Access)
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		c, e := d.Resolver.Dial(ctx, network, tcp.Addr().String())
		if e != nil {
			return nil, e
		}
		raw, e := c.(interface {
			SyscallConn() (syscall.RawConn, error)
		}).SyscallConn()
		if e != nil {
			c.Close()
			return nil, e
		}
		var device int
		raw.Control(func(fd uintptr) {
			device, e = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, 62 /* SO_BINDTOIFINDEX */)
		})
		if e != nil || device != 1 {
			c.Close()
			return nil, io.ErrUnexpectedEOF
		}
		return c, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, e := resolver.LookupNetIP(ctx, "ip4", "de.tribukvy.ltd")
	if e != nil || len(ips) != 1 || ips[0].Unmap() != netip.MustParseAddr("192.0.2.8") {
		t.Fatal("bound DNS fallback", ips, e)
	}
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	if !udpSeen.Load() || !tcpSeen.Load() {
		t.Fatal("UDP truncation / TCP fallback not exercised")
	}
}
