package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMihomoChild(t *testing.T) {
	if os.Getenv("WARPD_TEST_CHILD") != "1" {
		return
	}
	if os.Getenv("WARPD_TEST_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if err := os.WriteFile(os.Getenv("WARPD_TEST_READY"), nil, 0600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func mihomoSettings() Settings {
	g := Settings{Proxies: map[string]*ProxySettings{}}
	for _, key := range []string{"warp", "masque", "masqueh2", "m2w"} {
		g.Proxies[key] = new(ProxySettings).defaults(key)
	}
	return g
}

func newTestMihomo(t *testing.T) (*MihomoMan, func()) {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	m := &MihomoMan{configPath: filepath.Join(dir, "mihomo.yaml")}
	m.command = func(path string) *exec.Cmd {
		if path != m.configPath {
			t.Errorf("command config path = %q, want %q", path, m.configPath)
		}
		_ = os.Remove(ready)
		cmd := exec.Command(os.Args[0], "-test.run=^TestMihomoChild$")
		cmd.Env = append(os.Environ(), "WARPD_TEST_CHILD=1", "WARPD_TEST_READY="+ready)
		return cmd
	}
	m.reload = func() error { return nil }
	t.Cleanup(m.Stop)
	return m, func() {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(ready); err == nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("helper did not become ready")
	}
}

func TestMihomoUsesInstanceHooks(t *testing.T) {
	m, ready := newTestMihomo(t)
	g := mihomoSettings()
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	conf, err := os.ReadFile(m.configPath)
	if err != nil || string(conf) != mihomoConfText(g, endpoints{}, warpAccount{}) {
		t.Fatalf("instance config: %q, %v", conf, err)
	}
	if s := m.Snapshot(); !s.Running || s.PID <= 0 {
		t.Fatalf("started status = %+v", s)
	}
	called := false
	m.reload = func() error { called = true; return nil }
	g.Proxies["warp"].Enabled = true
	g.Proxies["warp"].Endpoint = "192.0.2.1:2408"
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("instance reload hook was not used")
	}
}

func TestMihomoChildExitClearsStatusAndRestartsSameConfig(t *testing.T) {
	m, ready := newTestMihomo(t)
	g := mihomoSettings()
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	before := m.Snapshot()
	m.mu.Lock()
	child := m.cmd
	m.mu.Unlock()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for m.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := m.Snapshot(); got.Running || got.PID != 0 {
		t.Fatalf("exited child still reported alive: %+v", got)
	}
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	if got := m.Snapshot(); !got.Running || got.PID == before.PID {
		t.Fatalf("same-config Ensure did not replace dead child: before=%+v after=%+v", before, got)
	}
}

func TestMihomoGenerationTracksSuccessfulApplies(t *testing.T) {
	m, ready := newTestMihomo(t)
	g := mihomoSettings()
	ensure := func(want uint64) MihomoStatus {
		t.Helper()
		if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
			t.Fatal(err)
		}
		got := m.Snapshot()
		if got.Generation != want {
			t.Fatalf("generation = %d, want %d", got.Generation, want)
		}
		return got
	}
	initial := ensure(1)
	ready()
	if got := ensure(1); got.PID != initial.PID {
		t.Fatal("same config unexpectedly restarted child")
	}
	g.Proxies["warp"].Enabled = true
	g.Proxies["warp"].Endpoint = "192.0.2.1:2408"
	if got := ensure(2); got.PID != initial.PID {
		t.Fatal("successful reload unexpectedly restarted child")
	}
	ensure(2)
	m.Stop()
	if got := m.Snapshot(); got.Generation != 2 || got.Running || got.PID != 0 {
		t.Fatalf("stopped status = %+v", got)
	}
	if got := ensure(3); got.PID == initial.PID {
		t.Fatal("restart did not replace child")
	}
	ready()
}

func TestMihomoFailedApplyKeepsLastSuccessfulConfig(t *testing.T) {
	m, ready := newTestMihomo(t)
	g := mihomoSettings()
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	before := m.Snapshot()
	m.mu.Lock()
	last := m.lastConf
	m.mu.Unlock()
	workingCommand := m.command
	m.command = func(string) *exec.Cmd { return exec.Command(filepath.Join(t.TempDir(), "missing")) }
	m.reload = func() error { return errors.New("reload rejected") }
	g.Proxies["warp"].Enabled = true
	g.Proxies["warp"].Endpoint = "192.0.2.1:2408"
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Fatal("failed restart returned success")
	}
	m.mu.Lock()
	gotConf := m.lastConf
	m.mu.Unlock()
	if gotConf != last {
		t.Fatal("failed apply replaced last successful config")
	}
	if got := m.Snapshot(); got.Generation != before.Generation || got.Running || got.PID != 0 {
		t.Fatalf("failed restart status = %+v, before = %+v", got, before)
	}
	m.command = workingCommand
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	if got := m.Snapshot(); got.Generation != before.Generation+1 || !got.Running {
		t.Fatalf("retry status = %+v", got)
	}
}

func TestMihomoStopReapsPromptlyWithoutWaitMutexDeadlock(t *testing.T) {
	m, ready := newTestMihomo(t)
	if err := m.Ensure(mihomoSettings(), endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	m.mu.Lock()
	child := m.cmd
	m.mu.Unlock()
	stopped := make(chan struct{})
	go func() { m.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop did not observe child exit promptly (or waits on waiter while holding its mutex)")
	}
	if got := m.Snapshot(); got.Running || got.PID != 0 {
		t.Fatalf("stopped status = %+v", got)
	}
	m.mu.Lock()
	retained := m.cmd != nil
	m.mu.Unlock()
	if retained {
		t.Fatal("Stop retained finished command")
	}
	if err := child.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("child was not reaped by Stop: %v", err)
	}
	m.Stop()
}

func TestMihomoM2WListenerRequiresActualChain(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		outer, inner, warp, masque string
		want                       bool
	}{
		{name: "missing endpoints"},
		{name: "outer only", outer: "192.0.2.1:443"},
		{name: "inner only", inner: "192.0.2.2:2408"},
		{name: "equal endpoints", outer: "192.0.2.1:443", inner: "192.0.2.1:443"},
		{name: "equal fallback", warp: "192.0.2.1:443"},
		{name: "explicit chain", outer: "192.0.2.1:443", inner: "192.0.2.2:2408", want: true},
		{name: "fallback chain", masque: "192.0.2.1:443", warp: "192.0.2.2:2408", want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := mihomoSettings()
			p := g.Proxies["m2w"]
			p.Enabled, p.Outer, p.Inner = true, tt.outer, tt.inner
			e := endpoints{warp: tt.warp, masque: map[string]string{"masque": tt.masque}}
			conf := mihomoConfText(g, e, warpAccount{})
			chain := strings.Contains(conf, "  - name: \"M2W INNER\"")
			listener := strings.Contains(conf, "    proxy: \"M2W INNER\"")
			if chain != tt.want || listener != tt.want {
				t.Fatalf("chain=%v listener=%v, want both %v", chain, listener, tt.want)
			}
		})
	}
}

func TestMihomoListenersHonorQuotedBindAddress(t *testing.T) {
	g := mihomoSettings()
	binds := map[string]string{
		"warp":     "127.0.0.1",
		"masque":   "::1",
		"masqueh2": "0.0.0.0",
		"m2w":      "127.0.0.1\"\nexternal-controller: 0.0.0.0:9099",
	}
	for key, bind := range binds {
		p := g.Proxies[key]
		p.Enabled, p.Listen, p.Endpoint = true, bind, "192.0.2.1:443"
	}
	g.Proxies["m2w"].Outer = "192.0.2.1:443"
	g.Proxies["m2w"].Inner = "192.0.2.2:2408"
	conf := mihomoConfText(g, endpoints{}, warpAccount{})
	for key, bind := range binds {
		p := g.Proxies[key]
		start := strings.Index(conf, fmt.Sprintf("  - name: l%d\n", p.Port))
		if start < 0 {
			t.Fatalf("missing %s listener", key)
		}
		block := conf[start:]
		if next := strings.Index(block[1:], "  - name:"); next >= 0 {
			block = block[:next+1]
		}
		if !strings.Contains(block, fmt.Sprintf("    listen: %q\n", bind)) {
			t.Errorf("%s listener missing quoted bind %q: %s", key, bind, block)
		}
	}
	if strings.Count(conf, "\nexternal-controller:") != 1 || !strings.Contains(conf, "\nexternal-controller: 127.0.0.1:9099\n") {
		t.Fatal("controller address was changed or bind injected extra YAML")
	}
}

func TestMihomoOldWaiterDoesNotClearReplacement(t *testing.T) {
	m, ready := newTestMihomo(t)
	g := mihomoSettings()
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	m.reload = func() error { return errors.New("force replacement") }
	for i := 0; i < 5; i++ {
		before := m.Snapshot()
		g.Proxies["warp"].Enabled = true
		g.Proxies["warp"].Endpoint = fmt.Sprintf("192.0.2.%d:2408", i+1)
		if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
			t.Fatal(err)
		}
		ready()
		// The old waiter was blocked on mu until Ensure installed the
		// replacement. Give it a scheduling turn before checking status.
		deadline := time.Now().Add(10 * time.Millisecond)
		for time.Now().Before(deadline) {
			got := m.Snapshot()
			if !got.Running || got.PID == 0 || got.PID == before.PID || got.Generation != before.Generation+1 {
				t.Fatalf("old waiter corrupted replacement: before=%+v after=%+v", before, got)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestMihomoStopKillsChildIgnoringTermination(t *testing.T) {
	m, ready := newTestMihomo(t)
	command := m.command
	m.command = func(path string) *exec.Cmd {
		cmd := command(path)
		cmd.Env = append(cmd.Env, "WARPD_TEST_IGNORE_TERM=1")
		return cmd
	}
	if err := m.Ensure(mihomoSettings(), endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	m.mu.Lock()
	child := m.cmd
	m.mu.Unlock()
	start := time.Now()
	m.Stop()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Stop exceeded bound: %s", elapsed)
	}
	if got := m.Snapshot(); got.Running || got.PID != 0 {
		t.Fatalf("stopped status = %+v", got)
	}
	if err := child.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("termination-resistant child not reaped: %v", err)
	}
}

func TestMihomoFailedInitialStartLeavesEmptyStatus(t *testing.T) {
	m, _ := newTestMihomo(t)
	m.command = func(string) *exec.Cmd { return exec.Command(filepath.Join(t.TempDir(), "missing")) }
	if err := m.Ensure(mihomoSettings(), endpoints{}, warpAccount{}); err == nil {
		t.Fatal("missing executable returned success")
	}
	if got := m.Snapshot(); got != (MihomoStatus{}) {
		t.Fatalf("failed initial start status = %+v", got)
	}
	if m.lastConf != "" {
		t.Fatal("failed initial start retained unapplied config")
	}
}

func TestMihomoConfigWriteFailurePreservesCurrentChild(t *testing.T) {
	m, ready := newTestMihomo(t)
	g := mihomoSettings()
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		t.Fatal(err)
	}
	ready()
	before := m.Snapshot()
	last := m.lastConf
	m.configPath = t.TempDir() // Writing a file over a directory must fail.
	g.Proxies["warp"].Enabled = true
	g.Proxies["warp"].Endpoint = "192.0.2.1:2408"
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err == nil {
		t.Fatal("config write returned success")
	}
	if got := m.Snapshot(); got != before || m.lastConf != last {
		t.Fatalf("config write failure changed applied status: before=%+v after=%+v", before, got)
	}
}

func TestMihomoConcurrentStatusDuringLifecycle(t *testing.T) {
	m, ready := newTestMihomo(t)
	g := mihomoSettings()
	done := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			var generation uint64
			for {
				select {
				case <-done:
					return
				default:
				}
				s := m.Snapshot()
				if s.Running != (s.PID > 0) || s.Generation < generation {
					t.Errorf("inconsistent status: %+v, previous generation %d", s, generation)
					return
				}
				generation = s.Generation
				_ = m.Running()
			}
		}()
	}
	defer func() { close(done); readers.Wait() }()
	for i := 0; i < 10; i++ {
		g.Proxies["warp"].Enabled = true
		g.Proxies["warp"].Endpoint = fmt.Sprintf("192.0.2.%d:2408", i+1)
		if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
			t.Fatal(err)
		}
		ready()
		if i%2 == 0 {
			var stops sync.WaitGroup
			for j := 0; j < 3; j++ {
				stops.Add(1)
				go func() { defer stops.Done(); m.Stop() }()
			}
			stops.Wait()
		}
	}
}

func TestMihomoReapedChildCannotRemainLiveWhileWaiterIsPending(t *testing.T) {
	// Reproduce the interval after cmd.Wait closes done but before its
	// goroutine obtains mu. The process really exited and was reaped.
	child := exec.Command(os.Args[0], "-test.run=^$")
	child.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"Snapshot", "Running", "Ensure"} {
		t.Run(operation, func(t *testing.T) {
			m, ready := newTestMihomo(t)
			g := mihomoSettings()
			m.cmd, m.pid, m.running = child, child.Process.Pid, true
			m.lastConf = mihomoConfText(g, endpoints{}, warpAccount{})
			m.generation = 1
			m.done = make(chan struct{})
			close(m.done)
			switch operation {
			case "Snapshot":
				if got := m.Snapshot(); got.Running || got.PID != 0 || got.Generation != 1 {
					t.Fatalf("reaped child status = %+v", got)
				}
			case "Running":
				if m.Running() {
					t.Fatal("reaped child reported running")
				}
			case "Ensure":
				if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
					t.Fatal(err)
				}
				if got := m.Snapshot(); !got.Running || got.PID == child.Process.Pid || got.Generation != 2 {
					t.Fatalf("same-config Ensure reused reaped child: %+v", got)
				}
				ready()
			}
		})
	}
}

func TestMihomoSnapshot(t *testing.T) {
	m := &MihomoMan{}
	if got := m.Snapshot(); got != (MihomoStatus{}) {
		t.Fatalf("initial status = %+v", got)
	}
	m.mu.Lock()
	m.pid, m.running, m.generation = 123, true, 7
	m.mu.Unlock()
	want := MihomoStatus{PID: 123, Running: true, Generation: 7}
	if got := m.Snapshot(); got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}
