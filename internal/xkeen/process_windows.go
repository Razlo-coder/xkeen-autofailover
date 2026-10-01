//go:build windows

package xkeen

import "os/exec"

func detachCommand(cmd *exec.Cmd) {}
