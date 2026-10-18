package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Only the destination dial and trust roots are replaced. The production host,
// SNI/hostname verification, method, URL validation and request path stay real.
func relayTLSFixture(t *testing.T, h http.Handler) (*RelayClient, *httptest.Server) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "relay fixture"}, DNSNames: []string{"relay.tribukvy.ltd", "api64.ipify.org"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: pk}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	c := &RelayClient{transportFor: func(a RelayAccess) (http.RoundTripper, error) {
		tr, err := newRelayTransport(a)
		if err != nil {
			return nil, err
		}
		tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		return tr, nil
	}}
	return c, srv
}

func TestRelayTLSRequestBoundsHeadersBodiesAndInformationalResponses(t *testing.T) {
	for _, name := range []string{"long_header", "many_headers", "informational", "large_body", "exact_chunked"} {
		t.Run(name, func(t *testing.T) {
			c, _ := relayTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "relay.tribukvy.ltd" || r.TLS.ServerName != "relay.tribukvy.ltd" || r.URL.Path != "/activate/SYNTHETIC_TEST_TOKEN" {
					t.Error("production request identity changed")
				}
				switch name {
				case "long_header":
					w.Header().Set("X-Large", strings.Repeat("x", relayHeaderLimit+1))
				case "many_headers":
					for i := 0; i < 2000; i++ {
						w.Header().Add("X-Tiny", "x")
					}
				case "informational":
					w.Header().Set("X-Info", strings.Repeat("x", 6000))
					for i := 0; i < 4; i++ {
						w.WriteHeader(103)
					}
					w.Header().Del("X-Info")
				case "large_body":
					w.WriteHeader(200)
					_, _ = io.WriteString(w, strings.Repeat("x", relayBodyLimit+1))
					return
				case "exact_chunked":
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, strings.Repeat("x", relayBodyLimit))
					return
				}
				_, _ = io.WriteString(w, "ok")
			}))
			got, err := c.Activate(context.Background(), RelayAccess{URL: relayFixtureURL, Interface: "lo", PathConfirmed: true})
			if name == "exact_chunked" {
				if err != nil || got.Outcome != "unverified" {
					t.Fatalf("exact bounded chunked body rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unbounded response accepted")
			}
			if err != nil && (strings.Contains(err.Error(), "SYNTHETIC") || strings.Contains(err.Error(), "https:")) {
				t.Fatal("credential in error")
			}
		})
	}
}

func TestRelayTLSCancellationClosesServerRequest(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	c, _ := relayTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(canceled) }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Activate(ctx, RelayAccess{URL: relayFixtureURL, Interface: "lo", PathConfirmed: true})
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("canceled request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation ignored")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cancellation left server socket open")
	}
}

func TestRelayTLSUsesVerificationAndNeverEnvironmentProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	c, _ := relayTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	a := RelayAccess{URL: relayFixtureURL, Interface: "lo", PathConfirmed: true}
	if _, err := c.Activate(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("environment proxy received credential")
	}
	original := c.transportFor
	c.transportFor = func(a RelayAccess) (http.RoundTripper, error) {
		rt, err := original(a)
		if err != nil {
			return nil, err
		}
		rt.(*http.Transport).TLSClientConfig.RootCAs = x509.NewCertPool()
		return rt, nil
	}
	if _, err := c.Activate(context.Background(), a); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
}

func TestRelayTLSCallerDeadlineBoundsStalledResponseBody(t *testing.T) {
	canceled := make(chan struct{})
	c, _ := relayTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Activate(ctx, RelayAccess{URL: relayFixtureURL, Interface: "lo", PathConfirmed: true})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("body deadline not enforced: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("expired body deadline left socket open")
	}
}
