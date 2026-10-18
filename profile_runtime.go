package main

import (
	"reflect"
	"strings"
)

func scanIdentityEqual(a, b Settings, id string) bool {
	an, ac := effectiveExclusions(a, a.Proxies[id])
	bn, bc := effectiveExclusions(b, b.Proxies[id])
	return a.Revision == b.Revision && an == bn && ac == bc && reflect.DeepEqual(a.Proxies[id], b.Proxies[id]) && reflect.DeepEqual(a.Relays, b.Relays)
}

func profileEngineName(id string, p *ProxySettings) string {
	switch id {
	case "warp":
		return "WARP"
	case "masque":
		return "MASQUE"
	case "masqueh2":
		return "MASQUEH2"
	case "m2w":
		return "M2W"
	}
	return "PROFILE-" + id
}
func profileLiveEndpoint(id string, p *ProxySettings, e endpoints) (string, string) {
	if p == nil {
		return "", ""
	}
	switch profileType(id, p) {
	case "relay-warp":
		return e.profiles[id], ""
	case "warp":
		if p.Endpoint != "" {
			return p.Endpoint, ""
		}
		if e.profiles[id] != "" {
			return e.profiles[id], ""
		}
		if id == "warp" {
			return e.warp, ""
		}
	case "masque":
		return firstNonEmpty(p.Endpoint, e.profiles[id], e.masque[id]), ""
	case "legacy-m2w":
		return firstNonEmpty(p.Inner, e.warp), firstNonEmpty(p.Outer, e.masque["masque"])
	}
	return "", ""
}
func profileProbeAddress(g Settings, id string, p *ProxySettings) (string, int) {
	if g.ProfileSchema == 0 {
		return p.Listen, p.Port
	}
	used := map[int]bool{9099: true, g.WebPort: true}
	for _, v := range g.Proxies {
		if v != nil {
			used[v.Port] = true
		}
	}
	port := 30000
	for _, key := range profileIDs(g) {
		for used[port] {
			port++
		}
		if key == id {
			return "127.0.0.1", port
		}
		used[port] = true
		port++
	}
	return "127.0.0.1", 0
}
func profilePolicySettings(g Settings, p *ProxySettings) Settings {
	g.ExcludeNodes, g.ExcludeCountries = effectiveExclusions(g, p)
	return g
}
func normalizedProfileTransport(p *ProxySettings) string { return strings.ToLower(p.Transport) }
