package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	gateRevoked chan struct{} // closed before changing/revoking an applied generation

	// Optional per-instance dependencies; the zero value uses production paths.
	// Configure these before concurrent use.
	configPath string
	command    func(string) *exec.Cmd
	reload     func() error
	procRoot   string
	// Explicit test seam. Injecting command/reload alone NEVER bypasses ownership.
	ownership        func(int, []mihomoListener) error
	ownershipTimeout time.Duration
	// Optional constructor dependency; production always uses NewRelayUDPBridge.
	// It cannot replace mandatory kernel ownership authorization.
	relayPrepare func(context.Context, RelayUDPConfig) (*RelayUDPBridge, error)
	relayLeases  map[string]*relayEngineLease
	relayChild   atomic.Pointer[relayChildIdentity]
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

// A gate lease identifies the exact child and applied generation, not merely
// a port or a recent health result. Never call gate code while holding m.mu.
type profileGateOwner struct {
	manager      *MihomoMan
	cmd          *exec.Cmd
	done         <-chan struct{}
	revoked      <-chan struct{}
	generation   uint64
	listener     mihomoListener
	relay        *relayEngineLease
	relayDone    <-chan struct{}
	relayExpires time.Time
}

func (m *MihomoMan) revokeProfileGatesLocked() {
	m.relayChild.Store(nil)
	m.revokeRelaysLocked(true)
	if m.gateRevoked != nil {
		close(m.gateRevoked)
		m.gateRevoked = nil
	}
}

func (m *MihomoMan) profileGateOwner(status MihomoStatus, target string, ids ...string) *profileGateOwner {
	address, portText, err := net.SplitHostPort(target)
	port, portErr := strconv.Atoi(portText)
	ip := net.ParseIP(address)
	if err != nil || portErr != nil || port < 1 || port > 65535 || ip == nil || !ip.IsLoopback() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshExitedLocked()
	if !m.running || m.pid != status.PID || m.generation != status.Generation || m.lastConf == "" || m.desiredConf != m.lastConf {
		return nil
	}
	listeners, err := mihomoListeners(m.lastConf)
	if err != nil {
		return nil
	}
	want := mihomoListener{address, port}
	found := false
	for _, listener := range listeners[1:] {
		found = found || listener == want
	}
	if err != nil || !found || m.checkOwnership(m.pid, []mihomoListener{want}) != nil {
		return nil
	}
	if m.gateRevoked == nil {
		m.gateRevoked = make(chan struct{})
	}
	owner := &profileGateOwner{manager: m, cmd: m.cmd, done: m.done, revoked: m.gateRevoked, generation: m.generation, listener: want}
	if len(ids) > 0 {
		if lease := m.relayLeases[ids[0]]; lease != nil {
			if lease.bridge == nil || !lease.live.Load() || !time.Now().Before(lease.bridge.expires) {
				return nil
			}
			owner.relay = lease
			owner.relayDone = lease.bridge.Done()
			owner.relayExpires = lease.bridge.expires
		}
	}
	return owner
}

// Check at the data-plane boundary, both before dialing and before writing any
// SOCKS bytes. /proc listener inodes must still belong to this exact child.
func (owner *profileGateOwner) valid() bool {
	m := owner.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshExitedLocked()
	select {
	case <-owner.revoked:
		return false
	default:
	}
	select {
	case <-owner.done:
		return false
	default:
	}
	select {
	case <-owner.relayDone:
		return false
	default:
	}
	if owner.relay != nil && (!owner.relay.live.Load() || !time.Now().Before(owner.relayExpires)) {
		return false
	}
	return m.running && m.cmd == owner.cmd && m.done == owner.done && m.generation == owner.generation && m.lastConf != "" && m.desiredConf == m.lastConf && m.checkOwnership(m.pid, []mihomoListener{owner.listener}) == nil
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

	awg := func(name, endpoint string, p *ProxySettings, credential *RelayCredential, extra ...string) {
		acc := acc
		if credential != nil {
			acc = warpAccount{PrivateKey: credential.PrivateKey, PeerPub: credential.PeerPublicKey, IPv4: strings.TrimSuffix(credential.TunnelIPv4, "/32"), IPv6: strings.TrimSuffix(credential.TunnelIPv6, "/128")}
		}
		fmt.Fprintf(&b, "  - name: %q\n    type: wireguard\n", name)
		fmt.Fprintf(&b, "    private-key: %s\n", acc.PrivateKey)
		fmt.Fprintf(&b, "    ip: %s\n", acc.IPv4)
		fmt.Fprintf(&b, "    peers:\n      - server: %s\n        port: %s\n", host(endpoint), port(endpoint))
		fmt.Fprintf(&b, "        public-key: %s\n", acc.PeerPub)
		b.WriteString("        allowed-ips: ['0.0.0.0/0']\n        persistent-keepalive: 25\n")
		a := p.AWG
		if a.Preset == "" {
			a = DefaultAWGOptions()
			a.I1 = firstNonEmpty(p.GenI1, acc.GenI1, a.I1)
		}
		fmt.Fprintf(&b, "    amnezia-wg-option:\n      jc: %d\n      jmin: %d\n      jmax: %d\n      s1: %d\n      s2: %d\n      h1: %d\n      h2: %d\n      h3: %d\n      h4: %d\n      i1: %q\n", a.JC, a.JMin, a.JMax, a.S1, a.S2, a.H1, a.H2, a.H3, a.H4, a.I1)
		if credential == nil {
			b.WriteString("    udp: true\n    remote-dns-resolve: true\n    dns: [\"1.1.1.1\", \"1.0.0.1\"]\n")
		} else {
			b.WriteString("    udp: true\n    remote-dns-resolve: false\n")
			if acc.IPv6 != "" {
				fmt.Fprintf(&b, "    ipv6: %s\n", acc.IPv6)
			}
			if credential.DNS != "" {
				fmt.Fprintf(&b, "    dns: [%s]\n", credential.DNS)
			}
			if credential.Reserved != "" {
				reserved, _ := base64.StdEncoding.DecodeString(credential.Reserved)
				fmt.Fprintf(&b, "    reserved: [%d, %d, %d]\n", reserved[0], reserved[1], reserved[2])
			}
		}
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

	served := map[string]string{}
	for _, id := range profileIDs(g) {
		p := g.Proxies[id]
		if !effectiveEnabled(g, p) {
			continue
		}
		ep, outer := profileLiveEndpoint(id, p, e)
		if ep == "" {
			continue
		}
		name := profileEngineName(id, p)
		switch profileType(id, p) {
		case "warp":
			awg(name, ep, p, nil, "    mtu: 1280\n")
		case "relay-warp":
			addr, err := netip.ParseAddrPort(ep)
			if err != nil || !addr.Addr().Is4() || !addr.Addr().IsLoopback() || addr.Port() == 0 || !relayCredentialConfigured(p) {
				continue
			}
			mtu := p.Credential.MTU
			if mtu == 0 {
				mtu = 1280
			}
			awg(name, ep, p, &p.Credential, fmt.Sprintf("    mtu: %d\n", mtu))
		case "masque":
			network := ""
			if profileProtocol(id, p) == "masque-h2" {
				network = "h2"
			}
			masq(name, ep, network)
		case "legacy-m2w":
			if outer == "" || outer == ep {
				continue
			}
			masq(name+" OUTER", outer, "")
			awg(name+" INNER", ep, p, nil, fmt.Sprintf("    mtu: 1250\n    dialer-proxy: %q\n", name+" OUTER"))
			name += " INNER"
		default:
			continue // Relay tunnel compatibility is unverified; never substitute direct WARP.
		}
		served[id] = name
	}
	b.WriteString("listeners:\n")
	for _, id := range profileIDs(g) {
		name := served[id]
		if name == "" {
			continue
		}
		p := g.Proxies[id]
		listen, port := profileProbeAddress(g, id, p)
		fmt.Fprintf(&b, "  - name: l%d\n    type: socks\n    listen: %q\n    port: %d\n    proxy: %q\n", port, listen, port, name)
	}
	return b.String()
}

// Ensure runs mihomo with the current config, reloading when it changes.
func (m *MihomoMan) Ensure(g Settings, e endpoints, acc warpAccount) (result error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshExitedLocked()
	e, relayPending := m.prepareRelaysLocked(g, e, acc)
	committed := false
	defer func() {
		if !committed {
			m.revokeRelaysLocked(false)
			for id, p := range g.Proxies {
				if effectiveEnabled(g, p) && profileType(id, p) == "relay-warp" {
					getCheck(id).set(func(st *ProxyState) { st.Error = "pending: Relay runtime apply failed" })
				}
			}
		}
	}()
	ordinary, prepared := false, false
	for id, p := range g.Proxies {
		if effectiveEnabled(g, p) {
			if profileType(id, p) != "relay-warp" {
				ordinary = true
			} else if lease := m.relayLeases[id]; lease != nil && lease.bridge != nil {
				prepared = true
			}
		}
	}
	if !ordinary && !prepared && relayPending != nil {
		m.stopLocked(true)
		committed = true
		return relayPending
	}
	conf := mihomoConfText(g, e, acc)
	if conf != m.desiredConf {
		m.revokeProfileGatesLocked()
	}
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
		committed = true
		return m.finishRelayApplyLocked(g, e, relayPending)
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
			if err := m.verifyRemovedListenersLocked(conf); err != nil {
				m.stopLocked()
				return err
			}
			m.lastConf = conf
			m.lastAccount = acc
			m.generation++
			logg.Printf("mihomo reloaded (pid %d)", m.pid)
			committed = true
			return m.finishRelayApplyLocked(g, e, relayPending)
		} else {
			logg.Printf("mihomo reload failed (%v), restarting process", err)
		}
		m.stopLocked(true)
	}
	if err := m.checkCollisions(0, listeners); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if m.command != nil {
		cmd = m.command(configPath)
	} else {
		cmd = exec.Command(mihomoBinary(), "-d", mihomoDir, "-f", configPath)
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
	committed = true
	return m.finishRelayApplyLocked(g, e, relayPending)
}

// Reload success must also prove that removed listeners no longer belong to
// our exact child. Never signal an unrelated process that reused a freed port.
func (m *MihomoMan) verifyRemovedListenersLocked(conf string) error {
	if m.ownership != nil {
		return nil
	} // explicit synthetic ownership fixture
	old, _ := mihomoListeners(m.lastConf)
	current, _ := mihomoListeners(conf)
	// A retained port is not a retained binding: narrowing or moving its
	// address must close any owned socket outside the new address coverage.
	covered := func(binding mihomoListener) bool {
		address := net.ParseIP(binding.address)
		for _, listener := range current {
			want := net.ParseIP(listener.address)
			if listener.port != binding.port || address == nil || want == nil {
				continue
			}
			if address.Equal(want) || want.IsUnspecified() && (want.To4() != nil) == (address.To4() != nil) {
				return true
			}
		}
		// /proc cannot prove IPv6 wildcard dual-stack coverage. In particular,
		// changing address families must not certify a retained obsolete fd.
		return false
	}
	removed := map[int]bool{}
	for _, listener := range old {
		if !covered(listener) {
			removed[listener.port] = true
		}
	}
	if len(removed) == 0 {
		return nil
	}
	root := firstNonEmpty(m.procRoot, "/proc")
	timeout := m.ownershipTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		sockets, err := readMihomoSockets(root)
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(root, strconv.Itoa(m.pid), "fd"))
		if err != nil {
			return fmt.Errorf("cannot verify removed mihomo listeners")
		}
		owned := map[string]bool{}
		for _, entry := range entries {
			link, err := os.Readlink(filepath.Join(root, strconv.Itoa(m.pid), "fd", entry.Name()))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("cannot verify removed mihomo listeners")
			}
			owned[link] = true
		}
		retained := false
		for _, socket := range sockets {
			if removed[socket.port] && owned["socket:["+socket.inode+"]"] && !covered(socket.mihomoListener) {
				retained = true
			}
		}
		if !retained {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("mihomo reload retained a disabled listener")
		}
		select {
		case <-m.done:
			return fmt.Errorf("mihomo exited during removal verification")
		case <-time.After(25 * time.Millisecond):
		}
	}
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

func (m *MihomoMan) stopLocked(preservePrepared ...bool) {
	m.revokeProfileGatesLocked()
	if len(preservePrepared) == 0 || !preservePrepared[0] {
		m.revokeRelaysLocked(false)
	}
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
