package xkeen

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ProbeResult struct {
	OK        bool
	Latency   int
	Successes int
	Total     int
}

// VPNProber runs a second, short-lived Xray with a private SOCKS listener. No
// routing rule can turn these HTTPS requests into a direct-WAN false positive.
// Only one child is allowed at once to bound memory on the router.
type VPNProber struct {
	Binary    string
	Mark      int
	URLs      []string
	Timeout   time.Duration
	mu        sync.Mutex
	tlsConfig *tls.Config // nil in production; test CA for local integration tests
}

func (p *VPNProber) Probe(ctx context.Context, outbound map[string]interface{}) (ProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Timeout <= 0 {
		p.Timeout = 8 * time.Second
	}
	ctx, probeCancel := context.WithTimeout(ctx, p.Timeout+10*time.Second)
	defer probeCancel()
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	data, err := json.Marshal(outbound)
	if err != nil {
		return ProbeResult{}, err
	}
	var ob map[string]interface{}
	json.Unmarshal(data, &ob)
	if err := p.prepareOutbound(ctx, ob); err != nil {
		return ProbeResult{}, err
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return ProbeResult{}, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir, err := os.MkdirTemp("", "panel-vpn-probe-")
	if err != nil {
		return ProbeResult{}, err
	}
	defer os.RemoveAll(dir)
	ob["tag"] = "probe-vpn"
	config := map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "none"},
		"inbounds": []interface{}{map[string]interface{}{
			"tag": "probe-socks", "listen": "127.0.0.1", "port": port, "protocol": "socks",
			"settings": map[string]interface{}{"auth": "noauth", "udp": false},
		}},
		"outbounds": []interface{}{ob},
		"routing": map[string]interface{}{"rules": []interface{}{map[string]interface{}{
			"type": "field", "inboundTag": []string{"probe-socks"}, "outboundTag": "probe-vpn",
		}}},
	}
	path := filepath.Join(dir, "config.json")
	data, err = json.Marshal(config)
	if err != nil {
		return ProbeResult{}, err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return ProbeResult{}, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, p.Binary, "run", "-config", path)
	probeCommand(cmd)
	// A distro's XRAY_LOCATION_CONFIG must not load production inbounds into
	// this second process. Assets are also unnecessary for the isolated config.
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "XRAY_") && !strings.HasPrefix(strings.ToUpper(key), "V2RAY_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	if err := cmd.Start(); err != nil {
		return ProbeResult{}, fmt.Errorf("не удалось запустить тестовое ядро Xray: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exited := false
	defer func() {
		cancel()
		if !exited {
			<-done
		}
	}()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	readyUntil := time.NewTimer(5 * time.Second)
	defer readyUntil.Stop()
	readyTick := time.NewTicker(50 * time.Millisecond)
	defer readyTick.Stop()
ready:
	for {
		select {
		case <-ctx.Done():
			return ProbeResult{}, ctx.Err()
		case <-done:
			exited = true
			return ProbeResult{}, fmt.Errorf("тестовое ядро отвергло конфигурацию или завершилось")
		case <-readyUntil.C:
			return ProbeResult{}, fmt.Errorf("тестовое ядро не открыло локальный порт")
		case <-readyTick.C:
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				break ready
			}
		}
	}
	proxyURL, _ := url.Parse("socks5://" + addr)
	tlsConfig := p.tlsConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{RootCAs: entwareRoots()}
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true, TLSHandshakeTimeout: p.Timeout, TLSClientConfig: tlsConfig}
	defer transport.CloseIdleConnections()
	result := probeHTTPS(ctx, &http.Client{Transport: transport, Timeout: p.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, p.URLs)
	select {
	case <-done:
		exited = true
		return ProbeResult{}, fmt.Errorf("тестовое ядро завершилось во время проверки")
	default:
	}
	return result, ctx.Err()
}

func (p *VPNProber) prepareOutbound(ctx context.Context, ob map[string]interface{}) error {
	address, _, _, ok := readProxyEndpoint(ob)
	if !ok {
		return fmt.Errorf("в тестовом outbound нет адреса")
	}
	ss := mapOf(ob["streamSettings"])
	if ss == nil {
		ss = map[string]interface{}{}
		ob["streamSettings"] = ss
	}
	sockopt := mapOf(ss["sockopt"])
	if sockopt == nil {
		sockopt = map[string]interface{}{}
		ss["sockopt"] = sockopt
	}
	sockopt["mark"] = p.Mark
	if ob["proxySettings"] != nil || sockopt["dialerProxy"] != nil {
		return fmt.Errorf("каскадные outbound не поддерживаются проверяемым переключением")
	}
	if net.ParseIP(address) != nil {
		return nil
	}
	ips, err := DirectDialer(p.Mark).Resolver.LookupIPAddr(ctx, address)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("не удалось разрешить адрес VPN-сервера")
	}
	ip := ips[0].IP.String()
	for _, a := range ips {
		if a.IP.To4() != nil {
			ip = a.IP.String()
			break
		}
	}
	settings := mapOf(ob["settings"])
	if vnext, ok := settings["vnext"].([]interface{}); ok {
		mapOf(vnext[0])["address"] = ip
	} else if servers, ok := settings["servers"].([]interface{}); ok && len(servers) > 0 {
		mapOf(servers[0])["address"] = ip
	} else {
		settings["address"] = ip
	}
	for _, key := range []string{"tlsSettings", "realitySettings"} {
		if tls := mapOf(ss[key]); tls != nil && (tls["serverName"] == nil || tls["serverName"] == "") {
			tls["serverName"] = address
		}
	}
	return nil
}

func probeHTTPS(ctx context.Context, client *http.Client, urls []string) ProbeResult {
	start := time.Now()
	oks := make(chan bool, len(urls))
	for _, target := range urls {
		go func(target string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				oks <- false
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; xkeen-panel)")
			resp, err := client.Do(req)
			if err != nil {
				oks <- false
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
			ok := err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300
			if strings.Contains(target, "generate_204") {
				ok = ok && resp.StatusCode == 204
			}
			if strings.Contains(target, "/cdn-cgi/trace") {
				ok = ok && strings.Contains(string(body), "ip=")
			}
			oks <- ok
		}(target)
	}
	r := ProbeResult{Total: len(urls), Latency: -1}
	for range urls {
		if <-oks {
			r.Successes++
		}
	}
	// One reachable HTTPS destination proves the tunnel works. Requiring both
	// would turn one website's outage into a needless server change.
	r.OK = r.Successes > 0
	if r.OK {
		r.Latency = int(time.Since(start).Milliseconds())
	}
	return r
}
