package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"
)

// relayRuntime schedules the independent monitor. It has no account loader,
// proxy renderer or engine dependency; monitor-only resources remain useful
// with an intentionally empty profile collection.
type relayRuntimeConfig struct {
	access   RelayAccess
	enabled  bool
	interval int
}
type relayRuntime struct {
	cfg      *Settings
	monitor  *RelayMonitor
	ctx      context.Context
	mu       sync.Mutex
	revision uint64
	configs  map[string]relayRuntimeConfig
	next     map[string]time.Time
	active   map[string]bool
	manual   map[string]bool
	blocked  map[string]bool
	wake     chan struct{}
	work     sync.WaitGroup
}

func newRelayRuntime(ctx context.Context, cfg *Settings, m *RelayMonitor) *relayRuntime {
	if m == nil {
		m = NewRelayMonitor(nil, RelayMonitorOptions{})
	}
	rt := &relayRuntime{cfg: cfg, monitor: m, ctx: ctx, configs: map[string]relayRuntimeConfig{}, next: map[string]time.Time{}, active: map[string]bool{}, manual: map[string]bool{}, blocked: map[string]bool{}, wake: make(chan struct{}, 1)}
	rt.sync(cfg.Snapshot())
	return rt
}
func (rt *relayRuntime) signal() {
	select {
	case rt.wake <- struct{}{}:
	default:
	}
}
func relayRuntimeConfigFor(r *RelaySettings) relayRuntimeConfig {
	return relayRuntimeConfig{RelayAccess{URL: r.ActivationURL, Interface: r.Interface, SourceIP: r.SourceIP, PathConfirmed: r.PathConfirmed}, r.Enabled, max(30, r.MonitorInterval)}
}
func (rt *relayRuntime) sync(g Settings) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	// Snapshots can arrive out of order when the scheduler races an API edit.
	// Once reconciled, a committed revision must never be replaced by older intent.
	if g.Revision < rt.revision {
		return nil
	}
	rt.revision = g.Revision
	changed := false
	hasBlocked := false
	for id, r := range g.Relays {
		if r == nil {
			continue
		}
		desired := relayRuntimeConfigFor(r)
		old, exists := rt.configs[id]
		if exists && old == desired && !rt.blocked[id] {
			continue
		}
		if err := rt.monitor.Configure(id, desired.access, desired.enabled); err != nil {
			// A canceled operation may still reserve the new access under another ID.
			// Never keep using this resource's OLD credential after saving a new one.
			if !rt.blocked[id] || old != desired {
				rt.monitor.Remove(id)
				changed = true
			}
			rt.configs[id] = desired
			rt.blocked[id] = true
			delete(rt.manual, id)
			hasBlocked = true
			continue
		}
		delete(rt.blocked, id)
		delete(rt.manual, id)
		rt.configs[id] = desired
		rt.next[id] = time.Now()
		changed = true
	}
	for id := range rt.configs {
		if g.Relays[id] == nil {
			rt.monitor.Remove(id)
			delete(rt.configs, id)
			delete(rt.next, id)
			delete(rt.manual, id)
			delete(rt.blocked, id)
			changed = true
		}
	}
	if changed {
		rt.signal()
	}
	if hasBlocked {
		return errors.New("Relay access configuration is blocked while previous work completes")
	}
	return nil
}
func (rt *relayRuntime) run() {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	defer func() { rt.monitor.Close(); rt.work.Wait() }()
	for {
		if rt.ctx.Err() != nil {
			return
		}
		rt.sync(rt.cfg.Snapshot())
		rt.dispatch()
		select {
		case <-rt.ctx.Done():
			return
		case <-timer.C:
		case <-rt.wake:
		}
	}
}
func (rt *relayRuntime) dispatch() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.ctx.Err() != nil {
		return
	}
	ids := make([]string, 0, len(rt.configs))
	for id := range rt.configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := rt.configs[id]
		// Bound concurrent provider requests across resources, not only per ID.
		if len(rt.active) >= 4 {
			break
		}
		if rt.blocked[id] || !c.enabled || rt.active[id] || time.Now().Before(rt.next[id]) {
			continue
		}
		manual := rt.manual[id]
		delete(rt.manual, id)
		rt.active[id] = true
		rt.next[id] = time.Now().Add(time.Duration(c.interval) * time.Second)
		rt.work.Add(1)
		go func(id string, c relayRuntimeConfig, manual bool) {
			defer rt.work.Done()
			status := rt.monitor.Reconcile(rt.ctx, id, manual)
			rt.mu.Lock()
			delete(rt.active, id)
			if current, ok := rt.configs[id]; ok && current == c {
				delay := time.Duration(c.interval) * time.Second
				if status.State == "debouncing" && delay > 10*time.Second {
					delay = 10 * time.Second
				}
				if status.State == "cooldown" && status.NextAttemptAt.After(time.Now()) {
					if remaining := time.Until(status.NextAttemptAt); remaining < delay {
						delay = remaining
					}
				}
				rt.next[id] = time.Now().Add(max(delay, time.Second))
			}
			rt.mu.Unlock()
			rt.signal()
		}(id, c, manual)
	}
}
func (rt *relayRuntime) requestManual(id string) (bool, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	c, ok := rt.configs[id]
	if !ok {
		return false, apiProblem(404, "Relay not found")
	}
	if rt.blocked[id] {
		return false, apiProblem(503, "Relay access configuration is pending")
	}
	if rt.ctx.Err() != nil {
		return false, apiProblem(503, "Relay runtime is stopping")
	}
	if !c.enabled {
		return false, apiProblem(409, "Relay is disabled")
	}
	if err := validateRelaySource(c.access); err != nil {
		return false, apiProblem(409, "confirm the Relay source IP or interface before activation")
	}
	if c.access.URL == "" {
		return false, apiProblem(409, "Relay activation link is not configured")
	}
	if rt.active[id] || rt.manual[id] {
		return false, nil
	}
	if rt.monitor.Status(id).NextAttemptAt.After(time.Now()) {
		return false, apiProblem(429, "Relay activation cooldown is active")
	}
	rt.manual[id] = true
	rt.next[id] = time.Now()
	rt.signal()
	return true, nil
}
func (rt *relayRuntime) status(id string) RelayMonitorStatus {
	return rt.statusFor(id, nil)
}

// statusFor binds observations to the private configuration behind DTO metadata.
// Check identity and read status under one lock so reconciliation cannot intervene.
func (rt *relayRuntime) statusFor(id string, desired *RelaySettings) RelayMonitorStatus {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if desired != nil {
		c := relayRuntimeConfigFor(desired)
		if current, ok := rt.configs[id]; !ok || current != c {
			return RelayMonitorStatus{Enabled: c.enabled, Configured: c.access.URL != "", State: "applying", ActivationOutcome: "unknown"}
		}
	}
	st := rt.monitor.Status(id)
	if rt.blocked[id] {
		c := rt.configs[id]
		st = RelayMonitorStatus{Enabled: c.enabled, Configured: c.access.URL != "", State: "configuration_blocked", ActivationOutcome: "unknown", Error: "Relay access configuration is pending"}
	}
	if rt.active[id] || rt.manual[id] {
		st.InFlight = true
	}
	return st
}

// StartRelays must run before serving HTTP; it deliberately is not called by
// NewServer, so constructing handlers or offline renderers cannot trigger WAN IO.
func (srv *Server) StartRelays(ctx context.Context) {
	srv.relays = newRelayRuntime(ctx, srv.cfg, nil)
	go srv.relays.run()
}
func (srv *Server) safeRelays(g Settings) []publicRelay {
	rows := publicRelays(g)
	for i := range rows {
		row := &rows[i]
		if srv.relays != nil {
			row.Status = srv.relays.statusFor(row.ID, g.Relays[row.ID])
		} else {
			row.Status = RelayMonitorStatus{Enabled: row.Enabled, Configured: row.Configured, State: "not_started", ActivationOutcome: "unknown"}
			if !row.Enabled {
				row.Status.State = "disabled"
			}
		}
		// Metadata and runtime snapshots may straddle a transaction. Never present
		// an old enabled/confirmed result as the new desired resource's status.
		if row.Status.Enabled != row.Enabled || row.Status.Configured != row.Configured {
			row.Status = RelayMonitorStatus{Enabled: row.Enabled, Configured: row.Configured, State: "applying", ActivationOutcome: "unknown"}
		}
	}
	return rows
}
func (srv *Server) relayActivationHandler(w http.ResponseWriter, r *http.Request) {
	if !srv.mutationAuthorized(w, r) {
		return
	}
	var body struct{}
	if !decodeProfilePatch(w, r, &body) {
		return
	}
	srv.orch.applyMu.Lock()
	defer srv.orch.applyMu.Unlock()
	if srv.relays == nil {
		writeJSON(w, 503, map[string]any{"queued": false, "error": "Relay runtime is not started"})
		return
	}
	srv.relays.sync(srv.cfg.Snapshot())
	queued, err := srv.relays.requestManual(r.PathValue("id"))
	if err != nil {
		code := 409
		if problem, ok := err.(profileAPIError); ok {
			code = problem.code
		}
		writeJSON(w, code, map[string]any{"queued": false, "error": err.Error()})
		return
	}
	code := 200
	if queued {
		code = 202
	}
	writeJSON(w, code, map[string]any{"queued": queued, "status": srv.relays.status(r.PathValue("id"))})
}
