package main

import (
	"io"
	"net"
	"testing"
	"time"
)

type gateControl interface {
	setProfileGate(string, string, string, bool, time.Time) error
	closeProfileGates()
}

func TestPolicyGateActualSocketRevocationAndExpiry(t *testing.T) {
	o := NewOrchestrator(&Settings{})
	g, ok := any(o).(gateControl)
	if !ok {
		t.Fatal("no public data-plane policy gate")
	}
	defer g.closeProfileGates()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			c, e := upstream.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 3)
				if _, e := io.ReadFull(c, buf); e != nil {
					return
				}
				c.Write([]byte{5, 0})
				buf = make([]byte, 10)
				if _, e := io.ReadFull(c, buf); e != nil {
					return
				}
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				io.Copy(c, c)
			}()
		}
	}()
	reserved, _ := net.Listen("tcp", "127.0.0.1:0")
	public := reserved.Addr().String()
	reserved.Close()
	deadline := time.Now().Add(time.Second)
	for {
		c, e := net.DialTimeout("tcp", public, 20*time.Millisecond)
		if e != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("fixture listener did not close")
		}
		time.Sleep(time.Millisecond)
	}
	if err := g.setProfileGate("p", public, upstream.Addr().String(), false, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c, e := net.DialTimeout("tcp", public, 100*time.Millisecond); e == nil {
		c.Close()
		t.Fatal("unverified candidate has public listener")
	}
	if err := g.setProfileGate("p", public, upstream.Addr().String(), true, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	c, e := net.Dial("tcp", public)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, e = io.ReadFull(c, reply); e != nil || reply[1] != 0 {
		t.Fatalf("greet: %v %v", reply, e)
	}
	c.Write([]byte{5, 1, 0, 1, 192, 0, 2, 1, 0, 80})
	reply = make([]byte, 10)
	if _, e = io.ReadFull(c, reply); e != nil {
		t.Fatal(e)
	}
	c.Write([]byte("allowed"))
	b := make([]byte, 7)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "allowed" {
		t.Fatalf("forward: %q %v", b, e)
	}
	if err := g.setProfileGate("p", public, upstream.Addr().String(), false, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, e = c.Read(make([]byte, 1)); e == nil {
		t.Fatal("revocation retained existing stream")
	}
	if c, e := net.DialTimeout("tcp", public, 100*time.Millisecond); e == nil {
		c.Close()
		t.Fatal("revocation retained listener")
	}
	if err := g.setProfileGate("p", public, upstream.Addr().String(), true, time.Now().Add(30*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if c, e := net.DialTimeout("tcp", public, 100*time.Millisecond); e == nil {
		c.Close()
		t.Fatal("stale health retained listener")
	}
}
