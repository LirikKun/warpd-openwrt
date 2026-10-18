package main

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"strings"
	"testing"
)

func integrationCredential(seed byte) RelayCredential {
	key, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{seed}, 32))
	peer, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{seed + 1}, 32))
	return RelayCredential{Mode: "manual", PrivateKey: base64.StdEncoding.EncodeToString(key.Bytes()), PeerPublicKey: base64.StdEncoding.EncodeToString(peer.PublicKey().Bytes()), TunnelIPv4: "10.77.0.2", TunnelIPv6: "2001:db8::2", Reserved: "AQID", DNS: "1.1.1.1", MTU: 1280}
}
func integrationSettings() Settings {
	g := Settings{ProfileSchema: 1, Revision: 1, CheckPeriod: 300, Proxies: map[string]*ProxySettings{}, Relays: map[string]*RelaySettings{"r": {Key: "r", Name: "Synthetic access", MonitorInterval: 300, Enabled: true, ActivationURL: "https://relay.tribukvy.ltd/activate/0123456789abcdef", SourceIP: "192.0.2.2", Interface: "lo", PathConfirmed: true}}}
	p := NewProxyProfile("relay", "relay-warp")
	p.Enabled = true
	p.RelayID = "r"
	p.RelayCountry = "DE"
	p.Credential = integrationCredential(31)
	p.Listen = "127.0.0.1"
	p.Port = 4811
	p.AWG.I1 = "<b 0x494e544547524154494f4e>"
	p.AWG.JC = 1
	p.AWG.JMin = 8
	p.AWG.JMax = 8
	g.Proxies["relay"] = p
	return g
}
func TestRelayIntegrationRendererOwnKeysPrivateEndpoint(t *testing.T) {
	g := integrationSettings()
	second := *g.Proxies["relay"]
	second.Key = "second"
	second.Port = 4812
	second.Credential = integrationCredential(51)
	second.RelayCountry = "NL"
	g.Proxies["second"] = &second
	w := NewProxyProfile("warp", "warp")
	w.Enabled = true
	w.Endpoint = "192.0.2.3:2408"
	w.Port = 4813
	g.Proxies["warp"] = w
	shared := warpAccount{PrivateKey: "SHARED-PRIVATE", PeerPub: "SHARED-PEER", IPv4: "10.88.0.2"}
	conf := mihomoConfText(g, endpoints{profiles: map[string]string{"relay": "127.0.0.1:23451", "second": "127.0.0.1:23452"}}, shared)
	for _, want := range []string{g.Proxies["relay"].Credential.PrivateKey, second.Credential.PrivateKey, second.Credential.PeerPublicKey, "server: 127.0.0.1", "port: 23451", "port: 23452", "ipv6: 2001:db8::2", "reserved: [1, 2, 3]", "SHARED-PRIVATE"} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing independent Relay renderer field %q", want)
		}
	}
	if strings.Contains(conf, "SYNTHETIC-CAPABILITY") || strings.Contains(conf, "tribukvy") {
		t.Fatal("activation/remote path leaked into engine configuration")
	}
	unprepared := mihomoConfText(g, endpoints{profiles: map[string]string{"relay": "198.51.100.2:4500"}}, shared)
	if strings.Contains(unprepared, g.Proxies["relay"].Credential.PrivateKey) {
		t.Fatal("renderer accepted unprepared direct Relay endpoint")
	}
}

func TestRelayIntegrationOnlyPendingNeverLoadsSharedAccount(t *testing.T) {
	isolateStatusState(t)
	g := integrationSettings()
	g.Relays["r"].SourceIP = "" // real bound DNS fails in the only-lo offline namespace
	o := NewOrchestrator(&g)
	calls := 0
	o.accountLoader = func() (warpAccount, error) { calls++; return warpAccount{}, nil }
	err := o.applyCurrent()
	if calls != 0 || mihomo.Running() {
		t.Fatalf("Relay-only startup loaded unrelated shared account or engine: calls=%d", calls)
	}
	if err == nil || !strings.Contains(err.Error(), "pending:") {
		t.Fatalf("missing precise pending failure instead of unsupported-stage block: %v", err)
	}
}
