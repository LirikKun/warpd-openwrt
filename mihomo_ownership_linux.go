//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

func protectMihomoChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Only the child we exec is affected. Never search or signal by binary name.
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}
