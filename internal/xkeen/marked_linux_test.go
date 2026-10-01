//go:build linux

package xkeen

import (
	"errors"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSocketMarkZeroNeedsNoPrivilegedControl(t *testing.T) {
	if socketMark(0) != nil {
		t.Fatal("zero mark should not require a privileged socket operation")
	}
}

func TestSocketMarkPositiveAppliedOrFailsClosed(t *testing.T) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw, err := listener.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	err = socketMark(255)("tcp", "127.0.0.1", raw)
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		if os.Getenv("TEST_REQUIRE_MARK") == "1" {
			t.Fatalf("privileged mark verification failed: %v", err)
		}
		// An unprivileged process must fail; it must never silently dial without
		// the mark and send recovery traffic through the failed router tunnel.
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var actual int
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		actual, readErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK)
	}); err != nil {
		t.Fatal(err)
	}
	if readErr != nil || actual != 255 {
		t.Fatalf("mark = %d, error = %v", actual, readErr)
	}
}
