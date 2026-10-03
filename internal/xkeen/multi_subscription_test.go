package xkeen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xkeen-panel/internal/models"
)

func TestTwoSubscriptionsKeepIndependentServersAndSelection(t *testing.T) {
	primary, changePrimary := subServer(t, body(uriA, uriB))
	secondary, changeSecondary := subServer(t, body(uriC))
	sm := NewSubscriptionManager(t.TempDir())
	if _, err := sm.UpdateURL(primary.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.UpdateSourceContext(context.Background(), 1, secondary.URL); err != nil {
		t.Fatal(err)
	}
	servers := sm.GetServers()
	if len(servers) != 3 || servers[0].SourceID != 0 || servers[2].SourceID != 1 || servers[2].ID != 2 {
		t.Fatalf("combined list has wrong source or IDs: %+v", servers)
	}
	if _, err := sm.SetActiveByRawURI(uriC, 1); err != nil {
		t.Fatal(err)
	}
	changePrimary(body(uriB, uriA))
	changeSecondary(body(uriC, uriA)) // the same URI can exist in both services
	if _, err := sm.Refresh(); err != nil {
		t.Fatal(err)
	}
	if active := sm.GetActiveServer(); active == nil || active.SourceID != 1 || active.RawURI != uriC {
		t.Fatalf("refresh lost the selected secondary server: %+v", active)
	}
	if _, err := sm.SetActiveByRawURI(uriA, 1); err != nil {
		t.Fatal(err)
	}
	if active := sm.GetActiveServer(); active == nil || active.SourceID != 1 {
		t.Fatalf("same URI selected the wrong provider: %+v", active)
	}
	if _, err := sm.UpdateSourceContext(context.Background(), 1, ""); err != nil {
		t.Fatal(err)
	}
	if data := sm.GetData(); data.SecondaryURL != "" || len(data.Servers) != 2 {
		t.Fatalf("removing secondary damaged primary: %+v", data)
	}
	reloaded := NewSubscriptionManager(sm.dataDir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.GetServers()) != 2 || reloaded.GetData().URL != primary.URL {
		t.Fatal("legacy primary subscription did not survive restart")
	}
}

func TestRefreshBothKeepsCachedSourceWhenOneProviderFails(t *testing.T) {
	var primaryFails atomic.Bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if primaryFails.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(uriA))
	}))
	defer primary.Close()
	secondary, changeSecondary := subServer(t, uriB)
	sm := NewSubscriptionManager(t.TempDir())
	if _, err := sm.UpdateURL(primary.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.UpdateSourceContext(context.Background(), 1, secondary.URL); err != nil {
		t.Fatal(err)
	}
	primaryFails.Store(true)
	changeSecondary(uriC)
	combined, err := sm.Refresh()
	if err != nil {
		t.Fatal("healthy secondary was blocked by failed primary:", err)
	}
	if len(combined) != 2 || combined[0].RawURI != uriA || combined[1].RawURI != uriC || combined[1].SourceID != 1 {
		t.Fatalf("partial refresh lost cached primary or new secondary: %+v", combined)
	}
	changeSecondary("unparseable")
	if _, err := sm.Refresh(); err == nil || strings.Contains(err.Error(), primary.URL) || strings.Contains(err.Error(), secondary.URL) {
		t.Fatalf("all-source failure did not report a safe error: %v", err)
	}
	if servers := sm.GetServers(); len(servers) != 2 || servers[0].RawURI != uriA || servers[1].RawURI != uriC {
		t.Fatal("failed refresh destroyed cached servers")
	}
}

func TestRefreshDownloadsBothSourcesConcurrently(t *testing.T) {
	var blocking atomic.Bool
	seen := make(chan int, 2)
	release := make(chan struct{})
	provider := func(id int, uri string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if blocking.Load() {
				seen <- id
				<-release
			}
			_, _ = w.Write([]byte(uri))
		}))
	}
	a, b := provider(0, uriA), provider(1, uriB)
	defer a.Close()
	defer b.Close()
	sm := NewSubscriptionManager(t.TempDir())
	if _, err := sm.UpdateURL(a.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.UpdateSourceContext(context.Background(), 1, b.URL); err != nil {
		t.Fatal(err)
	}
	blocking.Store(true)
	done := make(chan error, 1)
	go func() { _, err := sm.Refresh(); done <- err }()
	for range 2 {
		select {
		case <-seen:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("second subscription waited for the first download")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSourcePriorityUsesConfiguredServiceThenCountry(t *testing.T) {
	servers := []models.Server{
		{Name: "Amsterdam", Country: "NL", SourceID: 0},
		{Name: "Berlin", Country: "DE", SourceID: 1},
	}
	policy := models.VerifiedFailoverConfig{CountryPriority: []string{"NL", "DE"}, AllowOtherCountries: true, SourcePriority: "second"}
	if !PolicyBetter(servers[1], servers[0], policy) || !PolicyHasHigherPriority(servers[0], policy) {
		t.Fatal("secondary-first preference did not outrank country")
	}
	policy.SourcePriority = "all"
	if !PolicyBetter(servers[0], servers[1], policy) {
		t.Fatal("common mode did not rank by country")
	}
	policy.SourcePriority = "first"
	if !PolicyBetter(servers[0], servers[1], policy) || !PolicyHasHigherPriority(servers[1], policy) {
		t.Fatal("primary-first preference did not work")
	}
}
