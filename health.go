package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// socksMetaCheck dials the local SOCKS5 proxy, CONNECTs to
// speed.cloudflare.com:443, does a minimal TLS handshake and GET /meta,
// and parses {country, colo:{iata,city,cca2}}. It is the same probe
// warpscout itself uses (Referer header required for a non-empty answer).
func socksMetaCheck(ctx context.Context, listen string, port int) (exitParse, error) {
	return socksMetaCheckBudget(ctx, listen, port, 35*time.Second)
}

func socksMetaCheckBudget(ctx context.Context, listen string, port int, budget time.Duration) (exitParse, error) {
	// One budget covers dialing, all protocol stages, retries, and backoff.
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	d := net.Dialer{Timeout: 12 * time.Second}
	addr := net.JoinHostPort(listen, fmt.Sprintf("%d", port))
	if listen == "0.0.0.0" || listen == "::" {
		addr = net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port))
	}
	// The nested W2W chain is slow to establish: DNS + connect happen inside
	// two tunnels. Retry a couple of times before declaring failure.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		res, err := socksMetaCheckOnce(ctx, d, addr)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return exitParse{}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return exitParse{}, lastErr
}

func socksMetaCheckOnce(ctx context.Context, d net.Dialer, addr string) (exitParse, error) {
	return socksMetaCheckOnceWithRoots(ctx, d, addr, nil)
}

// roots is nil in production (system trust); the seam permits offline TLS fixtures.
func socksMetaCheckOnceWithRoots(ctx context.Context, d net.Dialer, addr string, roots *x509.CertPool) (result exitParse, retErr error) {
	defer func() {
		if retErr != nil && ctx.Err() != nil {
			retErr = ctx.Err()
		}
	}()
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return exitParse{}, fmt.Errorf("dial proxy: %w", err)
	}
	defer raw.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stopCancel()
	_ = setHealthDeadline(ctx, raw, 35*time.Second)

	// SOCKS5: greet (no auth), CONNECT speed.cloudflare.com:443
	if _, err := raw.Write([]byte{5, 1, 0}); err != nil {
		return exitParse{}, fmt.Errorf("greet: %w", err)
	}
	hdr := make([]byte, 2)
	if _, err := ioReadFull(raw, hdr); err != nil {
		return exitParse{}, fmt.Errorf("greet reply: %w", err)
	}
	if hdr[0] != 5 || hdr[1] != 0 {
		return exitParse{}, fmt.Errorf("proxy auth method %d", hdr[1])
	}
	host := "speed.cloudflare.com"
	req := []byte{5, 1, 0, 3, byte(len(host))}
	req = append(req, host...)
	req = append(req, 0x01, 0xbb) // port 443 big endian
	if _, err := raw.Write(req); err != nil {
		return exitParse{}, fmt.Errorf("connect: %w", err)
	}
	if err := readSOCKSConnectReply(raw); err != nil {
		return exitParse{}, err
	}

	// TLS through the tunnel
	tlsConn := tls.Client(raw, &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: false,
		RootCAs:            roots,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return exitParse{}, fmt.Errorf("tls: %w", err)
	}
	_ = setHealthDeadline(ctx, tlsConn, 20*time.Second)
	httpReq := strings.Join([]string{
		"GET /meta HTTP/1.1",
		"Host: " + host,
		"Referer: https://" + host,
		"User-Agent: warpd/1.0",
		"Accept: */*",
		"Connection: close",
		"", "",
	}, "\r\n")
	if _, err := tlsConn.Write([]byte(httpReq)); err != nil {
		return exitParse{}, fmt.Errorf("write: %w", err)
	}
	return readMetaResponse(bufio.NewReader(tlsConn))
}

func setHealthDeadline(ctx context.Context, conn net.Conn, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return conn.SetDeadline(deadline)
}

func readSOCKSConnectReply(r io.Reader) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return fmt.Errorf("connect reply: %w", err)
	}
	if hdr[0] != 5 || hdr[2] != 0 {
		return fmt.Errorf("invalid SOCKS CONNECT reply header")
	}
	if hdr[1] != 0 {
		return fmt.Errorf("connect refused (%d)", hdr[1])
	}
	var addressLen int
	switch hdr[3] {
	case 1:
		addressLen = net.IPv4len
	case 4:
		addressLen = net.IPv6len
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return fmt.Errorf("connect domain length: %w", err)
		}
		addressLen = int(size[0])
		if addressLen == 0 {
			return fmt.Errorf("empty SOCKS CONNECT domain")
		}
	default:
		return fmt.Errorf("connect address type %d", hdr[3])
	}
	if _, err := io.ReadFull(r, make([]byte, addressLen+2)); err != nil {
		return fmt.Errorf("connect address: %w", err)
	}
	return nil
}

func readMetaResponse(br *bufio.Reader) (exitParse, error) {
	// Bound the entire status/header block before net/http builds its map.
	// ReadByte uses br's bounded buffer and never buffers/drains the body.
	const maxMetaHeaders = 16 * 1024
	head := make([]byte, 0, maxMetaHeaders)
	lineStart := 0
	for {
		if len(head) == maxMetaHeaders {
			return exitParse{}, fmt.Errorf("meta headers exceed %d bytes", maxMetaHeaders)
		}
		b, err := br.ReadByte()
		if err != nil {
			return exitParse{}, fmt.Errorf("meta invalid HTTP response")
		}
		head = append(head, b)
		if b == '\n' {
			line := head[lineStart:]
			if len(line) == 1 || len(line) == 2 && line[0] == '\r' {
				break
			}
			lineStart = len(head)
		}
	}
	resp, err := http.ReadResponse(bufio.NewReader(io.MultiReader(strings.NewReader(string(head)), br)), nil)
	if err != nil {
		// net/http errors may quote peer-controlled status/header values.
		return exitParse{}, fmt.Errorf("meta invalid HTTP response")
	}
	// The caller owns and closes the connection. Body.Close here can drain an
	// arbitrarily large response, defeating the body limit on error paths.
	if resp.StatusCode != http.StatusOK {
		return exitParse{}, fmt.Errorf("meta status %d", resp.StatusCode)
	}
	const maxMetaBody = 4096
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody+1))
	if err != nil {
		return exitParse{}, fmt.Errorf("meta body read failed")
	}
	if len(body) > maxMetaBody {
		return exitParse{}, fmt.Errorf("meta body exceeds %d bytes", maxMetaBody)
	}

	var m struct {
		Country string `json:"country"`
		Colo    struct {
			IATA string `json:"iata"`
			City string `json:"city"`
			CCA2 string `json:"cca2"`
		} `json:"colo"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return exitParse{}, fmt.Errorf("meta invalid JSON")
	}
	if strings.TrimSpace(m.Country) == "" || strings.TrimSpace(m.Colo.IATA) == "" {
		return exitParse{}, fmt.Errorf("meta missing country or colo.iata")
	}
	info := m.Country
	if m.Colo.IATA != "" {
		info = fmt.Sprintf("%s via %s (%s node)", m.Country, m.Colo.City, m.Colo.IATA)
	}
	return exitParse{info: info, node: m.Colo.IATA, country: m.Country, nodeCountry: m.Colo.CCA2}, nil
}

func ioReadFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
