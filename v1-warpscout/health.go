package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// socksMetaCheck dials the local SOCKS5 proxy, CONNECTs to
// speed.cloudflare.com:443, does a minimal TLS handshake and GET /meta,
// and parses {country, colo:{iata,city,cca2}}. It is the same probe
// warpscout itself uses (Referer header required for a non-empty answer).
func socksMetaCheck(ctx context.Context, listen string, port int) (exitParse, error) {
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
		select {
		case <-ctx.Done():
			return exitParse{}, lastErr
		case <-time.After(3 * time.Second):
		}
	}
	return exitParse{}, lastErr
}

func socksMetaCheckOnce(ctx context.Context, d net.Dialer, addr string) (exitParse, error) {
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return exitParse{}, fmt.Errorf("dial proxy: %w", err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(35 * time.Second))

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
	rep := make([]byte, 10)
	if _, err := ioReadFull(raw, rep); err != nil {
		return exitParse{}, fmt.Errorf("connect reply: %w", err)
	}
	if rep[1] != 0 {
		return exitParse{}, fmt.Errorf("connect refused (%d)", rep[1])
	}

	// TLS through the tunnel
	tlsConn := tls.Client(raw, &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: false,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return exitParse{}, fmt.Errorf("tls: %w", err)
	}
	_ = tlsConn.SetDeadline(time.Now().Add(20 * time.Second))
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
	br := bufio.NewReader(tlsConn)
	var status string
	var body []byte
	// read status line + headers
	status, rerr := br.ReadString('\n')
	if rerr != nil {
		return exitParse{}, fmt.Errorf("read status: %w", rerr)
	}
	if !strings.Contains(status, "200") {
		return exitParse{}, fmt.Errorf("meta status %q", strings.TrimSpace(status))
	}
	for {
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			return exitParse{}, fmt.Errorf("read headers: %w", rerr)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	// read body (bounded)
	buf := make([]byte, 4096)
	total := 0
	for total < 4096 {
		n, err := br.Read(buf[total:])
		total += n
		if err != nil {
			break
		}
	}
	body = buf[:total]

	var m struct {
		Country string `json:"country"`
		Colo    struct {
			IATA string `json:"iata"`
			City string `json:"city"`
			CCA2 string `json:"cca2"`
		} `json:"colo"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return exitParse{}, fmt.Errorf("meta json: %w (%q)", err, string(body[:min(120, len(body))]))
	}
	info := m.Country
	if m.Colo.IATA != "" {
		info = fmt.Sprintf("%s via %s (%s node)", m.Country, m.Colo.City, m.Colo.IATA)
	}
	return exitParse{info: info, node: m.Colo.IATA, country: m.Colo.CCA2}, nil
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
