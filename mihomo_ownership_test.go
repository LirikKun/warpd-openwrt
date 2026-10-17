//go:build linux

package main

import (
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Different workers can run this package concurrently. Serialize only these
// real-controller fixtures; never weaken production ownership to accommodate it.
func lockOwnershipController(t *testing.T) {
	t.Helper()
	if os.Getenv("WARPD_OWNERSHIP_LOCK_HELD") == "1" {
		return
	}
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "warpd-mihomo-ownership-test.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			f.Close()
			t.Fatalf("ownership fixture lock: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() })
	// A just-reaped fixture may briefly retain a proc LISTEN row in WSL.
	deadline = time.Now().Add(time.Second)
	for {
		err := (&MihomoMan{}).checkCollisions(0, []mihomoListener{{"127.0.0.1", 9099}})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("controller fixture not isolated: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMihomoOwnershipChild(t *testing.T) {
	if os.Getenv("WARPD_OWNERSHIP_CHILD") != "1" {
		return
	}
	var listeners []net.Listener
	if path := os.Getenv("WARPD_OWNERSHIP_CONFIG"); path != "" {
		conf, err := os.ReadFile(path)
		if err != nil {
			os.Exit(2)
		}
		wants, err := mihomoListeners(string(conf))
		if err != nil {
			os.Exit(3)
		}
		for _, want := range wants {
			if os.Getenv("WARPD_OWNERSHIP_CONTROLLER_ONLY") == "1" && want.port != 9099 {
				continue
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(want.address, strconv.Itoa(want.port)))
			if err != nil {
				os.Exit(4)
			}
			listeners = append(listeners, ln)
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	for {
		<-signals
		for _, ln := range listeners {
			ln.Close()
		}
		if marker := os.Getenv("WARPD_OWNERSHIP_CLOSED"); marker != "" {
			_ = os.WriteFile(marker, nil, 0600)
		}
	}
}

func ownershipSleepingCommand() *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMihomoOwnershipChild$")
	cmd.Env = append(os.Environ(), "WARPD_OWNERSHIP_CHILD=1", "GORACE=atexit_sleep_ms=0")
	return cmd
}

func TestMihomoOwnershipLivingChildWithoutSocketsIsNotApplied(t *testing.T) {
	lockOwnershipController(t)
	m := &MihomoMan{configPath: filepath.Join(t.TempDir(), "mihomo.yaml"), command: func(string) *exec.Cmd { return ownershipSleepingCommand() }}
	defer m.Stop()
	start := time.Now()
	err := m.Ensure(mihomoSettings(), endpoints{}, warpAccount{PrivateKey: "synthetic-secret"})
	if err == nil {
		t.Fatal("living child with zero listener fds returned successful Ensure")
	}
	if !strings.Contains(err.Error(), "ownership") || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("unsafe/unclear startup error: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("ownership startup check was not bounded")
	}
	if m.lastConf != "" || m.Snapshot() != (MihomoStatus{}) {
		t.Fatalf("unowned startup advanced applied state: %+v", m.Snapshot())
	}
	if _, applied := m.AppliedSnapshot(mihomoSettings(), endpoints{}); applied {
		t.Fatal("unowned child reported applied")
	}
}

func TestMihomoOwnershipRejectsOccupiedControllerBeforeStart(t *testing.T) {
	lockOwnershipController(t)
	listener, err := net.Listen("tcp4", mihomoAPI)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path := filepath.Join(t.TempDir(), "mihomo.yaml")
	sentinel := "existing external service configuration\n"
	if err := os.WriteFile(path, []byte(sentinel), 0600); err != nil {
		t.Fatal(err)
	}
	started := false
	m := &MihomoMan{configPath: path, command: func(string) *exec.Cmd {
		started = true
		return ownershipSleepingCommand()
	}}
	defer m.Stop()
	err = m.Ensure(mihomoSettings(), endpoints{}, warpAccount{})
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("occupied controller must fail with collision, got %v (child launched=%v)", err, started)
	}
	if started {
		t.Fatal("started child despite external controller listener")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != sentinel {
		t.Fatal("collision overwrote external service configuration")
	}
	if got := m.Snapshot(); got != (MihomoStatus{}) {
		t.Fatalf("collision advanced status: %+v", got)
	}
	if _, applied := m.AppliedSnapshot(mihomoSettings(), endpoints{}); applied {
		t.Fatal("collision reported applied")
	}
	conn, err := net.DialTimeout("tcp", mihomoAPI, time.Second)
	if err != nil {
		t.Fatalf("external listener was harmed: %v", err)
	}
	conn.Close()
}
