package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"time"
)

type relayAccountStatus struct {
	State                string `json:"state"`
	Pending              bool   `json:"pending"`
	Error                string `json:"error,omitempty"`
	CredentialMode       string `json:"credential_mode"`
	CredentialConfigured bool   `json:"credential_configured"`
	RuntimeAvailable     bool   `json:"runtime_available"`
}
type relayAccountJob struct {
	revision   uint64
	state, err string
	cancel     context.CancelFunc
}
type relayAccountJobs struct {
	mu       sync.Mutex
	jobs     map[string]*relayAccountJob
	active   *relayAccountJob
	closed   bool
	generate func(context.Context) (RelayCredential, error)
	work     sync.WaitGroup
}

func newRelayAccountJobs() *relayAccountJobs {
	return &relayAccountJobs{jobs: map[string]*relayAccountJob{}, generate: func(ctx context.Context) (RelayCredential, error) {
		return generateRelayAccount(ctx, warpScoutBinary(), os.TempDir())
	}}
}

// Close is terminal: it cancels and reaps an active registration. Restart never
// restores job intent, so there is no automatic re-registration after a crash.
func (a *relayAccountJobs) Close() {
	a.mu.Lock()
	a.closed = true
	if a.active != nil {
		a.active.cancel()
	}
	a.mu.Unlock()
	a.work.Wait()
}
func (a *relayAccountJobs) status(id string, p *ProxySettings) relayAccountStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := relayAccountStatus{State: "idle", CredentialMode: p.Credential.Mode, CredentialConfigured: relayCredentialConfigured(p)}
	if j := a.jobs[id]; j != nil {
		s.State = j.state
		s.Error = j.err
		s.Pending = j.state == "pending"
	}
	return s
}

// Caller owns applyMu. Hold the job reservation until its goroutine is reaped,
// including cancellation, so a second click cannot start a concurrent process.
func (srv *Server) startRelayAccount(id string, revision uint64) (bool, error) {
	a := srv.accounts
	a.mu.Lock()
	defer a.mu.Unlock()
	g := srv.cfg.Snapshot()
	if g.ProfileSchema != profileSchema || g.ProfilePath == "" || g.ProfileError != "" {
		return false, apiProblem(409, "profile store is not initialized")
	}
	p := g.Proxies[id]
	if p == nil {
		return false, apiProblem(404, "profile not found")
	}
	if profileType(id, p) != "relay-warp" {
		return false, apiProblem(409, "credentials require Relay-WARP")
	}
	if a.closed {
		return false, apiProblem(503, "Relay account jobs stopped")
	}
	if j := a.jobs[id]; j != nil && a.active == j && j.revision == revision && j.state == "pending" {
		return false, nil
	}
	if g.Revision != revision {
		return false, apiProblem(409, "configuration revision changed")
	}
	if a.active != nil {
		return false, apiProblem(409, "Relay account generation already pending")
	}
	// A single process and at most one status per current profile bound RAM use.
	for key := range a.jobs {
		if g.Proxies[key] == nil {
			delete(a.jobs, key)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	j := &relayAccountJob{revision: revision, state: "pending", cancel: cancel}
	a.jobs[id] = j
	a.active = j
	a.work.Add(1)
	go func() {
		defer a.work.Done()
		defer cancel()
		c, err := a.generate(ctx)
		srv.orch.applyMu.Lock()
		defer srv.orch.applyMu.Unlock()
		a.mu.Lock()
		defer a.mu.Unlock()
		defer func() {
			if a.active == j {
				a.active = nil
			}
		}()
		if j.state != "pending" {
			return
		}
		if ctx.Err() != nil || a.closed {
			j.state = "cancelled"
			return
		}
		current := srv.cfg.Snapshot()
		if current.Revision != j.revision || current.Proxies[id] == nil || profileType(id, current.Proxies[id]) != "relay-warp" {
			j.state = "stale"
			return
		}
		if err != nil || c.Mode != "generated" || validateRelayCredential(c) != nil {
			j.state = "error"
			j.err = "Relay account generation failed"
			return
		}
		err = srv.cfg.ChangeProfiles(func(g *Settings) error {
			if g.Revision != j.revision || g.Proxies[id] == nil {
				return fmt.Errorf("stale generation")
			}
			g.Proxies[id].Credential = c
			return nil
		})
		if err != nil {
			j.state = "error"
			j.err = "Relay credential save failed"
			return
		}
		srv.orch.closeProfileGates()
		mihomo.ReconcileRelayIntent(srv.cfg.Snapshot())
		getCheck(id).clearGeo()
		j.state = "succeeded"
	}()
	return true, nil
}
func (a *relayAccountJobs) cancel(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if j := a.jobs[id]; j != nil && j.state == "pending" {
		j.state = "cancelled"
		j.cancel()
	}
}

// This is the only production registration process for profile credentials.
// Keys never enter argv, stdout, stderr, logs or public errors. The pinned CLI
// register help establishes these flags; the output file is never shared.
func generateRelayAccount(ctx context.Context, binary, root string) (RelayCredential, error) {
	fail := func() (RelayCredential, error) {
		return RelayCredential{}, fmt.Errorf("Relay account generation failed")
	}
	stageRoot, err := openRelayStageRoot(root)
	if err != nil {
		return fail()
	}
	defer stageRoot.dir.Close()
	stage, err := stageRoot.create()
	if err != nil {
		return fail()
	}
	if err = stage.createOutput(); err != nil {
		_ = stage.cleanup()
		return fail()
	}
	childPath := stage.childDir()
	cmd := exec.CommandContext(ctx, binary, "register", "-a", "account.json", "-fresh", "-plain")
	cmd.Dir = childPath
	cmd.Env = append(scannerEnv(cmd.Environ()), "GOMEMLIMIT=32MiB", "NO_COLOR=1", "TERM=dumb")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	output, openErr := stage.openOutput()
	if openErr == nil {
		defer output.Close()
	}
	var raw []byte
	if openErr == nil {
		st, statErr := output.Stat()
		if statErr != nil || st.Size() > 64<<10 {
			openErr = fmt.Errorf("unsafe output")
		} else {
			raw, openErr = io.ReadAll(io.LimitReader(output, (64<<10)+1))
			if openErr == nil && len(raw) > 64<<10 {
				openErr = fmt.Errorf("oversized output")
			}
		}
	}
	cleanupErr := stage.cleanup()
	if cleanupErr != nil || runErr != nil || ctx.Err() != nil || openErr != nil {
		return fail()
	}
	// Only the pinned fresh-account envelope is accepted. Registration IDs,
	// tokens and optional MASQUE/outer WG fields are recognized then discarded;
	// only the primary device supplies tunnel parameters. Unknown fields and
	// nested outer chains still fail closed.
	type device struct {
		ID            string `json:"id"`
		Token         string `json:"token"`
		PrivateKey    string `json:"private_key"`
		PeerPublicKey string `json:"peer_public_key"`
		IPv4          string `json:"ipv4"`
		IPv6          string `json:"ipv6"`
	}
	var result struct {
		ID            string  `json:"id"`
		Token         string  `json:"token"`
		PrivateKey    string  `json:"private_key"`
		PeerPublicKey string  `json:"peer_public_key"`
		IPv4          string  `json:"ipv4"`
		IPv6          string  `json:"ipv6"`
		Masque        *device `json:"masque"`
		Outer         *device `json:"outer"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if privateProfileValue(d, reflect.TypeOf(result), 0) != nil {
		return fail()
	}
	if _, err = d.Token(); err != io.EOF {
		return fail()
	}
	if json.Unmarshal(raw, &result) != nil {
		return fail()
	}
	c := RelayCredential{Mode: "generated", PrivateKey: result.PrivateKey, PeerPublicKey: result.PeerPublicKey, TunnelIPv4: result.IPv4, TunnelIPv6: result.IPv6}
	if validateRelayCredential(c) != nil {
		return fail()
	}
	return c, nil
}
