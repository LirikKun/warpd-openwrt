package main

import (
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var fallbackProfileMu sync.Mutex

func (s *Settings) profileMutex() *sync.Mutex {
	if s.profileMu != nil {
		return s.profileMu
	}
	return &fallbackProfileMu
}

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var nodePattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Pinned Mihomo's default AWG (device_v1) UAPI uses bufio.Scanner's
// 64 KiB line buffer: reserve "i1=" and its newline. The packet itself must
// also fit the IPv4 UDP payload limit; MTU is not the I1 packet size limit.
const maxAWGI1Text = (64 << 10) - len("i1=\n")
const maxAWGI1Packet = 65507

var i1Pattern = regexp.MustCompile(`^(<(r[cd]? [0-9]{1,4}|b 0x[0-9a-fA-F]+)>)+$`)
var i1RandomPattern = regexp.MustCompile(`<r[cd]? ([0-9]+)>`)
var i1BytesPattern = regexp.MustCompile(`<b 0x([0-9a-fA-F]+)>`)

func normalizedCodes(raw string, country bool) (string, error) {
	seen := map[string]bool{}
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	for _, v := range strings.Split(raw, ",") {
		v = strings.ToUpper(strings.TrimSpace(v))
		if (country && !knownCountry(v)) || (!country && !nodePattern.MatchString(v)) {
			return "", fmt.Errorf("invalid exclusion code")
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return strings.Join(out, ","), nil
}
func effectiveExclusions(g Settings, p *ProxySettings) (nodes, countries string) {
	merge := func(parts ...string) string {
		seen := map[string]bool{}
		out := []string{}
		for _, s := range parts {
			for _, v := range strings.Split(s, ",") {
				v = strings.ToUpper(strings.TrimSpace(v))
				if v != "" && !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
		}
		return strings.Join(out, ",")
	}
	if p == nil {
		return merge("DME", g.ExcludeNodes), merge("RU", g.ExcludeCountries)
	}
	return merge("DME", g.ExcludeNodes, p.ExcludeNodes), merge("RU", g.ExcludeCountries, p.ExcludeCountries)
}
func validateAWG(a AWGOptions) error {
	if a.Preset != DefaultAWGOptions().Preset || a.JC < 1 || a.JC > 128 || a.JMin < 0 || a.JMax < a.JMin || a.JMax > 1280 || a.S1 < 0 || a.S1 > 1280 || a.S2 < 0 || a.S2 > 1280 {
		return fmt.Errorf("unsupported AWG preset or numeric range")
	}
	if a.S1 != 0 || a.S2 != 0 || a.H1 != 1 || a.H2 != 2 || a.H3 != 3 || a.H4 != 4 {
		return fmt.Errorf("AWG S1/S2/H1-H4 must match pinned WarpScout peer protocol")
	}
	hs := []uint32{a.H1, a.H2, a.H3, a.H4}
	for i, h := range hs {
		for j := 0; j < i; j++ {
			if hs[j] == h {
				return fmt.Errorf("AWG headers must be distinct")
			}
		}
	}
	if len(a.I1) > maxAWGI1Text || !i1Pattern.MatchString(a.I1) {
		return fmt.Errorf("invalid AWG I1 tag chain")
	}
	packetBytes := 0
	for _, m := range i1RandomPattern.FindAllStringSubmatch(a.I1, -1) {
		n, err := strconv.Atoi(m[1])
		// device_v1's random byte/ASCII/digit generators reject sizes >1000.
		if err != nil || n < 1 || n > 1000 {
			return fmt.Errorf("invalid AWG random tag length")
		}
		packetBytes += n
	}
	for _, m := range i1BytesPattern.FindAllStringSubmatch(a.I1, -1) {
		if len(m[1])%2 != 0 {
			return fmt.Errorf("invalid AWG byte tag")
		}
		packetBytes += len(m[1]) / 2
	}
	if packetBytes > maxAWGI1Packet {
		return fmt.Errorf("AWG I1 packet exceeds UDP payload limit")
	}
	return nil
}
func profileProtocol(id string, p *ProxySettings) string {
	if profileType(id, p) == "masque" {
		if p.Transport == "http2" || p.Transport == "" && (id == "masqueh2" || p.Protocol == "masque-h2") {
			return "masque-h2"
		}
		return "masque"
	}
	return "awg"
}
func validateProfiles(g Settings, before *Settings) error {
	if len(g.Proxies) > 128 || len(g.Relays) > 128 {
		return fmt.Errorf("profile store limit exceeded")
	}
	capabilities := map[string]bool{}
	for id, r := range g.Relays {
		if r == nil || !profileIDPattern.MatchString(id) || r.Key != id || strings.TrimSpace(r.Name) == "" || len(r.Name) > 128 {
			return fmt.Errorf("invalid Relay resource")
		}
		if r.ActivationURL != "" {
			if err := ValidateRelayActivationURL(r.ActivationURL); err != nil {
				return err
			}
			if capabilities[r.ActivationURL] {
				return fmt.Errorf("Relay access already belongs to another resource")
			}
			capabilities[r.ActivationURL] = true
		}
		if r.MonitorInterval < 30 || r.MonitorInterval > 86400 {
			return fmt.Errorf("invalid Relay monitor interval")
		}
		if r.SourceIP != "" {
			ip, err := netip.ParseAddr(r.SourceIP)
			if err != nil || ip.Zone() != "" || !ip.IsGlobalUnicast() {
				return fmt.Errorf("invalid Relay source address")
			}
		}
		if r.Interface != "" && !relayInterfaceName.MatchString(r.Interface) {
			return fmt.Errorf("invalid Relay interface")
		}
	}
	ports := map[int]string{}
	for _, id := range profileIDs(g) {
		p := g.Proxies[id]
		if p == nil || !profileIDPattern.MatchString(id) || p.Key != id || strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 {
			return fmt.Errorf("invalid profile identity or name")
		}
		kind := profileType(id, p)
		if err := validateRelayCredential(p.Credential); err != nil {
			return err
		}
		if kind != "relay-warp" && p.Credential != (RelayCredential{}) {
			return fmt.Errorf("credentials require Relay-WARP")
		}
		if before != nil {
			old := before.Proxies[id]
			if old != nil && profileType(id, old) != kind {
				return fmt.Errorf("profile type is immutable")
			}
			if kind == "legacy-m2w" {
				if old == nil {
					return fmt.Errorf("legacy chain is migration-only")
				}
				copy := *p
				copy.Enabled = old.Enabled
				copy.Name = old.Name
				if !reflect.DeepEqual(old, &copy) {
					return fmt.Errorf("legacy chain tunnel settings are read-only")
				}
			}
		}
		if kind != "warp" && kind != "masque" && kind != "relay-warp" && kind != "legacy-m2w" {
			return fmt.Errorf("unsupported profile type")
		}
		if kind != "legacy-m2w" && (p.Outer != "" || p.Inner != "") {
			return fmt.Errorf("chain endpoints are migration-only")
		}
		if kind == "masque" {
			if p.AWG != (AWGOptions{}) && p.AWG != DefaultAWGOptions() {
				return fmt.Errorf("AWG options do not apply to MASQUE")
			}
			if p.Transport != "quic" && p.Transport != "http2" {
				return fmt.Errorf("invalid MASQUE transport")
			}
		} else if p.Transport != "" {
			return fmt.Errorf("transport only applies to MASQUE")
		}
		if p.Port < 1024 || p.Port > 65535 || p.Port == 9099 || p.Port == g.WebPort {
			return fmt.Errorf("invalid or reserved listener port")
		}
		if net.ParseIP(p.Listen) == nil {
			return fmt.Errorf("listener must be an IP address")
		}
		if ports[p.Port] != "" {
			return fmt.Errorf("conflicting profile listener ports")
		}
		ports[p.Port] = id
		for _, ep := range []string{p.Endpoint, p.Outer, p.Inner} {
			if ep != "" && !isEndpointLine(ep) {
				return fmt.Errorf("invalid profile endpoint")
			}
		}
		var err error
		p.ExcludeNodes, err = normalizedCodes(p.ExcludeNodes, false)
		if err != nil {
			return err
		}
		p.ExcludeCountries, err = normalizedCodes(p.ExcludeCountries, true)
		if err != nil {
			return err
		}
		if kind == "warp" || kind == "relay-warp" || kind == "legacy-m2w" {
			if err := validateAWG(p.AWG); err != nil {
				return err
			}
		}
		if kind == "relay-warp" {
			r := g.Relays[p.RelayID]
			if r == nil || r.ActivationURL == "" {
				return fmt.Errorf("Relay-WARP requires a configured Relay resource")
			}
			switch p.RelayCountry {
			case "DE", "NL", "FI", "LV", "EE":
			default:
				return fmt.Errorf("invalid Relay country")
			}
			if p.Endpoint != "" {
				return fmt.Errorf("Relay-WARP endpoint is provider-managed")
			}
		} else if p.RelayID != "" || p.RelayCountry != "" {
			return fmt.Errorf("Relay options require Relay-WARP")
		}
	}
	return nil
}
