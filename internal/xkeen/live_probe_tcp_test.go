package xkeen

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestProbeRunningTCPUsesSocksRoute(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	request := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var greeting [3]byte
		if _, err := io.ReadFull(conn, greeting[:]); err != nil {
			return
		}
		conn.Write([]byte{5, 0})
		var header [5]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return
		}
		name := make([]byte, int(header[4])+2)
		if _, err := io.ReadFull(conn, name); err != nil {
			return
		}
		request <- append(header[:], name...)
		conn.Write([]byte{5, 0, 0, 1})
	}()
	if err := ProbeRunningTCP(context.Background(), listener.Addr().String(), "https://example.org/generate_204", time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-request:
		if string(got[5:len(got)-2]) != "example.org" || got[len(got)-2] != 1 || got[len(got)-1] != 187 {
			t.Fatalf("wrong SOCKS target: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS route was not used")
	}
}

func TestProbeRunningTCPRejectsFailedConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var greeting [3]byte
		io.ReadFull(conn, greeting[:])
		conn.Write([]byte{5, 0})
		var header [5]byte
		io.ReadFull(conn, header[:])
		io.CopyN(io.Discard, conn, int64(header[4])+2)
		conn.Write([]byte{5, 4, 0, 1})
	}()
	if err := ProbeRunningTCP(context.Background(), listener.Addr().String(), "https://example.org/", time.Second); err == nil {
		t.Fatal("SOCKS connect failure was accepted")
	}
}
