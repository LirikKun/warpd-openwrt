//go:build linux

package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMihomoOwnershipIPv6OnlyWithOwnedIPv4LoopbackRejectsWildcard(t *testing.T) {
	v6, err := net.Listen("tcp6", "[::]:0") // tcp6 explicitly sets IPV6_V6ONLY.
	if err != nil {
		t.Fatal(err)
	}
	defer v6.Close()
	port := v6.Addr().(*net.TCPAddr).Port
	v4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer v4.Close()

	if err := (&MihomoMan{}).checkOwnership(os.Getpid(), []mihomoListener{{"0.0.0.0", port}}); err == nil {
		t.Fatal("ownership accepted IPv6-only wildcard plus owned IPv4-loopback-only listener as IPv4 wildcard")
	}
}

func TestMihomoOwnershipSingleOwnedDualStackWildcard(t *testing.T) {
	ln, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if ln.Addr().(*net.TCPAddr).IP.To4() != nil {
		t.Fatal("fixture did not create an IPv6 wildcard socket")
	}
	conn, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("fixture is not dual-stack: %v", err)
	}
	conn.Close()

	if err := (&MihomoMan{}).checkOwnership(os.Getpid(), []mihomoListener{{"0.0.0.0", port}}); err != nil {
		t.Fatalf("single owned dual-stack wildcard rejected: %v", err)
	}
}

func TestMihomoOwnershipIPv6OnlyRejectsIPv4Wildcard(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if err := (&MihomoMan{}).checkOwnership(os.Getpid(), []mihomoListener{{"0.0.0.0", port}}); err == nil {
		t.Fatal("IPv6-only wildcard satisfied IPv4 wildcard")
	}
}

func TestMihomoOwnershipExactIPv4WithIPv6OnlyWildcard(t *testing.T) {
	v6, err := net.Listen("tcp6", "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer v6.Close()
	port := v6.Addr().(*net.TCPAddr).Port
	v4, err := net.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer v4.Close()

	if err := (&MihomoMan{}).checkOwnership(os.Getpid(), []mihomoListener{{"0.0.0.0", port}}); err != nil {
		t.Fatalf("exact owned IPv4 wildcard with IPv6-only wildcard rejected: %v", err)
	}
}

func TestMihomoOwnershipIPv6OnlyWithForeignIPv4LoopbackCollision(t *testing.T) {
	v6, err := net.Listen("tcp6", "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer v6.Close()
	port := v6.Addr().(*net.TCPAddr).Port
	v4, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer v4.Close()
	fd, err := v4.File()
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	child := ownershipSleepingCommand()
	child.ExtraFiles = []*os.File{fd}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	// Leave the IPv4 listener exclusively in the child, while this PID retains
	// the IPv6-only wildcard. Executable names are deliberately identical.
	v4.Close()
	fd.Close()

	m := &MihomoMan{}
	wants := []mihomoListener{{"0.0.0.0", port}}
	for _, check := range []struct {
		name string
		run  func(int, []mihomoListener) error
	}{{"ownership", m.checkOwnership}, {"collisions", m.checkCollisions}} {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(os.Getpid(), wants); err == nil || !strings.Contains(err.Error(), "collision") {
				t.Fatalf("foreign IPv4 listener must be a collision, got %v", err)
			}
		})
	}
}
