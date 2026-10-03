package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xkeen-panel/internal/models"
	"xkeen-panel/internal/xkeen"
)

func TestSubscriptionAPIAddsAndRemovesSecondSource(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("vless://00000000-0000-4000-8000-000000000001@192.0.2.1:443?type=tcp#Primary"))
	}))
	defer primary.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#Reserve"))
	}))
	defer provider.Close()
	dir := t.TempDir()
	sub := xkeen.NewSubscriptionManager(dir)
	detector := xkeen.NewDetector(t.TempDir(), "", "", "", "", "", "")
	h := NewHandlers(&models.Config{}, sub, nil, detector, xkeen.NewPoolStore(dir), nil)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		h.HandleUpdateSubscription(r, httptest.NewRequest(http.MethodPost, "/api/subscription", bytes.NewBufferString(body)))
		return r
	}
	if result := post(`{"url":"` + primary.URL + `"}`); result.Code != http.StatusOK {
		t.Fatalf("legacy primary update: %d %s", result.Code, result.Body)
	}
	if result := post(`{"source":1,"url":"` + provider.URL + `"}`); result.Code != http.StatusOK {
		t.Fatalf("add second: %d %s", result.Code, result.Body)
	}
	get := httptest.NewRecorder()
	h.HandleGetSubscription(get, httptest.NewRequest(http.MethodGet, "/api/subscription", nil))
	var response struct {
		Sources []struct {
			ID          int    `json:"id"`
			URL         string `json:"url"`
			ServerCount int    `json:"server_count"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil || len(response.Sources) != 2 || response.Sources[0].URL != primary.URL || response.Sources[0].ServerCount != 1 || response.Sources[1].URL != provider.URL || response.Sources[1].ServerCount != 1 {
		t.Fatalf("second source metadata incorrect: %s (%v)", get.Body, err)
	}
	if result := post(`{"source":1,"url":""}`); result.Code != http.StatusOK || len(sub.GetServers()) != 1 || sub.GetServers()[0].SourceID != 0 {
		t.Fatalf("remove second: %d %s", result.Code, result.Body)
	}
	if result := post(`{"source":3,"url":"` + provider.URL + `"}`); result.Code != http.StatusBadRequest {
		t.Fatal("invalid source accepted")
	}
}
