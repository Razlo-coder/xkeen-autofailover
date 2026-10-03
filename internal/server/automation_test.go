package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xkeen-panel/internal/auth"
	"xkeen-panel/internal/models"
	"xkeen-panel/internal/monitor"
	"xkeen-panel/internal/sse"
	"xkeen-panel/internal/xkeen"

	"github.com/pquerna/otp/totp"
)

func TestAutomationAPIAuthPersistenceAndInvalidRules(t *testing.T) {
	dir := t.TempDir()
	policy := models.DefaultVerifiedFailover()
	policy.Enabled = true
	cfg := &models.Config{DataDir: dir, WatchdogAutoStart: false, VerifiedFailover: policy}
	um := auth.NewUserManager(dir)
	const demoSecret = "JBSWY3DPEHPK3PXP"
	if err := um.CreatePendingUser("preview", "preview-only-password", demoSecret); err != nil {
		t.Fatal(err)
	}
	if err := um.ConfirmSetup(); err != nil {
		t.Fatal(err)
	}
	sub := xkeen.NewSubscriptionManager(dir)
	fixture := models.SubscriptionData{ActiveID: -1, Servers: []models.Server{
		{ID: 0, Name: "Amsterdam Primary", Country: "NL", Protocol: "vless", Address: "192.0.2.1", Port: 443, Latency: -1},
		{ID: 1, Name: "Amsterdam Reserve", Country: "NL", Protocol: "vless", Address: "192.0.2.2", Port: 443, Latency: -1},
		{ID: 2, Name: "Berlin", Country: "DE", Protocol: "trojan", Address: "192.0.2.3", Port: 443, Latency: -1},
	}}
	data, _ := json.Marshal(fixture)
	os.WriteFile(filepath.Join(dir, "subscription.json"), data, 0600)
	sub.Load()
	det := xkeen.NewDetector(t.TempDir(), "", "", "", "", "", "")
	wd := monitor.NewWatchdog(cfg, sub, det)
	bus := sse.NewEventBus()
	wd.SetEventBus(bus)
	srv := New(cfg, um, sub, wd, det, xkeen.NewPoolStore(dir), nil, bus, os.DirFS(filepath.Join("..", "..", "frontend", "dist")))
	handler := srv.Handler()
	token, err := auth.GenerateToken("preview", um.GetUser().JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	if out := request("GET", "/api/automation", "", false); out.Code != http.StatusUnauthorized {
		t.Fatal("settings exposed without authentication")
	}
	if out := request("PUT", "/api/automation", `{"enabled":true,"allow_other_countries":false}`, true); out.Code != http.StatusBadRequest {
		t.Fatal("empty strict country list accepted")
	}
	body := `{"enabled":true,"country_priority":["de","NL"],"allow_other_countries":true,"preferred_server_names":["Berlin"],"excluded_server_names":[],"exclude_name_contains":["Unsupported"]}`
	if out := request("PUT", "/api/automation", body, true); out.Code != http.StatusOK {
		t.Fatalf("save failed: %s", out.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "automation.json")); err != nil {
		t.Fatal("settings not persisted")
	}
	if out := request("GET", "/api/automation", "", true); !bytes.Contains(out.Body.Bytes(), []byte(`"country_priority":["DE","NL"]`)) {
		t.Fatalf("normalised state not returned: %s", out.Body)
	}
	if !wd.GetAutomation().QualityEnabled || wd.GetAutomation().QualityThresholdMs != 1500 || !wd.GetAutomation().ReturnToPriority {
		t.Fatal("saving from an older UI reset new connection settings")
	}
	if out := request("PUT", "/api/automation", `{"quality_threshold_ms":0}`, true); out.Code != http.StatusBadRequest {
		t.Fatal("invalid quality threshold accepted")
	}
	if out := request("POST", "/api/watchdog/toggle", `{"active":false}`, true); out.Code != http.StatusOK || wd.GetAutomation().Enabled {
		t.Fatal("toggle did not persist disable")
	}
	if os.Getenv("TEST_UI_PREVIEW") == "1" {
		// A large, entirely synthetic subscription exercises popup filtering.
		previewServers := sub.GetServers()
		for i := range previewServers {
			uri := fmt.Sprintf("vless://00000000-0000-4000-8000-%012d@%s:443?type=tcp#%s", i+1, previewServers[i].Address, previewServers[i].Name)
			if previewServers[i].Protocol == "trojan" {
				uri = "trojan://demo-password@192.0.2.3:443?security=tls#Berlin"
			}
			previewServers[i].RawURI = uri
		}
		for i := 3; i < 70; i++ {
			name := fmt.Sprintf("Amsterdam Node %02d", i)
			address := fmt.Sprintf("192.0.2.%d", i+1)
			previewServers = append(previewServers, models.Server{ID: i, Name: name, Protocol: "vless", Country: "NL", Address: address, Port: 443, Latency: -1,
				RawURI: fmt.Sprintf("vless://00000000-0000-4000-8000-%012d@%s:443?type=tcp#%s", i+1, address, name)})
		}
		for i := 0; i < 8; i++ {
			name := fmt.Sprintf("⚡ Авто · %d", i+1)
			address := fmt.Sprintf("198.51.100.%d", i+1)
			previewServers = append(previewServers, models.Server{ID: 70 + i, SourceID: 1, Name: name, GroupName: "⚡ Авто", Protocol: "vless", Address: address, Port: 443, Latency: 120 + i*25,
				RawURI: fmt.Sprintf("vless://00000000-0000-4000-8000-%012d@%s:443?type=tcp#%%E2%%9A%%A1%%20%%D0%%90%%D0%%B2%%D1%%82%%D0%%BE%%20%%C2%%B7%%20%d", 71+i, address, i+1)})
		}
		previewServers[0].Active = true
		previewProvider := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			var uris []string
			for _, s := range previewServers {
				uris = append(uris, s.RawURI)
			}
			rw.Write([]byte(strings.Join(uris, "\n")))
		}))
		defer previewProvider.Close()
		previewData, _ := json.Marshal(models.SubscriptionData{URL: previewProvider.URL, Name: "Blank", SecondaryURL: "https://example.invalid/skip", SecondaryName: "SkipVPN", ActiveID: 0, Servers: previewServers})
		os.WriteFile(filepath.Join(dir, "subscription.json"), previewData, 0600)
		sub.Load()
		cfg.OutboundsFile = filepath.Join(dir, "04_outbounds.json")
		previewOutbound, err := xkeen.OutboundForServer(map[string]interface{}{"protocol": "vless", "tag": "vpn"}, &previewServers[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := xkeen.WriteOutboundsConfig(cfg.OutboundsFile, map[string]interface{}{"outbounds": []interface{}{previewOutbound}}); err != nil {
			t.Fatal(err)
		}
		otp, _ := totp.GenerateCode(demoSecret, time.Now())
		t.Logf("UI preview at http://127.0.0.1:18080; username=preview password=preview-only-password TOTP=%s", otp)
		// Synthetic slow ping/selection endpoints are used only for browser UI
		// checks. Real probe cancellation and application are tested in monitor.
		var previewRunning atomic.Bool
		previewRunning.Store(true)
		var previewSince atomic.Pointer[time.Time]
		started := time.Now().Add(-25*time.Hour - 18*time.Minute).UTC()
		previewSince.Store(&started)
		previewStatus := previewStatusProvider(func() models.Status {
			// Only the name lookup uses real config matching; core and time are synthetic.
			status := wd.GetStatus()
			settings := wd.GetAutomation()
			status.Connected = previewRunning.Load()
			status.XrayRunning = status.Connected
			status.Latency = 291
			if status.Connected {
				status.ConnectedSince = previewSince.Load()
				status.UptimeSeconds = int64(time.Since(*status.ConnectedSince).Seconds())
			} else {
				status.Latency = -1
				status.ConnectedSince = nil
			}
			status.QualityDegraded = settings.QualityEnabled && status.Latency > settings.QualityThresholdMs
			return status
		})
		previewHandler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "GET /api/events":
				sse.HandleEvents(bus, previewStatus).ServeHTTP(rw, r)
			case "GET /api/status":
				rw.Header().Set("Content-Type", "application/json")
				json.NewEncoder(rw).Encode(previewStatus())
			case "POST /api/xkeen/stop":
				if err := wd.SetAutomationEnabled(false); err != nil {
					t.Error(err)
				}
				previewRunning.Store(false)
				bus.Publish(sse.Event{Type: "status", Data: previewStatus()})
				t.Log("UI preview: stop accepted")
				json.NewEncoder(rw).Encode(map[string]bool{"success": true})
			case "POST /api/xkeen/start", "POST /api/xkeen/restart":
				now := time.Now().UTC()
				previewSince.Store(&now)
				previewRunning.Store(true)
				bus.Publish(sse.Event{Type: "status", Data: previewStatus()})
				t.Log("UI preview: start/restart accepted")
				json.NewEncoder(rw).Encode(map[string]bool{"success": true})
			case "GET /api/servers/check":
				rw.Header().Set("Content-Type", "text/event-stream")
				rw.Header().Set("Cache-Control", "no-cache")
				flusher := rw.(http.Flusher)
				rw.Write([]byte(": ping started\n\n"))
				flusher.Flush()
				for _, s := range sub.GetServers() {
					select {
					case <-r.Context().Done():
						t.Log("UI preview: bulk ping cancelled")
						return
					case <-time.After(10 * time.Second):
					}
					data, _ := sse.FormatSSE(sse.Event{Type: "latency", Data: map[string]int{"id": s.ID, "latency_ms": 15 + s.ID}})
					rw.Write(data)
					flusher.Flush()
				}
				rw.Write([]byte("event: done\ndata: {}\n\n"))
				flusher.Flush()
			case "POST /api/servers/select":
				var req struct {
					ID int `json:"id"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					http.Error(rw, "bad request", http.StatusBadRequest)
					return
				}
				s, err := sub.SetActive(req.ID)
				if err != nil {
					http.Error(rw, err.Error(), http.StatusBadRequest)
					return
				}
				current, err := xkeen.SingleProxy(cfg.OutboundsFile)
				if err != nil {
					http.Error(rw, err.Error(), http.StatusBadRequest)
					return
				}
				chosen, err := xkeen.OutboundForServer(current, s)
				if err != nil {
					http.Error(rw, err.Error(), http.StatusBadRequest)
					return
				}
				if err := xkeen.WriteOutboundsConfig(cfg.OutboundsFile, map[string]interface{}{"outbounds": []interface{}{chosen}}); err != nil {
					http.Error(rw, err.Error(), http.StatusBadRequest)
					return
				}
				t.Log("UI preview: selected server " + strconv.Itoa(s.ID))
				now := time.Now().UTC()
				previewSince.Store(&now)
				bus.Publish(sse.Event{Type: "status", Data: previewStatus()})
				rw.Header().Set("Content-Type", "application/json")
				json.NewEncoder(rw).Encode(map[string]interface{}{"server": s})
			default:
				handler.ServeHTTP(rw, r)
			}
		})
		preview := &http.Server{Addr: "127.0.0.1:18080", Handler: previewHandler, ReadHeaderTimeout: 5 * time.Second}
		time.AfterFunc(5*time.Minute, func() { preview.Close() })
		if err := preview.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Fatal(err)
		}
	}
}

type previewStatusProvider func() models.Status

func (p previewStatusProvider) GetStatus() models.Status { return p() }
