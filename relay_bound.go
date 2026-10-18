//go:build linux

package main

import (
	"context"
	"net"
	"strings"
	"syscall"
	"time"
)

// relayBoundDialers matches RelayClient source/device/family policy without
// changing or invoking HTTP. UDP and DNS (including TCP fallback) share the
// same binding. Callers validate first; no proxy or unbound resolver is used.
func relayBoundDialers(a RelayUDPPath) (string, *net.Dialer, *net.Dialer) {
	family := "4"
	localIP := net.ParseIP(a.SourceIP)
	if localIP != nil && localIP.To4() == nil {
		family = "6"
	}
	control := func(network, address string, conn syscall.RawConn) error {
		if a.Interface == "" {
			return nil
		}
		var bindErr error
		if e := conn.Control(func(fd uintptr) {
			bindErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, a.Interface)
		}); e != nil {
			return e
		}
		return bindErr
	}
	tcp := &net.Dialer{Timeout: 5 * time.Second, Control: control}
	udp := &net.Dialer{Timeout: 5 * time.Second, Control: control}
	if localIP != nil {
		tcp.LocalAddr = &net.TCPAddr{IP: localIP}
		udp.LocalAddr = &net.UDPAddr{IP: localIP}
	}
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		dns := &net.Dialer{Timeout: 5 * time.Second, Control: control}
		if strings.HasPrefix(network, "tcp") {
			if localIP != nil {
				dns.LocalAddr = &net.TCPAddr{IP: localIP}
			}
			return dns.DialContext(ctx, "tcp"+family, address)
		}
		if localIP != nil {
			dns.LocalAddr = &net.UDPAddr{IP: localIP}
		}
		return dns.DialContext(ctx, "udp"+family, address)
	}}
	tcp.Resolver = resolver
	udp.Resolver = resolver
	return family, tcp, udp
}
