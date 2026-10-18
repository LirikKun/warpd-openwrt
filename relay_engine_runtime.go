package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Published once per successful applied child generation. The data-plane callback
// reads only immutable values and atomics, never applyMu or MihomoMan.mu.
type relayChildIdentity struct {
	cmd        *exec.Cmd
	pid        int
	generation uint64
	exited     <-chan struct{}
}
type relayRuntimeIdentity struct {
	revision uint64
	profile  ProxySettings
	access   RelaySettings
}
type relayEngineLease struct {
	identity relayRuntimeIdentity
	bridge   *RelayUDPBridge
	live     atomic.Bool
	child    *relayChildIdentity // manager mutex only; captured by value in Current
	retryAt  time.Time
}

func relayIdentity(g Settings, id string) relayRuntimeIdentity {
	p := g.Proxies[id]
	r := g.Relays[p.RelayID]
	identity := relayRuntimeIdentity{revision: g.Revision, profile: *p}
	if r != nil {
		identity.access = *r
	}
	return identity
}
func (m *MihomoMan) closeRelayLocked(id string) {
	if lease := m.relayLeases[id]; lease != nil {
		lease.live.Store(false)
		if lease.bridge != nil {
			lease.bridge.Close()
		}
		delete(m.relayLeases, id)
	}
}
func (m *MihomoMan) revokeRelaysLocked(authorizedOnly bool) {
	for id, lease := range m.relayLeases {
		if !authorizedOnly || lease.child != nil {
			m.closeRelayLocked(id)
		}
	}
}

// RevokeRelays joins transports without stopping healthy ordinary siblings when
// an apply cannot load its shared standalone account.
func (m *MihomoMan) RevokeRelays() { m.mu.Lock(); defer m.mu.Unlock(); m.revokeRelaysLocked(false) }

// Revoke changed persisted intent before any account loading, monitoring or
// replacement setup can block. The caller serializes desired mutations.
func (m *MihomoMan) ReconcileRelayIntent(g Settings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, lease := range m.relayLeases {
		p := g.Proxies[id]
		if g.ProfileError != "" || p == nil || !effectiveEnabled(g, p) || profileType(id, p) != "relay-warp" || !reflect.DeepEqual(lease.identity, relayIdentity(g, id)) {
			m.closeRelayLocked(id)
		}
	}
}
func (m *MihomoMan) relayEndpointsLocked(e endpoints) endpoints {
	e = copyEndpoints(e)
	for id, lease := range m.relayLeases {
		if lease.bridge != nil {
			e.profiles[id] = lease.bridge.Target().String()
		}
	}
	return e
}
func (m *MihomoMan) prepareRelaysLocked(g Settings, e endpoints, acc warpAccount) (endpoints, error) {
	if m.relayLeases == nil {
		m.relayLeases = map[string]*relayEngineLease{}
	}
	// No externally supplied/scanned endpoint may substitute for a prepared bridge.
	e = copyEndpoints(e)
	for id, p := range g.Proxies {
		if profileType(id, p) == "relay-warp" {
			delete(e.profiles, id)
		}
	}
	for id, lease := range m.relayLeases {
		p := g.Proxies[id]
		expired := false
		if lease.bridge != nil {
			select {
			case <-lease.bridge.Done():
				expired = true
			default:
			}
		}
		if p == nil || !effectiveEnabled(g, p) || profileType(id, p) != "relay-warp" || !reflect.DeepEqual(lease.identity, relayIdentity(g, id)) || expired {
			m.closeRelayLocked(id)
		}
	}
	// A hot reload replaces the entire engine generation. Revoke/join every old
	// generation before installing fresh endpoints, including unchanged siblings.
	candidate := m.relayEndpointsLocked(e)
	if m.running && mihomoConfText(g, candidate, acc) != m.lastConf {
		m.revokeRelaysLocked(true)
	}
	var pending error
	for _, id := range profileIDs(g) {
		p := g.Proxies[id]
		if !effectiveEnabled(g, p) || profileType(id, p) != "relay-warp" {
			continue
		}
		identity := relayIdentity(g, id)
		lease := m.relayLeases[id]
		if lease != nil && lease.bridge != nil {
			continue
		}
		if lease != nil && time.Now().Before(lease.retryAt) {
			pending = fmt.Errorf("pending: Relay transport retry delayed")
			continue
		}
		if lease == nil {
			lease = &relayEngineLease{identity: identity}
			m.relayLeases[id] = lease
		}
		var err error
		if !relayCredentialConfigured(p) {
			err = fmt.Errorf("Relay tunnel credential required")
		} else {
			prepare := m.relayPrepare
			if prepare == nil {
				prepare = NewRelayUDPBridge
			}
			r := identity.access
			lease.bridge, err = prepare(context.Background(), RelayUDPConfig{ProfileID: id, Revision: g.Revision, Country: p.RelayCountry, Access: RelayUDPPath{SourceIP: r.SourceIP, Interface: r.Interface, PathConfirmed: r.PathConfirmed}, Freshness: relayUDPMaxFreshness})
		}
		if err != nil {
			lease.retryAt = time.Now().Add(5 * time.Second)
			pending = fmt.Errorf("pending: %s", err)
			getCheck(id).set(func(st *ProxyState) { st.Error = pending.Error() })
		}
	}
	e = m.relayEndpointsLocked(e)
	// State holds only the private endpoint, never a credential or ownership token.
	stateSetEndpoints(e)
	return e, pending
}
func (m *MihomoMan) finishRelayApplyLocked(g Settings, e endpoints, pending error) error {
	child := m.relayChild.Load()
	if child == nil || child.cmd != m.cmd || child.pid != m.pid || child.generation != m.generation {
		child = &relayChildIdentity{m.cmd, m.pid, m.generation, m.done}
		m.relayChild.Store(child)
	}
	for _, id := range profileIDs(g) {
		lease := m.relayLeases[id]
		if lease == nil || lease.bridge == nil || lease.child != nil {
			continue
		}
		lease.live.Store(true)
		lease.child = child
		captured := child
		expires := lease.bridge.expires
		current := func() bool {
			select {
			case <-captured.exited:
				return false
			default:
			}
			return lease.live.Load() && m.relayChild.Load() == captured && time.Now().Before(expires)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		p := g.Proxies[id]
		listen, port := profileProbeAddress(g, id, p)
		// Trigger the lazy WG adapter through only its owned private SOCKS listener.
		// No public gate or health result is involved; this is not a tunnel probe.
		conn, err := m.bootstrapRelayLocked(ctx, captured, net.JoinHostPort(listen, strconv.Itoa(port)))
		if err == nil {
			err = lease.bridge.Authorize(ctx, RelayUDPChild{PID: captured.pid, Generation: captured.generation, Exited: captured.exited, Current: current})
		}
		cancel()
		if conn != nil {
			conn.Close()
		}
		if err != nil {
			lease.live.Store(false)
			lease.bridge.Close()
			lease.bridge = nil
			lease.child = nil
			lease.retryAt = time.Now().Add(5 * time.Second)
			pending = fmt.Errorf("pending: Relay exact-child UDP authorization unavailable")
			getCheck(id).set(func(st *ProxyState) { st.Error = pending.Error() })
		} else {
			getCheck(id).set(func(st *ProxyState) {
				if strings.HasPrefix(st.Error, "pending:") {
					st.Error = ""
				}
			})
		}
	}
	return pending
}
func (m *MihomoMan) bootstrapRelayLocked(ctx context.Context, child *relayChildIdentity, target string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errRelayUDPOwner
	}
	for ctx.Err() == nil {
		select {
		case <-child.exited:
			return nil, errRelayUDPOwner
		default:
		}
		if m.relayChild.Load() != child || m.checkOwnership(child.pid, []mihomoListener{{host, port}}) != nil {
			return nil, errRelayUDPOwner
		}
		dialer := net.Dialer{Timeout: 150 * time.Millisecond}
		conn, e := dialer.DialContext(ctx, "tcp4", target)
		if e == nil {
			conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
			// Recheck ownership immediately before any protocol bytes.
			if m.checkOwnership(child.pid, []mihomoListener{{host, port}}) != nil {
				conn.Close()
				return nil, errRelayUDPOwner
			}
			_, e = conn.Write([]byte{5, 1, 0})
			var response [2]byte
			if e == nil {
				_, e = io.ReadFull(conn, response[:])
			}
			if e == nil && response == [2]byte{5, 0} {
				_, e = conn.Write([]byte{5, 1, 0, 1, 192, 0, 2, 1, 1, 187})
				if e == nil {
					return conn, nil
				}
			}
			conn.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil, ctx.Err()
}
func (m *MihomoMan) RelayReady(g Settings, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := g.Proxies[id]
	lease := m.relayLeases[id]
	if p == nil || !effectiveEnabled(g, p) || lease == nil || lease.bridge == nil || !time.Now().Before(lease.bridge.expires) || !lease.live.Load() || lease.child == nil || m.relayChild.Load() != lease.child || !reflect.DeepEqual(lease.identity, relayIdentity(g, id)) {
		return false
	}
	select {
	case <-lease.child.exited:
		return false
	case <-lease.bridge.Done():
		return false
	default:
		return true
	}
}
