package xkeen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSubscriptionRetries446WithVPNClientUserAgent(t *testing.T) {
	var primaryRequests, genericRequests, compatibleRequests atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryRequests.Add(1)
		if r.UserAgent() != "Go-http-client/1.1" {
			t.Errorf("working provider received unexpected User-Agent %q", r.UserAgent())
		}
		_, _ = w.Write([]byte(uriA))
	}))
	defer primary.Close()
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "Hiddify" {
			genericRequests.Add(1)
			w.WriteHeader(446)
			return
		}
		compatibleRequests.Add(1)
		w.Header().Set("Profile-Title", "SkipVPN")
		_, _ = w.Write([]byte(uriB))
	}))
	defer secondary.Close()

	sm := NewSubscriptionManager(t.TempDir())
	if _, err := sm.UpdateURL(primary.URL); err != nil {
		t.Fatal(err)
	}
	servers, err := sm.UpdateSourceContext(context.Background(), 1, secondary.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[1].SourceID != 1 || primaryRequests.Load() != 1 || genericRequests.Load() != 1 || compatibleRequests.Load() != 1 {
		t.Fatalf("incorrect retry or merged list: servers=%+v requests=%d/%d/%d", servers, primaryRequests.Load(), genericRequests.Load(), compatibleRequests.Load())
	}
	if sm.GetData().SecondaryDetectedName != "SkipVPN" {
		t.Fatal("profile title from successful 446 retry was not saved")
	}
	if _, err := sm.Refresh(); err != nil {
		t.Fatal(err)
	}
	if primaryRequests.Load() != 2 || genericRequests.Load() != 2 || compatibleRequests.Load() != 2 {
		t.Fatal("refresh did not retain per-provider request behavior")
	}
}

func TestSubscriptionStillReports446IfCompatibleRetryFails(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(446)
	}))
	defer provider.Close()

	sm := NewSubscriptionManager(t.TempDir())
	_, err := sm.UpdateSourceContext(context.Background(), 1, provider.URL)
	if err == nil || !strings.Contains(err.Error(), "446") || requests.Load() != 2 {
		t.Fatalf("unresolved provider error should be retained: %v, requests=%d", err, requests.Load())
	}
}
