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
	// ensure mihomo is up before probing
	for i := 0; i < 60 && !mihomoAlive(); i++ {
		time.Sleep(2 * time.Second)
	}
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
		for _, name := range proxyNames {
			p := g.Proxies[name]
			if !p.Enabled {
				continue
			}
			// a proxy only serves once its endpoint exists
			ep, outer := liveEndpoint(name, p, e)
			if ep == "" || (name == "m2w" && outer == "") {
				continue
			}
			checkProxy(g, name, orch)
		}
		sleepCtx2(ctx, time.Duration(g.CheckPeriod)*time.Second)
	}
}

func checkProxy(g Settings, name string, orch *Orchestrator) {
	p := g.Proxies[name]
	c := getCheck(name)
	attempts := 3
	if name == "warp" || name == "masqueh2" {
		attempts = 2
	}
	var res exitParse
	var err error
	for a := 0; a < attempts; a++ {
		res, err = socksMetaCheck(context.Background(), "127.0.0.1", p.Port)
		if err == nil {
			break
		}
		if a < attempts-1 {
			time.Sleep(8 * time.Second)
		}
	}
	now := time.Now().Format(time.RFC3339)
	if err != nil {
		c.set(func(st *ProxyState) {
			st.Checks++
			st.FailStreak++
			st.LastCheck = now
			st.Error = "health: " + err.Error()
		})
		fails := c.snapshot().FailStreak
		logg.Printf("[%s] health check failed (%d in a row): %v", name, fails, err)
		if fails >= g.FailRescan {
			orch.requestScan(name, fmt.Sprintf("%d failed checks", fails))
			time.Sleep(15 * time.Second)
		} else if fails >= g.FailRestart && fails%g.FailRestart == 0 {
			// restart the whole mihomo (single process serves all)
			logg.Printf("[%s] %d failed checks -> restarting mihomo", name, fails)
			mihomo.Stop()
			acc, err := loadAccount(account)
			if err == nil {
				_ = mihomo.Ensure(g, stateGetEndpoints(), acc)
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
	if badNode(g, res.node) {
		logg.Printf("[%s] exit landed on %s node -> rescanning", name, res.node)
		orch.requestScan(name, res.node+" exit")
	}
}

var (
	nodesMu sync.Mutex
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
