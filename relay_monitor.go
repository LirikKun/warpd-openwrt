package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

// RelayMonitorOptions bounds work, not monitoring frequency; the owner schedules
// Reconcile once per resource independently from proxy/profile health checks.
// Nonpositive values select defaults. MaxAttempts is capped at three.
type RelayMonitorOptions struct {
	Debounce    time.Duration
	Cooldown    time.Duration
	RetryBase   time.Duration
	MaxAttempts int
}

type RelayMonitorStatus struct {
	Enabled           bool      `json:"enabled"`
	Configured        bool      `json:"configured"`
	State             string    `json:"state"`
	ObservedIP        string    `json:"observed_ip,omitempty"`
	LastAttemptIP     string    `json:"last_attempt_ip,omitempty"`
	LastObservationAt time.Time `json:"last_observation_at,omitempty"`
	LastAttemptAt     time.Time `json:"last_attempt_at,omitempty"`
	NextAttemptAt     time.Time `json:"next_attempt_at,omitempty"`
	InFlight          bool      `json:"in_flight"`
	ActivationOutcome string    `json:"activation_outcome"`
	Error             string    `json:"error,omitempty"`
	BindingVerified   bool      `json:"binding_verified"`
	TunnelVerified    bool      `json:"tunnel_verified"`
}

type relayMonitorWork struct {
	cancel     context.CancelFunc
	capability [sha256.Size]byte
}
type relayMonitorResource struct {
	access    RelayAccess
	status    RelayMonitorStatus
	candidate string
	since     time.Time
	doneIP    string
	attempts  int
	work      *relayMonitorWork
	removed   bool
}

// RelayMonitor owns no accounts, engines, listeners, routing or goroutines.
// Reconciliation is synchronous and bounded. Concurrent calls for the same ID
// return its in-flight snapshot immediately; different IDs can progress freely.
// Configure/Remove/Close cancel pending work and invalidate its result identity.
type RelayMonitor struct {
	mu        sync.Mutex
	resources map[string]*relayMonitorResource
	closed    bool
	opts      RelayMonitorOptions
	now       func() time.Time
	observe   func(context.Context, RelayAccess) (RelayObservation, error)
	activate  func(context.Context, RelayAccess) (RelayActivation, error)
}

func NewRelayMonitor(c *RelayClient, opts RelayMonitorOptions) *RelayMonitor {
	if c == nil {
		c = &RelayClient{}
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 10 * time.Second
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = time.Minute
	}
	if opts.RetryBase <= 0 {
		opts.RetryBase = 30 * time.Second
	}
	if opts.Debounce > time.Hour {
		opts.Debounce = time.Hour
	}
	if opts.Cooldown > 24*time.Hour {
		opts.Cooldown = 24 * time.Hour
	}
	if opts.RetryBase > time.Hour {
		opts.RetryBase = time.Hour
	}
	if opts.MaxAttempts <= 0 || opts.MaxAttempts > 3 {
		opts.MaxAttempts = 3
	}
	return &RelayMonitor{resources: make(map[string]*relayMonitorResource), opts: opts, now: time.Now, observe: c.Observe, activate: c.Activate}
}

// Configure is idempotent. An empty URL can be saved, but cannot activate.
// Cooldown survives edits/disable/re-enable; changing desired state must not
// accidentally turn repeated settings writes into repeated activation requests.
func (m *RelayMonitor) Configure(id string, a RelayAccess, enabled bool) error {
	if id == "" {
		return errors.New("Relay resource ID required")
	}
	if a.URL != "" {
		if err := ValidateRelayActivationURL(a.URL); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("Relay monitor closed")
	}
	capability := sha256.Sum256([]byte(a.URL))
	for key, resource := range m.resources {
		if key != id && a.URL != "" && (resource.access.URL == a.URL || resource.work != nil && resource.work.capability == capability) {
			return errors.New("Relay access resource already configured")
		}
	}
	old := m.resources[id]
	if old != nil && !old.removed && old.access == a && old.status.Enabled == enabled {
		return nil
	}
	state := "disabled"
	if enabled {
		state = "waiting"
	}
	r := &relayMonitorResource{access: a, status: RelayMonitorStatus{Enabled: enabled, Configured: a.URL != "", State: state, ActivationOutcome: "unknown"}}
	if old != nil {
		r.work = old.work
		if r.work != nil {
			r.work.cancel()
		}
		r.status.LastAttemptAt = old.status.LastAttemptAt
		r.status.LastAttemptIP = old.status.LastAttemptIP
		r.status.NextAttemptAt = old.status.NextAttemptAt
		if old.access == a {
			r.doneIP = old.doneIP
			r.attempts = old.attempts
			r.status.ActivationOutcome = old.status.ActivationOutcome
		}
	}
	m.resources[id] = r
	return nil
}

func (m *RelayMonitor) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.resources[id]; r != nil && r.work != nil {
		r.work.cancel()
		// Retain only the cancellation gate, not the credential, until completion.
		m.resources[id] = &relayMonitorResource{work: r.work, removed: true, status: relayMissingStatus()}
	} else {
		delete(m.resources, id)
	}
}

func (m *RelayMonitor) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, r := range m.resources {
		if r.work != nil {
			r.work.cancel()
		}
	}
	m.resources = make(map[string]*relayMonitorResource)
}

func relayMissingStatus() RelayMonitorStatus {
	return RelayMonitorStatus{State: "not_configured", ActivationOutcome: "unknown"}
}
func (m *RelayMonitor) Status(id string) RelayMonitorStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.resources[id]; r != nil {
		return r.status
	}
	return relayMissingStatus()
}

// Reconcile never bypasses debounce, cooldown, source confirmation or IP
// observation, even for manual requests. Manual only permits retrying an already
// attempted/exhausted stable IP after its cooldown. A nil error from Activate
// remains unverified, and is not automatically repeated for the same stable IP.
func (m *RelayMonitor) Reconcile(parent context.Context, id string, manual bool) (out RelayMonitorStatus) {
	m.mu.Lock()
	r := m.resources[id]
	if r == nil {
		m.mu.Unlock()
		return relayMissingStatus()
	}
	if m.closed || !r.status.Enabled || parent.Err() != nil {
		out = r.status
		m.mu.Unlock()
		return out
	}
	if r.work != nil {
		out = r.status
		out.InFlight = true
		m.mu.Unlock()
		return out
	}
	if r.access.URL == "" {
		r.status.State = "not_configured"
		out = r.status
		m.mu.Unlock()
		return out
	}
	if err := validateRelaySource(r.access); err != nil {
		r.status.State = "path_unconfirmed"
		r.status.Error = "Relay source path is not confirmed"
		out = r.status
		m.mu.Unlock()
		return out
	}
	ctx, cancel := context.WithTimeout(parent, 2*relayRequestTimeout)
	w := &relayMonitorWork{cancel: cancel, capability: sha256.Sum256([]byte(r.access.URL))}
	r.work = w
	r.status.InFlight = true
	access := r.access
	m.mu.Unlock()
	defer func() {
		contextErr := ctx.Err()
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()
		current := m.resources[id]
		if current == r && contextErr != nil {
			current.status.State = "canceled"
		}
		if current != nil && current.work == w {
			current.work = nil
			current.status.InFlight = false
		}
		if current != nil {
			out = current.status
			if current.removed {
				delete(m.resources, id)
			}
		} else {
			out = relayMissingStatus()
		}
	}()
	observation, err := m.observe(ctx, access)
	shouldActivate := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.resources[id] != r {
			return false
		}
		if ctx.Err() != nil {
			r.status.State = "canceled"
			return false
		}
		ip, ipOK := relayObservationAddress(observation.PublicIP, access)
		if err != nil || !ipOK {
			r.status.State = "ip_unknown"
			r.status.ObservedIP = ""
			r.status.Error = "Relay public IP unavailable"
			r.candidate = ""
			r.since = time.Time{}
			return false
		}
		now := m.now()
		r.status.Error = ""
		r.status.ObservedIP = ip.Unmap().String()
		r.status.LastObservationAt = now
		if r.candidate != r.status.ObservedIP {
			r.candidate = r.status.ObservedIP
			r.since = now
		}
		if now.Sub(r.since) < m.opts.Debounce {
			r.status.State = "debouncing"
			return false
		}
		if now.Before(r.status.NextAttemptAt) {
			r.status.State = "cooldown"
			return false
		}
		if r.doneIP == r.candidate && !manual {
			r.status.State = "unverified"
			return false
		}
		if r.status.LastAttemptIP != r.candidate || manual {
			r.attempts = 0
		}
		if r.attempts >= m.opts.MaxAttempts {
			r.status.State = "retry_exhausted"
			return false
		}
		r.attempts++
		r.doneIP = ""
		r.status.LastAttemptAt = now
		r.status.LastAttemptIP = r.candidate
		r.status.NextAttemptAt = now.Add(m.opts.Cooldown)
		r.status.State = "activating"
		return true
	}()
	if !shouldActivate || ctx.Err() != nil {
		return
	}
	_, activationErr := m.activate(ctx, access)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resources[id] != r {
		return
	}
	if ctx.Err() != nil {
		r.status.State = "canceled"
		return
	}
	if activationErr != nil {
		delay := m.opts.RetryBase * time.Duration(1<<uint(r.attempts-1))
		if delay > m.opts.Cooldown {
			r.status.NextAttemptAt = r.status.LastAttemptAt.Add(delay)
		}
		r.status.State = "error"
		r.status.Error = "Relay activation failed"
		r.status.ActivationOutcome = "error"
		return
	}
	r.doneIP = r.candidate
	r.status.State = "unverified"
	r.status.ActivationOutcome = "unverified"
	return
}
