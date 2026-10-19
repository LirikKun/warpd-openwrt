//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// Synthetic SOCKS echo child, with real proc/fd ownership; not provider evidence.
func TestProfilePolicyGateClosesNewAndEstablishedTraffic(t *testing.T) {
	for _, mode := range []string{"strict", "exclusions", "off"} {
		t.Run(mode, func(t *testing.T) {
			o, m, s, public, _ := gateReviewFixture(t, "echo")
			p := s.Proxies["review"]
			p.PolicyMode = mode
			p.PolicyCountry = "DE"
			c := getCheck("review")
			c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "DE"}, nil }
			checkProxyWith(context.Background(), s.Snapshot(), "review", o, geoMeta)
			if !o.profileGateOpen("review") {
				t.Fatal("verified policy did not open gate")
			}
			stream, err := gateReviewConnect(public, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			header := make([]byte, 10)
			if _, err = io.ReadFull(stream, header); err != nil {
				t.Fatal(err)
			}
			if _, err = stream.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			one := make([]byte, 1)
			if _, err = io.ReadFull(stream, one); err != nil || one[0] != 'x' {
				t.Fatalf("echo failed: %q %v", one, err)
			}
			before := m.Snapshot()
			switch mode {
			case "strict":
				// Existing evidence is cached for at most 60 seconds. At renewal the
				// independent service observes a changed exit; /meta remains unchanged.
				c.set(func(st *ProxyState) { st.GeoCheck = time.Now().Add(-61 * time.Second).Format(time.RFC3339Nano) })
				c.geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "NL"}, nil }
				checkProxyWith(context.Background(), s.Snapshot(), "review", o, geoMeta)
			case "exclusions":
				checkProxyWith(context.Background(), s.Snapshot(), "review", o, func(context.Context, string, int) (exitParse, error) {
					return exitParse{node: "AMS", nodeCountry: "NL", country: "RU"}, nil
				})
			case "off":
				checkProxyWith(context.Background(), s.Snapshot(), "review", o, func(context.Context, string, int) (exitParse, error) {
					return exitParse{}, errors.New("offline transport loss")
				})
			}
			if o.profileGateOpen("review") {
				t.Fatal("mismatch/unhealthy profile retained gate")
			}
			stream.SetReadDeadline(time.Now().Add(time.Second))
			if _, err = stream.Read(one); err == nil {
				t.Fatal("existing client traffic survived revocation")
			} else if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("existing stream was not closed")
			}
			if conn, err := net.DialTimeout("tcp", public, 100*time.Millisecond); err == nil {
				conn.Close()
				t.Fatal("new client traffic survived revocation")
			}
			after := m.Snapshot()
			if after.PID != before.PID || after.Generation != before.Generation || len(o.scanCh) != 0 {
				t.Fatal("policy mismatch restarted engine or enqueued scan")
			}
			st := NewServer(s, o).status()["proxies"].(map[string]ProxyState)["review"]
			if st.Ready || mode != "off" && (!st.TransportReady || st.FailStreak != 0) {
				t.Fatalf("policy rejection changed liveness: %+v", st)
			}
		})
	}
}

func TestProfilePolicyAPIModeEditRevokesBeforeResponse(t *testing.T) {
	o, m, s, public, _ := gateReviewFixture(t, "echo")
	s.ProfileSchema = 0
	if err := s.InitProfiles(filepath.Join(t.TempDir(), "profiles.json")); err != nil {
		t.Fatal(err)
	}
	o.accountLoader = func() (warpAccount, error) { return warpAccount{}, nil }
	c := getCheck("review")
	calls := 0
	c.geoProbe = func(context.Context, string, int) (geoResult, error) { calls++; return geoResult{country: "DE"}, nil }
	srv := NewServer(s, o)
	w := profileAPIRequest(srv, "PATCH", "/api/profiles/review", `{"policy_mode":"strict","policy_country":"DE"}`)
	if w.Code != 200 {
		t.Fatalf("policy edit %d %s", w.Code, w.Body.String())
	}
	if o.profileGateOpen("review") {
		t.Fatal("strict edit inherited legacy ready health")
	}
	checkProxyWith(context.Background(), s.Snapshot(), "review", o, geoMeta)
	stream, err := gateReviewConnect(public, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err = io.ReadFull(stream, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(stream, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	w = profileAPIRequest(srv, "PATCH", "/api/profiles/review", `{"policy_country":"NL"}`)
	if w.Code != 200 {
		t.Fatalf("country edit %d %s", w.Code, w.Body.String())
	}
	if o.profileGateOpen("review") {
		t.Fatal("country edit did not synchronously revoke gate")
	}
	stream.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = stream.Read(make([]byte, 1)); err == nil {
		t.Fatal("country edit retained existing stream")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("country edit stream merely timed out")
	}
	if after := m.Snapshot(); after.Generation != before.Generation || after.PID != before.PID {
		t.Fatal("non-rendered policy edit reloaded engine")
	}
	// Reverting a durable policy edit still requires a new independent observation.
	w = profileAPIRequest(srv, "PATCH", "/api/profiles/review", `{"policy_country":"DE"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	checkProxyWith(context.Background(), s.Snapshot(), "review", o, geoMeta)
	if calls != 2 || !o.profileGateOpen("review") {
		t.Fatalf("reverted policy lacked fresh evidence: calls=%d", calls)
	}
}

func TestProfilePolicyOffStillEnforcesOwnedListener(t *testing.T) {
	o, m, s, _, _ := gateReviewFixture(t, "echo")
	s.Proxies["review"].PolicyMode = "off"
	checkProxyWith(context.Background(), s.Snapshot(), "review", o, func(context.Context, string, int) (exitParse, error) { return exitParse{}, nil })
	if !o.profileGateOpen("review") {
		t.Fatal("off blocked healthy owned transport")
	}
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for o.profileGateOpen("review") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if o.profileGateOpen("review") {
		t.Fatal("off bypassed child ownership")
	}
}
