package main

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// The engine only binds private loopback listeners. This owned TCP gate is the
// public SOCKS surface: closed until current-policy verification, closes all
// streams on revocation, and expires even if the supervisor stops checking.
// UDP ASSOCIATE and BIND are deliberately unsupported (not an ungated escape).
type profileGate struct {
	mu             sync.Mutex
	listener       net.Listener
	public, target string
	conns          map[net.Conn]bool
	closed         bool
	timer          *time.Timer
	owner          *profileGateOwner
	stopped        chan struct{}
}

func (o *Orchestrator) syncProfileGatesLocked(g Settings) {
	if g.ProfileSchema == 0 {
		return
	}
	process, applied := mihomo.AppliedSnapshot(g, stateGetEndpoints())
	if !applied || g.ProfileError != "" {
		o.closeProfileGates()
		return
	}
	now := time.Now()
	e := stateGetEndpoints()
	o.gatesMu.Lock()
	var deleted []string
	for id := range o.gates {
		if g.Proxies[id] == nil {
			deleted = append(deleted, id)
		}
	}
	o.gatesMu.Unlock()
	for _, id := range deleted {
		o.setOwnedProfileGate(id, "", "", false, time.Time{}, nil)
	}
	for _, id := range profileIDs(g) {
		p := g.Proxies[id]
		if p == nil {
			continue
		}
		st := getCheck(id).snapshot()
		ep, outer := profileLiveEndpoint(id, p, e)
		key := geoKeyFor(g, process.Generation, ep, outer, p)
		checked, err := time.Parse(time.RFC3339Nano, st.LastCheck)
		expiry := checked.Add(time.Duration(g.CheckPeriod)*time.Second + 40*time.Second)
		verified := false
		if profileType(id, p) == "legacy-m2w" || profileType(id, p) == "relay-warp" {
			verified = geoVerified(profilePolicySettings(g, p), st, key, now)
			geoAt, _ := time.Parse(time.RFC3339Nano, st.GeoCheck)
			if geoAt.Add(60 * time.Second).Before(expiry) {
				expiry = geoAt.Add(60 * time.Second)
			}
		}
		allowed := effectiveEnabled(g, p) && err == nil && !checked.After(now) && expiry.After(now) && st.checkedPolicy == key && st.CheckedGeneration == process.Generation && st.Error == "" && st.FailStreak == 0 && st.OKChecks > 0 && ep != "" && exitPolicyError(g, id, st.ExitNode, st.ExitNodeCountry, st.ExitCountry, verified) == ""
		if profileType(id, p) == "relay-warp" {
			allowed = allowed && mihomo.RelayReady(g, id)
		}
		listen, port := profileProbeAddress(g, id, p)
		target := net.JoinHostPort(listen, strconv.Itoa(port))
		var owner *profileGateOwner
		if allowed {
			owner = mihomo.profileGateOwner(process, target, id)
			allowed = owner != nil
			if owner != nil && !owner.relayExpires.IsZero() && owner.relayExpires.Before(expiry) {
				expiry = owner.relayExpires
			}
		}
		if err := o.setOwnedProfileGate(id, net.JoinHostPort(p.Listen, strconv.Itoa(p.Port)), target, allowed, expiry, owner); err != nil {
			getCheck(id).set(func(st *ProxyState) { st.Error = "public profile listener unavailable" })
		}
	}
}

func (g *profileGate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	close(g.stopped)
	g.listener.Close()
	if g.timer != nil {
		g.timer.Stop()
	}
	for c := range g.conns {
		c.Close()
	}
}
func (o *Orchestrator) setOwnedProfileGate(id, public, target string, allowed bool, expires time.Time, owner *profileGateOwner) error {
	allowed = allowed && owner != nil
	o.gatesMu.Lock()
	defer o.gatesMu.Unlock()
	if o.gates == nil {
		o.gates = map[string]*profileGate{}
	}
	gate := o.gates[id]
	if !allowed || !expires.After(time.Now()) {
		if gate != nil {
			gate.close()
			delete(o.gates, id)
		}
		return nil
	}
	if gate != nil {
		gate.mu.Lock()
		valid := !gate.closed && gate.public == public && gate.target == target && (gate.owner == nil && owner == nil || gate.owner != nil && owner != nil && *gate.owner == *owner)
		gate.mu.Unlock()
		if !valid {
			gate.close()
			delete(o.gates, id)
			gate = nil
		}
	}
	if gate == nil {
		l, err := net.Listen("tcp", public)
		if err != nil {
			return fmt.Errorf("public profile listener unavailable")
		}
		gate = &profileGate{listener: l, public: public, target: target, conns: map[net.Conn]bool{}, owner: owner, stopped: make(chan struct{})}
		o.gates[id] = gate
		if owner != nil {
			go gate.watchOwner()
		}
		go gate.serve()
	}
	gate.mu.Lock()
	if gate.timer != nil {
		gate.timer.Stop()
	}
	gate.timer = time.AfterFunc(time.Until(expires), gate.close)
	gate.mu.Unlock()
	return nil
}
func (o *Orchestrator) closeProfileGates() {
	o.gatesMu.Lock()
	defer o.gatesMu.Unlock()
	for id, g := range o.gates {
		g.close()
		delete(o.gates, id)
	}
}
func (o *Orchestrator) profileGateOpen(id string) bool {
	o.gatesMu.Lock()
	defer o.gatesMu.Unlock()
	g := o.gates[id]
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.closed
}
func (g *profileGate) track(c net.Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || len(g.conns) >= 256 {
		c.Close()
		return false
	}
	g.conns[c] = true
	return true
}
func (g *profileGate) untrack(c net.Conn) { c.Close(); g.mu.Lock(); delete(g.conns, c); g.mu.Unlock() }
func (g *profileGate) serve() {
	for {
		c, e := g.listener.Accept()
		if e != nil {
			return
		}
		if g.track(c) {
			go g.forward(c)
		}
	}
}
func (g *profileGate) watchOwner() {
	select {
	case <-g.owner.done:
	case <-g.owner.revoked:
	case <-g.owner.relayDone:
	case <-g.stopped:
		return
	}
	g.close()
}

func (g *profileGate) authorizeForward() bool {
	if g.owner != nil && g.owner.valid() {
		return true
	}
	g.close()
	return false
}

func (g *profileGate) forward(client net.Conn) {
	defer g.untrack(client)
	client.SetDeadline(time.Now().Add(10 * time.Second))
	h := make([]byte, 2)
	if _, e := io.ReadFull(client, h); e != nil || h[0] != 5 || h[1] == 0 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, e := io.ReadFull(client, methods); e != nil {
		return
	}
	noauth := false
	for _, m := range methods {
		noauth = noauth || m == 0
	}
	if !noauth {
		client.Write([]byte{5, 255})
		return
	}
	if _, e := client.Write([]byte{5, 0}); e != nil {
		return
	}
	req := make([]byte, 4)
	if _, e := io.ReadFull(client, req); e != nil || req[0] != 5 || req[2] != 0 {
		return
	}
	if req[1] != 1 {
		client.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	n := 0
	switch req[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		b := make([]byte, 1)
		if _, e := io.ReadFull(client, b); e != nil || b[0] == 0 {
			return
		}
		req = append(req, b[0])
		n = int(b[0])
	default:
		return
	}
	tail := make([]byte, n+2)
	if _, e := io.ReadFull(client, tail); e != nil {
		return
	}
	req = append(req, tail...)
	if !g.authorizeForward() {
		return
	}
	upstream, e := net.DialTimeout("tcp", g.target, 3*time.Second)
	if e != nil {
		return
	}
	if !g.track(upstream) {
		return
	}
	defer g.untrack(upstream)
	if !g.authorizeForward() {
		return
	}
	upstream.SetDeadline(time.Now().Add(10 * time.Second))
	if _, e = upstream.Write([]byte{5, 1, 0}); e != nil {
		return
	}
	if _, e = io.ReadFull(upstream, h); e != nil || h[0] != 5 || h[1] != 0 {
		return
	}
	if _, e = upstream.Write(req); e != nil {
		return
	}
	client.SetDeadline(time.Time{})
	upstream.SetDeadline(time.Time{})
	// EOF ends only one direction of TCP. Propagate FIN and let the reverse
	// stream drain; revocation still closes both tracked sockets immediately.
	done := make(chan struct{})
	copyHalf := func(dst, src net.Conn) {
		if _, err := io.Copy(dst, src); err != nil {
			dst.Close()
			src.Close()
			return
		}
		if tcp, ok := dst.(*net.TCPConn); ok {
			if err := tcp.CloseWrite(); err == nil {
				return
			}
		}
		dst.Close()
	}
	go func() { copyHalf(upstream, client); close(done) }()
	copyHalf(client, upstream)
	<-done
}
