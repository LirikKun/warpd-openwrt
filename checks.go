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
	name     string
	port     int
	geoProbe geoProbe // initialized before checks start; tests inject offline fixtures
	mu       sync.Mutex
	st       ProxyState
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
		c = &ProxyCheck{name: name, geoProbe: socksGeoCheck}
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
		getCheck(name).clearGeo()
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
	if name == "m2w" {
		if err != nil {
			c.clearGeo()
		} else {
			c.observeGeo(ctx, current, res, geoKeyFor(current, process.Generation, ep, outer, p))
		}
		if ctx.Err() != nil {
			c.clearGeo()
			return
		}
		// Geo can take up to eight seconds. Recheck the complete applied child
		// and policy key after that asynchronous observation as well.
		latest := orch.settings.Snapshot()
		latestEPs := stateGetEndpoints()
		latestProcess, ok := mihomo.AppliedSnapshot(latest, latestEPs)
		latestP := latest.Proxies[name]
		if !ok || latestP == nil || !latestP.Enabled {
			c.clearGeo()
			return
		}
		latestEP, latestOuter := liveEndpoint(name, latestP, latestEPs)
		if latestProcess.Generation != process.Generation || geoKeyFor(latest, process.Generation, latestEP, latestOuter, latestP) != geoKeyFor(current, process.Generation, ep, outer, p) {
			c.clearGeo()
			return
		}
		current = latest
	}
	recordProxyResult(current, name, orch, res, err, time.Now())
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
		c.clearGeo()
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
		st.ExitNodeCountry = res.nodeCountry
		st.Error = ""
	})
	if res.node != "" && res.node != lastNode(name) {
		logg.Printf("[%s] exit: %s (node %s)", name, res.info, res.node)
		setLastNode(name, res.node)
	}
	// /meta is an observed HTTP serving colo, not tunnel-peer identity.
	// Only WARP has an existing scanner that can honor these exclusions.
	if reason := exitPolicyError(g, name, res.node, res.nodeCountry, res.country); name == "warp" && reason != "" {
		if orch.requestScan(name, reason) {
			c.set(func(st *ProxyState) { st.Rescans++ })
			logg.Printf("[%s] %s -> scan queued", name, reason)
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

// exitPolicyError assesses the observation only. Callers separately require fresh
// transport health bound to the applied configuration and owned listeners.
func exitPolicyError(g Settings, name, node, nodeCountry, publicCountry string, verified ...bool) string {
	if name != "warp" && name != "masque" && name != "masqueh2" && name != "m2w" {
		return "unknown exit policy"
	}
	reason := observedNodePolicyError(g, node, nodeCountry)
	if name == "m2w" {
		if reason != "" {
			reason += "; "
		}
		if strings.ToUpper(strings.TrimSpace(publicCountry)) == "RU" {
			reason += "public IP country RU is excluded; "
		}
		// /meta and independent observation are both necessary.
		if len(verified) == 0 || !verified[0] || !knownCountry(publicCountry) || strings.ToUpper(strings.TrimSpace(publicCountry)) == "RU" {
			reason += "foreign IP not verified"
		}
		reason = strings.TrimSuffix(reason, "; ")
	}
	return reason
}

func observedNodePolicyError(g Settings, node, nodeCountry string) string {
	node = strings.ToUpper(strings.TrimSpace(node))
	nodeCountry = strings.ToUpper(strings.TrimSpace(nodeCountry))
	if node == "" {
		return "node unknown"
	}
	if badNode(g, node) {
		return "excluded node " + node
	}
	// Recognized ISO alpha-2 codes plus XK (used for Kosovo by geo services).
	// Do not treat arbitrary two-letter strings or provider unknown sentinels
	// as evidence of a known, non-excluded country.
	const countries = " AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS XK YE YT ZA ZM ZW "
	if len(nodeCountry) != 2 || !strings.Contains(countries, " "+nodeCountry+" ") {
		return "node country unknown"
	}
	if nodeCountry == "RU" {
		return "excluded node country RU"
	}
	for _, country := range strings.Split(g.ExcludeCountries, ",") {
		if strings.ToUpper(strings.TrimSpace(country)) == nodeCountry {
			return "excluded node country " + nodeCountry
		}
	}
	return ""
}

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
