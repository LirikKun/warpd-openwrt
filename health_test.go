package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const healthMetaJSON = `{"country":"US","colo":{"iata":"AMS","city":"Amsterdam","cca2":"NL"}}`

func TestHealthChunkedResponse(t *testing.T) {
	wire := fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(healthMetaJSON), healthMetaJSON)
	got, err := readMetaResponse(bufio.NewReader(strings.NewReader(wire)))
	if err != nil {
		t.Fatalf("valid chunked meta response rejected: %v", err)
	}
	if got.node != "AMS" {
		t.Fatalf("node = %q, want AMS", got.node)
	}
}

func TestHealthRejectsNon200Status(t *testing.T) {
	wire := "HTTP/1.1 500 upstream200\r\n\r\n" + healthMetaJSON
	if _, err := readMetaResponse(bufio.NewReader(strings.NewReader(wire))); err == nil {
		t.Fatal("accepted non-200 response whose reason contains 200")
	}
}

func TestHealthClientCountry(t *testing.T) {
	got, err := readMetaResponse(bufio.NewReader(strings.NewReader("HTTP/1.1 200 OK\r\n\r\n" + healthMetaJSON)))
	if err != nil {
		t.Fatal(err)
	}
	if got.country != "US" || got.info != "US via Amsterdam (AMS node)" {
		t.Fatalf("got country=%q info=%q; want client country US, not colo country NL", got.country, got.info)
	}
}

func TestHealthRejectsNonMetaJSON(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"other":"value"}`, `{"country":"US"}`, `{"colo":{"iata":"AMS"}}`, `{"country":"  ","colo":{"iata":"AMS"}}`, `{"country":"US","colo":{"iata":"\t"}}`} {
		t.Run(body, func(t *testing.T) {
			_, err := readMetaResponse(bufio.NewReader(strings.NewReader("HTTP/1.1 200 OK\r\n\r\n" + body)))
			if err == nil {
				t.Fatal("accepted JSON without nonempty country and colo.iata")
			}
		})
	}
}

func TestHealthRejectsOversizeBody(t *testing.T) {
	body := healthMetaJSON + strings.Repeat(" ", 8192)
	wire := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	r := &healthByteReader{data: wire}
	if _, err := readMetaResponse(bufio.NewReader(r)); err == nil {
		t.Fatal("accepted body larger than 4096 bytes")
	}
	if remaining := len(r.data); remaining < len(body)-4097 {
		t.Fatalf("oversize body was drained: %d unread bytes", remaining)
	}
}

type healthByteReader struct{ data string }

func (r *healthByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestHealthErrorsDoNotLeakResponseBody(t *testing.T) {
	const secret = "synthetic-secret-marker"
	for _, body := range []string{`{"private":"` + secret + `",`, `{"country":{"` + secret + `":"value"}}`} {
		_, err := readMetaResponse(bufio.NewReader(strings.NewReader("HTTP/1.1 200 OK\r\n\r\n" + body)))
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("expected sanitized JSON error, got %v", err)
		}
	}
}

func TestHealthSOCKSReplyAddressLengths(t *testing.T) {
	for name, reply := range map[string][]byte{
		"IPv4":         {5, 0, 0, 1, 127, 0, 0, 1, 0, 0},
		"short domain": {5, 0, 0, 3, 1, 'x', 0, 0},
		"long domain":  append([]byte{5, 0, 0, 3, 12}, append([]byte("example.test"), 0, 0)...),
		"IPv6":         append([]byte{5, 0, 0, 4}, make([]byte, 18)...),
	} {
		t.Run(name, func(t *testing.T) {
			const next = "next-TLS-record"
			r := strings.NewReader(string(reply) + next)
			if err := readSOCKSConnectReply(r); err != nil {
				t.Fatal(err)
			}
			remaining, err := io.ReadAll(r)
			if err != nil || string(remaining) != next {
				t.Fatalf("reply framing left %q, want %q (err %v)", remaining, next, err)
			}
		})
	}
}

func TestHealthSOCKSRejectsInvalidReply(t *testing.T) {
	for name, reply := range map[string][]byte{
		"version":               {4, 0, 0, 1, 0, 0, 0, 0, 0, 0},
		"reserved":              {5, 0, 1, 1, 0, 0, 0, 0, 0, 0},
		"refused":               {5, 5, 0, 1},
		"unknown type":          {5, 0, 0, 9},
		"empty domain":          {5, 0, 0, 3, 0, 0, 0},
		"truncated IPv4":        {5, 0, 0, 1, 0},
		"truncated IPv6":        {5, 0, 0, 4, 0},
		"truncated domain":      {5, 0, 0, 3, 5, 'x'},
		"missing domain length": {5, 0, 0, 3},
		"short header":          {5, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if err := readSOCKSConnectReply(strings.NewReader(string(reply))); err == nil {
				t.Fatal("accepted invalid SOCKS CONNECT reply")
			}
		})
	}
}

func TestHealthDoesNotSleepAfterFinalRetry(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	third := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for attempt := 0; attempt < 3; attempt++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var greet [3]byte
			_, _ = io.ReadFull(conn, greet[:])
			_, _ = conn.Write([]byte{5, 255})
			_ = conn.Close()
		}
		close(third)
	}()
	defer func() { ln.Close(); <-serverDone }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := socksMetaCheck(ctx, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
		done <- err
	}()
	select {
	case <-third:
	case <-ctx.Done():
		t.Fatal("did not reach third local attempt")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected final failed greeting")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("probe sleeps after its final retry")
	}
}

type healthDeadlineConn struct {
	net.Conn
	deadline time.Time
}

func (c *healthDeadlineConn) SetDeadline(deadline time.Time) error {
	c.deadline = deadline
	return nil
}

func TestHealthDeadlineUsesEarlierContext(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	defer cancel()
	want, _ := ctx.Deadline()
	conn := &healthDeadlineConn{}
	if err := setHealthDeadline(ctx, conn, 35*time.Second); err != nil {
		t.Fatal(err)
	}
	if !conn.deadline.Equal(want) {
		t.Fatalf("socket deadline %v ignores earlier context deadline %v", conn.deadline, want)
	}
	before := time.Now()
	if err := setHealthDeadline(context.Background(), conn, time.Second); err != nil {
		t.Fatal(err)
	}
	if conn.deadline.Before(before.Add(time.Second)) || conn.deadline.After(time.Now().Add(time.Second)) {
		t.Fatal("missing fallback stage deadline")
	}
}

func TestHealthRetryWaitCancellation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var greet [3]byte
		_, _ = io.ReadFull(conn, greet[:])
		_, _ = conn.Write([]byte{5, 255})
		_, _ = io.Copy(io.Discard, conn)
		// EOF establishes that the first attempt closed; cancel in retry backoff.
		time.AfterFunc(50*time.Millisecond, cancel)
	}()
	defer func() { ln.Close(); <-serverDone }()
	done := make(chan error, 1)
	go func() {
		_, err := socksMetaCheck(ctx, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("retry cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry wait ignored cancellation")
	}
}

func healthTLSCertificate(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func healthLocalSOCKS(t *testing.T, cert tls.Certificate, reply []byte, stage string, cancel context.CancelFunc) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		done <- healthServeSOCKS(conn, cert, reply, stage, cancel)
	}()
	return ln.Addr().String(), done
}

func healthServeSOCKS(conn net.Conn, cert tls.Certificate, reply []byte, stage string, cancel context.CancelFunc) error {
	var greeting [3]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return err
	}
	if greeting != [3]byte{5, 1, 0} {
		return fmt.Errorf("unexpected greeting %v", greeting)
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return err
	}
	const host = "speed.cloudflare.com"
	wantConnect := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	wantConnect = append(wantConnect, 1, 187)
	connect := make([]byte, len(wantConnect))
	if _, err := io.ReadFull(conn, connect); err != nil {
		return err
	}
	if string(connect) != string(wantConnect) {
		return fmt.Errorf("unexpected CONNECT")
	}
	if stage == "connect" {
		cancel()
		return healthAwaitClosed(conn)
	}
	if stage == "connect tail" {
		if _, err := conn.Write([]byte{5, 0, 0, 3, 8, 'x'}); err != nil {
			return err
		}
		cancel()
		return healthAwaitClosed(conn)
	}
	if _, err := conn.Write(reply); err != nil {
		return err
	}
	if stage == "tls" {
		var first [1]byte
		if _, err := io.ReadFull(conn, first[:]); err != nil {
			return err
		}
		cancel()
		return healthAwaitClosed(conn)
	}
	tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err := tlsConn.Handshake(); err != nil {
		if stage == "reject certificate" {
			return nil
		}
		return err
	}
	if stage == "reject certificate" {
		return fmt.Errorf("client accepted invalid certificate")
	}
	req, err := http.ReadRequest(bufio.NewReader(tlsConn))
	if err != nil {
		return err
	}
	if req.Method != "GET" || req.URL.Path != "/meta" || req.Host != host || req.Header.Get("Referer") != "https://"+host {
		return fmt.Errorf("unexpected meta request")
	}
	if stage == "http headers" {
		cancel()
		return healthAwaitClosed(conn)
	}
	if stage == "http body" {
		if _, err := tlsConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n{")); err != nil {
			return err
		}
		cancel()
		return healthAwaitClosed(conn)
	}
	wire := fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(healthMetaJSON), healthMetaJSON)
	_, err = tlsConn.Write([]byte(wire))
	return err
}

func healthAwaitClosed(conn net.Conn) error {
	_, err := io.Copy(io.Discard, conn)
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		return fmt.Errorf("probe did not close canceled socket: %w", err)
	}
	return nil
}

func TestHealthCancellationAcrossStages(t *testing.T) {
	cert, roots := healthTLSCertificate(t, "speed.cloudflare.com")
	for _, stage := range []string{"connect", "connect tail", "tls", "http headers", "http body"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			addr, serverDone := healthLocalSOCKS(t, cert, []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}, stage, cancel)
			done := make(chan error, 1)
			go func() { _, err := socksMetaCheckOnceWithRoots(ctx, net.Dialer{}, addr, roots); done <- err }()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("probe ignored cancellation at " + stage)
			}
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHealthLocalSOCKSTLSChunked(t *testing.T) {
	cert, roots := healthTLSCertificate(t, "speed.cloudflare.com")
	for _, reply := range [][]byte{
		{5, 0, 0, 1, 127, 0, 0, 1, 0, 0},
		{5, 0, 0, 3, 1, 'x', 0, 0},
		append([]byte{5, 0, 0, 4}, make([]byte, 18)...),
	} {
		addr, serverDone := healthLocalSOCKS(t, cert, reply, "success", nil)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		got, err := socksMetaCheckOnceWithRoots(ctx, net.Dialer{}, addr, roots)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if got.country != "US" || got.node != "AMS" {
			t.Fatalf("wrong meta result: %+v", got)
		}
		if err := <-serverDone; err != nil {
			t.Fatal(err)
		}
	}
}

func TestHealthTLSStillVerifiesCertificates(t *testing.T) {
	for _, host := range []string{"speed.cloudflare.com", "wrong.example"} {
		t.Run(host, func(t *testing.T) {
			cert, roots := healthTLSCertificate(t, host)
			addr, serverDone := healthLocalSOCKS(t, cert, []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}, "reject certificate", nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var err error
			if host == "speed.cloudflare.com" {
				_, err = socksMetaCheckOnce(ctx, net.Dialer{}, addr) // Untrusted root must fail on production path.
			} else {
				_, err = socksMetaCheckOnceWithRoots(ctx, net.Dialer{}, addr, roots) // Even a trusted root cannot waive hostname verification.
			}
			var verification *tls.CertificateVerificationError
			if !errors.As(err, &verification) {
				t.Fatalf("got %v, want certificate verification error", err)
			}
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHealthBudgetBoundsStalledProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		serverDone <- healthAwaitClosed(conn)
	}()
	done := make(chan error, 1)
	go func() {
		_, err := socksMetaCheckBudget(ctx, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port, 80*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("probe ignored total budget without caller deadline")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestHealthHTTPErrorsDoNotLeakResponseFields(t *testing.T) {
	const secret = "synthetic-private-key-marker"
	wires := []string{
		secret + "\r\n\r\n",
		"HTTP/1.1 200 OK\r\n" + secret + "\r\n\r\n",
		fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n%s\r\n\r\n", len(healthMetaJSON), healthMetaJSON, secret),
	}
	for _, wire := range wires {
		_, err := readMetaResponse(bufio.NewReader(strings.NewReader(wire)))
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("expected sanitized HTTP error, got %v", err)
		}
	}
}

func TestHealthHTTPBodyBoundaries(t *testing.T) {
	atLimit := healthMetaJSON + strings.Repeat(" ", 4096-len(healthMetaJSON))
	for name, fixture := range map[string]struct {
		wire  string
		valid bool
	}{
		"content length":           {fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(healthMetaJSON), healthMetaJSON), true},
		"exact limit":              {fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(atLimit), atLimit), true},
		"empty":                    {"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n", false},
		"truncated content length": {"HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n" + healthMetaJSON, false},
		"truncated chunk":          {fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(healthMetaJSON), healthMetaJSON), false},
		"chunk above limit":        {fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s \r\n0\r\n\r\n", len(atLimit)+1, atLimit), false},
		"trailing JSON":            {"HTTP/1.1 200 OK\r\n\r\n" + healthMetaJSON + `{}`, false},
		"wrong field type":         {"HTTP/1.1 200 OK\r\n\r\n" + `{"country":123,"colo":{"iata":"AMS"}}`, false},
		"bodyless non200":          {"HTTP/1.1 204 No Content\r\n\r\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readMetaResponse(bufio.NewReader(strings.NewReader(fixture.wire)))
			if (err == nil) != fixture.valid {
				t.Fatalf("valid=%v, err=%v", fixture.valid, err)
			}
		})
	}
}

func TestHealthRejectStatusDoesNotDrainBody(t *testing.T) {
	const header = "HTTP/1.1 503 Unavailable\r\nContent-Length: 10000\r\n\r\n"
	r := &healthByteReader{data: header + strings.Repeat("x", 10000)}
	if _, err := readMetaResponse(bufio.NewReader(r)); err == nil {
		t.Fatal("accepted non-200 response")
	}
	if len(r.data) != 10000 {
		t.Fatal("read or drained body of rejected response")
	}
}

func TestHealthEarlierCallerDeadlineWins(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		serverDone <- healthAwaitClosed(conn)
	}()
	done := make(chan error, 1)
	go func() {
		_, err := socksMetaCheck(ctx, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("probe ignored earlier caller deadline")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestHealthGreetingCancellation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- conn
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(conn, greeting); err == nil {
			cancel()
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := socksMetaCheckOnce(ctx, net.Dialer{}, ln.Addr().String())
		done <- err
	}()
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(time.Second):
		t.Fatal("local probe did not connect")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("greeting read did not unblock promptly after cancellation")
	}
}
