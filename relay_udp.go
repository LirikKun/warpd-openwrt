//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var errRelayUDPOwner = errors.New("Relay UDP child socket ownership unavailable")

// relayUDPProcess pins PID identity through Linux starttime, namespace and pidfd.
// No configurable proc root or callback can replace the real kernel checks.
func relayUDPProcess(pid int) (string, error) {
	if pid <= 0 || pid == os.Getpid() {
		return "", errRelayUDPOwner
	}
	p := strconv.Itoa(pid)
	ours, e := os.Readlink("/proc/self/ns/net")
	if e != nil {
		return "", errRelayUDPOwner
	}
	theirs, e := os.Readlink("/proc/" + p + "/ns/net")
	if e != nil || ours != theirs {
		return "", errRelayUDPOwner
	}
	b, e := os.ReadFile("/proc/" + p + "/stat")
	if e != nil {
		return "", errRelayUDPOwner
	}
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return "", errRelayUDPOwner
	}
	f := strings.Fields(string(b)[i+2:])
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return "", errRelayUDPOwner
	}
	return f[19], nil
}

func relayUDPProcAddr(s string) (netip.AddrPort, error) {
	f := strings.Split(s, ":")
	if len(f) != 2 {
		return netip.AddrPort{}, errRelayUDPOwner
	}
	b, e := hex.DecodeString(f[0])
	if e != nil || (len(b) != 4 && len(b) != 16) {
		return netip.AddrPort{}, errRelayUDPOwner
	}
	for i := 0; i < len(b); i += 4 {
		binary.BigEndian.PutUint32(b[i:i+4], binary.NativeEndian.Uint32(b[i:i+4]))
	}
	a, ok := netip.AddrFromSlice(b)
	if !ok {
		return netip.AddrPort{}, errRelayUDPOwner
	}
	p, e := strconv.ParseUint(f[1], 16, 16)
	if e != nil {
		return netip.AddrPort{}, errRelayUDPOwner
	}
	return netip.AddrPortFrom(a, uint16(p)), nil
}

// Any potentially overlapping bind (including :: without provable V6ONLY)
// invalidates proof. A wildcard *single* socket is valid if child-owned and
// non-reusable; an additional wildcard or exact socket is never ignored.
func relayUDPInode(peer, ingress netip.AddrPort) (uint64, error) {
	if !peer.IsValid() || !peer.Addr().Is4() || !peer.Addr().IsLoopback() || peer.Port() == 0 || !ingress.Addr().Is4() {
		return 0, errRelayUDPOwner
	}
	var found uint64
	for _, table := range []string{"udp", "udp6"} {
		f, e := os.Open("/proc/net/" + table)
		if e != nil {
			return 0, errRelayUDPOwner
		}
		scan := bufio.NewScanner(f)
		if !scan.Scan() || !strings.Contains(scan.Text(), "local_address") {
			f.Close()
			return 0, errRelayUDPOwner
		}
		for scan.Scan() {
			v := strings.Fields(scan.Text())
			if len(v) < 10 {
				f.Close()
				return 0, errRelayUDPOwner
			}
			local, e := relayUDPProcAddr(v[1])
			if e != nil {
				f.Close()
				return 0, e
			}
			if local.Port() != peer.Port() {
				continue
			}
			ip := local.Addr()
			overlap := ip.IsUnspecified() || ip.Unmap() == peer.Addr()
			if !overlap {
				continue
			}
			remote, e := relayUDPProcAddr(v[2])
			if e != nil {
				f.Close()
				return 0, e
			}
			// Even a different connected remote may represent ambiguous same-port
			// ownership. Do not count an incomplete table row as accepted proof.
			if table != "udp" || found != 0 || (!remote.Addr().IsUnspecified() && remote != ingress) {
				f.Close()
				return 0, errRelayUDPOwner
			}
			found, e = strconv.ParseUint(v[9], 10, 64)
			if e != nil || found == 0 {
				f.Close()
				return 0, errRelayUDPOwner
			}
		}
		e = scan.Err()
		f.Close()
		if e != nil {
			return 0, errRelayUDPOwner
		}
	}
	if found == 0 {
		return 0, errRelayUDPOwner
	}
	return found, nil
}

func relayUDPChildFD(pid int, inode uint64) (int, error) {
	path := "/proc/" + strconv.Itoa(pid) + "/fd"
	directory, e := os.Open(path)
	if e != nil {
		return 0, errRelayUDPOwner
	}
	defer directory.Close()
	entries, e := directory.ReadDir(4097)
	if (e != nil && e != io.EOF) || len(entries) > 4096 {
		return 0, errRelayUDPOwner
	}
	want := fmt.Sprintf("socket:[%d]", inode)
	for _, entry := range entries {
		link, e := os.Readlink(filepath.Join(path, entry.Name()))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return 0, errRelayUDPOwner
		}
		if link == want {
			fd, e := strconv.Atoi(entry.Name())
			if e == nil {
				return fd, nil
			}
		}
	}
	return 0, errRelayUDPOwner
}

// Duplicate and retain the precise child socket rather than trusting a sampled
// port. The lease prevents unprivileged port reuse between verification/send.
// pidfd_getfd needs kernel support + ptrace permission; unsupported is CLOSED.
func acquireRelayUDPSocket(pid int, peer, ingress netip.AddrPort) (*os.File, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64", "riscv64":
	default:
		return nil, errRelayUDPOwner
	}
	identity, e := relayUDPProcess(pid)
	if e != nil {
		return nil, e
	}
	inode, e := relayUDPInode(peer, ingress)
	if e != nil {
		return nil, e
	}
	fd, e := relayUDPChildFD(pid, inode)
	if e != nil {
		return nil, e
	}
	p, _, eno := syscall.Syscall(434, uintptr(pid), 0, 0)
	if eno != 0 {
		return nil, errRelayUDPOwner
	}
	defer syscall.Close(int(p))
	dup, _, eno := syscall.Syscall(438, p, uintptr(fd), 0)
	if eno != 0 {
		return nil, errRelayUDPOwner
	}
	syscall.CloseOnExec(int(dup))
	lease := os.NewFile(dup, "relay-child-udp-lease")
	fail := func() (*os.File, error) { lease.Close(); return nil, errRelayUDPOwner }
	var stat syscall.Stat_t
	if syscall.Fstat(int(dup), &stat) != nil || stat.Ino != inode || stat.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		return fail()
	}
	for _, opt := range []int{syscall.SO_REUSEADDR, 15 /* Linux SO_REUSEPORT */} {
		v, e := syscall.GetsockoptInt(int(dup), syscall.SOL_SOCKET, opt)
		if e != nil || v != 0 {
			return fail()
		}
	}
	ty, e := syscall.GetsockoptInt(int(dup), syscall.SOL_SOCKET, syscall.SO_TYPE)
	if e != nil || ty != syscall.SOCK_DGRAM {
		return fail()
	}
	now, e := relayUDPProcess(pid)
	if e != nil || now != identity {
		return fail()
	}
	current, e := relayUDPInode(peer, ingress)
	if e != nil || current != inode {
		return fail()
	}
	if _, e = relayUDPChildFD(pid, inode); e != nil {
		return fail()
	}
	return lease, nil
}

// RelayUDPPath carries only binding policy, never an activation credential.
type RelayUDPPath struct {
	SourceIP      string
	Interface     string
	PathConfirmed bool
}

// RelayUDPConfig is immutable input scoped to one profile revision.
type RelayUDPConfig struct {
	ProfileID string
	Revision  uint64
	Country   string
	Access    RelayUDPPath
	Freshness time.Duration
}

// Current must nonblockingly validate the captured profile/revision/exact command,
// PID and generation using caller-synchronized state. Never call bridge methods
// or acquire a lock held while calling Close. This is additional to mandatory
// production /proc proof; it cannot authorize a socket on its own.
type RelayUDPChild struct {
	PID        int
	Generation uint64
	Exited     <-chan struct{}
	Current    func() bool
}
type RelayUDPBridge struct {
	mu                sync.Mutex
	ingress, outbound *net.UDPConn
	lease             *os.File
	cfg               RelayUDPConfig
	owner             *RelayUDPChild
	identity          string
	inode             uint64
	peer              netip.AddrPort
	ctx               context.Context
	cancel            context.CancelFunc
	expires           time.Time
	closed            bool
	fixture           bool
	ifindex           int
	done              chan struct{}
	wg                sync.WaitGroup
}

const relayUDPMaxDatagram = 65507 // largest non-jumbogram IPv4 UDP payload; never truncate
const relayUDPMaxFreshness = 5 * time.Minute

// NewRelayUDPBridge prepares a paused per-profile bridge. It resolves only an
// allowlisted country hostname, app-side, then connects a literal UDP4500 peer.
// Authorize must subsequently establish a kernel lease before any data can flow.
func NewRelayUDPBridge(ctx context.Context, cfg RelayUDPConfig) (*RelayUDPBridge, error) {
	return newRelayUDPBridge(ctx, cfg, netip.AddrPort{})
}
func newRelayUDPBridge(ctx context.Context, cfg RelayUDPConfig, fixture netip.AddrPort) (*RelayUDPBridge, error) {
	if ctx == nil || ctx.Err() != nil || cfg.ProfileID == "" || len(cfg.ProfileID) > 128 || cfg.Revision == 0 || cfg.Freshness <= 0 || cfg.Freshness > relayUDPMaxFreshness {
		return nil, errors.New("invalid Relay UDP lifetime or identity")
	}
	host := ""
	switch cfg.Country {
	case "DE", "NL", "FI", "LV", "EE":
		host = strings.ToLower(cfg.Country) + ".tribukvy.ltd"
	default:
		return nil, errors.New("invalid Relay UDP country")
	}
	validation := RelayAccess{SourceIP: cfg.Access.SourceIP, Interface: cfg.Access.Interface, PathConfirmed: cfg.Access.PathConfirmed}
	if fixture.IsValid() {
		ip, e := netip.ParseAddr(validation.SourceIP)
		if e != nil || !ip.IsLoopback() || ip.Is4() != fixture.Addr().Is4() {
			return nil, errors.New("invalid Relay UDP fixture path")
		}
		if ip.Is4() {
			validation.SourceIP = "192.0.2.2"
		} else {
			validation.SourceIP = "2001:db8::2"
		}
	}
	if e := validateRelaySource(validation); e != nil {
		return nil, e
	}
	family, _, dial := relayBoundDialers(cfg.Access)
	setup, setupCancel := context.WithTimeout(ctx, 5*time.Second)
	defer setupCancel()
	endpoint := fixture
	if !endpoint.IsValid() {
		ips, e := dial.Resolver.LookupNetIP(setup, "ip"+family, host)
		if e != nil {
			return nil, errors.New("Relay UDP bound DNS unavailable")
		}
		// Deterministic choice, no alternate-address or alternate-path retry.
		var chosen netip.Addr
		for _, ip := range ips {
			ip = ip.Unmap()
			if ip.Is4() != (family == "4") || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				continue
			}
			if !chosen.IsValid() || ip.Less(chosen) {
				chosen = ip
			}
		}
		if !chosen.IsValid() {
			return nil, errors.New("Relay UDP endpoint family unavailable")
		}
		endpoint = netip.AddrPortFrom(chosen, 4500)
	}
	if endpoint.Addr().Is4() != (family == "4") {
		return nil, errors.New("Relay UDP endpoint family mismatch")
	}
	out, e := dial.DialContext(setup, "udp"+family, endpoint.String())
	if e != nil {
		return nil, errors.New("Relay UDP bound socket unavailable")
	}
	outbound, ok := out.(*net.UDPConn)
	if !ok {
		out.Close()
		return nil, errors.New("invalid Relay UDP socket")
	}
	ingress, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		out.Close()
		return nil, errors.New("Relay UDP ingress unavailable")
	}
	lifetime, cancel := context.WithCancel(ctx)
	b := &RelayUDPBridge{ingress: ingress, outbound: outbound, cfg: cfg, ctx: lifetime, cancel: cancel, expires: time.Now().Add(cfg.Freshness), done: make(chan struct{}), fixture: fixture.IsValid()}
	if cfg.Access.Interface != "" {
		iface, e := net.InterfaceByName(cfg.Access.Interface)
		if e != nil {
			cancel()
			ingress.Close()
			out.Close()
			return nil, errors.New("Relay UDP interface unavailable")
		}
		b.ifindex = iface.Index
	}
	if !b.pathValid() {
		cancel()
		ingress.Close()
		out.Close()
		return nil, errors.New("Relay UDP source address unavailable")
	}
	b.wg.Add(1)
	go b.watch()
	// A separate join closer is intentionally bounded (one per bridge).
	go func() { b.wg.Wait(); close(b.done) }()
	return b, nil
}
func (b *RelayUDPBridge) Target() netip.AddrPort {
	return b.ingress.LocalAddr().(*net.UDPAddr).AddrPort()
}
func (b *RelayUDPBridge) Done() <-chan struct{} { return b.done }
func (b *RelayUDPBridge) stopLocked() {
	if !b.closed {
		b.closed = true
		b.cancel()
		b.ingress.Close()
		b.outbound.Close()
		if b.lease != nil {
			b.lease.Close()
			b.lease = nil
		}
	}
}
func (b *RelayUDPBridge) Close() { b.mu.Lock(); b.stopLocked(); b.mu.Unlock(); <-b.done }

func (b *RelayUDPBridge) pathValid() bool {
	local := b.outbound.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	if a := b.cfg.Access.SourceIP; a != "" {
		ip, e := netip.ParseAddr(a)
		if e != nil || ip.Unmap() != local {
			return false
		}
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		return false
	}
	for _, iface := range interfaces {
		if b.ifindex != 0 && (iface.Index != b.ifindex || iface.Name != b.cfg.Access.Interface) {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, e := iface.Addrs()
		if e != nil {
			return false
		}
		for _, addr := range addrs {
			prefix, e := netip.ParsePrefix(addr.String())
			if e == nil && (prefix.Addr().Unmap() == local || (b.fixture && local.IsLoopback() && prefix.Contains(local))) {
				return true
			}
		}
	}
	return false
}
func (b *RelayUDPBridge) validLocked() bool {
	if b.closed || b.ctx.Err() != nil || !time.Now().Before(b.expires) || !b.pathValid() {
		return false
	}
	if b.owner == nil {
		return true
	}
	o := b.owner
	select {
	case <-o.Exited:
		return false
	default:
	}
	if !o.Current() || b.lease == nil {
		return false
	}
	for _, opt := range []int{syscall.SO_REUSEADDR, 15 /* Linux SO_REUSEPORT */} {
		v, e := syscall.GetsockoptInt(int(b.lease.Fd()), syscall.SOL_SOCKET, opt)
		if e != nil || v != 0 {
			return false
		}
	}
	identity, e := relayUDPProcess(o.PID)
	if e != nil || identity != b.identity {
		return false
	}
	inode, e := relayUDPInode(b.peer, b.Target())
	if e != nil || inode != b.inode {
		return false
	}
	_, e = relayUDPChildFD(o.PID, b.inode)
	return e == nil
}
func (b *RelayUDPBridge) watch() {
	defer b.wg.Done()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			b.mu.Lock()
			b.stopLocked()
			b.mu.Unlock()
			return
		case <-ticker.C:
			b.mu.Lock()
			valid := b.validLocked()
			if !valid {
				b.stopLocked()
			}
			b.mu.Unlock()
			if !valid {
				return
			}
		}
	}
}

// Authorize discovers one eligible exact-child UDP4 socket without sending any
// datagram. More than one eligible socket is ambiguous and cannot be selected.
// Child lazy initialization is allowed for up to 5s. Initial queued datagrams
// are discarded after leasing/connect: UDP has no per-packet PID credentials,
// so certifying a stale pre-lease packet by its reused tuple would be unsafe.
func (b *RelayUDPBridge) Authorize(ctx context.Context, owner RelayUDPChild) (result error) {
	defer func() {
		if result != nil {
			b.Close()
		}
	}()
	if ctx == nil || ctx.Err() != nil || owner.PID <= 0 || owner.Generation == 0 || owner.Exited == nil || owner.Current == nil {
		return errRelayUDPOwner
	}
	identity, e := relayUDPProcess(owner.PID)
	if e != nil {
		return e
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.mu.Lock()
		if ctx.Err() != nil || b.owner != nil || !b.validLocked() {
			b.mu.Unlock()
			return errRelayUDPOwner
		}
		select {
		case <-owner.Exited:
			b.mu.Unlock()
			return errRelayUDPOwner
		default:
		}
		if !owner.Current() {
			b.mu.Unlock()
			return errRelayUDPOwner
		}
		peer, lease, e := discoverRelayUDPSocket(owner.PID, b.Target())
		if e == nil {
			raw, err := b.ingress.SyscallConn()
			var operationErr error
			if err == nil {
				err = raw.Control(func(fd uintptr) {
					a := peer.Addr().As4()
					operationErr = syscall.Connect(int(fd), &syscall.SockaddrInet4{Port: int(peer.Port()), Addr: a})
					if operationErr != nil {
						return
					}
					buf := make([]byte, 65536)
					for i := 0; i < 4096; i++ {
						_, _, readErr := syscall.Recvfrom(int(fd), buf, syscall.MSG_DONTWAIT)
						if readErr == syscall.EAGAIN || readErr == syscall.EWOULDBLOCK {
							return
						}
						if readErr != nil {
							operationErr = readErr
							return
						}
					}
					operationErr = errors.New("Relay UDP pre-authorization queue did not drain")
				})
			}
			if err == nil {
				err = operationErr
			}
			if err != nil {
				lease.Close()
				b.stopLocked()
				b.mu.Unlock()
				return errRelayUDPOwner
			}
			var stat syscall.Stat_t
			if syscall.Fstat(int(lease.Fd()), &stat) != nil {
				lease.Close()
				b.stopLocked()
				b.mu.Unlock()
				return errRelayUDPOwner
			}
			b.owner = &owner
			b.identity = identity
			b.inode = stat.Ino
			b.peer = peer
			b.lease = lease
			if ctx.Err() != nil || !b.validLocked() {
				b.stopLocked()
				b.mu.Unlock()
				return errRelayUDPOwner
			}
			b.wg.Add(2)
			go b.copyDatagrams(true)
			go b.copyDatagrams(false)
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			return errRelayUDPOwner
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.ctx.Done():
			return errRelayUDPOwner
		case <-owner.Exited:
			return errRelayUDPOwner
		case <-time.After(5 * time.Millisecond):
		}
	}
}
func discoverRelayUDPSocket(pid int, ingress netip.AddrPort) (netip.AddrPort, *os.File, error) {
	// Read the real IPv4 table, select from CHILD fd inodes only, never from the
	// first incoming packet. Validation scans udp6 too to reject overlap.
	f, e := os.Open("/proc/net/udp")
	if e != nil {
		return netip.AddrPort{}, nil, errRelayUDPOwner
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	if !scan.Scan() {
		return netip.AddrPort{}, nil, errRelayUDPOwner
	}
	var peer, connectedPeer netip.AddrPort
	var unconnectedCount, connectedCount int
	for scan.Scan() {
		v := strings.Fields(scan.Text())
		if len(v) < 10 {
			return netip.AddrPort{}, nil, errRelayUDPOwner
		}
		inode, e := strconv.ParseUint(v[9], 10, 64)
		if e != nil {
			return netip.AddrPort{}, nil, errRelayUDPOwner
		}
		if _, e = relayUDPChildFD(pid, inode); e != nil {
			continue
		}
		local, e := relayUDPProcAddr(v[1])
		if e != nil {
			return netip.AddrPort{}, nil, e
		}
		remote, e := relayUDPProcAddr(v[2])
		if e != nil {
			return netip.AddrPort{}, nil, e
		}
		if (!local.Addr().IsUnspecified() && !local.Addr().IsLoopback()) || (!remote.Addr().IsUnspecified() && remote != ingress) {
			continue
		}
		candidate := netip.AddrPortFrom(ingress.Addr(), local.Port())
		if !local.Addr().IsUnspecified() {
			candidate = local
		}
		if remote == ingress {
			connectedPeer = candidate
			connectedCount++
		} else {
			peer = candidate
			unconnectedCount++
		}
	}
	if connectedCount == 1 {
		peer = connectedPeer
	} else if connectedCount != 0 || unconnectedCount != 1 {
		return netip.AddrPort{}, nil, errRelayUDPOwner
	}
	if scan.Err() != nil || !peer.IsValid() {
		return netip.AddrPort{}, nil, errRelayUDPOwner
	}
	lease, e := acquireRelayUDPSocket(pid, peer, ingress)
	return peer, lease, e
}
func (b *RelayUDPBridge) copyDatagrams(outward bool) {
	defer b.wg.Done()
	buffer := make([]byte, 65536)
	source := b.outbound
	if outward {
		source = b.ingress
	}
	for {
		n, _, flags, peer, e := source.ReadMsgUDPAddrPort(buffer, nil)
		if e != nil {
			b.mu.Lock()
			b.stopLocked()
			b.mu.Unlock()
			return
		}
		if flags&syscall.MSG_TRUNC != 0 || n == 0 || n > relayUDPMaxDatagram {
			continue
		}
		b.mu.Lock()
		if !b.validLocked() {
			b.stopLocked()
			b.mu.Unlock()
			return
		}
		if outward && peer != b.peer {
			b.mu.Unlock()
			continue
		}
		// The child socket lease stays held across validation and this kernel send;
		// if the child closes/releases it, the old socket (not a replacement) retains
		// the port. It is never read/written through the duplicate descriptor.
		if outward {
			b.outbound.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			_, e = b.outbound.Write(buffer[:n])
		} else {
			b.ingress.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			_, e = b.ingress.WriteToUDPAddrPort(buffer[:n], b.peer)
		}
		if e != nil {
			b.stopLocked()
		}
		b.mu.Unlock()
		if e != nil {
			return
		}
	}
}
