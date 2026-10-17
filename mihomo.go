package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MihomoMan runs ONE mihomo process that serves all four proxies
// (warp/masque/masqueh2 + masque-to-warp
// chains). warpd regenerates /tmp/warpd-mihomo.yaml whenever an endpoint
// changes and reloads mihomo over its external-controller API; a full
// process restart is only the fallback.

const (
	mihomoBin  = "/opt/clash/bin/clash"
	mihomoConf = "/tmp/warpd-mihomo.yaml"
	mihomoDir  = "/tmp"
	mihomoAPI  = "127.0.0.1:9099"
)

type MihomoMan struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	pid      int
	running  bool
	lastConf string
	started  time.Time
}

var mihomo = &MihomoMan{}

// mihomoConfText renders the full proxy set from live settings + endpoints.
// Port map (fixed, documented in web UI):
//   4001 warp, 4003 masque, 4004 masqueh2,
//   4005 masque->warp (m2w)
func mihomoConfText(g Settings, e endpoints, acc warpAccount) string {
	var b strings.Builder
	b.WriteString("port: 0\nsocks-port: 0\nmixed-port: 0\n")
	fmt.Fprintf(&b, "mode: direct\nlog-level: warning\nprofile: { store-selected: false }\n")
	fmt.Fprintf(&b, "external-controller: %s\n", mihomoAPI)
	b.WriteString("dns: { enable: false }\nproxies:\n")

	awg := func(name, endpoint string, extra ...string) {
		fmt.Fprintf(&b, "  - name: %q\n    type: wireguard\n", name)
		fmt.Fprintf(&b, "    private-key: %s\n", acc.PrivateKey)
		fmt.Fprintf(&b, "    ip: %s\n", acc.IPv4)
		fmt.Fprintf(&b, "    peers:\n      - server: %s\n        port: %s\n", host(endpoint), port(endpoint))
		fmt.Fprintf(&b, "        public-key: %s\n", acc.PeerPub)
		b.WriteString("        allowed-ips: ['0.0.0.0/0']\n        persistent-keepalive: 25\n")
		b.WriteString("    amnezia-wg-option:\n      jc: 6\n      jmin: 10\n      jmax: 50\n      s1: 0\n      s2: 0\n      h1: 1\n      h2: 2\n      h3: 3\n      h4: 4\n")
		if acc.GenI1 != "" {
			fmt.Fprintf(&b, "      i1: %s\n", acc.GenI1)
		} else {
			b.WriteString("      i1: <r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>\n")
		}
		b.WriteString("    udp: true\n    remote-dns-resolve: true\n    dns: [\"1.1.1.1\", \"1.0.0.1\"]\n")
		for _, x := range extra {
			b.WriteString(x)
		}
	}
	masq := func(name, endpoint, network string, extra ...string) {
		fmt.Fprintf(&b, "  - name: %q\n    type: masque\n", name)
		fmt.Fprintf(&b, "    server: %s\n    port: %s\n", host(endpoint), port(endpoint))
		fmt.Fprintf(&b, "    private-key: %q\n", acc.MasquePriv)
		fmt.Fprintf(&b, "    public-key: %q\n", acc.MasquePeerPub)
		fmt.Fprintf(&b, "    ip: %s/32\n", acc.IPv4)
		if acc.IPv6 != "" {
			fmt.Fprintf(&b, "    ipv6: %s/128\n", acc.IPv6)
		}
		b.WriteString("    udp: true\n    remote-dns-resolve: true\n    dns: [\"1.1.1.1\", \"1.0.0.1\"]\n")
		if network != "" {
			fmt.Fprintf(&b, "    network: %s\n", network)
		}
		for _, x := range extra {
			b.WriteString(x)
		}
		b.WriteString("    mtu: 1280\n")
	}

	// warp
	if p := g.Proxies["warp"]; p.Enabled {
		ep := firstNonEmpty(p.Endpoint, e.warp)
		if ep != "" {
			awg("WARP", ep, fmt.Sprintf("    mtu: 1280\n"))
		}
	}
	if p := g.Proxies["masque"]; p.Enabled {
		ep := firstNonEmpty(p.Endpoint, e.masque["masque"])
		if ep != "" {
			masq("MASQUE", ep, "")
		}
	}
	if p := g.Proxies["masqueh2"]; p.Enabled {
		ep := firstNonEmpty(p.Endpoint, e.masque["masqueh2"])
		if ep != "" {
			masq("MASQUEH2", ep, "h2")
		}
	}
	if p := g.Proxies["m2w"]; p.Enabled {
		mep := firstNonEmpty(p.Outer, e.masque["masque"], e.warp)
		wep := firstNonEmpty(p.Inner, e.warp)
		if mep != "" && wep != "" && mep != wep {
			masq("M2W OUTER", mep, "")
			awg("M2W INNER", wep, "    mtu: 1250\n    dialer-proxy: \"M2W OUTER\"\n")
		}
	}

	b.WriteString("listeners:\n")
	listener := func(port int, name string) {
		if name != "" {
			fmt.Fprintf(&b, "  - name: l%d\n    type: socks\n    port: %d\n    proxy: %q\n", port, port, name)
		}
	}
	if p := g.Proxies["warp"]; p.Enabled && (p.Endpoint != "" || e.warp != "") {
		listener(p.Port, "WARP")
	}
	if p := g.Proxies["masque"]; p.Enabled && (p.Endpoint != "" || e.masque["masque"] != "") {
		listener(p.Port, "MASQUE")
	}
	if p := g.Proxies["masqueh2"]; p.Enabled && (p.Endpoint != "" || e.masque["masqueh2"] != "") {
		listener(p.Port, "MASQUEH2")
	}
	if p := g.Proxies["m2w"]; p.Enabled {
		listener(p.Port, "M2W INNER")
	}
	return b.String()
}

// Ensure runs mihomo with the current config, reloading when it changes.
func (m *MihomoMan) Ensure(g Settings, e endpoints, acc warpAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	conf := mihomoConfText(g, e, acc)
	if m.running && conf == m.lastConf {
		return nil
	}
	if err := os.WriteFile(mihomoConf, []byte(conf), 0600); err != nil {
		return err
	}
	m.lastConf = conf
	if m.running {
		// try hot reload via external-controller
		if err := mihomoReload(); err == nil {
			logg.Printf("mihomo reloaded (pid %d)", m.pid)
			return nil
		} else {
			logg.Printf("mihomo reload failed (%v), restarting process", err)
		}
		m.stopLocked()
	}
	cmd := exec.Command(mihomoBin, "-d", mihomoDir, "-f", mihomoConf)
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=40MiB")
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	m.pid = cmd.Process.Pid
	m.running = true
	m.started = time.Now()
	go func() { _ = cmd.Wait() }()
	logg.Printf("mihomo started (pid %d)", m.pid)
	return nil
}

func (m *MihomoMan) stopLocked() {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(1500 * time.Millisecond)
		_ = m.cmd.Process.Kill()
	}
	m.running = false
	m.pid = 0
}

func (m *MihomoMan) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *MihomoMan) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func host(ep string) string {
	if i := strings.LastIndex(ep, ":"); i > 0 {
		return ep[:i]
	}
	return ep
}
func port(ep string) string {
	if i := strings.LastIndex(ep, ":"); i > 0 {
		return ep[i+1:]
	}
	return "2408"
}
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
