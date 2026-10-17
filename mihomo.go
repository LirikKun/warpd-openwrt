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
	mu          sync.Mutex
	cmd         *exec.Cmd
	done        chan struct{}
	pid         int
	running     bool
	lastConf    string
	desiredConf string      // latest Ensure intent, including credentials; never exposed
	lastAccount warpAccount // value copy, advanced only with lastConf
	started     time.Time
	generation  uint64

	// Optional per-instance dependencies; the zero value uses production paths.
	// Configure these before concurrent use.
	configPath string
	command    func(string) *exec.Cmd
	reload     func() error
	procRoot   string
	// Explicit test seam. Injecting command/reload alone NEVER bypasses ownership.
	ownership        func(int, []mihomoListener) error
	ownershipTimeout time.Duration
}

// MihomoStatus is one consistent view of the child and its applied config.
// Generation advances only on a successful start or changed-config reload;
// it survives exits/stops, so checks from a previous apply can be discarded.
type MihomoStatus struct {
	PID        int
	Running    bool
	Generation uint64
}

func (m *MihomoMan) Snapshot() MihomoStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshExitedLocked()
	return MihomoStatus{PID: m.pid, Running: m.running, Generation: m.generation}
}

// AppliedSnapshot compares the effective desired configuration to the last
// successful apply, under the same lock as its process/generation. Credentials
// are those bound to that apply; neither they nor the rendered YAML escape.
func (m *MihomoMan) AppliedSnapshot(g Settings, e endpoints) (MihomoStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshExitedLocked()
	status := MihomoStatus{PID: m.pid, Running: m.running, Generation: m.generation}
	applied := m.running && m.lastConf != "" && m.desiredConf == m.lastConf && mihomoConfText(g, e, m.lastAccount) == m.lastConf
	if applied {
		listeners, err := mihomoListeners(m.lastConf)
		applied = err == nil && m.checkOwnership(m.pid, listeners) == nil
	}
	return status, applied
}

// Reconcile the short interval between reaping and the waiter's mutex lock.
func (m *MihomoMan) refreshExitedLocked() {
	select {
	case <-m.done:
		m.cmd = nil
		m.done = nil
		m.pid = 0
		m.running = false
		m.started = time.Time{}
	default:
	}
}

var mihomo = &MihomoMan{}

// mihomoConfText renders the full proxy set from live settings + endpoints.
// Port map (fixed, documented in web UI):
//
//	4001 warp, 4003 masque, 4004 masqueh2,
//	4005 masque->warp (m2w)
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
	m2wReady := false
	if p := g.Proxies["m2w"]; p.Enabled {
		mep := firstNonEmpty(p.Outer, e.masque["masque"], e.warp)
		wep := firstNonEmpty(p.Inner, e.warp)
		if mep != "" && wep != "" && mep != wep {
			masq("M2W OUTER", mep, "")
			awg("M2W INNER", wep, "    mtu: 1250\n    dialer-proxy: \"M2W OUTER\"\n")
			m2wReady = true
		}
	}

	b.WriteString("listeners:\n")
	listener := func(p *ProxySettings, name string) {
		if name != "" {
			fmt.Fprintf(&b, "  - name: l%d\n    type: socks\n    listen: %q\n    port: %d\n    proxy: %q\n", p.Port, p.Listen, p.Port, name)
		}
	}
	if p := g.Proxies["warp"]; p.Enabled && (p.Endpoint != "" || e.warp != "") {
		listener(p, "WARP")
	}
	if p := g.Proxies["masque"]; p.Enabled && (p.Endpoint != "" || e.masque["masque"] != "") {
		listener(p, "MASQUE")
	}
	if p := g.Proxies["masqueh2"]; p.Enabled && (p.Endpoint != "" || e.masque["masqueh2"] != "") {
		listener(p, "MASQUEH2")
	}
	if p := g.Proxies["m2w"]; m2wReady {
		listener(p, "M2W INNER")
	}
	return b.String()
}

// Ensure runs mihomo with the current config, reloading when it changes.
func (m *MihomoMan) Ensure(g Settings, e endpoints, acc warpAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshExitedLocked()
	conf := mihomoConfText(g, e, acc)
	m.desiredConf = conf
	if m.running {
		listeners, err := mihomoListeners(m.lastConf)
		if err == nil {
			err = m.checkOwnership(m.pid, listeners)
		}
		if err != nil {
			m.stopLocked() // only our exact child; never the foreign socket owner
			return fmt.Errorf("mihomo managed listener ownership lost: %w", err)
		}
	}
	if m.running && conf == m.lastConf {
		return nil
	}
	configPath := firstNonEmpty(m.configPath, mihomoConf)
	listeners, err := mihomoListeners(conf)
	if err != nil {
		return err
	}
	if err := m.checkCollisions(m.pid, listeners); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, []byte(conf), 0600); err != nil {
		return err
	}
	if m.running {
		// try hot reload via external-controller
		// Recheck at the request boundary, after the config write/preflight.
		if err := m.checkOwnership(m.pid, listeners[:1]); err != nil {
			m.stopLocked()
			return fmt.Errorf("mihomo controller ownership lost before reload: %w", err)
		}
		reload := m.reload
		if reload == nil {
			reload = mihomoReload
		}
		if err := reload(); err == nil {
			if err := m.waitOwnershipLocked(listeners); err != nil {
				m.stopLocked()
				return fmt.Errorf("mihomo reload ownership check failed: %w", err)
			}
			m.lastConf = conf
			m.lastAccount = acc
			m.generation++
			logg.Printf("mihomo reloaded (pid %d)", m.pid)
			return nil
		} else {
			logg.Printf("mihomo reload failed (%v), restarting process", err)
		}
		m.stopLocked()
	}
	if err := m.checkCollisions(0, listeners); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if m.command != nil {
		cmd = m.command(configPath)
	} else {
		cmd = exec.Command(mihomoBin, "-d", mihomoDir, "-f", configPath)
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "GOMEMLIMIT=40MiB")
	protectMihomoChild(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	m.pid = cmd.Process.Pid
	m.started = time.Now()
	done := make(chan struct{})
	m.done = done
	go func() {
		_ = cmd.Wait()
		// Stop holds mu while waiting. Publish reaping before taking mu so
		// it can finish and a late waiter cannot erase a replacement child.
		close(done)
		m.mu.Lock()
		defer m.mu.Unlock()
		// A previous child may finish after a replacement was installed.
		if m.cmd == cmd {
			m.cmd = nil
			m.done = nil
			m.pid = 0
			m.running = false
			m.started = time.Time{}
		}
	}()
	err = m.waitOwnershipLocked(listeners)
	if err != nil {
		m.stopLocked()
		return fmt.Errorf("mihomo startup ownership check failed: %w", err)
	}
	m.running = true
	m.lastConf = conf
	m.lastAccount = acc
	m.generation++
	logg.Printf("mihomo started (pid %d)", m.pid)
	return nil
}

func (m *MihomoMan) waitOwnershipLocked(listeners []mihomoListener) error {
	timeout := m.ownershipTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		err := m.checkOwnership(m.pid, listeners)
		if err == nil {
			select {
			case <-m.done:
				return fmt.Errorf("mihomo exited before listener ownership was established")
			default:
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-m.done:
			return fmt.Errorf("mihomo exited before listener ownership was established")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (m *MihomoMan) stopLocked() {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(1500 * time.Millisecond)
		select {
		case <-m.done:
			timer.Stop()
		case <-timer.C:
			_ = m.cmd.Process.Kill()
			// Even reaping has a bound; status and subsequent Ensure must
			// not get stuck behind a child which cannot finish exiting.
			timer.Reset(1500 * time.Millisecond)
			select {
			case <-m.done:
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	m.cmd = nil
	m.done = nil
	m.running = false
	m.pid = 0
	m.started = time.Time{}
}

func (m *MihomoMan) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *MihomoMan) Running() bool {
	return m.Snapshot().Running
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
