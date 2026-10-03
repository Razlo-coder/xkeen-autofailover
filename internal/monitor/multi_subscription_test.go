package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"xkeen-panel/internal/xkeen"
)

func TestFailoverAndReturnAcrossTwoProviders(t *testing.T) {
	w := verifiedWatchdog(t)
	w.config.VerifiedFailover.SourcePriority = "first"
	// Source preference takes precedence over countries when both work.
	w.config.VerifiedFailover.CountryPriority = []string{"DE", "NL"}
	primary := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#Netherlands"))
	}))
	defer primary.Close()
	secondary := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("vless://00000000-0000-4000-8000-000000000003@192.0.2.3:443?type=tcp#Germany"))
	}))
	defer secondary.Close()
	if _, err := w.subscription.UpdateURL(primary.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := w.subscription.UpdateSourceContext(context.Background(), 1, secondary.URL); err != nil {
		t.Fatal(err)
	}
	var primaryAvailable atomic.Bool
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		switch addressOf(ob) {
		case "192.0.2.2":
			if primaryAvailable.Load() {
				return xkeen.ProbeResult{OK: true, Latency: 200}, nil
			}
		case "192.0.2.3":
			return xkeen.ProbeResult{OK: true, Latency: 230}, nil
		}
		return xkeen.ProbeResult{OK: false, Latency: -1}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.checkVerified(context.Background())
	w.checkVerified(context.Background())
	if active := w.subscription.GetActiveServer(); active == nil || active.SourceID != 1 || addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatalf("blocked first provider prevented fallback: %+v", active)
	}
	primaryAvailable.Store(true)
	w.lastVerifiedSwitch = time.Time{}
	w.lastPriorityAttempt = time.Time{}
	w.checkVerified(context.Background())
	if active := w.subscription.GetActiveServer(); active == nil || active.SourceID != 0 || addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.2" {
		t.Fatalf("healthy preferred provider was not restored: %+v", active)
	}
}
