package main

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"
)

func relayUDPCheckBinding(t *testing.T, c net.Conn, ip string, index int) {
	t.Helper()
	defer c.Close()
	host, _, err := net.SplitHostPort(c.LocalAddr().String())
	if err != nil || host != ip {
		t.Fatalf("local binding %s want %s: %v", c.LocalAddr(), ip, err)
	}
	raw, err := c.(interface {
		SyscallConn() (syscall.RawConn, error)
	}).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var got int
	var sockErr error
	if err = raw.Control(func(fd uintptr) {
		got, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, 62 /* SO_BINDTOIFINDEX */)
	}); err != nil || sockErr != nil || got != index {
		t.Fatalf("device binding=%d want=%d raw=%v socket=%v", got, index, err, sockErr)
	}
}

func TestRelayUDPActualBoundSocketAndDNSPolicy(t *testing.T) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp := relayUDPFixture(t)
	for _, mode := range []string{"combined", "source-only", "device-only"} {
		t.Run(mode, func(t *testing.T) {
			path := relayUDPCfg().Access
			wantIP := "127.0.0.2"
			wantIndex := 1
			if mode == "source-only" {
				path.Interface = ""
				wantIndex = 0
			}
			if mode == "device-only" {
				path.SourceIP = ""
				wantIP = "127.0.0.1"
			}
			_, tcpDial, udpDial := relayBoundDialers(path)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c, err := udpDial.DialContext(ctx, "udp4", udp.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			relayUDPCheckBinding(t, c, wantIP, wantIndex)
			c, err = tcpDial.DialContext(ctx, "tcp4", tcp.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			relayUDPCheckBinding(t, c, wantIP, wantIndex)
			for _, network := range []string{"udp", "tcp"} {
				target := udp.LocalAddr().String()
				if network == "tcp" {
					target = tcp.Addr().String()
				}
				c, err = udpDial.Resolver.Dial(ctx, network, target)
				if err != nil {
					t.Fatal(err)
				}
				relayUDPCheckBinding(t, c, wantIP, wantIndex)
			}
		})
	}
	// The unchanged HTTP transport is only dialed to a local TCP fixture, without
	// constructing an HTTP request or any activation URL.
	transport, err := newRelayTransport(RelayAccess{Interface: "lo", PathConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	c, err := transport.DialContext(context.Background(), "tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	relayUDPCheckBinding(t, c, "127.0.0.1", 1)
}

func TestRelayUDPDNSBindingFailureHasNoFallback(t *testing.T) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp := relayUDPFixture(t)
	for _, mode := range []string{"device", "source"} {
		t.Run(mode, func(t *testing.T) {
			path := relayUDPCfg().Access
			if mode == "device" {
				path.Interface = "missing0"
			} else {
				path.SourceIP = "192.0.2.88"
			}
			_, _, dial := relayBoundDialers(path)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			for _, network := range []string{"udp", "tcp"} {
				target := udp.LocalAddr().String()
				if network == "tcp" {
					target = tcp.Addr().String()
				}
				if c, err := dial.Resolver.Dial(ctx, network, target); err == nil {
					c.Close()
					t.Fatalf("%s DNS fell back after %s bind failure", network, mode)
				}
			}
		})
	}
}
