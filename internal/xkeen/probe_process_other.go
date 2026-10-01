//go:build !linux

package xkeen

import "os/exec"

func probeCommand(cmd *exec.Cmd) {}
