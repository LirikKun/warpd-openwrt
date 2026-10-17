package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	stateDir   = "/var/run/warpd"
	account    = "/etc/warpd/warp-account.json"
	warpScout  = "/usr/bin/warpscout"
	workDir    = "/var/warpd"
	maxLogSize = 48 * 1024
)

// warpdLog is a tiny RAM ring-buffer logger: keeps N lines, never touches
// ROM, and flushes nothing to disk.
type warpdLog struct {
	mu    sync.Mutex
	lines []string
	max   int
}

var logg = &warpdLog{max: 300}

func (l *warpdLog) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := fmt.Sprintf("%s %s", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	l.lines = append(l.lines, msg)
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
	fmt.Fprintln(os.Stderr, msg)
}

func (l *warpdLog) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

// ProxyState is the JSON shape served by the web UI.
type ProxyState struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Ready     bool   `json:"ready"`     // listening + handshake ok
	Pid       int    `json:"pid"`
	Listen    string `json:"listen"`
	Port      int    `json:"port"`
	Endpoint  string `json:"endpoint"`
	Outer     string `json:"outer,omitempty"`
	ManualEP  bool   `json:"manual_endpoint"`
	ExitInfo  string `json:"exit_info,omitempty"` // e.g. "RU via Amsterdam (AMS node)"
	ExitNode  string `json:"exit_node,omitempty"` // IATA code
	ExitCountry string `json:"exit_country,omitempty"`
	LastCheck string `json:"last_check,omitempty"`
	Checks    int    `json:"checks"`
	OKChecks  int    `json:"ok_checks"`
	FailStreak int   `json:"fail_streak"`
	Rescans   int    `json:"rescans"`
	Restarts  int    `json:"restarts"`
	Error     string `json:"error,omitempty"`
	Log       []string `json:"log,omitempty"`
}

// Supervisor keeps one warpscout socks process alive and re-creates it
// when the health check fails repeatedly. It never configures the network.
type Supervisor struct {
	name string
	cfg  func() *ProxySettings
	glob func() Settings

	mu       sync.Mutex
	proc     *exec.Cmd
	running  bool
	ready    bool
	pid      int
	endpoint string
	outer    string
	manual   bool
	state    ProxyState

	lastReportedNode string

	stopCh chan struct{}
	gen    int // generation counter to kill stale loops
}

func (s *Supervisor) stateSnapshot() ProxyState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.Name = s.name
	st.Running = s.running
	st.Ready = s.ready
	st.Pid = s.pid
	st.Endpoint = s.endpoint
	st.Outer = s.outer
	st.ManualEP = s.manual
	if p := s.cfg(); p != nil {
		st.Listen = p.Listen
		st.Port = p.Port
		st.Enabled = p.Enabled
	}
	return st
}

func (s *Supervisor) setState(f func(*ProxyState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.state)
}

// buildArgs assembles the warpscout socks argv for this proxy.
func (s *Supervisor) buildArgs(p *ProxySettings, endpoint, outer string) []string {
	args := []string{
		"socks",
		"-a", account,
		"-e", endpoint,
		"-p", p.Protocol,
		"-l", p.Listen,
		"-port", fmt.Sprintf("%d", p.Port),
	}
	if p.GenI1 != "" && p.Protocol == "awg" {
		args = append(args, "-gen-i1", p.GenI1)
	}
	if outer != "" {
		args = append(args, "-through", outer)
	}
	return args
}

// startProcess launches warpscout socks for the current endpoint and waits
// for the listening banner. The banner format (plain mode, non-TTY):
//   SOCKS5  socks5h://addr:port
//   Endpoint ip:port
//   Tunnel  awg [through ip:port]
//   Exit    <region> via <city>, CC (NODE node)
// It also prints "did not answer" errors to stderr on failure.
func (s *Supervisor) startProcess(endpoint, outer string) error {
	p := s.cfg()
	args := s.buildArgs(p, endpoint, outer)
	cmd := exec.Command(warpScout, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"GOMEMLIMIT=40MiB",
		"GOGC=200",
		"NO_COLOR=1",
		"TERM=dumb",
	)
	// stdout+stderr merged into a pipe we read for readiness
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return fmt.Errorf("start warpscout: %w", err)
	}
	pw.Close()

	s.mu.Lock()
	s.proc = cmd
	s.running = true
	s.pid = cmd.Process.Pid
	s.ready = false
	s.endpoint = endpoint
	s.outer = outer
	s.mu.Unlock()

	// read until the "Exit"/"Point clients" lines appear or the process dies
	buf := make([]byte, 4096)
	var acc string
	deadline := time.Now().Add(45 * time.Second)
	go func() {
		for {
			if time.Now().After(deadline) {
				_ = pr.Close()
				s.setState(func(st *ProxyState) { st.Error = "startup: banner timeout" })
				return
			}
			pr.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := pr.Read(buf)
			if n > 0 {
				acc += string(buf[:n])
				lines := strings.Split(acc, "\n")
				if len(lines) > 400 {
					acc = strings.Join(lines[len(lines)-300:], "\n")
				}
			}
			if err != nil {
				_ = pr.Close()
				if os.IsTimeout(err) {
					continue
				}
				// EOF: process died
				s.mu.Lock()
				s.running = false
				s.ready = false
				s.mu.Unlock()
				s.setState(func(st *ProxyState) {
					st.Error = "process exited during startup: " + lastLines(acc, 3)
				})
				return
			}
			if strings.Contains(acc, "Ctrl+C to stop") || strings.Contains(acc, "did not answer") {
				s.mu.Lock()
				s.ready = strings.Contains(acc, "Ctrl+C to stop")
				s.mu.Unlock()
				if s.ready {
					exit := parseExitLine(acc)
					s.setState(func(st *ProxyState) {
						st.ExitInfo = exit.info
						st.ExitNode = exit.node
						st.ExitCountry = exit.country
						st.Error = ""
						st.LastCheck = time.Now().Format(time.RFC3339)
						st.OKChecks++
						st.Checks++
					})
				}
				// keep draining in background to reap later output
				go func(r *os.File, initial string) {
					defer r.Close()
					b := make([]byte, 4096)
					for {
						rr, err := r.Read(b)
						if rr > 0 {
							s.appendProcLog(string(b[:rr]))
						}
						if err != nil {
							return
						}
					}
				}(pr, acc)
				return
			}
		}
	}()
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func (s *Supervisor) appendProcLog(chunk string) {
	// keep a short tail of process output for diagnostics (RAM only)
	s.setState(func(st *ProxyState) {
		lines := append(st.Log, strings.Split(chunk, "\n")...)
		if len(lines) > 60 {
			lines = lines[len(lines)-60:]
		}
		st.Log = lines
	})
}

type exitParse struct {
	info, node, country string
}

// parseExitLine pulls "Exit RU via Amsterdam, NL (AMS node)" out of the banner.
func parseExitLine(banner string) exitParse {
	for _, line := range strings.Split(banner, "\n") {
		line = strings.TrimSpace(line)
		// strip ANSI boxing chars
		line = strings.NewReplacer("│", "", "╭", "", "╮", "", "╰", "", "╯", "").Replace(line)
		// the boxed banner has "│ Exit     RU via ..." (label + spaces);
		// the follow-up note line starts with "Exit is what ..."
		if strings.HasPrefix(line, "Exit is what") {
			continue
		}
		if i := strings.Index(line, "Exit "); i >= 0 {
			line = strings.TrimSpace(line[i+len("Exit "):])
		} else if !strings.HasPrefix(line, "Exit") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "Exit"))
		body = strings.TrimSpace(body)
		node := ""
		if i := strings.Index(body, "("); i >= 0 {
			if j := strings.Index(body, " node)"); j >= 0 && j > i {
				inner := body[i+1 : j]
				parts := strings.Fields(inner)
				if len(parts) > 0 {
					node = parts[len(parts)-1]
				}
				// cut warpscout's trailing note ("— is what
				// speed.cloudflare.com reports. ...")
				body = strings.TrimSpace(body[:j+len(" node)")])
			}
		}
		country := ""
		if i := strings.Index(body, ", "); i >= 0 {
			country = strings.TrimSpace(body[i+2:])
			if j := strings.Index(country, " ("); j >= 0 {
				country = country[:j]
			}
		}
		return exitParse{info: body, node: node, country: country}
	}
	return exitParse{}
}

// stop kills the current process group (warpscout + its children).
func (s *Supervisor) stop() {
	s.mu.Lock()
	cmd := s.proc
	s.proc = nil
	s.running = false
	s.ready = false
	s.pid = 0
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		go func() {
			time.Sleep(3 * time.Second)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}()
		go cmd.Wait()
	}
}

// switchEndpoint applies a new endpoint (manual or from a scan) on the fly.
func (s *Supervisor) switchEndpoint(endpoint, outer string, manual bool) {
	s.stop()
	time.Sleep(500 * time.Millisecond)
	s.mu.Lock()
	s.manual = manual
	s.mu.Unlock()
	if err := s.startProcess(endpoint, outer); err != nil {
		logg.Printf("[%s] start failed: %v", s.name, err)
		s.setState(func(st *ProxyState) { st.Error = err.Error() })
	} else {
		logg.Printf("[%s] switched to %s (through %s, manual=%v)", s.name, endpoint, outer, manual)
		s.setState(func(st *ProxyState) { st.Restarts++ })
	}
}

// healthCheck measures the proxy from the outside: dial the SOCKS port,
// CONNECT to speed.cloudflare.com:443... actually simpler: run the check
// through the warpscout binary itself is heavyweight; instead we do a raw
// SOCKS CONNECT + TLS HTTP GET of /meta with the Referer header.
func (s *Supervisor) healthCheck(ctx context.Context, listen string, port int) (exitParse, error) {
	res, err := socksMetaCheck(ctx, listen, port)
	if err != nil {
		return exitParse{}, err
	}
	return res, nil
}

// run is the supervisor loop: keep the process up, check exit periodically,
// act on failures (restart, rescan).
func (s *Supervisor) run(outer *Orchestrator) {
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		p := s.cfg()
		if !p.Enabled {
			time.Sleep(5 * time.Second)
			continue
		}

		s.mu.Lock()
		running, ready := s.running, s.ready
		s.mu.Unlock()

		// bring up if needed
		if !running {
			ep, out, _ := s.pickEndpoints(p)
			if ep == "" {
				// nothing scanned yet and no manual pin: trigger a scan
				if outer.requestScan(s.name, "no endpoint") {
					time.Sleep(10 * time.Second)
				} else {
					time.Sleep(15 * time.Second)
				}
				continue
			}
			if err := s.startProcess(ep, out); err != nil {
				logg.Printf("[%s] start failed: %v", s.name, err)
				time.Sleep(10 * time.Second)
				continue
			}
			// wait for readiness banner
			time.Sleep(3 * time.Second)
			continue
		}
		if !ready {
			// still starting; check again shortly
			time.Sleep(5 * time.Second)
			continue
		}

		// periodic health check
		g := s.glob()
		period := time.Duration(g.CheckPeriod) * time.Second
		// check more often than the exit-report period: /meta through a
		// chained tunnel is flaky, so probe several times per period and
		// only count failure when ALL attempts within a window fail.
		attempts := 3
		if s.name == "warp" {
			attempts = 2
		}
		var res exitParse
		var err error
		for a := 0; a < attempts; a++ {
			res, err = s.healthCheck(context.Background(), p.Listen, p.Port)
			if err == nil {
				break
			}
			if a < attempts-1 {
				time.Sleep(10 * time.Second)
			}
		}
		now := time.Now().Format(time.RFC3339)
		if err != nil {
			s.setState(func(st *ProxyState) {
				st.Checks++
				st.FailStreak++
				st.LastCheck = now
				st.Error = "health: " + err.Error()
			})
			logg.Printf("[%s] health check failed (%d in a row): %v", s.name, s.stateSnapshot().FailStreak, err)
		} else {
			dme := res.node == "DME"
			s.setState(func(st *ProxyState) {
				st.Checks++
				st.OKChecks++
				st.FailStreak = 0
				st.LastCheck = now
				st.ExitInfo = res.info
				st.ExitNode = res.node
				st.ExitCountry = res.country
				st.Error = ""
			})
			if res.node != "" && res.node != s.lastReportedNode {
				logg.Printf("[%s] exit: %s (node %s)", s.name, res.info, res.node)
				s.lastReportedNode = res.node
			}
			if dme && !s.isManual() {
				logg.Printf("[%s] exit landed on DME node -> rescanning", s.name)
				outer.requestScan(s.name, "DME exit")
			}
		}

	// failures: restart after failRestart, rescan after failRescan
		fails := s.stateSnapshot().FailStreak
		if fails >= g.FailRescan && !s.isManual() {
			outer.requestScan(s.name, fmt.Sprintf("%d failed checks", fails))
			time.Sleep(15 * time.Second)
		} else if fails >= g.FailRestart {
			logg.Printf("[%s] %d failed checks -> restarting process", s.name, fails)
			s.setState(func(st *ProxyState) { st.Restarts++; st.FailStreak = 0 })
			s.stop()
			time.Sleep(3 * time.Second)
		}

		sleepCtx(s.stopCh, period)
	}
}

func (s *Supervisor) pickEndpoints(p *ProxySettings) (endpoint, outer string, manual bool) {
	if s.name == "masque" || s.name == "masqueh2" {
		if p.Endpoint != "" {
			return p.Endpoint, "", true
		}
		return stateGetEndpoints().masque[s.name], "", false
	}
	if s.name == "warp" {
		if p.Endpoint != "" {
			return p.Endpoint, "", true
		}
		return stateGetEndpoints().warp, "", false
	}
	// w2w: manual pins have priority, else last scan result
	if p.Outer != "" && p.Inner != "" {
		return p.Inner, p.Outer, true
	}
	if p.Outer != "" {
		// outer pinned, inner from scan
		in := stateGetEndpoints().w2wInner
		if in != "" {
			return in, p.Outer, true
		}
		return "", "", false
	}
	e := stateGetEndpoints()
	if e.w2wOuter != "" && e.w2wInner != "" {
		return e.w2wInner, e.w2wOuter, false
	}
	return "", "", false
}

func (s *Supervisor) currentGen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

func atomicGen() int { return 0 }

func sleepCtx(ch chan struct{}, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}

var _ = filepath.Join
