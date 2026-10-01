//go:build !linux

package xkeen

import (
	"fmt"
	"syscall"
)

func socketMark(mark int) func(string, string, syscall.RawConn) error {
	return func(_, _ string, _ syscall.RawConn) error {
		if mark != 0 {
			return fmt.Errorf("служебная метка поддерживается только на Linux")
		}
		return nil
	}
}
