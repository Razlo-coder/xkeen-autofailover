package xkeen

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSubscriptionTitleFromHeaderAndBodyWithManualOverride(t *testing.T) {
	var version atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch version.Load() {
		case 0:
			w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte("SkipVPN")))
			_, _ = w.Write([]byte("#profile-title: Старое имя\n" + uriA))
		case 1:
			body := "#profile-title: base64:" + base64.StdEncoding.EncodeToString([]byte("Финляндия+")) + "\n" + uriB
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(body))))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer provider.Close()

	sm := NewSubscriptionManager(t.TempDir())
	if _, err := sm.UpdateSourceContext(context.Background(), 1, provider.URL); err != nil {
		t.Fatal(err)
	}
	if got := sm.GetData(); got.SecondaryDetectedName != "SkipVPN" || got.SecondaryName != "" {
		t.Fatalf("header title was not detected: %+v", got)
	}
	if err := sm.SetSourceName(1, "Моё название"); err != nil {
		t.Fatal(err)
	}
	version.Store(1)
	if _, err := sm.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := sm.GetData(); got.SecondaryDetectedName != "Финляндия+" || got.SecondaryName != "Моё название" {
		t.Fatalf("body title did not refresh independently of manual name: %+v", got)
	}
	version.Store(2)
	if _, err := sm.Refresh(); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if got := sm.GetData(); got.SecondaryDetectedName != "Финляндия+" {
		t.Fatal("failed refresh discarded cached profile title")
	}
	reloaded := NewSubscriptionManager(sm.dataDir)
	if err := reloaded.Load(); err != nil || reloaded.GetData().SecondaryDetectedName != "Финляндия+" {
		t.Fatalf("profile title did not survive restart: %v", err)
	}
	if err := reloaded.SetSourceName(1, ""); err != nil || reloaded.GetData().SecondaryName != "" {
		t.Fatalf("manual title could not be cleared: %v", err)
	}
	if _, err := reloaded.UpdateSourceContext(context.Background(), 1, ""); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.GetData(); got.SecondaryDetectedName != "" || got.SecondaryName != "" {
		t.Fatal("deleted subscription retained its old title")
	}
}

func TestSubscriptionTitleRejectsInvalidMetadata(t *testing.T) {
	if got := subscriptionProfileTitle("base64:invalid!", []byte("#profile-title: Резерв\n"+uriA)); got != "Резерв" {
		t.Fatalf("invalid header did not fall back to body: %q", got)
	}
	if got := subscriptionProfileTitle("", []byte(uriA+"\n#profile-title: Подмена")); got != "" {
		t.Fatalf("title after server list was accepted: %q", got)
	}
	if got := cleanProfileTitle("Bad\nName"); got != "" {
		t.Fatalf("control characters were accepted: %q", got)
	}
	if got := cleanProfileTitle(strings.Repeat("Я", 50)); len([]rune(got)) != 40 {
		t.Fatalf("title length cap failed: %d", len([]rune(got)))
	}
}
