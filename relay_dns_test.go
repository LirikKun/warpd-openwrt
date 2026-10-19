package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestRelayDNSTunnelPeerRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		ipv6 string
		dns  string
	}{
		{"ipv6_dns_with_assigned_ipv6", "2001:db8::2", "2001:db8::53"},
		{"mixed_dns_with_assigned_ipv6", "2001:db8::2", "192.0.2.53,2001:db8::53"},
		{"ipv6_dns_with_assigned_ipv6_prefix", "2001:db8::2/128", "2001:db8::53"},
		{"ipv4_dns_with_assigned_ipv6", "2001:db8::2", "192.0.2.53"},
		{"default_dns_with_assigned_ipv6", "2001:db8::2", ""},
		{"ipv4_dns_without_assigned_ipv6", "", "192.0.2.53"},
		{"default_dns_without_assigned_ipv6", "", ""},
		{"mixed_dns_without_assigned_ipv6", "", "192.0.2.53,2001:db8::53"},
		// This accepted input remains unsupported for IPv6 DNS reachability:
		// rendering must not invent a tunnel address, route, or replacement DNS.
		{"ipv6_dns_without_assigned_ipv6", "", "2001:db8::53"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := integrationSettings()
			p := g.Proxies["relay"]
			p.Credential.TunnelIPv6 = tc.ipv6
			p.Credential.DNS = tc.dns
			if err := validateRelayCredential(p.Credential); err != nil {
				t.Fatalf("invalid synthetic fixture: %v", err)
			}
			before := p.Credential
			conf := mihomoConfText(g, endpoints{profiles: map[string]string{"relay": "127.0.0.1:23451"}}, warpAccount{})
			marker := fmt.Sprintf("  - name: %q\n", profileEngineName("relay", p))
			_, block, ok := strings.Cut(conf, marker)
			if !ok {
				t.Fatal("Relay adapter was not rendered")
			}
			block, _, _ = strings.Cut(block, "\nlisteners:")
			wantRoute := "        allowed-ips: ['0.0.0.0/0']\n"
			if tc.ipv6 != "" {
				wantRoute = "        allowed-ips: ['0.0.0.0/0', '::/0']\n"
				if !strings.Contains(block, "    ipv6: "+strings.TrimSuffix(tc.ipv6, "/128")+"\n") {
					t.Error("assigned tunnel IPv6 was not preserved")
				}
			} else if strings.Contains(block, "    ipv6:") || strings.Contains(block, "::/0") {
				t.Error("renderer invented IPv6 configuration without an assigned tunnel IPv6")
			}
			if !strings.Contains(block, wantRoute) || strings.Count(block, "        allowed-ips:") != 1 {
				t.Errorf("Relay peer route does not match assigned tunnel families; want %q", strings.TrimSpace(wantRoute))
			}
			wantDNS := `    dns: ["1.1.1.1", "1.0.0.1"]`
			if tc.dns != "" {
				wantDNS = "    dns: [" + tc.dns + "]"
			}
			if !strings.Contains(block, wantDNS+"\n") || !strings.Contains(block, "    remote-dns-resolve: true\n") {
				t.Error("tunnel DNS policy changed while selecting peer routes")
			}
			if p.Credential != before {
				t.Fatal("rendering peer routes mutated profile-owned credentials")
			}
		})
	}
}

func TestRelayDNSResolverPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		dns  string
		want string
	}{
		{"generated_without_dns", "", `dns: ["1.1.1.1", "1.0.0.1"]`},
		{"explicit_ipv4_dns", "192.0.2.53", "dns: [192.0.2.53]"},
		{"explicit_ipv6_dns", "2001:db8::53", "dns: [2001:db8::53]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := integrationSettings()
			p := g.Proxies["relay"]
			p.Credential.DNS = tc.dns
			if tc.dns == "" {
				p.Credential.Mode = "generated"
			}
			before := p.Credential
			conf := mihomoConfText(g, endpoints{profiles: map[string]string{"relay": "127.0.0.1:23451"}}, warpAccount{})
			marker := fmt.Sprintf("  - name: %q\n", profileEngineName("relay", p))
			_, block, ok := strings.Cut(conf, marker)
			if !ok {
				t.Fatal("Relay adapter was not rendered")
			}
			block, _, _ = strings.Cut(block, "\n  - name:")
			block, _, _ = strings.Cut(block, "\nlisteners:")
			if !strings.Contains(block, "    remote-dns-resolve: true\n") {
				t.Error("Relay must resolve SOCKS hostnames inside its tunnel, not through the system Fake-IP resolver")
			}
			if !strings.Contains(block, "    "+tc.want+"\n") {
				t.Errorf("Relay is missing the required tunnel resolver policy %q", tc.want)
			}
			if strings.Count(block, "    dns:") != 1 {
				t.Error("Relay must have exactly one nonempty tunnel DNS list")
			}
			if p.Credential != before {
				t.Fatal("rendering DNS defaults mutated profile-owned credentials")
			}
			if !strings.Contains(conf, "dns: { enable: false }\n") {
				t.Fatal("Relay DNS policy must not enable a system-facing DNS listener")
			}
		})
	}
}
