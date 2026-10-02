package xkeen

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xkeen-panel/internal/models"
)

func TestVerifiedRealXraySupportedProtocols(t *testing.T) {
	bin := os.Getenv("TEST_XRAY_BIN")
	if bin == "" {
		t.Skip("set TEST_XRAY_BIN for real-core integration")
	}
	const id = "00000000-0000-4000-8000-000000000002"
	for _, protocol := range []string{"vless", "vmess", "trojan", "shadowsocks"} {
		t.Run(protocol, func(t *testing.T) {
			var requests atomic.Int32
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(204) }))
			defer target.Close()
			roots := x509.NewCertPool()
			roots.AddCert(target.Certificate())
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			settings := map[string]interface{}{}
			var uri string
			switch protocol {
			case "vless":
				settings["clients"] = []interface{}{map[string]interface{}{"id": id}}
				settings["decryption"] = "none"
				uri = fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp", id, port)
			case "vmess":
				settings["clients"] = []interface{}{map[string]interface{}{"id": id}}
				data, _ := json.Marshal(map[string]interface{}{"add": "127.0.0.1", "port": port, "id": id, "aid": 0, "net": "tcp"})
				uri = "vmess://" + base64.StdEncoding.EncodeToString(data)
			case "trojan":
				settings["clients"] = []interface{}{map[string]interface{}{"password": "test-password"}}
				uri = fmt.Sprintf("trojan://test-password@127.0.0.1:%d?security=none", port)
			case "shadowsocks":
				settings["method"] = "aes-128-gcm"
				settings["password"] = "test-password"
				settings["network"] = "tcp"
				uri = fmt.Sprintf("ss://%s@127.0.0.1:%d", base64.RawStdEncoding.EncodeToString([]byte("aes-128-gcm:test-password")), port)
			}
			// Xray server inbounds block private targets by default. Permit only
			// this loopback HTTPS test fixture, not arbitrary private destinations.
			freedomSettings := map[string]interface{}{"finalRules": []interface{}{map[string]interface{}{"action": "allow", "network": "tcp", "ip": []string{"127.0.0.1"}, "port": target.Listener.Addr().(*net.TCPAddr).Port}}}
			config := map[string]interface{}{"log": map[string]interface{}{"loglevel": "error"}, "inbounds": []interface{}{map[string]interface{}{"listen": "127.0.0.1", "port": port, "protocol": protocol, "settings": settings, "streamSettings": map[string]interface{}{"network": "tcp"}}}, "outbounds": []interface{}{map[string]interface{}{"protocol": "freedom", "settings": freedomSettings}}}
			data, _ := json.Marshal(config)
			path := filepath.Join(t.TempDir(), "server.json")
			os.WriteFile(path, data, 0600)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "run", "-config", path)
			probeCommand(cmd)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			defer func() { cancel(); <-done }()
			ready := false
			for start := time.Now(); time.Since(start) < 3*time.Second; {
				select {
				case <-done:
					t.Fatalf("test server exited: %s", output.String())
				default:
				}
				if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
					conn.Close()
					ready = true
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !ready {
				t.Fatal("local test server did not start")
			}
			ob, err := OutboundForServer(map[string]interface{}{"tag": "vpn"}, &models.Server{Protocol: protocol, RawURI: uri})
			if err != nil {
				t.Fatal(err)
			}
			p := VPNProber{Binary: bin, URLs: []string{target.URL + "/generate_204"}, Timeout: 3 * time.Second, tlsConfig: &tls.Config{RootCAs: roots}}
			result, err := p.Probe(ctx, ob)
			if err != nil || !result.OK || requests.Load() != 1 {
				cancel()
				<-done
				t.Fatalf("%s tunnel failed: %+v %v; server log: %s", protocol, result, err, output.String())
			}
		})
	}
}

func TestVerifiedRealXraySOCKSNoDirectFallback(t *testing.T) {
	bin := os.Getenv("TEST_XRAY_BIN")
	if bin == "" {
		t.Skip("set TEST_XRAY_BIN for real-core integration")
	}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	p := VPNProber{Binary: bin, URLs: []string{server.URL + "/generate_204", server.URL + "/another_generate_204"}, Timeout: 2 * time.Second, tlsConfig: &tls.Config{RootCAs: roots}}
	ob := map[string]interface{}{"protocol": "freedom", "settings": map[string]interface{}{"address": "127.0.0.1", "port": 443}, "streamSettings": map[string]interface{}{"network": "tcp"}}
	result, err := p.Probe(context.Background(), ob)
	if err != nil || !result.OK || result.Successes != 2 {
		t.Fatalf("real SOCKS probe failed: %v, %v", result, err)
	}
	count := requests.Load()
	ob["protocol"] = "blackhole"
	result, err = p.Probe(context.Background(), ob)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || requests.Load() != count {
		t.Fatal("failed proxy fell back to a direct HTTPS request")
	}
	// The caller's config must not be overwritten with the probe's tag/mark.
	if _, ok := ob["tag"]; ok {
		t.Fatal("probe mutated source outbound")
	}
}

func TestVerifiedRealXrayParallelLimitAndCancellation(t *testing.T) {
	bin := os.Getenv("TEST_XRAY_BIN")
	if bin == "" {
		t.Skip("set TEST_XRAY_BIN for real-core integration")
	}
	started := make(chan struct{}, 9)
	var requests atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer target.Close()
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	p := VPNProber{Binary: bin, URLs: []string{target.URL + "/generate_204"}, Timeout: 10 * time.Second, tlsConfig: &tls.Config{RootCAs: roots}}
	ob := map[string]interface{}{"protocol": "freedom", "settings": map[string]interface{}{"address": "127.0.0.1", "port": 443}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 9)
	for range 9 {
		go func() { _, err := p.Probe(ctx, ob); done <- err }()
	}
	for range 8 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("eight isolated real Xray probes did not run concurrently")
		}
	}
	p.once.Do(func() { t.Fatal("prober was not initialized") })
	if len(p.slots) != 8 {
		t.Fatal("child process limit exceeded")
	}
	cancel()
	for range 9 {
		select {
		case err := <-done:
			if err == nil {
				t.Error("cancelled probe returned success")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("child Xray did not stop on cancellation")
		}
	}
	if len(p.slots) != 0 || requests.Load() != 8 {
		t.Fatalf("unreaped slots=%d HTTPS requests=%d", len(p.slots), requests.Load())
	}
}

// This optional check reads real private inputs without copying credentials
// into the repository. It validates only the allowed candidates, with no VPN
// connections to any subscription node.
func TestVerifiedPrivateBlancConfigCompatibility(t *testing.T) {
	bin := os.Getenv("TEST_XRAY_BIN")
	subPath := os.Getenv("TEST_PRIVATE_SUBSCRIPTION")
	obPath := os.Getenv("TEST_PRIVATE_OUTBOUNDS")
	if bin == "" || subPath == "" || obPath == "" {
		t.Skip("private compatibility fixtures not provided")
	}
	body, err := os.ReadFile(subPath)
	if err != nil {
		t.Fatal(err)
	}
	servers, err := ParseSubscription(string(body))
	if err != nil {
		t.Fatal(err)
	}
	current, err := SingleProxy(obPath)
	if err != nil {
		t.Fatal(err)
	}
	allowed := PolicyCandidates(servers, models.VerifiedFailoverConfig{CountryPriority: []string{"NL", "DE"}, ExcludeNameContains: []string{"Extra Whitelist2", "Extra2"}})
	if len(allowed) == 0 {
		t.Fatal("no eligible Blanc servers")
	}
	for _, s := range allowed {
		ob, err := OutboundForServer(current, &s)
		if err != nil {
			t.Fatal("failed to render allowed candidate")
		}
		if outboundTag(ob) != outboundTag(current) {
			t.Fatal("routing tag changed")
		}
		data, _ := json.Marshal(map[string]interface{}{"outbounds": []interface{}{ob}})
		path := filepath.Join(t.TempDir(), "candidate.json")
		os.WriteFile(path, data, 0600)
		cmd := exec.Command(bin, "run", "-test", "-config", path)
		if _, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Xray rejected %s (%s)", s.Name, strings.ToUpper(s.Country))
		}
	}
	t.Logf("Xray accepted %d eligible candidates; excluded servers were not probed", len(allowed))
}
