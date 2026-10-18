//go:build linux

package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Exercise actual kernel denial, not an injected ownership callback. The
// seccomp filter exists only in this subprocess/thread and denies only getfd.
func TestRelayUDPUnavailableKernelLeaseFailsClosed(t *testing.T) {
	mode := os.Getenv("RELAY_UDP_DENY_GETFD")
	if mode == "" {
		for _, errno := range []syscall.Errno{syscall.EPERM, syscall.ENOSYS} {
			t.Run(errno.Error(), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRelayUDPUnavailableKernelLeaseFailsClosed$", "-test.v")
				cmd.Env = append(os.Environ(), "RELAY_UDP_DENY_GETFD="+strconv.Itoa(int(errno)))
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("lease denial subprocess: %v\n%s", err, output)
				}
				t.Logf("%s", output)
			})
		}
		return
	}
	if os.Getenv("OFFLINE_NETNS") != "1" {
		t.Fatal("offline namespace required")
	}
	fixture := relayUDPFixture(t)
	b, err := newRelayUDPFixture(context.Background(), relayUDPCfg(), fixture.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	child := relayUDPSpawn(t, "")
	child.command(t, "send "+b.Target().String()+" 64656e696564")
	errno, err := strconv.Atoi(mode)
	if err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	filters := []syscall.SockFilter{
		{Code: 0x20, K: 0},                          // BPF_LD|BPF_W|BPF_ABS: syscall number
		{Code: 0x15, Jf: 1, K: 438},                 // BPF_JMP|BPF_JEQ|BPF_K: pidfd_getfd
		{Code: 0x06, K: 0x00050000 | uint32(errno)}, // SECCOMP_RET_ERRNO
		{Code: 0x06, K: 0x7fff0000},                 // SECCOMP_RET_ALLOW
	}
	prog := syscall.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if _, _, e := syscall.Syscall6(syscall.SYS_PRCTL, 38 /* PR_SET_NO_NEW_PRIVS */, 1, 0, 0, 0, 0); e != 0 {
		t.Fatal("no-new-privs:", e)
	}
	if _, _, e := syscall.Syscall6(syscall.SYS_PRCTL, 22 /* PR_SET_SECCOMP */, 2 /* FILTER */, uintptr(unsafe.Pointer(&prog)), 0, 0, 0); e != 0 {
		t.Fatal("seccomp:", e)
	}
	// First verify the kernel actually installed the denial, then use production.
	if _, _, e := syscall.Syscall(438, 0, 0, 0); e != syscall.Errno(errno) {
		t.Fatalf("getfd denial=%v want=%d", e, errno)
	}
	if lease, e := acquireRelayUDPSocket(child.cmd.Process.Pid, child.peer, b.Target()); e == nil {
		lease.Close()
		t.Fatal("ownership lease bypassed kernel denial")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err = b.Authorize(ctx, RelayUDPChild{PID: child.cmd.Process.Pid, Generation: 1, Exited: child.exited, Current: func() bool { return true }}); err == nil {
		t.Fatal("callback alone authorized without lease")
	}
	relayUDPWaitClosed(t, b)
	fixture.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
	buf := make([]byte, 64)
	if _, _, err = fixture.ReadFromUDP(buf); err == nil {
		t.Fatal("traffic escaped without kernel lease")
	}
	t.Logf("kernel denied pidfd_getfd errno=%d; exact proc/fd plus Current=true did not bypass; bridge closed/joined", errno)
}
