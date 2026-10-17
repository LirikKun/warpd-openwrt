package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// All requests, including TLS and remote-name SOCKS CONNECT, stay on loopback.
func geoFixture(t *testing.T, serve func(*tls.Conn)) (string, int, *x509.CertPool, <-chan error) {
	t.Helper()
	cert, roots := healthTLSCertificate(t, "ifconfig.co")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(2 * time.Second))
		var greet [3]byte
		if _, err = io.ReadFull(c, greet[:]); err != nil {
			done <- err
			return
		}
		if greet != [3]byte{5, 1, 0} {
			done <- fmt.Errorf("wrong greeting")
			return
		}
		c.Write([]byte{5, 0})
		want := append([]byte{5, 1, 0, 3, 11}, []byte("ifconfig.co")...)
		want = append(want, 1, 187)
		got := make([]byte, len(want))
		_, err = io.ReadFull(c, got)
		if err != nil || string(got) != string(want) {
			done <- fmt.Errorf("wrong remote-name CONNECT: %v %v", got, err)
			return
		}
		c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		if err = tc.Handshake(); err != nil {
			done <- nil
			return
		} // rejection is expected in trust tests
		r, err := http.ReadRequest(bufio.NewReader(tc))
		if err != nil {
			done <- err
			return
		}
		if r.Method != "GET" || r.Host != "ifconfig.co" || r.URL.Path != "/json" {
			done <- fmt.Errorf("wrong request")
			return
		}
		serve(tc)
		done <- nil
	}()
	host, ps, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(ps)
	return host, port, roots, done
}

func TestGeoRejectsInvalidIndependentResponses(t *testing.T) {
	for _, tc := range []struct{ name, wire string }{
		{"RU", `{"ip":"8.8.8.8","country_iso":"RU"}`},
		{"unknown", `{"ip":"8.8.8.8","country_iso":"QQ"}`},
		{"unknown sentinel", `{"ip":"8.8.8.8","country_iso":"XX"}`},
		{"missing IP", `{"country_iso":"DE"}`},
		{"private", `{"ip":"192.168.1.1","country_iso":"DE"}`},
		{"loopback", `{"ip":"127.0.0.1","country_iso":"DE"}`},
		{"multicast", `{"ip":"224.0.0.1","country_iso":"DE"}`},
		{"shared", `{"ip":"100.64.0.1","country_iso":"DE"}`},
		{"documentation", `{"ip":"192.0.2.1","country_iso":"DE"}`},
		{"documentation IPv6", `{"ip":"2001:db8::1","country_iso":"DE"}`},
		{"malformed", `{"ip":"8.8.8.8","country_iso":"DE"}secret`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, port, roots, done := geoFixture(t, func(c *tls.Conn) { fmt.Fprint(c, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n"+tc.wire) })
			got, err := socksGeoCheckWithRoots(context.Background(), host, port, roots)
			if tc.name == "unknown" && got.country != "" {
				t.Error("unrecognized provider country retained as status evidence")
			}
			if err == nil {
				t.Error("invalid public country/IP accepted")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeoCancellationClosesStalledSOCKS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		var greet [3]byte
		_, err = io.ReadFull(c, greet[:])
		if err != nil {
			closed <- err
			return
		}
		cancel()
		_, err = io.Copy(io.Discard, c)
		closed <- err
	}()
	_, err = socksGeoCheckWithRoots(ctx, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port, nil)
	if err == nil {
		t.Fatal("canceled request succeeded")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("SOCKS not closed: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancel returned but SOCKS socket remained open")
	}
}

func TestGeoBoundedHTTPFailures(t *testing.T) {
	for _, tc := range []struct{ name, wire string }{
		{"429", "HTTP/1.1 429 Too Many Requests\r\nContent-Length: 0\r\n\r\n"},
		{"non200", "HTTP/1.1 503 synthetic-secret-marker\r\nContent-Length: 0\r\n\r\n"},
		{"redirect", "HTTP/1.1 302 Found\r\nLocation: https://127.0.0.1:1/secret\r\nContent-Length: 0\r\n\r\n"},
		{"aggregate headers", "HTTP/1.1 200 OK\r\n" + strings.Repeat("X-Small: "+strings.Repeat("x", 80)+"\r\n", 200) + "\r\n{}"},
		{"aggregate informational headers", strings.Repeat("HTTP/1.1 103 Early Hints\r\nX-Small: "+strings.Repeat("x", 80)+"\r\n\r\n", 200) + "HTTP/1.1 200 OK\r\n\r\n{}"},
		{"oversized body", "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n" + `{"ip":"8.8.8.8","country_iso":"DE"}` + strings.Repeat(" ", 8192)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, port, roots, done := geoFixture(t, func(c *tls.Conn) { fmt.Fprint(c, tc.wire) })
			_, err := socksGeoCheckWithRoots(context.Background(), host, port, roots)
			if err == nil || strings.Contains(err.Error(), "synthetic-secret-marker") || strings.Contains(err.Error(), "/secret") {
				t.Fatalf("unsafe response accepted/leaked: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeoProductionSystemTrustRejectsFixtureRoot(t *testing.T) {
	host, port, _, done := geoFixture(t, func(*tls.Conn) { t.Error("production trusted test certificate") })
	if _, err := socksGeoCheck(context.Background(), host, port); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGeoParentDeadlineBoundsBodyRead(t *testing.T) {
	host, port, roots, done := geoFixture(t, func(c *tls.Conn) {
		fmt.Fprint(c, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n{")
		io.Copy(io.Discard, c)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := socksGeoCheckWithRoots(ctx, host, port, roots); err == nil {
		t.Fatal("stalled body verified")
	}
	if time.Since(start) > time.Second {
		t.Fatal("body read ignored total parent budget")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGeoDefaultBudgetEndsStalledSOCKS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(10 * time.Second))
		var greet [3]byte
		_, err = io.ReadFull(c, greet[:])
		if err != nil {
			done <- err
			return
		}
		_, err = io.Copy(io.Discard, c)
		done <- err
	}()
	start := time.Now()
	if _, err := socksGeoCheck(context.Background(), "127.0.0.1", ln.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("stalled SOCKS verified")
	}
	elapsed := time.Since(start)
	if elapsed < 7*time.Second || elapsed > 9*time.Second {
		t.Fatalf("single eight-second budget took %v", elapsed)
	}
	if err := <-done; err != nil {
		t.Fatalf("budget did not close SOCKS: %v", err)
	}
}

func TestGeoActualTLSOverCurrentSOCKS(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	host, port, roots, done := geoFixture(t, func(c *tls.Conn) {
		fmt.Fprint(c, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n"+`{"ip":"8.8.8.8","country_iso":"DE"}`)
	})
	got, err := socksGeoCheckWithRoots(context.Background(), host, port, roots)
	if err != nil || got.country != "DE" {
		t.Fatalf("actual independent observation = %+v, %v", got, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
