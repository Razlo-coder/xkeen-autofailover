package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"xkeen-panel/internal/xkeen"
)

func TestConnectionSinceSurvivesPanelRestartAndSubscriptionRefresh(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	ob := mustSingle(t, w.config.OutboundsFile)
	started := time.Now().Add(-78 * time.Minute).UTC().Truncate(time.Second)
	w.confirmConnection(ob, started, false)
	w.connected = true
	status := w.GetStatus()
	if status.ConnectedSince == nil || !status.ConnectedSince.Equal(started) || status.Uptime != "1 ч 18 мин" || status.UptimeSeconds < 4680 {
		t.Fatalf("wrong connection duration: %+v", status)
	}
	// Metadata refresh (including removing the old IP) does not touch the session.
	w.rememberVerifiedCurrent(ob)
	w.confirmConnection(ob, time.Now(), false)
	if since := w.currentConnectionSince(ob, time.Now()); since == nil || !since.Equal(started) {
		t.Fatal("a healthy recheck or subscription reconciliation reset uptime")
	}
	data, err := os.ReadFile(w.connectionSessionPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "192.0.2.") || strings.Contains(string(data), "00000000-") || strings.Contains(string(data), "vless://") {
		t.Fatal("connection record contains private connection values")
	}
	restarted := NewWatchdog(w.config, w.subscription, w.detector)
	restarted.coreInstance = w.coreInstance
	restarted.confirmConnection(ob, time.Now(), false)
	restarted.connected = true
	if since := restarted.GetStatus().ConnectedSince; since == nil || !since.Equal(started) {
		t.Fatal("panel restart lost the confirmed connection time")
	}
}

func TestConnectionSinceResetsOnCoreOrRouterRestartAndConfigChange(t *testing.T) {
	for _, change := range []string{"core", "router", "config", "missing-core"} {
		t.Run(change, func(t *testing.T) {
			w := verifiedWatchdog(t)
			ob := mustSingle(t, w.config.OutboundsFile)
			started := time.Now().Add(-time.Hour)
			w.confirmConnection(ob, started, false)
			w.connected = true
			switch change {
			case "core":
				w.coreInstance = func(string) string { return "test-boot:100:300" }
			case "router":
				w.coreInstance = func(string) string { return "other-boot:100:200" }
			case "missing-core":
				w.coreInstance = func(string) string { return "" }
			case "config":
				ob["settings"].(map[string]interface{})["address"] = "192.0.2.99"
				if err := xkeen.WriteOutboundsConfig(w.config.OutboundsFile, map[string]interface{}{"outbounds": []interface{}{ob}}); err != nil {
					t.Fatal(err)
				}
			}
			if status := w.GetStatus(); status.ConnectedSince != nil || status.Uptime != "" {
				t.Fatal("old connection duration displayed for a changed/missing core or config")
			}
			confirmed := time.Now().UTC()
			w.confirmConnection(ob, confirmed, false)
			if change != "missing-core" {
				if since := w.GetStatus().ConnectedSince; since == nil || !since.Equal(confirmed) {
					t.Fatal("new connection did not start at confirmation")
				}
			}
		})
	}
}

func TestConnectionSinceClearsOnFailureAndStopButNotPoorQuality(t *testing.T) {
	w := verifiedWatchdog(t)
	w.config.MaxFails = 99
	w.config.VerifiedFailover.QualityFailCount = 99
	ob := mustSingle(t, w.config.OutboundsFile)
	started := time.Now().Add(-time.Hour).UTC()
	w.confirmConnection(ob, started, false)
	ok := true
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: ok, Latency: 2000}, nil
	}
	w.checkVerified(context.Background())
	if since := w.GetStatus().ConnectedSince; since == nil || !since.Equal(started) {
		t.Fatal("poor but reachable connection reset uptime")
	}
	ok = false
	w.checkVerified(context.Background())
	if status := w.GetStatus(); status.ConnectedSince != nil || status.Uptime != "" {
		t.Fatal("failed connection kept live uptime")
	}
	ok = true
	w.checkVerified(context.Background())
	if since := w.GetStatus().ConnectedSince; since == nil || !since.After(started) {
		t.Fatal("recovery failed to start a new connection session")
	}
	w.MarkCoreStopped()
	restarted := NewWatchdog(w.config, w.subscription, w.detector)
	restarted.coreInstance = w.coreInstance
	confirmed := time.Now().UTC()
	restarted.confirmConnection(ob, confirmed, false)
	if since := restarted.currentConnectionSince(ob, time.Now()); since == nil || !since.Equal(confirmed) {
		t.Fatal("stopped session revived after panel restart")
	}
}

func TestConnectionSinceFollowsConfirmedSelectionAndKeepsFailedSelection(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 25}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	old := time.Now().Add(-time.Hour)
	w.confirmConnection(mustSingle(t, w.config.OutboundsFile), old, false)
	if _, err := w.SelectVerified(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	since := w.GetStatus().ConnectedSince
	if since == nil || !since.After(old) {
		t.Fatal("confirmed selection kept previous uptime")
	}
	w.verifiedApplier.Validate = func() error { return errors.New("validation rejected") }
	if _, err := w.SelectVerified(context.Background(), 2); err == nil {
		t.Fatal("expected failed selection")
	}
	if after := w.GetStatus().ConnectedSince; after == nil || !after.Equal(*since) {
		t.Fatal("rejected selection changed connection time")
	}
	// A deliberate reapply of the same server also starts a fresh session.
	w.verifiedApplier.Validate = func() error { return nil }
	if _, err := w.SelectVerified(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if after := w.GetStatus().ConnectedSince; after == nil || !after.After(*since) {
		t.Fatal("reapplying the same server did not reset connection time")
	}
}

func TestConnectionRecordRejectsCorruptFutureAndUnknownCore(t *testing.T) {
	for _, fixture := range []string{"corrupt", "future", "unknown", "large"} {
		t.Run(fixture, func(t *testing.T) {
			w := verifiedWatchdog(t)
			ob := mustSingle(t, w.config.OutboundsFile)
			hash, _ := xkeen.OutboundFingerprint(ob)
			record := connectionSession{Fingerprint: hash, CoreInstance: w.coreInstance("xray"), Since: time.Now().Add(time.Hour)}
			if fixture == "unknown" {
				record.CoreInstance = ""
				record.Since = time.Now().Add(-time.Hour)
			}
			body, _ := json.Marshal(record)
			if fixture == "corrupt" {
				body = []byte("invalid")
			}
			if fixture == "large" {
				body = []byte(strings.Repeat(" ", 65537))
			}
			os.WriteFile(w.connectionSessionPath(), body, 0600)
			confirmed := time.Now().UTC()
			w.confirmConnection(ob, confirmed, false)
			if since := w.currentConnectionSince(ob, time.Now()); since == nil || !since.Equal(confirmed) {
				t.Fatal("invalid saved session trusted")
			}
		})
	}
}

func TestConnectionRecordRetriesFailedWrite(t *testing.T) {
	w := verifiedWatchdog(t)
	ob := mustSingle(t, w.config.OutboundsFile)
	os.Mkdir(w.connectionSessionPath(), 0700)
	started := time.Now().Add(-time.Hour).UTC()
	w.confirmConnection(ob, started, false)
	if since := w.currentConnectionSince(ob, time.Now()); since == nil || !since.Equal(started) {
		t.Fatal("write error lost in-memory connection time")
	}
	os.Remove(w.connectionSessionPath())
	w.confirmConnection(ob, time.Now(), false)
	data, err := os.ReadFile(w.connectionSessionPath())
	var record connectionSession
	if err != nil || json.Unmarshal(data, &record) != nil || !record.Since.Equal(started) {
		t.Fatal("failed save was not retried without resetting uptime")
	}
}

func TestFormatConnectionUptime(t *testing.T) {
	for seconds, expected := range map[int64]string{0: "0 с", 59: "59 с", 60: "1 мин", 4680: "1 ч 18 мин", 90061: "1 д 1 ч 1 мин"} {
		if got := formatUptime(seconds); got != expected {
			t.Fatalf("duration(%d)=%s; want %s", seconds, got, expected)
		}
	}
}
