package xkeen

import (
	"os/exec"
	"syscall"
)

func probeCommand(cmd *exec.Cmd) {
	// Prevent an orphaned second Xray if the panel is killed unexpectedly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
