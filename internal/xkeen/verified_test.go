package xkeen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xkeen-panel/internal/models"
)

const verifiedFixture = `{
// Preserve these exact bytes on failure.
"outbounds":[{"protocol":"vless","tag":"vless-reality","settings":{"vnext":[{"address":"192.0.2.1","port":443,"users":[{"id":"00000000-0000-4000-8000-000000000001","encryption":"none","level":0}]}]},"streamSettings":{"network":"tcp","security":"reality","sockopt":{"mark":7},"realitySettings":{"serverName":"example.org","fingerprint":"firefox","publicKey":"test","shortId":"01","spiderX":"/"}}},{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"block"}]
}`

func verifiedCandidate(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	ob, err := SingleProxy(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &models.Server{Protocol: "vless", RawURI: "vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp&security=reality&sni=example.org&fp=firefox&pbk=test&sid=01&spx=%2F"}
	newOB, err := OutboundForServer(ob, s)
	if err != nil {
		t.Fatal(err)
	}
	return newOB
}

func TestVerifiedApplyRollbackAndCommit(t *testing.T) {
	for _, scenario := range []string{"commit", "invalid", "restart-fails", "probe-fails", "main-dies"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "04_outbounds.json")
			os.WriteFile(path, []byte(verifiedFixture), 0600)
			candidate := verifiedCandidate(t, path)
			restarts := 0
			probes := 0
			a := VerifiedApplier{Path: path, DataDir: dir,
				Validate: func() error {
					if scenario == "invalid" {
						return errors.New("invalid")
					}
					return nil
				},
				Restart: func() error {
					restarts++
					if scenario == "restart-fails" && restarts == 1 {
						return errors.New("restart")
					}
					return nil
				},
				Running: func() bool { return scenario != "main-dies" || probes == 0 },
				Probe: func(context.Context, map[string]interface{}) (ProbeResult, error) {
					probes++
					return ProbeResult{OK: scenario != "probe-fails"}, nil
				},
			}
			err := a.Apply(context.Background(), candidate)
			bytes, _ := os.ReadFile(path)
			if scenario == "commit" {
				if err != nil {
					t.Fatal(err)
				}
				actual, _ := SingleProxy(path)
				if !SameOutbound(actual, candidate) {
					t.Fatal("candidate not committed")
				}
				if len(asSlice(mustConfig(t, path)["outbounds"])) != 3 {
					t.Fatal("service outbounds changed")
				}
				if restarts != 1 || probes != 1 {
					t.Fatalf("restarts %d probes %d", restarts, probes)
				}
			} else {
				if err == nil {
					t.Fatal("failure incorrectly reported success")
				}
				if string(bytes) != verifiedFixture {
					t.Fatal("exact original was not restored")
				}
				if scenario == "invalid" && restarts != 0 {
					t.Fatal("restarted invalid configuration")
				}
				if scenario != "invalid" && restarts != 2 {
					t.Fatalf("expected switch and rollback restart, got %d", restarts)
				}
			}
			if _, err := os.Stat(a.journal()); !os.IsNotExist(err) {
				t.Fatal("journal not removed")
			}
		})
	}
}

func mustConfig(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	cfg, err := ReadOutboundsConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestVerifiedRecoverInterruptedSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "04_outbounds.json")
	os.WriteFile(path, []byte(verifiedFixture), 0600)
	candidate := verifiedCandidate(t, path)
	a := VerifiedApplier{Path: path, DataDir: dir, Validate: func() error { return nil }, Restart: func() error { return nil }}
	os.WriteFile(a.journal(), []byte(verifiedFixture), 0600)
	cfg := mustConfig(t, path)
	asSlice(cfg["outbounds"])[0] = candidate
	WriteOutboundsConfig(path, cfg)
	if err := a.Recover(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != verifiedFixture {
		t.Fatal("interrupted transaction not restored")
	}
	if err := a.Recover(); err != nil {
		t.Fatal("recovery is not idempotent")
	}
}

func TestVerifiedRollbackFailureKeepsJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "04_outbounds.json")
	os.WriteFile(path, []byte(verifiedFixture), 0600)
	a := VerifiedApplier{Path: path, DataDir: dir, Validate: func() error { return nil }, Restart: func() error { return errors.New("restart failed") }}
	if err := a.Apply(context.Background(), verifiedCandidate(t, path)); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(a.journal()); err != nil {
		t.Fatal("recovery journal was lost")
	}
	old, _ := os.ReadFile(path)
	if string(old) != verifiedFixture {
		t.Fatal("rollback did not restore file")
	}
}

func TestPolicyStrictPriorityAndExclusion(t *testing.T) {
	servers := []models.Server{
		{ID: 0, Name: "🇩🇪 Германия", Protocol: "vless"},
		{ID: 1, Name: "🇳🇱 Нидерланды Extra Whitelist2", Protocol: "vless"},
		{ID: 2, Name: "🇫🇷 Франция", Protocol: "vless"},
		{ID: 3, Name: "🇳🇱 Амстердам Extra", Protocol: "vless"},
		{ID: 4, Name: "🇩🇪 EXTRA WHITELIST2", Protocol: "vless"},
		{ID: 5, Name: "Unknown", Protocol: "vless"},
		{ID: 6, Name: "🇳🇱 Netherlands Extra Whitelist", Protocol: "vless"},
	}
	got := PolicyCandidates(servers, models.VerifiedFailoverConfig{CountryPriority: []string{"NL", "DE"}, ExcludeNameContains: []string{"Extra Whitelist2"}})
	if len(got) != 3 || got[0].ID != 3 || got[1].ID != 6 || got[2].ID != 0 {
		t.Fatalf("unexpected candidates: %v", got)
	}
}

func TestVerifiedHTTPSStatusAndNoRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/generate_204":
			w.WriteHeader(204)
		case "/cdn-cgi/trace":
			w.Write([]byte("ip=192.0.2.1\n"))
		case "/redirect":
			http.Redirect(w, r, "/generate_204", 302)
		case "/forbidden":
			w.WriteHeader(403)
		default:
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, tc := range []struct {
		paths []string
		want  bool
	}{
		{[]string{"/generate_204", "/cdn-cgi/trace"}, true},
		{[]string{"/forbidden", "/generate_204"}, true},
		{[]string{"/forbidden", "/bad"}, false},
		{[]string{"/redirect"}, false},
	} {
		var urls []string
		for _, path := range tc.paths {
			urls = append(urls, server.URL+path)
		}
		if result := probeHTTPS(context.Background(), client, urls); result.OK != tc.want {
			t.Errorf("%v => %v", tc.paths, result)
		}
	}
}

func TestSubscriptionErrorDoesNotLeakToken(t *testing.T) {
	err := subscriptionDownloadError(&url.Error{Op: "Get", URL: "https://example.org/s/secret-bearer-token", Err: errors.New("timeout")})
	if strings.Contains(err.Error(), "secret-bearer-token") {
		t.Fatal("subscription token leaked")
	}
}

func TestDirectTransportIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	client := DirectHTTPClient(0)
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("direct requests use environment proxy")
	}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
