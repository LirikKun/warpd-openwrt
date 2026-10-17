package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func orchestratorSettings() *Settings {
	s := &Settings{mu: &sync.RWMutex{}, DMERescanGap: 600, Proxies: map[string]*ProxySettings{}}
	for _, key := range []string{"warp", "masque", "masqueh2", "m2w"} {
		s.Proxies[key] = (&ProxySettings{}).defaults(key)
		s.Proxies[key].Enabled = true
	}
	return s
}

func TestOrchestratorPendingDedup(t *testing.T) {
	o := NewOrchestrator(orchestratorSettings())
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if o.requestScan("warp", "failed checks") {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("accepted %d duplicate pending scans; want 1", got)
	}
	if o.ForceScan("warp", "manual") {
		t.Error("manual scan bypassed pending dedup")
	}
	if !o.requestScan("masqueh2", "failed checks") {
		t.Error("independent pending target rejected")
	}
	if o.requestScan("both", "startup") {
		t.Error("overlapping aggregate scan accepted")
	}
}

func TestOrchestratorRejectsUnscannableTargets(t *testing.T) {
	for _, target := range []string{"invalid", "", "WARP", "warp", "masque", "masqueh2", "m2w", "both"} {
		t.Run("disabled/"+target, func(t *testing.T) {
			s := orchestratorSettings()
			for _, p := range s.Proxies {
				p.Enabled = false
			}
			o := NewOrchestrator(s)
			if o.requestScan(target, "automatic") {
				t.Error("accepted unsupported or disabled scan")
			}
			if !o.lastScan.IsZero() {
				t.Error("rejection updated last scan")
			}
		})
	}
	for _, target := range []string{"warp", "masque", "masqueh2", "m2w", "both"} {
		t.Run("pinned/"+target, func(t *testing.T) {
			s := orchestratorSettings()
			for _, p := range s.Proxies {
				p.Endpoint = "192.0.2.1:443"
				p.Inner = "192.0.2.2:2408"
				p.Outer = "192.0.2.3:443"
			}
			if NewOrchestrator(s).requestScan(target, "automatic") {
				t.Error("accepted wholly pinned automatic scan")
			}
		})
	}
}

func TestOrchestratorAutomaticCooldown(t *testing.T) {
	for _, gap := range []int{0, 120} {
		for _, target := range []string{"warp", "masque", "masqueh2"} {
			for _, reason := range []string{"DME exit", "SVO exit", "5 failed checks", "no endpoint"} {
				t.Run(fmt.Sprintf("%d/%s/%s", gap, target, reason), func(t *testing.T) {
					s := orchestratorSettings()
					s.DMERescanGap = gap
					o := NewOrchestrator(s)
					now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
					o.now = func() time.Time { return now }
					o.scanner = func([]string, time.Duration) (string, error) { return "", errors.New("failed") }
					o.runScan(target, reason)
					if !o.lastScan.Equal(now) {
						t.Errorf("last_scan = %v; want fake attempt time %v", o.lastScan, now)
					}
					if o.requestScan(target, reason) {
						t.Fatal("failed automatic attempt was not cooled down")
					}
					wait := max(60, gap)
					now = now.Add(time.Duration(wait-1) * time.Second)
					if o.requestScan(target, reason) {
						t.Fatal("accepted scan before cooldown elapsed")
					}
					now = now.Add(time.Second)
					if !o.requestScan(target, reason) {
						t.Fatal("cooldown did not expire at boundary")
					}
				})
			}
		}
	}
}

func TestOrchestratorCooldownIsolationAndForce(t *testing.T) {
	s := orchestratorSettings()
	o := NewOrchestrator(s)
	o.scanner = func([]string, time.Duration) (string, error) { return "", errors.New("failed") }
	o.runScan("masqueh2", "failed checks")
	if !o.requestScan("warp", "DME exit") {
		t.Error("failed H2 scan starved WARP cooldown")
	}
	if !o.ForceScan("masqueh2", "manual") {
		t.Error("explicit scan did not bypass cooldown")
	}
	if o.ForceScan("masqueh2", "manual") {
		t.Error("explicit scan bypassed pending protection")
	}
	o2 := NewOrchestrator(s)
	o2.scanner = o.scanner
	o2.runScan("warp", "failed checks")
	if !o2.ForceScan("warp", "DME exit") {
		t.Error("ForceScan retained automatic DME throttle")
	}
}

func TestOrchestratorBothSkipsCoolingTargets(t *testing.T) {
	s := orchestratorSettings()
	o := NewOrchestrator(s)
	var got []string
	o.scanner = func(args []string, _ time.Duration) (string, error) {
		for i := range args {
			if args[i] == "-p" {
				got = append(got, args[i+1])
				break
			}
		}
		return "", errors.New("failed")
	}
	o.runScan("masqueh2", "failed checks")
	got = nil
	if !o.requestScan("both", "startup") {
		t.Fatal("cooled H2 blocked independent dependencies")
	}
	o.runRequest(<-o.scanCh)
	if want := []string{"awg", "masque"}; !reflect.DeepEqual(got, want) {
		t.Errorf("protocols = %v; want %v", got, want)
	}
	if o.requestScan("both", "failed checks") {
		t.Error("all underlying attempts should be cooling")
	}
	if len(o.reserved) != 0 {
		t.Errorf("completed request leaked reservations: %v", o.reserved)
	}
}

func TestOrchestratorScannerDrainsBeforeWait(t *testing.T) {
	o := NewOrchestrator(orchestratorSettings())
	dir := t.TempDir()
	o.scoutCommand = func(ctx context.Context, _ []string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", "printf '%04000d\\n' 0; printf 'stderr-marker\\n' >&2; printf '192.0.2.1:2408\\n'")
		cmd.Dir = dir
		return cmd
	}
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	// Block logging after the first read. The child can still exit with more
	// than one read's worth of buffered output; Wait must not close that pipe.
	o.mu.Lock()
	go func() { out, err := o.runWarpScout(nil, 3*time.Second); done <- result{out, err} }()
	var got result
	select {
	case got = <-done:
		o.mu.Unlock()
		t.Errorf("scanner returned before its log reader completed")
	case <-time.After(100 * time.Millisecond):
		o.mu.Unlock()
		select {
		case got = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("scanner did not finish")
		}
	}
	want := strings.Repeat("0", 4000) + "\nstderr-marker\n192.0.2.1:2408\n"
	if got.err != nil {
		t.Errorf("scanner: %v", got.err)
	}
	if got.out != want {
		t.Errorf("scanner output truncated: got %d bytes, want %d", len(got.out), len(want))
	}
}

func TestOrchestratorLoopBusyAndReservationRelease(t *testing.T) {
	o := NewOrchestrator(orchestratorSettings())
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	o.scanner = func([]string, time.Duration) (string, error) {
		close(entered)
		<-release
		return "", errors.New("failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	if !o.requestScan("warp", "automatic") {
		cancel()
		t.Fatal("initial request rejected")
	}
	go func() { defer close(loopDone); o.runScanLoop(ctx) }()
	t.Cleanup(func() { cancel(); unblock(); <-loopDone })
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("queued scan did not start")
	}
	if !o.State().Scanning {
		t.Error("active scan missing from state")
	}
	if o.ForceScan("masqueh2", "manual") {
		t.Error("ForceScan bypassed global active/busy protection")
	}
	if o.requestScan("warp", "automatic") {
		t.Error("active duplicate accepted")
	}
	unblock()
	cancel()
	select {
	case <-loopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("loop did not stop")
	}
	if o.State().Scanning {
		t.Error("completed scan still marked active")
	}
	if len(o.reserved) != 0 {
		t.Errorf("completed scan leaked reservations: %v", o.reserved)
	}
	if o.lastScan.IsZero() {
		t.Error("failed executed scan did not record attempt")
	}
	if o.requestScan("warp", "no endpoint") {
		t.Error("failed loop attempt did not start cooldown")
	}
	if !o.ForceScan("warp", "manual") {
		t.Error("completed scan left pending reservation")
	}
}

func TestOrchestratorQueuedSettingsRevalidated(t *testing.T) {
	s := orchestratorSettings()
	o := NewOrchestrator(s)
	if !o.requestScan("warp", "automatic") {
		t.Fatal("initial request rejected")
	}
	s.mu.Lock()
	s.Proxies["warp"].Enabled = false
	s.mu.Unlock()
	called := false
	o.scanner = func([]string, time.Duration) (string, error) { called = true; return "", errors.New("failed") }
	o.runRequest(<-o.scanCh)
	if called || !o.lastScan.IsZero() {
		t.Error("disabled queued work executed or changed last_scan")
	}
	if len(o.reserved) != 0 {
		t.Errorf("discarded request leaked reservations: %v", o.reserved)
	}
}

func TestOrchestratorDispatch(t *testing.T) {
	tests := []struct {
		name, target string
		enabled      []string
		inner, outer string
		want         []string
	}{
		{"warp", "warp", []string{"warp"}, "", "", []string{"awg"}},
		{"quic-only", "masque", []string{"masque", "masqueh2"}, "", "", []string{"masque"}},
		{"h2-only", "masqueh2", []string{"masqueh2"}, "", "", []string{"masque-h2"}},
		{"chain-dependencies", "m2w", []string{"m2w"}, "", "", []string{"awg", "masque"}},
		{"chain-pinned-inner", "m2w", []string{"m2w"}, "192.0.2.1:2408", "", []string{"masque"}},
		{"chain-pinned-outer", "m2w", []string{"m2w"}, "", "192.0.2.1:443", []string{"awg"}},
		{"both-chain-only", "both", []string{"m2w"}, "", "", []string{"awg", "masque"}},
		{"both-enabled", "both", []string{"warp", "masqueh2", "m2w"}, "", "", []string{"awg", "masque", "masque-h2"}},
		{"invalid-noop", "invalid", nil, "", "", nil},
		{"disabled-noop", "warp", nil, "", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := orchestratorSettings()
			for _, p := range s.Proxies {
				p.Enabled = false
			}
			for _, key := range tt.enabled {
				s.Proxies[key].Enabled = true
			}
			s.Proxies["m2w"].Inner, s.Proxies["m2w"].Outer = tt.inner, tt.outer
			o := NewOrchestrator(s)
			var got []string
			o.scanner = func(args []string, _ time.Duration) (string, error) {
				for i := range args {
					if args[i] == "-p" {
						got = append(got, args[i+1])
						break
					}
				}
				return "", errors.New("fake failed scan")
			}
			o.runScan(tt.target, "automatic")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("protocols = %v; want %v", got, tt.want)
			}
			if len(tt.want) == 0 && !o.lastScan.IsZero() {
				t.Error("no-op updated last_scan")
			}
			if len(tt.want) > 0 && o.lastScan.IsZero() {
				t.Error("executed failure did not update last_scan")
			}
		})
	}
}

func TestOrchestratorEndpointCopies(t *testing.T) {
	stateMu.Lock()
	old := stateEp
	stateEp = endpoints{masque: map[string]string{}}
	stateMu.Unlock()
	t.Cleanup(func() { stateMu.Lock(); stateEp = old; stateMu.Unlock() })
	src := endpoints{warp: "192.0.2.1:2408", masque: map[string]string{"masque": "192.0.2.2:443"}}
	stateSetEndpoints(src)
	src.masque["masque"] = "source mutation"
	if got := stateGetEndpoints().masque["masque"]; got != "192.0.2.2:443" {
		t.Errorf("setter retained caller map: %q", got)
	}
	snapshot := stateGetEndpoints()
	snapshot.masque["masque"] = "snapshot mutation"
	if got := stateGetEndpoints().masque["masque"]; got != "192.0.2.2:443" {
		t.Errorf("getter leaked internal map: %q", got)
	}
	stateSetEndpoints(endpoints{warp: "192.0.2.3:2408"})
	if got := stateGetEndpoints().masque["masque"]; got != "192.0.2.2:443" {
		t.Errorf("nil map should preserve existing MASQUE endpoints: %q", got)
	}
}
