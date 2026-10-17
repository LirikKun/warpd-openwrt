package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type geoResult struct{ country string }
type geoProbe func(context.Context, string, int) (geoResult, error)

// Only these locally constructed reasons may be exposed in status. Network and
// JSON errors can contain peer-controlled strings, so never wrap them here.
type geoError string

func (e geoError) Error() string { return string(e) }

// Production always uses the real independent service and system TLS roots.
func socksGeoCheck(ctx context.Context, listen string, port int) (geoResult, error) {
	return socksGeoCheckWithRoots(ctx, listen, port, nil)
}

func socksGeoCheckWithRoots(parent context.Context, listen string, port int, roots *x509.CertPool) (geoResult, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	tr := &http.Transport{
		Proxy: nil, DialContext: geoSOCKSDialContext(listen, port),
		TLSClientConfig:        &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		MaxResponseHeaderBytes: 16 * 1024, DisableKeepAlives: true, DisableCompression: true,
		ForceAttemptHTTP2: false,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://ifconfig.co/json", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "warpd/1.0")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return geoResult{}, geoError("geo request canceled or timed out")
		}
		return geoResult{}, geoError("geo request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return geoResult{}, geoError("geo status " + strconv.Itoa(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024+1))
	if err != nil {
		return geoResult{}, geoError("geo body read failed")
	}
	if len(body) > 8*1024 {
		return geoResult{}, geoError("geo body exceeds 8192 bytes")
	}
	var result struct {
		IP      string `json:"ip"`
		Country string `json:"country_iso"`
	}
	if json.Unmarshal(body, &result) != nil {
		return geoResult{}, geoError("geo invalid JSON")
	}
	ip := net.ParseIP(result.IP)
	if ip == nil || !geoPublicIP(ip) {
		return geoResult{}, geoError("geo invalid public IP")
	}
	country := strings.ToUpper(strings.TrimSpace(result.Country))
	if !knownCountry(country) {
		return geoResult{}, geoError("geo country unknown")
	}
	if country == "RU" {
		return geoResult{country: country}, geoError("geo public IP country RU is excluded")
	}
	return geoResult{country: country}, nil
}

var geoNonPublicPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func geoPublicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	addr, _ := netip.AddrFromSlice(ip)
	addr = addr.Unmap()
	if addr.Is6() && !netip.MustParsePrefix("2000::/3").Contains(addr) {
		return false
	}
	for _, prefix := range geoNonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// Only the listener is dialed locally; the target hostname is sent to SOCKS.
// Cancellation closes the socket even while a peer stalls mid-handshake.
func geoSOCKSDialContext(listen string, port int) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, target string) (net.Conn, error) {
		if listen == "0.0.0.0" {
			listen = "127.0.0.1"
		}
		if listen == "::" {
			listen = "::1"
		}
		if net.ParseIP(listen) == nil {
			return nil, fmt.Errorf("geo listener must be an IP")
		}
		host, ps, err := net.SplitHostPort(target)
		if err != nil || host != "ifconfig.co" || ps != "443" {
			return nil, fmt.Errorf("geo unexpected target")
		}
		raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(listen, strconv.Itoa(port)))
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { raw.Close() })
		ok := false
		defer func() {
			stop()
			if !ok {
				raw.Close()
			}
		}()
		setHealthDeadline(ctx, raw, 8*time.Second)
		if _, err = raw.Write([]byte{5, 1, 0}); err != nil {
			return nil, err
		}
		var greet [2]byte
		if _, err = io.ReadFull(raw, greet[:]); err != nil {
			return nil, err
		}
		if greet != [2]byte{5, 0} {
			return nil, fmt.Errorf("geo SOCKS auth refused")
		}
		request := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
		request = append(request, 1, 187)
		if _, err = raw.Write(request); err != nil {
			return nil, err
		}
		if err = readSOCKSConnectReply(raw); err != nil {
			return nil, err
		}
		ok = true
		return raw, nil
	}
}
