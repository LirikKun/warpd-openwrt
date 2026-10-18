//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// This child owns real loopback listeners; no procRoot/ownership override is used.
func TestGateReviewChild(t *testing.T) {
	if os.Getenv("WARPD_GATE_REVIEW_CHILD") != "1" {
		return
	}
	conf, err := os.ReadFile(os.Getenv("WARPD_GATE_REVIEW_CONFIG"))
	if err != nil {
		os.Exit(2)
	}
	listeners, err := mihomoListeners(string(conf))
	if err != nil {
		os.Exit(3)
	}
	var private []net.Listener
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	for _, want := range listeners {
		ln, err := net.Listen("tcp", net.JoinHostPort(want.address, strconv.Itoa(want.port)))
		if err != nil {
			os.Exit(4)
		}
		if want.port != 9099 {
			private = append(private, ln)
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					c.SetDeadline(time.Now().Add(5 * time.Second))
					if !gateReviewHandshake(c) {
						return
					}
					if os.Getenv("WARPD_GATE_REVIEW_MODE") == "halfclose" {
						request, err := io.ReadAll(c)
						if err == nil && string(request) == "request" {
							c.Write([]byte("response-after-request-eof"))
						}
					} else {
						io.Copy(c, c)
					}
				}()
			}
		}()
	}
	for range signals {
		for _, ln := range private {
			ln.Close()
		}
		os.WriteFile(os.Getenv("WARPD_GATE_REVIEW_CONFIG")+".closed", nil, 0600)
	}
}

func gateReviewHandshake(c net.Conn) bool {
	h := make([]byte, 3)
	if _, err := io.ReadFull(c, h); err != nil || !bytes.Equal(h, []byte{5, 1, 0}) {
		return false
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return false
	}
	h = make([]byte, 10)
	if _, err := io.ReadFull(c, h); err != nil {
		return false
	}
	_, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	return err == nil
}

func gateReviewFixture(t *testing.T, mode string) (*Orchestrator, *MihomoMan, *Settings, string, string) {
	t.Helper()
	isolateStatusState(t)
	lockOwnershipController(t)
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	public := reserved.Addr().String()
	publicPort := reserved.Addr().(*net.TCPAddr).Port
	reserved.Close()
	p := NewProxyProfile("review", "warp")
	p.Enabled = true
	p.Listen = "127.0.0.1"
	p.Port = publicPort
	p.Endpoint = "192.0.2.1:2408"
	disabled := NewProxyProfile("zzz", "warp")
	disabled.Port = 30001
	s := &Settings{ProfileSchema: 1, WebPort: 30000, CheckPeriod: 300, Proxies: map[string]*ProxySettings{"review": p, "zzz": disabled}}
	if err := validateProfiles(s.Snapshot(), nil); err != nil {
		t.Fatal(err)
	}
	o := NewOrchestrator(s)
	t.Cleanup(o.closeProfileGates)
	m := &MihomoMan{configPath: filepath.Join(t.TempDir(), "mihomo.yaml")}
	m.command = func(path string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestGateReviewChild$")
		cmd.Env = append(os.Environ(), "WARPD_GATE_REVIEW_CHILD=1", "WARPD_GATE_REVIEW_CONFIG="+path, "WARPD_GATE_REVIEW_MODE="+mode, "GORACE=atexit_sleep_ms=0")
		return cmd
	}
	mihomo = m
	t.Cleanup(m.Stop)
	if err := m.Ensure(s.Snapshot(), stateGetEndpoints(), warpAccount{}); err != nil {
		t.Fatal(err)
	}
	checkProxyWith(context.Background(), s.Snapshot(), "review", o, func(context.Context, string, int) (exitParse, error) {
		return exitParse{node: "AMS", nodeCountry: "NL", country: "US"}, nil
	})
	if !o.profileGateOpen("review") {
		t.Fatal("fresh verified child gate not authorized")
	}
	host, port := profileProbeAddress(s.Snapshot(), "review", p)
	return o, m, s, public, net.JoinHostPort(host, strconv.Itoa(port))
}

func gateReviewConnect(address string, command byte) (*net.TCPConn, error) {
	c, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return nil, err
	}
	tcp := c.(*net.TCPConn)
	tcp.SetDeadline(time.Now().Add(4 * time.Second))
	fail := func(err error) (*net.TCPConn, error) { tcp.Close(); return nil, err }
	if _, err = tcp.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	h := make([]byte, 2)
	if _, err = io.ReadFull(tcp, h); err != nil {
		return fail(err)
	}
	if !bytes.Equal(h, []byte{5, 0}) {
		return fail(fmt.Errorf("greeting %v", h))
	}
	if _, err = tcp.Write([]byte{5, command, 0, 1, 192, 0, 2, 1, 0, 80}); err != nil {
		return fail(err)
	}
	return tcp, nil
}

// Compatibility for transport-only tests, absent from production builds. The
// fixture's real listener belongs to the test process, so retain proc/fd checks
// instead of giving production gates a nil-owner bypass.
func (o *Orchestrator) setProfileGate(id, public, target string, allowed bool, expires time.Time) error {
	var owner *profileGateOwner
	if allowed {
		conf := fmt.Sprintf("listeners:\n    listen: %q\n    port: %s\n", host(target), port(target))
		m := &MihomoMan{pid: os.Getpid(), running: true, lastConf: conf, desiredConf: conf}
		owner = m.profileGateOwner(m.Snapshot(), target)
	}
	return o.setOwnedProfileGate(id, public, target, allowed, expires, owner)
}

func TestGateReviewRejectsReplacedPrivateOwner(t *testing.T) {
	for _, death := range []bool{true, false} {
		t.Run(fmt.Sprintf("child_exited_%v", death), func(t *testing.T) {
			o, m, s, public, target := gateReviewFixture(t, "echo")
			// Start the public negotiation before losing ownership too: checking only at
			// Accept (or the next health tick) does not protect the forwarding boundary.
			pending, err := net.Dial("tcp", public)
			if err != nil {
				t.Fatal(err)
			}
			defer pending.Close()
			pending.SetDeadline(time.Now().Add(3 * time.Second))
			m.mu.Lock()
			child := m.cmd
			done := m.done
			m.mu.Unlock()
			if death {
				if err := child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("child not reaped")
				}
			} else {
				if err := child.Process.Signal(syscall.SIGUSR1); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(2 * time.Second)
				for {
					if _, err := os.Stat(m.configPath + ".closed"); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("child did not release private listener")
					}
					time.Sleep(time.Millisecond)
				}
				if !m.Running() {
					t.Fatal("ownership-loss fixture child exited")
				}
			}
			rogue, err := net.Listen("tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			defer rogue.Close()
			served := make(chan struct{}, 1)
			go func() {
				for {
					c, err := rogue.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						c.SetDeadline(time.Now().Add(time.Second))
						if gateReviewHandshake(c) {
							c.Write([]byte("UNOWNED"))
							served <- struct{}{}
						}
					}()
				}
			}()
			if _, ok := m.AppliedSnapshot(s.Snapshot(), stateGetEndpoints()); ok {
				t.Fatal("lost ownership still applied")
			}
			// A closed public listener OR a rejected in-flight handshake is fail-closed.
			pending.Write([]byte{5, 1, 0})
			h := make([]byte, 2)
			if _, err := io.ReadFull(pending, h); err == nil {
				pending.Write([]byte{5, 1, 0, 1, 192, 0, 2, 1, 0, 80})
				reply := make([]byte, 17)
				if _, err := io.ReadFull(pending, reply); err == nil && string(reply[10:]) == "UNOWNED" {
					t.Fatal("public gate forwarded UNOWNED replacement after exact-child ownership loss")
				}
			}
			c, err := gateReviewConnect(public, 1)
			if err == nil {
				defer c.Close()
				reply := make([]byte, 17)
				if _, err := io.ReadFull(c, reply); err == nil && string(reply[10:]) == "UNOWNED" {
					t.Fatal("new public connection forwarded UNOWNED replacement")
				}
			}
			deadline := time.Now().Add(time.Second)
			for o.profileGateOpen("review") && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if o.profileGateOpen("review") {
				t.Fatal("gate authorization survived child/ownership loss")
			}
			select {
			case <-served:
				t.Fatal("replacement received public SOCKS traffic")
			default:
			}
		})
	}
}

func TestGateReviewRevocationClosesExistingStreams(t *testing.T) {
	for _, reason := range []string{"expiry", "revoke", "generation", "exit"} {
		t.Run(reason, func(t *testing.T) {
			o, m, s, public, _ := gateReviewFixture(t, "echo")
			c, err := gateReviewConnect(public, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			h := make([]byte, 10)
			if _, err := io.ReadFull(c, h); err != nil {
				t.Fatal(err)
			}
			c.Write([]byte("x"))
			b := make([]byte, 1)
			if _, err := io.ReadFull(c, b); err != nil || b[0] != 'x' {
				t.Fatalf("initial stream %q %v", b, err)
			}
			o.gatesMu.Lock()
			gate := o.gates["review"]
			o.gatesMu.Unlock()
			switch reason {
			case "expiry":
				if err := o.setOwnedProfileGate("review", gate.public, gate.target, true, time.Now().Add(40*time.Millisecond), gate.owner); err != nil {
					t.Fatal(err)
				}
			case "revoke":
				if err := o.setOwnedProfileGate("review", "", "", false, time.Time{}, nil); err != nil {
					t.Fatal(err)
				}
			case "generation":
				g := s.Snapshot()
				g.Proxies["review"].Endpoint = "192.0.2.2:2408"
				m.reload = func() error { return nil }
				if err := m.Ensure(g, stateGetEndpoints(), warpAccount{}); err != nil {
					t.Fatal(err)
				}
			case "exit":
				m.mu.Lock()
				child := m.cmd
				m.mu.Unlock()
				if err := child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.Read(b); err == nil {
				t.Fatal("revocation retained live stream")
			}
			deadline := time.Now().Add(time.Second)
			for {
				gate.mu.Lock()
				closed, n := gate.closed, len(gate.conns)
				gate.mu.Unlock()
				if closed && n == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("revocation did not close both tracked sockets: closed=%v sockets=%d", closed, n)
				}
				time.Sleep(time.Millisecond)
			}
			if c, err := net.DialTimeout("tcp", public, 100*time.Millisecond); err == nil {
				c.Close()
				t.Fatal("revocation retained public listener")
			}
		})
	}
}

func TestGateReviewRejectsBindAndUDP(t *testing.T) {
	_, _, _, public, _ := gateReviewFixture(t, "echo")
	for _, command := range []byte{2, 3} {
		c, err := gateReviewConnect(public, command)
		if err != nil {
			t.Fatal(err)
		}
		h := make([]byte, 10)
		_, err = io.ReadFull(c, h)
		c.Close()
		if err != nil || h[1] != 7 {
			t.Fatalf("command %d not rejected: %v %v", command, h, err)
		}
	}
}

func TestGateReviewTCPHalfCloseDrainsResponse(t *testing.T) {
	_, _, _, public, _ := gateReviewFixture(t, "halfclose")
	c, err := gateReviewConnect(public, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := make([]byte, 10)
	if _, err := io.ReadFull(c, h); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "response-after-request-eof" {
		t.Fatalf("TCP half-close truncated response: received=%q err=%v", got, err)
	}
}
