package xkeen

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const SpeedTestBytes = 512 * 1024
const SpeedTestURL = "https://speed.cloudflare.com/__down?bytes=524288"

func liveHTTPClient(addr string, timeout time.Duration) (*http.Client, *http.Transport, error) {
	proxyURL, err := url.Parse("socks5://" + addr)
	if err != nil {
		return nil, nil, err
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true,
		DisableCompression: true, TLSHandshakeTimeout: timeout,
		TLSClientConfig: &tls.Config{RootCAs: entwareRoots()}}
	client := &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, transport, nil
}

// ProbeRunningXray checks the configured outbound through the main Xray core.
func ProbeRunningXray(ctx context.Context, addr string, urls []string, timeout time.Duration) (ProbeResult, error) {
	client, transport, err := liveHTTPClient(addr, timeout)
	if err != nil {
		return ProbeResult{}, err
	}
	defer transport.CloseIdleConnections()
	return probeHTTPS(ctx, client, urls), ctx.Err()
}

// MeasureRunningSpeed reports application download throughput through the main core.
// Failed or truncated downloads are errors, not evidence of a slow VPN server.
func MeasureRunningSpeed(ctx context.Context, addr string, timeout time.Duration) (float64, error) {
	client, transport, err := liveHTTPClient(addr, timeout)
	if err != nil {
		return 0, err
	}
	defer transport.CloseIdleConnections()
	return measureDownload(ctx, client, SpeedTestURL)
}

func measureDownload(ctx context.Context, client *http.Client, target string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("сервер проверки скорости вернул HTTP %d", resp.StatusCode)
	}
	start := time.Now()
	n, err := io.CopyN(io.Discard, resp.Body, SpeedTestBytes)
	if err != nil && n == 0 {
		return 0, fmt.Errorf("неполная загрузка: %d байт: %v", n, err)
	}
	seconds := time.Since(start).Seconds()
	if seconds <= 0 {
		seconds = 0.001
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			return 0, fmt.Errorf("неполная загрузка: %d байт: %v", n, err)
		}
	}
	return float64(n*8) / seconds / 1e6, nil
}
