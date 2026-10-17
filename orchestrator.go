package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Orchestrator coordinates scans and endpoint selection for both proxies.
// Scans are serialized (one warpscout scan at a time) to keep CPU/RAM
// usage predictable on the Filogic 820.
type Orchestrator struct {
	onApply  func(name string)
	settings *Settings

	mu sync.Mutex
	// endpoints chosen by the last successful scan (persisted in RAM only;
	// the UCI manual pins live in config)
	cur endpoints
	// scan state
	scanning   bool
	scanLog    []string
	scanStart  time.Time
	scanFor    string
	scanReason string
	lastScan   time.Time
	lastResult string

	scanCh       chan scanRequest
	reserved     map[string]bool                               // accepted work, including requests not yet consumed
	lastAttempt  map[string]time.Time                          // per underlying scan, including failures
	scanner      func([]string, time.Duration) (string, error) // optional local test runner
	scoutCommand func(context.Context, []string) *exec.Cmd     // optional local executable
	now          func() time.Time                              // clock seam; set before starting the loop
}

type scanRequest struct {
	for_    string
	reason  string
	targets []string
	force   bool
}

type endpoints struct {
	warp   string
	masque map[string]string // "masque"/"masqueh2" -> ip:port
}

var (
	stateMu sync.RWMutex
	stateEp = endpoints{masque: map[string]string{}}
)

func stateGetEndpoints() endpoints {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return copyEndpoints(stateEp)
}

func copyEndpoints(e endpoints) endpoints {
	cp := endpoints{warp: e.warp, masque: make(map[string]string, len(e.masque))}
	for k, v := range e.masque {
		cp.masque[k] = v
	}
	return cp
}

func stateSetEndpoints(e endpoints) {
	stateMu.Lock()
	defer stateMu.Unlock()
	if e.masque == nil {
		e.masque = stateEp.masque
	}
	stateEp = copyEndpoints(e)
}

func NewOrchestrator(s *Settings) *Orchestrator {
	return &Orchestrator{
		settings:    s,
		scanCh:      make(chan scanRequest, 16),
		reserved:    make(map[string]bool),
		lastAttempt: make(map[string]time.Time),
		now:         time.Now,
	}
}

// requestScan queues a scan for a proxy (non-blocking).
func (o *Orchestrator) requestScan(for_, reason string) bool {
	return o.queueScan(for_, reason, false)
}

// scanTargets resolves proxy names to the underlying scans they need. A chain
// may need scans even when its standalone warp/masque listeners are disabled.
func scanTargets(g Settings, for_ string, force bool) []string {
	wanted := make(map[string]bool)
	addProxy := func(key string) {
		if p := g.Proxies[key]; p != nil && p.Enabled && (force || p.Endpoint == "") {
			wanted[key] = true
		}
	}
	addChain := func() {
		if p := g.Proxies["m2w"]; p != nil && p.Enabled {
			if p.Inner == "" {
				wanted["warp"] = true
			}
			if p.Outer == "" {
				wanted["masque"] = true
			}
		}
	}
	switch for_ {
	case "warp", "masque", "masqueh2":
		addProxy(for_)
	case "m2w":
		addChain()
	case "both":
		for _, key := range []string{"warp", "masque", "masqueh2"} {
			addProxy(key)
		}
		addChain()
	default:
		return nil
	}
	var targets []string
	for _, key := range []string{"warp", "masque", "masqueh2"} {
		if wanted[key] {
			targets = append(targets, key)
		}
	}
	return targets
}

func (o *Orchestrator) queueScan(for_, reason string, force bool) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.scanning {
		return false
	}
	// Resolve and reserve underlying targets before releasing the mutex, so
	// duplicate health checks cannot flood the queue before its consumer runs.
	g := o.settings.Snapshot()
	targets := scanTargets(g, for_, force)
	for _, key := range targets {
		if o.reserved[key] {
			return false
		}
	}
	if !force {
		eligible := targets[:0]
		gap := time.Duration(max(60, g.DMERescanGap)) * time.Second
		now := o.now()
		for _, key := range targets {
			if last := o.lastAttempt[key]; last.IsZero() || now.Sub(last) >= gap {
				eligible = append(eligible, key)
			}
		}
		targets = eligible
	}
	if len(targets) == 0 {
		return false
	}
	select {
	case o.scanCh <- scanRequest{for_: for_, reason: reason, targets: targets, force: force}:
		for _, key := range targets {
			o.reserved[key] = true
		}
		return true
	default:
		return false
	}
}

// ForceScan queues an explicit scan, bypassing only the automatic cooldown.
// Target validation, pending deduplication and active/busy protection still apply.
func (o *Orchestrator) ForceScan(for_, reason string) bool {
	return o.queueScan(for_, reason, true)
}

// RegisterAccount creates a new WARP account with warpscout register.
// name: "warp" for the main account, or a fresh registration with -fresh.
func (o *Orchestrator) RegisterAccount(fresh bool) (string, error) {
	args := []string{"register", "-a", account}
	if fresh {
		args = append(args, "-fresh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, warpScout, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=32MiB", "NO_COLOR=1", "TERM=dumb")
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		return msg, fmt.Errorf("register failed: %v", err)
	}
	return msg, nil
}

// ScanState is the JSON shape for the web UI.
type ScanState struct {
	Scanning   bool     `json:"scanning"`
	For        string   `json:"for,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Started    string   `json:"started,omitempty"`
	LastScan   string   `json:"last_scan,omitempty"`
	LastResult string   `json:"last_result,omitempty"`
	Log        []string `json:"log,omitempty"`
}

func (o *Orchestrator) State() ScanState {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := ScanState{
		Scanning:   o.scanning,
		For:        o.scanFor,
		Reason:     o.scanReason,
		LastScan:   o.lastScan.Format(time.RFC3339),
		LastResult: o.lastResult,
		Log:        append([]string{}, o.scanLog...),
	}
	if !o.scanStart.IsZero() && o.scanning {
		st.Started = o.scanStart.Format(time.RFC3339)
	}
	if len(st.Log) > 60 {
		st.Log = st.Log[len(st.Log)-60:]
	}
	return st
}

func (o *Orchestrator) log(format string, args ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.scanLog = append(o.scanLog, time.Now().Format("15:04:05 ")+fmt.Sprintf(format, args...))
	if len(o.scanLog) > 200 {
		o.scanLog = o.scanLog[len(o.scanLog)-200:]
	}
}

// runScanLoop consumes scan requests one at a time.
func (o *Orchestrator) runScanLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-o.scanCh:
			o.runRequest(req)
		}
	}
}

// runScan performs one warpscout scan and updates the endpoint state.
// The scan command writes its report to /tmp (RAM) and never to ROM.
func (o *Orchestrator) runScan(for_, reason string) {
	g := o.settings.Snapshot()
	o.runRequest(scanRequest{for_: for_, reason: reason, targets: scanTargets(g, for_, false)})
}

func (o *Orchestrator) runRequest(req scanRequest) {
	g := o.settings.Snapshot()
	eligible := make(map[string]bool)
	for _, key := range scanTargets(g, req.for_, req.force) {
		eligible[key] = true
	}
	var targets []string
	for _, key := range req.targets {
		if eligible[key] {
			targets = append(targets, key)
		}
	}

	o.mu.Lock()
	if len(targets) == 0 {
		for _, key := range req.targets {
			delete(o.reserved, key)
		}
		o.mu.Unlock()
		return
	}
	o.scanning = true
	o.scanFor = req.for_
	o.scanReason = req.reason
	o.scanStart = o.now()
	o.scanLog = nil
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		o.scanning = false
		for _, key := range req.targets {
			delete(o.reserved, key)
		}
		o.mu.Unlock()
	}()

	o.log("scan for %s (reason: %s)", req.for_, req.reason)
	for _, key := range targets {
		if g.Proxies[key] == nil {
			g.Proxies[key] = (&ProxySettings{}).defaults(key)
		}
		if key == "warp" {
			o.scanWarp(g)
		} else {
			o.scanMasque(g, key)
		}
		o.mu.Lock()
		o.lastScan = o.now()
		o.lastAttempt[key] = o.lastScan
		o.mu.Unlock()
	}
}

// excludeArgs turns "DME,SVO" / "RU,KZ" into warpscout -exclude-* flags.
// The hard-coded DME (warp) default stays: user filters only add to it
// add to them.
func excludeArgs(nodes, countries string) []string {
	var out []string
	for _, n := range strings.Split(nodes, ",") {
		n = strings.ToUpper(strings.TrimSpace(n))
		if n != "" {
			out = append(out, "-exclude-node", n)
		}
	}
	for _, c := range strings.Split(countries, ",") {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "" {
			out = append(out, "-exclude-country", c)
		}
	}
	return out
}

// bestUnderPing re-reads the warpscout report and returns the best endpoint
// whose ENDPOINT PING column is <= maxPing. Empty string = keep warpscout's
// own -best pick (or no report).
func bestUnderPing(report string, maxPing int) string {
	if maxPing <= 0 || report == "" {
		return ""
	}
	data, err := os.ReadFile(report)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		// row shape: ENDPOINT PING TUN PING LOSS SEEN AS NODE ...
		if len(f) >= 3 && strings.Contains(f[0], ":") {
			ping := strings.TrimSuffix(f[1], "ms")
			n, err := strconv.Atoi(ping)
			if err == nil && n > 0 && n <= maxPing {
				return f[0]
			}
		}
	}
	return ""
}

// reportPing returns the ENDPOINT PING (ms) of the given endpoint in the
// report; -1 when absent.
func reportPing(report, endpoint string) int {
	data, err := os.ReadFile(report)
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == endpoint {
			n, err := strconv.Atoi(strings.TrimSuffix(f[1], "ms"))
			if err == nil {
				return n
			}
		}
	}
	return -1
}

// scanWarp: exclude DME, best by ping. Args copied from the live test.
func (o *Orchestrator) scanWarp(g Settings) {
	report := "/tmp/warpd-scan-warp.report.txt"
	args := []string{
		"scan",
		"-a", account,
		"-p", g.Proxies["warp"].Protocol,
		"-P",
		"-exclude-node", "DME",
		"-n", fmt.Sprintf("%d", g.ScanSample),
		"-jt", fmt.Sprintf("%d", g.ScanJobs),
		"-best",
		"-o", report,
	}
	args = append(args, excludeArgs(g.ExcludeNodes, g.ExcludeCountries)...)
	if g.Proxies["warp"].GenI1 != "" {
		args = append(args, "-gen-i1", g.Proxies["warp"].GenI1)
	}
	out, err := o.runWarpScout(args, 10*time.Minute)
	best := strings.TrimSpace(lastNonEmpty(out))
	if err != nil || best == "" || !strings.Contains(best, ":") {
		o.log("warp scan failed: %v / %s", err, lastLines(out, 2))
		o.mu.Lock()
		o.lastResult = "warp: no endpoint"
		o.mu.Unlock()
		return
	}
	if alt := bestUnderPing(report, g.MaxPing); alt != "" && alt != best {
		o.log("warp: %s over max ping, using %s", best, alt)
		best = alt
	} else if alt == "" && g.MaxPing > 0 {
		if bp := reportPing(report, best); bp > g.MaxPing {
			o.log("warp: only endpoints over %dms found (best %dms); keeping warpscout pick", g.MaxPing, bp)
		}
	}
	stateMu.Lock()
	stateEp.warp = best
	stateMu.Unlock()
	o.mu.Lock()
	o.lastResult = "warp: " + best
	o.mu.Unlock()
	o.log("warp best endpoint: %s", best)
	os.Remove(report)
	o.applyLive("warp")
}

// applyLive hot-switches the running proxy process to the freshly scanned
// endpoints (unless the proxy is manually pinned). The callback is wired up
// in main.go so the orchestrator has no dependency on supervisors.
func (o *Orchestrator) applyLive(name string) {
	if o.onApply != nil {
		o.onApply(name)
	}
}

// scanMasque: find a working MASQUE endpoint (QUIC or TCP transport).
// No node/country filters apply (one node per network), no -n sampling for
// QUIC (fixed pool of 2 addresses x 7 ports).
func (o *Orchestrator) scanMasque(g Settings, key string) {
	p := g.Proxies[key]
	report := "/tmp/warpd-scan-" + key + ".report.txt"
	args := []string{
		"scan",
		"-a", account,
		"-p", p.Protocol,
		"-P",
		"-best",
		"-o", report,
	}
	if p.Protocol == "masque-h2" {
		args = append(args, "-n", fmt.Sprintf("%d", g.ScanSample))
	}
	args = append(args, "-jt", fmt.Sprintf("%d", g.ScanJobs))
	out, err := o.runWarpScout(args, 12*time.Minute)
	best := strings.TrimSpace(lastNonEmpty(out))
	if err != nil || best == "" || !strings.Contains(best, ":") {
		o.log("%s scan failed: %v / %s", key, err, lastLines(out, 2))
		o.mu.Lock()
		o.lastResult = key + ": no endpoint"
		o.mu.Unlock()
		return
	}
	if alt := bestUnderPing(report, g.MaxPing); alt != "" && alt != best {
		o.log("%s: %s over max ping, using %s", key, best, alt)
		best = alt
	}
	stateMu.Lock()
	stateEp.masque[key] = best
	stateMu.Unlock()
	o.mu.Lock()
	o.lastResult = key + ": " + best
	o.mu.Unlock()
	o.log("%s best endpoint: %s", key, best)
	os.Remove(report)
	o.applyLive(key)
}

// runWarpScout executes a scan command, streaming plain output into the
// scan log (which the web UI polls). The account file is passed via -a.
func (o *Orchestrator) runWarpScout(args []string, maxWait time.Duration) (string, error) {
	if o.scanner != nil {
		return o.scanner(args, maxWait)
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxWait)
	defer cancel()
	var cmd *exec.Cmd
	if o.scoutCommand != nil {
		cmd = o.scoutCommand(ctx, args)
	} else {
		cmd = exec.CommandContext(ctx, warpScout, args...)
		cmd.Dir = workDir
	}
	cmd.Env = append(cmd.Environ(),
		"GOMEMLIMIT=48MiB",
		"NO_COLOR=1",
		"TERM=dumb",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var acc strings.Builder
	buf := make([]byte, 2048)
	// StdoutPipe must be drained before Wait closes it. Read and accumulate on
	// this goroutine so returning output cannot race with the log reader.
	var readErr error
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			acc.WriteString(chunk)
			o.logPlain(strings.Split(chunk, "\n"))
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	err = cmd.Wait()
	if err == nil {
		err = readErr
	}
	return acc.String(), err
}

func (o *Orchestrator) logPlain(lines []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		o.scanLog = append(o.scanLog, l)
	}
	if len(o.scanLog) > 200 {
		o.scanLog = o.scanLog[len(o.scanLog)-200:]
	}
}

// lastNonEmpty returns the last non-empty line that is exactly "ip:port"
// (what -best prints). The "Full report written to ..." line after it is
// ignored.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func lastNonEmpty(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if isEndpointLine(l) {
			return l
		}
	}
	return ""
}

// isEndpointLine: "host:port" and nothing else (no spaces inside).
func isEndpointLine(l string) bool {
	if l == "" || strings.ContainsAny(l, " 	") {
		return false
	}
	i := strings.LastIndex(l, ":")
	if i <= 0 {
		return false
	}
	port := l[i+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}
	return net.ParseIP(l[:i]) != nil || strings.Contains(l[:i], ":")
}

// ScanStatusOK reports whether a scan is currently running.
func (o *Orchestrator) ScanStatusOK() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.scanning
}

var _ = filepath.Join
var _ = json.Marshal
