package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"xkeen-panel/internal/xkeen"
)

func TestQualityRequiresConsecutiveSlowChecksAndRejectsSlowReplacement(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	var calls []string
	currentLatency := 2000
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		addr := addressOf(ob)
		calls = append(calls, addr)
		latency := 100
		if addr == "192.0.2.1" {
			latency = currentLatency
		}
		if addr == "192.0.2.2" {
			latency = 2500
		}
		return xkeen.ProbeResult{OK: true, Latency: latency}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.checkVerified(context.Background())
	if !w.GetStatus().QualityDegraded || !w.connected {
		t.Fatal("reachable slow connection must be shown as degraded")
	}
	currentLatency = 1500 // exact threshold is healthy and resets the streak
	w.checkVerified(context.Background())
	if w.GetStatus().QualityDegraded || w.qualityFailCount != 0 {
		t.Fatal("healthy check did not reset quality streak")
	}
	currentLatency = 2000
	w.checkVerified(context.Background())
	w.checkVerified(context.Background())
	if len(calls) != 4 {
		t.Fatal("replacement searched before three consecutive poor checks")
	}
	w.checkVerified(context.Background())
	if !reflect.DeepEqual(calls, []string{"192.0.2.1", "192.0.2.1", "192.0.2.1", "192.0.2.1", "192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.3"}) {
		t.Fatalf("unexpected probe order: %v", calls)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" || w.GetStatus().QualityDegraded || w.qualityFailCount != 0 {
		t.Fatal("healthy fallback was not committed")
	}
}

func TestQualityNoGoodReplacementKeepsConfigAndCooldown(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			w := verifiedWatchdog(t)
			importVerified(t, w)
			w.config.VerifiedFailover.QualityEnabled = enabled
			var calls int
			w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
				calls++
				return xkeen.ProbeResult{OK: true, Latency: 2000}, nil
			}
			w.verifiedApplier.Probe = w.verifiedProbe
			before, _ := os.ReadFile(w.config.OutboundsFile)
			for i := 0; i < 4; i++ {
				w.checkVerified(context.Background())
			}
			after, _ := os.ReadFile(w.config.OutboundsFile)
			wantCalls := 4
			if enabled {
				wantCalls += 2
			}
			if string(before) != string(after) || calls != wantCalls || !w.connected {
				t.Fatalf("config changed or bad retry count: calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}

func TestQualityPostRestartFailureRollsBack(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.config.VerifiedFailover.QualityFailCount = 1
	probed := map[string]int{}
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		addr := addressOf(ob)
		probed[addr]++
		latency := 100
		if addr == "192.0.2.1" || probed[addr] > 1 {
			latency = 3000
		}
		return xkeen.ProbeResult{OK: true, Latency: latency}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	before, _ := os.ReadFile(w.config.OutboundsFile)
	w.checkVerified(context.Background())
	after, _ := os.ReadFile(w.config.OutboundsFile)
	if string(before) != string(after) || w.subscription.GetActiveServer() != nil {
		t.Fatal("slow post-restart result was committed instead of rolled back")
	}
	if _, err := os.Stat(w.config.DataDir + "/failover-pending.json"); !os.IsNotExist(err) {
		t.Fatal("rollback journal remained")
	}
}

func TestPriorityReturnRefreshesRotatedIPsAndWaitsAfterManualChoice(t *testing.T) {
	w := verifiedWatchdog(t)
	oldNL := "vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#Netherlands"
	oldDE := "vless://00000000-0000-4000-8000-000000000003@192.0.2.3:443?type=tcp#Germany"
	newNL := "vless://00000000-0000-4000-8000-000000000002@192.0.2.5:443?type=tcp#Netherlands"
	newDE := "vless://00000000-0000-4000-8000-000000000003@192.0.2.6:443?type=tcp#Germany"
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			rw.Write([]byte(oldDE + "\n" + oldNL))
			return
		}
		rw.Write([]byte(newDE + "\n" + newNL + "\nvless://00000000-0000-4000-8000-000000000004@192.0.2.4:443?type=tcp#Netherlands%20Extra%20Whitelist2"))
	}))
	defer srv.Close()
	if _, err := w.subscription.UpdateURL(srv.URL); err != nil {
		t.Fatal(err)
	}
	var calls []string
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		calls = append(calls, addressOf(ob))
		return xkeen.ProbeResult{OK: true, Latency: 100}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if _, err := w.SelectVerified(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	w.blacklistServer(oldNL)
	w.checkVerified(context.Background())
	if requests != 1 || len(calls) != 3 {
		t.Fatal("manual selection hold time was ignored")
	}
	w.lastVerifiedSwitch = time.Time{}
	calls = nil
	w.checkVerified(context.Background())
	if requests != 2 || !reflect.DeepEqual(calls, []string{"192.0.2.3", "192.0.2.5", "192.0.2.5"}) {
		t.Fatalf("did not refresh/return using new IP: %d %v", requests, calls)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.5" {
		t.Fatal("preferred country not applied")
	}
	// The best rank stays put despite another reachable entry and refreshes.
	w.lastVerifiedSwitch = time.Time{}
	w.lastPriorityAttempt = time.Time{}
	w.checkVerified(context.Background())
	if requests != 2 || len(calls) != 4 {
		t.Fatal("best rank unnecessarily rotated")
	}
}

func TestPriorityReturnWithinCountryRejectsPoorQualityAndEqualRanks(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.config.VerifiedFailover.PreferredServerNames = []string{"Netherlands Preferred"}
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 100}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if _, err := w.SelectVerified(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	content := "vless://00000000-0000-4000-8000-000000000005@192.0.2.5:443?type=tcp#Netherlands%20Preferred\n" +
		"vless://00000000-0000-4000-8000-000000000006@192.0.2.6:443?type=tcp#Netherlands%20Other\n" +
		"vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#Netherlands"
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte(content)) }))
	defer srv.Close()
	if _, err := w.RefreshVerified(srv.URL); err != nil {
		t.Fatal(err)
	}
	w.lastVerifiedSwitch = time.Time{}
	preferredLatency := 2000
	var calls []string
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		addr := addressOf(ob)
		calls = append(calls, addr)
		latency := 100
		if addr == "192.0.2.5" {
			latency = preferredLatency
		}
		return xkeen.ProbeResult{OK: true, Latency: latency}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	before, _ := os.ReadFile(w.config.OutboundsFile)
	w.checkVerified(context.Background())
	w.checkVerified(context.Background()) // interval prevents another search
	after, _ := os.ReadFile(w.config.OutboundsFile)
	if string(before) != string(after) || !reflect.DeepEqual(calls, []string{"192.0.2.2", "192.0.2.5", "192.0.2.2"}) {
		t.Fatalf("slow/equal-rank return or missing cooldown: %v", calls)
	}
	w.ClearBlacklist(w.subscription.GetServers()[0].RawURI)
	w.lastPriorityAttempt = time.Time{}
	preferredLatency = 200
	w.checkVerified(context.Background())
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.5" {
		t.Fatal("preferred name did not recover")
	}
}

func TestPriorityReturnCanBeDisabled(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	calls := 0
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		calls++
		return xkeen.ProbeResult{OK: true, Latency: 100}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if _, err := w.SelectVerified(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	w.lastVerifiedSwitch = time.Time{}
	w.config.VerifiedFailover.ReturnToPriority = false
	w.checkVerified(context.Background())
	if calls != 3 || addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("disabled return changed working fallback")
	}
}
