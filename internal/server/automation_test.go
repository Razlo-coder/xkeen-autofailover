package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	cfg := &models.Config{DataDir: dir, WatchdogAutoStart: false, VerifiedFailover: models.VerifiedFailoverConfig{Enabled: true, AllowOtherCountries: true}}
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
	if out := request("POST", "/api/watchdog/toggle", `{"active":false}`, true); out.Code != http.StatusOK || wd.GetAutomation().Enabled {
		t.Fatal("toggle did not persist disable")
	}
	if os.Getenv("TEST_UI_PREVIEW") == "1" {
		otp, _ := totp.GenerateCode(demoSecret, time.Now())
		t.Logf("UI preview at http://127.0.0.1:18080; username=preview password=preview-only-password TOTP=%s", otp)
		// Synthetic slow ping/selection endpoints are used only for browser UI
		// checks. Real probe cancellation and application are tested in monitor.
		previewHandler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
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
				t.Log("UI preview: selected server " + strconv.Itoa(s.ID))
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
