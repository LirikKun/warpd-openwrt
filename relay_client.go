package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const relayRequestTimeout = 15 * time.Second
const relayBodyLimit = 64 << 10
const relayHeaderLimit = 16 << 10

var relayActivationPath = regexp.MustCompile(`^/activate/[A-Za-z0-9_-]{1,256}$`)
var relayInterfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

// RelayAccess is private configuration, never a status or persistence DTO.
// SourceIP is a local router address, not the observed NAT public address.
// PathConfirmed acknowledges that observation, activation and the Relay tunnel
// use the same intended egress. Socket binding cannot bypass transparent policy.
type RelayAccess struct {
	URL           string `json:"-"`
	Interface     string `json:"-"`
	SourceIP      string `json:"-"`
	PathConfirmed bool   `json:"-"`
}

func (a RelayAccess) String() string   { return "RelayAccess{[REDACTED]}" }
func (a RelayAccess) GoString() string { return a.String() }

type RelayActivation struct {
	Outcome         string `json:"outcome"`
	BindingVerified bool   `json:"binding_verified"`
	TunnelVerified  bool   `json:"tunnel_verified"`
	HTTPStatus      int    `json:"http_status,omitempty"`
}

// RelayClient is safe for concurrent use. Its zero value uses only direct,
// bound HTTPS requests with system trust. It never reads environment proxies.
// transportFor is an unexported local-test seam; URL/path validation always runs.
type RelayClient struct {
	transportFor func(RelayAccess) (http.RoundTripper, error)
}

// ValidateRelayActivationURL never includes the credential in its error.
func ValidateRelayActivationURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 512 || u.Scheme != "https" || u.Host != "relay.tribukvy.ltd" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || !relayActivationPath.MatchString(u.Path) || u.String() != raw {
		return errors.New("invalid Relay activation URL")
	}
	return nil
}

func validateRelaySource(a RelayAccess) error {
	if !a.PathConfirmed || (a.SourceIP == "" && a.Interface == "") {
		return errors.New("Relay source path is not confirmed")
	}
	if a.Interface != "" && !relayInterfaceName.MatchString(a.Interface) {
		return errors.New("invalid Relay source interface")
	}
	if a.SourceIP != "" {
		ip, err := netip.ParseAddr(a.SourceIP)
		if err != nil || ip.Zone() != "" || !ip.IsGlobalUnicast() {
			return errors.New("invalid Relay source address")
		}
	}
	return nil
}

func (c *RelayClient) Activate(ctx context.Context, access RelayAccess) (RelayActivation, error) {
	if err := ValidateRelayActivationURL(access.URL); err != nil {
		return RelayActivation{}, err
	}
	_, status, err := c.request(ctx, access, access.URL, relayBodyLimit)
	if err != nil {
		return RelayActivation{Outcome: "error", HTTPStatus: status}, err
	}
	// Neither public repository specifies the personal activation API. Even
	// {"success":true} is not a verified binding, let alone a working tunnel.
	return RelayActivation{Outcome: "unverified", HTTPStatus: status}, nil
}

type RelayObservation struct {
	PublicIP   string    `json:"public_ip,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

func relayObservationAddress(raw string, a RelayAccess) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return netip.Addr{}, false
	}
	source, _ := netip.ParseAddr(a.SourceIP)
	wantV4 := !source.IsValid() || source.Unmap().Is4()
	if ip.Is4() != wantV4 {
		return netip.Addr{}, false
	}
	if ip.Is4() {
		b := ip.As4()
		if b[0] == 0 || b[0] >= 240 || (b[0] == 100 && b[1] >= 64 && b[1] <= 127) {
			return netip.Addr{}, false
		}
	}
	return ip, true
}

func (c *RelayClient) Observe(ctx context.Context, a RelayAccess) (RelayObservation, error) {
	body, _, err := c.request(ctx, a, "https://api64.ipify.org/", 64)
	if err != nil {
		return RelayObservation{}, err
	}
	ip, ok := relayObservationAddress(strings.TrimSpace(string(body)), a)
	if !ok {
		return RelayObservation{}, errors.New("Relay public IP unavailable")
	}
	return RelayObservation{PublicIP: ip.String(), ObservedAt: time.Now().UTC()}, nil
}

func newRelayTransport(a RelayAccess) (*http.Transport, error) {
	if err := validateRelaySource(a); err != nil {
		return nil, err
	}
	family := "4"
	localIP := net.ParseIP(a.SourceIP)
	if localIP != nil && localIP.To4() == nil {
		family = "6"
	}
	control := func(network, address string, conn syscall.RawConn) error {
		if a.Interface == "" {
			return nil
		}
		var bindErr error
		if err := conn.Control(func(fd uintptr) {
			bindErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, a.Interface)
		}); err != nil {
			return err
		}
		return bindErr
	}
	d := &net.Dialer{Timeout: 5 * time.Second, Control: control}
	if localIP != nil {
		d.LocalAddr = &net.TCPAddr{IP: localIP}
	}
	// DNS sockets use the same device, local address and family as HTTPS.
	d.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		dns := &net.Dialer{Timeout: 5 * time.Second, Control: control}
		if strings.HasPrefix(network, "tcp") {
			if localIP != nil {
				dns.LocalAddr = &net.TCPAddr{IP: localIP}
			}
			return dns.DialContext(ctx, "tcp"+family, address)
		}
		if localIP != nil {
			dns.LocalAddr = &net.UDPAddr{IP: localIP}
		}
		return dns.DialContext(ctx, "udp"+family, address)
	}}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp"+family, address)
	}
	return &http.Transport{Proxy: nil, DialContext: dial, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: relayHeaderLimit, DisableCompression: true, DisableKeepAlives: true}, nil
}

func (c *RelayClient) request(ctx context.Context, a RelayAccess, target string, limit int64) ([]byte, int, error) {
	if err := validateRelaySource(a); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, relayRequestTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, 0, errors.New("Relay request canceled")
	}
	var rt http.RoundTripper
	var err error
	if c != nil && c.transportFor != nil {
		rt, err = c.transportFor(a)
	} else {
		rt, err = newRelayTransport(a)
	}
	if err != nil || rt == nil {
		return nil, 0, errors.New("Relay source transport unavailable")
	}
	if closer, ok := rt.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	client := &http.Client{Transport: rt, Timeout: relayRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, errors.New("Relay request invalid")
	}
	req.Header.Set("Accept", "text/plain, application/json")
	req.Header.Set("Cache-Control", "no-store")
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("Relay request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode, errors.New("Relay provider rejected request")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, res.StatusCode, errors.New("Relay response invalid or too large")
	}
	return body, res.StatusCode, nil
}
