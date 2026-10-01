//go:build linux

package xkeen

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func socketMark(mark int) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var err error
		if e := raw.Control(func(fd uintptr) {
			err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
		}); e != nil {
			return e
		}
		if err != nil {
			return fmt.Errorf("не удалось установить служебную метку: %w", err)
		}
		return nil
	}
}
