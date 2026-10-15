package xkeen

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"time"
)

// ProbeRunningTCP asks the running Xray SOCKS inbound to establish one TCP
// connection through its proxy outbound. It transfers no HTTP body. The full
// HTTPS probe still has to confirm a failure before failover begins.
func ProbeRunningTCP(ctx context.Context, socksAddr, targetURL string, timeout time.Duration) error {
	target, err := url.Parse(targetURL)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" {
		return fmt.Errorf("неверный адрес быстрой проверки")
	}
	host := target.Hostname()
	if len(host) > 255 {
		return fmt.Errorf("слишком длинное имя узла")
	}
	port := 443
	if target.Port() != "" {
		port, err = strconv.Atoi(target.Port())
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("неверный порт быстрой проверки")
		}
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", socksAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var greeting [2]byte
	if _, err = io.ReadFull(conn, greeting[:]); err != nil {
		return err
	}
	if greeting != [2]byte{5, 0} {
		return fmt.Errorf("SOCKS-вход Xray не принял проверку")
	}
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	request = append(request, byte(port>>8), byte(port))
	if _, err = conn.Write(request); err != nil {
		return err
	}
	var reply [4]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 0 {
		return fmt.Errorf("Xray не смог подключиться к проверочному узлу (код %d)", reply[1])
	}
	// A successful SOCKS reply means the remote TCP connection was established;
	// the bound-address bytes are irrelevant because this connection is closed.
	return nil
}
