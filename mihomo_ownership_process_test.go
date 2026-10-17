//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMihomoOwnershipParentHelper(t *testing.T) {
	if os.Getenv("WARPD_OWNERSHIP_PARENT") != "1" {
		return
	}
	m, g, _ := newOwnershipMihomo(t)
	if err := m.Ensure(g, endpoints{}, warpAccount{}); err != nil {
		os.Exit(5)
	}
	if err := os.WriteFile(os.Getenv("WARPD_OWNERSHIP_PIDFILE"), []byte(strconv.Itoa(m.pid)), 0600); err != nil {
		os.Exit(6)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestMihomoOwnershipParentDeathTerminatesChild(t *testing.T) {
	lockOwnershipController(t)
	pidfile := filepath.Join(t.TempDir(), "pid")
	parent := exec.Command(os.Args[0], "-test.run=^TestMihomoOwnershipParentHelper$")
	parent.Env = append(os.Environ(), "WARPD_OWNERSHIP_PARENT=1", "WARPD_OWNERSHIP_PIDFILE="+pidfile, "WARPD_OWNERSHIP_LOCK_HELD=1", "GORACE=atexit_sleep_ms=0")
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidfile)
		if err == nil {
			pid, err = strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("parent helper did not start owned child")
	}
	defer syscall.Kill(pid, syscall.SIGKILL) // exact test-created child, even on RED
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := parent.Wait(); err == nil {
		t.Fatal("parent unexpectedly exited successfully")
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if os.IsNotExist(err) {
			return
		}
		// A dead orphan may remain a zombie until the host init reaps it.
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) > 2 && fields[2] == "Z" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("managed child survived abrupt parent death")
}
