package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// proxyCheck loops per proxy: SOCKS5 /meta probe via the mihomo listener port.
// Fail-rescan logic is kept, but restarts are mihomo-level.

type ProxyCheck struct {
	name string
	port int
	mu   sync.Mutex
	st   ProxyState
}

var (
	checksMu sync.Mutex
	checks   = map[string]*ProxyCheck{}
)

// proxyNames: every proxy warpd can serve, in UI order.
var proxyNames = []string{"warp", "masque", "masqueh2", "m2w"}

func getCheck(name string) *ProxyCheck {
	checksMu.Lock()
	defer checksMu.Unlock()
	c, ok := checks[name]
	if !ok {
		c = &ProxyCheck{name: name}
		checks[name] = c
	}
	return c
}

func (c *ProxyCheck) snapshot() ProxyState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

func (c *ProxyCheck) set(f func(*ProxyState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(&c.st)
}

// runChecks is the health loop for all enabled proxies.
func runChecks(ctx context.Context, cfg *Settings, orch *Orchestrator) {
	var lastRestart time.Time
	// Supervision must run even before the controller exists: it is Ensure
	// below that starts the child. Do not delay cancellation for startup.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		g := cfg.Snapshot()
		e := stateGetEndpoints()
		acc, err := loadAccount(account)
		if err == nil {
			if err := mihomo.Ensure(g, e, acc); err != nil {
				logg.Printf("mihomo: %v", err)
			}
		}
		roundStarted := time.Now()
		process := mihomo.Snapshot()
		var served []string
		var probes sync.WaitGroup
		for _, name := range proxyNames {
			p := g.Proxies[name]
			if p == nil || !p.Enabled {
				continue
			}
			// A proxy only serves once all of its endpoints exist.
			ep, outer := liveEndpoint(name, p, e)
			if ep == "" || (name == "m2w" && (outer == "" || outer == ep)) {
				if g.AutoscanStart {
					orch.requestScan(name, "missing endpoint")
				}
				continue
			}
			served = append(served, name)
			probes.Add(1)
			go func(name string) {
				defer probes.Done()
				checkProxy(ctx, g, name, orch)
			}(name)
		}
		probes.Wait()
		if ctx.Err() != nil {
			return
		}
		if current := mihomo.Snapshot(); current.Running && current.Generation == process.Generation &&
			healthRoundRestartDue(g, served, process.Generation, roundStarted, lastRestart, time.Now()) {
			// One failed chain must never restart its healthy neighbours.
			// Only a complete, current failed round permits bounded recovery.
			if acc, err := loadAccount(account); err == nil {
				lastRestart = time.Now()
				logg.Printf("all served proxies failed -> restarting shared mihomo")
				mihomo.Stop()
				if err := mihomo.Ensure(cfg.Snapshot(), stateGetEndpoints(), acc); err != nil {
					logg.Printf("mihomo recovery: %v", err)
				}
				for _, name := range served {
					getCheck(name).set(func(st *ProxyState) { st.Restarts++ })
				}
			}
		}
		sleepCtx2(ctx, time.Duration(g.CheckPeriod)*time.Second)
	}
}

func healthRoundRestartDue(g Settings, served []string, generation uint64, started, lastRestart, now time.Time) bool {
	if len(served) == 0 || g.FailRestart <= 0 || !lastRestart.IsZero() && now.Sub(lastRestart) < time.Duration(max(60, g.DMERescanGap))*time.Second {
		return false
	}
	for _, name := range served {
		st := getCheck(name).snapshot()
		checked, err := time.Parse(time.RFC3339, st.LastCheck)
		if err != nil || checked.Before(started) || st.Checking || st.CheckedGeneration != generation || st.FailStreak < g.FailRestart {
			return false
		}
	}
	return true
}

func checkProxy(ctx context.Context, g Settings, name string, orch *Orchestrator) {
	checkProxyWith(ctx, g, name, orch, socksMetaCheck)
}

func checkProxyWith(ctx context.Context, g Settings, name string, orch *Orchestrator, probe func(context.Context, string, int) (exitParse, error)) {
	p := g.Proxies[name]
	e := stateGetEndpoints()
	process, applied := mihomo.AppliedSnapshot(g, e)
	if !applied || p == nil || !p.Enabled {
		return
	}
	ep, outer := liveEndpoint(name, p, e)
	c := getCheck(name)
	c.set(func(st *ProxyState) { st.Checking = true })
	defer c.set(func(st *ProxyState) { st.Checking = false })
	// socksMetaCheck owns all retries within one 35-second total budget.
	// A second retry loop here used to multiply that budget into minutes.
	res, err := probe(ctx, p.Listen, p.Port)
	if ctx.Err() != nil {
		return // shutdown is not a failed health check
	}
	current := orch.settings.Snapshot()
	currentEndpoints := stateGetEndpoints()
	after, applied := mihomo.AppliedSnapshot(current, currentEndpoints)
	fresh := current.Proxies[name]
	if !applied || fresh == nil {
		return
	}
	currentEP, currentOuter := liveEndpoint(name, fresh, currentEndpoints)
	if after.Generation != process.Generation || after.Running != process.Running ||
		!fresh.Enabled || fresh.Port != p.Port || fresh.Listen != p.Listen ||
		currentEP != ep || currentOuter != outer {
		return // a result from an obsolete applied configuration is not current health
	}
	recordProxyResult(g, name, orch, res, err, time.Now())
	c.set(func(st *ProxyState) {
		st.CheckedGeneration = process.Generation
		st.CheckedEndpoint, st.CheckedOuter = ep, outer
		st.CheckedPort, st.CheckedListen = p.Port, p.Listen
	})
}

// recordProxyResult is the state/automation boundary after one bounded probe.
func recordProxyResult(g Settings, name string, orch *Orchestrator, res exitParse, err error, checked time.Time) {
	c := getCheck(name)
	now := checked.Format(time.RFC3339Nano)
	if err != nil {
		c.set(func(st *ProxyState) {
			st.Checks++
			st.FailStreak++
			st.LastCheck = now
			st.Error = "health: " + err.Error()
		})
		fails := c.snapshot().FailStreak
		logg.Printf("[%s] health check failed (%d in a row): %v", name, fails, err)
		if g.FailRescan > 0 && fails >= g.FailRescan {
			if orch.requestScan(name, fmt.Sprintf("%d failed checks", fails)) {
				c.set(func(st *ProxyState) { st.Rescans++ })
			}
		}
		return
	}
	c.set(func(st *ProxyState) {
		st.Checks++
		st.OKChecks++
		st.FailStreak = 0
		st.LastCheck = now
		st.ExitInfo = res.info
		st.ExitNode = res.node
		st.ExitCountry = res.country
		st.Error = ""
	})
	if res.node != "" && res.node != lastNode(name) {
		logg.Printf("[%s] exit: %s (node %s)", name, res.info, res.node)
		setLastNode(name, res.node)
	}
	// /meta reports the HTTP serving colo, not a controllable MASQUE peer.
	// The WARP scanner can honor exit filters; MASQUE/chain scans cannot.
	if name == "warp" && badNode(g, res.node) {
		if orch.requestScan(name, res.node+" exit") {
			c.set(func(st *ProxyState) { st.Rescans++ })
			logg.Printf("[%s] excluded HTTP colo %s -> scan queued", name, res.node)
		}
	}
}

var (
	nodesMu   sync.Mutex
	lastNodes = map[string]string{}
)

func lastNode(name string) string {
	nodesMu.Lock()
	defer nodesMu.Unlock()
	return lastNodes[name]
}
func setLastNode(name, node string) {
	nodesMu.Lock()
	defer nodesMu.Unlock()
	lastNodes[name] = node
}

func sleepCtx2(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

var _ = log.Printf

// badNode reports whether an exit node is filtered: DME always, plus every
// node from the user's exclusion list (UCI exclude_nodes).
func badNode(g Settings, node string) bool {
	if node == "" {
		return false
	}
	if node == "DME" {
		return true
	}
	for _, n := range strings.Split(g.ExcludeNodes, ",") {
		if strings.ToUpper(strings.TrimSpace(n)) == node {
			return true
		}
	}
	return false
}
