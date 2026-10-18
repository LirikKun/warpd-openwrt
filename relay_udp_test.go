package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// This private fixture seam allows only literal loopback endpoints and loopback
// sources. There is no arbitrary endpoint or validation/ownership bypass option
// in the exported caller API.
func newRelayUDPFixture(ctx context.Context, cfg RelayUDPConfig, endpoint netip.AddrPort) (*RelayUDPBridge, error) {
	if !endpoint.IsValid() || !endpoint.Addr().IsLoopback() || endpoint.Port() == 0 {
		return nil, errors.New("invalid local UDP fixture")
	}
	return newRelayUDPBridge(ctx, cfg, endpoint)
}

func relayUDPRequireIsolation(t *testing.T) {
	t.Helper()
	ours, err := os.Readlink("/proc/self/ns/net")
	host := os.Getenv("HOST_NETNS")
	if err != nil || os.Getenv("OFFLINE_NETNS") != "1" || host == "" || ours == host {
		t.Fatal("fresh offline namespace required; use candidate run.py")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("offline namespace must contain only lo")
	}
}

// Actual subprocess sockets; acceptance never substitutes ownership callbacks
// for production /proc/fd inspection.
func TestRelayUDPChild(t *testing.T) {
	if os.Getenv("RELAY_UDP_CHILD") != "1" {
		return
	}
	relayUDPRequireIsolation(t)
	addr := os.Getenv("RELAY_UDP_CHILD_ADDR")
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	c, e := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.MustParseAddrPort(addr)))
	if e != nil {
		panic(e)
	}
	fmt.Println(c.LocalAddr())
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 4096), 200000)
	for scan.Scan() {
		f := strings.Fields(scan.Text())
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "manyfd":
			count, _ := strconv.Atoi(f[1])
			for i := 0; i < count; i++ {
				file, e := os.Open("/dev/null")
				if e != nil {
					panic(e)
				}
				defer file.Close()
			}
			fmt.Println("opened")
		case "extra":
			other, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if e != nil {
				panic(e)
			}
			defer other.Close()
			fmt.Println(other.LocalAddr())
		case "connect":
			target := netip.MustParseAddrPort(f[1])
			raw, _ := c.SyscallConn()
			var err error
			raw.Control(func(fd uintptr) {
				err = syscall.Connect(int(fd), &syscall.SockaddrInet4{Port: int(target.Port()), Addr: target.Addr().As4()})
			})
			fmt.Println(err == nil)
		case "reuse":
			raw, _ := c.SyscallConn()
			raw.Control(func(fd uintptr) { syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1) })
			fmt.Println("reuse")
		case "send":
			b, _ := hex.DecodeString(f[2])
			_, e = c.WriteToUDPAddrPort(b, netip.MustParseAddrPort(f[1]))
			fmt.Println(e == nil)
		case "read":
			c.SetReadDeadline(time.Now().Add(time.Second))
			b := make([]byte, 65536)
			n, _, e := c.ReadFromUDP(b)
			if e != nil {
				fmt.Println("error")
			} else {
				fmt.Println(hex.EncodeToString(b[:n]))
			}
		case "release":
			c.Close()
			fmt.Println("released")
		case "exit":
			os.Exit(0)
		}
	}
	os.Exit(0)
}

type relayUDPTestChild struct {
	cmd    *exec.Cmd
	in     interface{ Write([]byte) (int, error) }
	out    *bufio.Scanner
	peer   netip.AddrPort
	exited chan struct{}
}

func relayUDPSpawn(t *testing.T, addr string) *relayUDPTestChild {
	t.Helper()
	relayUDPRequireIsolation(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRelayUDPChild$")
	cmd.Env = append(os.Environ(), "RELAY_UDP_CHILD=1", "RELAY_UDP_CHILD_ADDR="+addr)
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	h := &relayUDPTestChild{cmd: cmd, in: in, out: bufio.NewScanner(out), exited: make(chan struct{})}
	h.out.Buffer(make([]byte, 4096), 200000)
	h.peer = netip.MustParseAddrPort(h.line(t))
	go func() { cmd.Wait(); close(h.exited) }()
	t.Cleanup(func() {
		in.Close()
		cmd.Process.Kill()
		select {
		case <-h.exited:
		case <-time.After(2 * time.Second):
			t.Error("child not joined")
		}
	})
	return h
}
func (h *relayUDPTestChild) line(t *testing.T) string {
	t.Helper()
	if !h.out.Scan() {
		t.Fatal("child output ended")
	}
	return h.out.Text()
}
func (h *relayUDPTestChild) command(t *testing.T, s string) string {
	t.Helper()
	fmt.Fprintln(h.in, s)
	return h.line(t)
}
func relayUDPFixture(t *testing.T) *net.UDPConn {
	t.Helper()
	relayUDPRequireIsolation(t)
	c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRelayUDPOwnershipLease(t *testing.T) {
	child := relayUDPSpawn(t, "")
	ingress := relayUDPFixture(t).LocalAddr().(*net.UDPAddr).AddrPort()
	lease, e := acquireRelayUDPSocket(child.cmd.Process.Pid, child.peer, ingress)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	stranger := relayUDPFixture(t)
	if f, e := acquireRelayUDPSocket(child.cmd.Process.Pid, stranger.LocalAddr().(*net.UDPAddr).AddrPort(), ingress); e == nil {
		f.Close()
		t.Fatal("unrelated process certified")
	}
	if child.command(t, "release") != "released" {
		t.Fatal("release")
	}
	// Holding a duplicate kernel socket prevents port reuse after /proc observation.
	rebound, e := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(child.peer))
	if e == nil {
		rebound.Close()
		t.Fatal("lease did not reserve exact socket")
	}
	if f, e := acquireRelayUDPSocket(child.cmd.Process.Pid, child.peer, ingress); e == nil {
		f.Close()
		t.Fatal("released fd still certified")
	}
	lease.Close()
	rebound, e = net.ListenUDP("udp4", net.UDPAddrFromAddrPort(child.peer))
	if e != nil {
		t.Fatal("lease leaked:", e)
	}
	rebound.Close()
	t.Log("exact child inode lease rejects unrelated and released ownership; port reuse prevented until close; pid=" + strconv.Itoa(child.cmd.Process.Pid))
}

func TestRelayUDPExactChildRoundTrip(t *testing.T) {
	fixture := relayUDPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := RelayUDPConfig{ProfileID: "p_test", Revision: 1, Country: "DE", Access: RelayUDPPath{SourceIP: "127.0.0.2", Interface: "lo", PathConfirmed: true}, Freshness: time.Minute}
	b, e := newRelayUDPFixture(ctx, cfg, fixture.LocalAddr().(*net.UDPAddr).AddrPort())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	// Stranger arrives first. Authorize discovers exact child's socket from /proc,
	// not this source tuple; pre-authorization queue must be discarded.
	stranger := relayUDPFixture(t)
	stranger.WriteToUDPAddrPort([]byte("stranger"), b.Target())
	child := relayUDPSpawn(t, "")
	var current atomic.Bool
	current.Store(true)
	owner := RelayUDPChild{PID: child.cmd.Process.Pid, Generation: 7, Exited: child.exited, Current: func() bool { return current.Load() }}
	if e = b.Authorize(ctx, owner); e != nil {
		t.Fatal(e)
	}
	stranger.WriteToUDPAddrPort([]byte("stranger-after"), b.Target())
	if child.command(t, "send "+b.Target().String()+" "+hex.EncodeToString([]byte("opaque-I1"))) != "true" {
		t.Fatal("send failed")
	}
	fixture.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 65536)
	n, peer, e := fixture.ReadFromUDPAddrPort(buf)
	if e != nil || !bytes.Equal(buf[:n], []byte("opaque-I1")) {
		t.Fatalf("forward got %q err=%v", buf[:n], e)
	}
	if peer.Addr().String() != "127.0.0.2" {
		t.Fatal("unbound source", peer)
	}
	fixture.WriteToUDPAddrPort([]byte("reply"), peer)
	if got := child.command(t, "read"); got != hex.EncodeToString([]byte("reply")) {
		t.Fatal("reply", got)
	}
	current.Store(false)
	child.command(t, "send "+b.Target().String()+" 626c6f636b6564")
	fixture.SetReadDeadline(time.Now().Add(120 * time.Millisecond))
	if n, _, e := fixture.ReadFromUDP(buf); e == nil {
		t.Fatal("old generation forwarded", n)
	}
	b.Close()
	select {
	case <-b.Done():
	default:
		t.Fatal("close did not join")
	}
}
