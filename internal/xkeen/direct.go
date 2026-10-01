package xkeen

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// DirectDialer marks DNS sockets too; marking just the final TCP socket leaves
// subscription recovery dependent on the failed transparent proxy.
func DirectDialer(mark int) *net.Dialer {
	dns := &net.Dialer{Timeout: 5 * time.Second, Control: socketMark(mark)}
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: socketMark(mark),
		Resolver: &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dns.DialContext(ctx, network, address)
		}},
	}
}

func DirectHTTPClient(mark int) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         DirectDialer(mark).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{RootCAs: entwareRoots()},
		DisableKeepAlives:   true,
	}}
}
