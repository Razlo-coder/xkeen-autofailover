package monitor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"xkeen-panel/internal/xkeen"
)

func TestOutageUsesReachableSlowFallbackAfterHealthySearch(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		switch addressOf(ob) {
		case "192.0.2.2":
			return xkeen.ProbeResult{OK: false, Latency: -1}, nil // preferred Netherlands is blocked
		case "192.0.2.3":
			return xkeen.ProbeResult{OK: true, Latency: 3000}, nil
		default:
			return xkeen.ProbeResult{OK: false, Latency: -1}, nil
		}
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if err := w.failoverVerified(context.Background(), mustSingle(t, w.config.OutboundsFile)); err != nil {
		t.Fatal(err)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("reachable 3000 ms Germany did not replace the blocked server")
	}
	if !w.GetStatus().QualityDegraded || w.lastLatency != 3000 {
		t.Fatal("slow but reachable fallback was not shown as degraded")
	}
	if got := w.subscription.GetActiveServer(); got == nil || got.Name != "Germany" {
		t.Fatal("slow fallback was not saved as active")
	}
}

func TestOutagePrefersHealthyThenLowestDelaySlowFallback(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		w := verifiedWatchdog(t)
		importVerified(t, w)
		w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
			switch addressOf(ob) {
			case "192.0.2.2":
				return xkeen.ProbeResult{OK: true, Latency: 6500}, nil
			case "192.0.2.3":
				latency := 3000
				if healthy {
					latency = 100
				}
				return xkeen.ProbeResult{OK: true, Latency: latency}, nil
			default:
				return xkeen.ProbeResult{OK: false, Latency: -1}, nil
			}
		}
		w.verifiedApplier.Probe = w.verifiedProbe
		if err := w.failoverVerified(context.Background(), mustSingle(t, w.config.OutboundsFile)); err != nil {
			t.Fatal(err)
		}
		if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
			t.Fatalf("healthy=%v: slower preferred candidate displaced the better connection", healthy)
		}
	}
}

func TestSlowFallbackRequiresReachabilityAndPostRestartConfirmation(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		if addressOf(ob) == "192.0.2.3" {
			return xkeen.ProbeResult{OK: true, Latency: 7999}, nil
		}
		return xkeen.ProbeResult{OK: false, Latency: -1}, nil
	}
	w.verifiedApplier.Probe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: false, Latency: -1}, nil
	}
	before, _ := os.ReadFile(w.config.OutboundsFile)
	if err := w.failoverVerified(context.Background(), mustSingle(t, w.config.OutboundsFile)); err == nil {
		t.Fatal("candidate with failed post-restart check was committed")
	}
	after, _ := os.ReadFile(w.config.OutboundsFile)
	if string(after) != string(before) {
		t.Fatal("unconfirmed slow fallback did not roll back")
	}
	if _, err := os.Stat(w.config.DataDir + "/failover-pending.json"); !os.IsNotExist(err) {
		t.Fatal("rollback journal was not cleared")
	}
}

func TestDegradedButReachableConnectionKeepsItsServerWhenAllAlternativesSlow(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.config.VerifiedFailover.QualityFailCount = 1
	currentLatency := 100
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		if addressOf(ob) == "192.0.2.3" {
			return xkeen.ProbeResult{OK: true, Latency: currentLatency}, nil
		}
		return xkeen.ProbeResult{OK: true, Latency: 2500}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if _, err := w.SelectVerified(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	currentLatency = 3000
	w.checkVerified(context.Background())
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("slow current server was replaced by another slow server")
	}
}

func TestManualSlowChoiceStaysUntilBetterServerReallyPasses(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	netherlandsAvailable := false
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		if addressOf(ob) == "192.0.2.3" {
			return xkeen.ProbeResult{OK: true, Latency: 3000}, nil
		}
		if netherlandsAvailable {
			return xkeen.ProbeResult{OK: true, Latency: 100}, nil
		}
		return xkeen.ProbeResult{OK: false, Latency: -1}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.config.VerifiedFailover.QualityFailCount = 1
	if _, err := w.SelectVerified(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if !w.manualSlowChoice {
		t.Fatal("manual acceptance of high latency not recorded")
	}
	w.lastVerifiedSwitch = time.Time{} // simulate five minutes passing
	w.checkVerified(context.Background())
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("blocked Netherlands replaced manually chosen Germany")
	}
	// The exception must survive a panel restart and subscription refresh.
	restarted := NewWatchdog(w.config, w.subscription, w.detector)
	restarted.coreInstance = w.coreInstance
	restarted.active = true
	restarted.verifiedProbe = w.verifiedProbe
	restarted.verifiedApplier = w.verifiedApplier
	restarted.rememberVerifiedCurrent(mustSingle(t, w.config.OutboundsFile))
	if !restarted.manualSlowChoice {
		t.Fatal("panel restart forgot deliberate slow selection")
	}
	if _, err := restarted.RefreshVerified(""); err == nil {
		t.Fatal("test subscription unexpectedly refreshed")
	}
	restarted.checkVerified(context.Background())
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("subscription refresh reset manual slow choice")
	}
	// A better preferred server must still return after it genuinely passes.
	netherlandsAvailable = true
	restarted.ClearBlacklist(w.subscription.GetServers()[2].RawURI)
	restarted.lastVerifiedSwitch = time.Time{}
	restarted.lastPriorityAttempt = time.Time{}
	restarted.checkVerified(context.Background())
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.2" || restarted.manualSlowChoice {
		t.Fatal("confirmed healthy priority did not replace the slow manual fallback")
	}
}

func TestSlowFallbackAtLimitAndAbove(t *testing.T) {
	for _, latency := range []int{7999, 8000} {
		w := verifiedWatchdog(t)
		importVerified(t, w)
		w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
			if addressOf(ob) == "192.0.2.3" {
				return xkeen.ProbeResult{OK: true, Latency: latency}, nil
			}
			return xkeen.ProbeResult{OK: false, Latency: -1}, nil
		}
		w.verifiedApplier.Probe = w.verifiedProbe
		err := w.failoverVerified(context.Background(), mustSingle(t, w.config.OutboundsFile))
		selected := addressOf(mustSingle(t, w.config.OutboundsFile)) == "192.0.2.3"
		if selected != (latency == 7999) || (err == nil) != selected {
			t.Fatalf("latency %d: selected=%v err=%v", latency, selected, err)
		}
	}
}

func TestManualSlowIdentityRecordContainsNoConnectionSecrets(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 3000}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if _, err := w.SelectVerified(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(w.verifiedIdentityPath())
	if err != nil || !strings.Contains(string(data), `"manual_slow_choice": true`) {
		t.Fatal("manual slow choice not persisted")
	}
	if strings.Contains(string(data), "192.0.2.") || strings.Contains(string(data), "00000000-") || strings.Contains(string(data), "vless://") {
		t.Fatal("connection secrets copied into display record")
	}
}
