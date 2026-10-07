package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"xkeen-panel/internal/models"
	"xkeen-panel/internal/xkeen"
)

func verifiedWatchdog(t *testing.T) *Watchdog {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "04_outbounds.json")
	fixture := `{"outbounds":[{"tag":"vless-reality","protocol":"vless","settings":{"address":"192.0.2.1","port":443,"id":"00000000-0000-4000-8000-000000000001","encryption":"none"},"streamSettings":{"network":"tcp"}},{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"}]}`
	os.WriteFile(path, []byte(fixture), 0600)
	policy := models.DefaultVerifiedFailover()
	policy.Enabled = true
	policy.CountryPriority = []string{"NL", "DE"}
	policy.AllowOtherCountries = false
	policy.ExcludeNameContains = []string{"Extra Whitelist2"}
	cfg := &models.Config{DataDir: dir, OutboundsFile: path, MaxFails: 2, BlacklistTTLSec: 300, ProbeConcurrency: 1, VerifiedFailover: policy}
	w := NewWatchdog(cfg, xkeen.NewSubscriptionManager(dir), xkeen.NewDetector(t.TempDir(), "", "", "", "", "", ""))
	w.coreInstance = func(string) string { return "test-boot:100:200" }
	w.active = true
	w.verifiedApplier = &xkeen.VerifiedApplier{Path: path, DataDir: dir, Validate: func() error { return nil }, Restart: func() error { return nil }, Running: func() bool { return true }}
	return w
}

func TestManualSelectionCancelsBulkPingWithoutOverlappingProbes(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	pingStarted := make(chan struct{})
	pingCancelled := make(chan struct{})
	var probeCalls atomic.Int32
	var runningProbes atomic.Int32
	var overlapping atomic.Bool
	w.verifiedProbe = func(ctx context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		if runningProbes.Add(1) > 1 {
			overlapping.Store(true)
		}
		defer runningProbes.Add(-1)
		if probeCalls.Add(1) == 1 {
			close(pingStarted)
			<-ctx.Done()
			close(pingCancelled)
			return xkeen.ProbeResult{}, ctx.Err()
		}
		return xkeen.ProbeResult{OK: true, Latency: 15}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pingDone := make(chan error, 1)
	go func() {
		_, err := w.CheckVerifiedServers(ctx, nil)
		pingDone <- err
	}()
	select {
	case <-pingStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("bulk ping did not start")
	}
	if _, err := w.CheckVerifiedServers(ctx, nil); err == nil {
		t.Fatal("a second bulk ping was allowed to queue")
	}
	selectionDone := make(chan error, 1)
	go func() {
		_, err := w.SelectVerified(ctx, 0) // Germany, while the NL ping is stalled.
		selectionDone <- err
	}()
	select {
	case err := <-selectionDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual selection waited for the full ping")
	}
	select {
	case <-pingCancelled:
	default:
		t.Fatal("bulk probe was not cancelled")
	}
	if err := <-pingDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("ping result = %v, want cancellation", err)
	}
	if overlapping.Load() || probeCalls.Load() != 3 {
		t.Fatalf("probe overlap=%v, calls=%d; want one ping + two selection probes", overlapping.Load(), probeCalls.Load())
	}
	if active := w.subscription.GetActiveServer(); active == nil || active.ID != 0 {
		t.Fatal("manual choice was not committed")
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("selected server was not applied")
	}
	if _, err := w.CheckVerifiedServers(ctx, nil); err != nil {
		t.Fatalf("ping could not be restarted after selecting: %v", err)
	}
}

func TestBulkPingDoesNotStartDuringManualSelection(t *testing.T) {
	w := verifiedWatchdog(t)
	finish := w.prioritizeManualSelection()
	if _, _, err := w.beginVerifiedCheck(context.Background()); err == nil {
		finish()
		t.Fatal("bulk ping started while manual selection was pending")
	}
	finish()
	_, stop, err := w.beginVerifiedCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stop()
}

func importVerified(t *testing.T, w *Watchdog) {
	t.Helper()
	content := "vless://00000000-0000-4000-8000-000000000003@192.0.2.3:443?type=tcp#Germany\n" +
		"vless://00000000-0000-4000-8000-000000000004@192.0.2.4:443?type=ws#Netherlands%20Extra%20Whitelist2\n" +
		"vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#Netherlands\n"
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte(content)) }))
	if _, err := w.subscription.UpdateURL(srv.URL); err != nil {
		t.Fatal(err)
	}
	srv.Close() // recovery must use the cached list despite a refresh failure
}

func addressOf(ob map[string]interface{}) string {
	return ob["settings"].(map[string]interface{})["address"].(string)
}

func TestVerifiedFailoverNLBeforeDEExcludedNeverProbed(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	var calls []string
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		addr := addressOf(ob)
		calls = append(calls, addr)
		return xkeen.ProbeResult{OK: addr == "192.0.2.3", Latency: 10, Successes: 1, Total: 2}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.checkVerified(context.Background())
	w.checkVerified(context.Background())
	if !reflect.DeepEqual(calls, []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.3", "192.0.2.3"}) {
		t.Fatalf("probe order: %v", calls)
	}
	active := w.subscription.GetActiveServer()
	if active == nil || active.Country != "DE" {
		t.Fatal("DE was not committed")
	}
	actual, _ := xkeen.SingleProxy(w.config.OutboundsFile)
	if addressOf(actual) != "192.0.2.3" {
		t.Fatal("wrong actual config")
	}
}

func TestVerifiedFailureRefreshesChangedIPWithSameName(t *testing.T) {
	w := verifiedWatchdog(t)
	const oldNL = "vless://00000000-0000-4000-8000-000000000001@192.0.2.1:443?type=tcp#Amsterdam%2C%20Netherlands%2C%20Extra"
	const oldDE = "vless://00000000-0000-4000-8000-000000000003@192.0.2.3:443?type=tcp#Frankfurt%2C%20Germany"
	const newNL = "vless://00000000-0000-4000-8000-000000000001@192.0.2.2:443?type=tcp#Amsterdam%2C%20Netherlands%2C%20Extra"
	const newDE = "vless://00000000-0000-4000-8000-000000000003@192.0.2.5:443?type=tcp#Frankfurt%2C%20Germany"
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			rw.Write([]byte(oldDE + "\n" + oldNL + "\n"))
			return
		}
		rw.Write([]byte(newDE + "\n" + newNL + "\n"))
	}))
	defer srv.Close()
	if _, err := w.subscription.UpdateURL(srv.URL); err != nil {
		t.Fatal(err)
	}
	// Previously failed IPs must not exclude replacements with the same names.
	w.blacklistServer(oldNL)
	w.blacklistServer(oldDE)
	var calls []string
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		addr := addressOf(ob)
		calls = append(calls, addr)
		if addr != "192.0.2.1" && requests.Load() != 2 {
			t.Fatal("candidate was tested before refreshing the subscription")
		}
		return xkeen.ProbeResult{OK: addr == "192.0.2.2", Latency: 10, Successes: 1, Total: 2}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.checkVerified(context.Background())
	w.checkVerified(context.Background())
	if len(calls) < 3 || calls[0] != "192.0.2.1" || calls[len(calls)-1] != "192.0.2.2" {
		t.Fatalf("fresh IP was not checked and confirmed: %v", calls)
	}
	for _, addr := range calls[1:] {
		if addr != "192.0.2.2" && addr != "192.0.2.5" {
			t.Fatalf("retired endpoint probed: %s", addr)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("subscription requests: got %d, want initial load + failure refresh", requests.Load())
	}
	for _, server := range w.subscription.GetServers() {
		if server.RawURI == oldNL || server.RawURI == oldDE {
			t.Fatal("old IP retained in the refreshed candidate list")
		}
	}
	active := w.subscription.GetActiveServer()
	if active == nil || active.RawURI != newNL {
		t.Fatal("replacement with the same name was not activated")
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.2" {
		t.Fatal("replacement IP was not written to the actual config")
	}
}

func TestVerifiedWorkingCurrentRetainedAcrossRefresh(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	original, _ := os.ReadFile(w.config.OutboundsFile)
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 20}, nil
	}
	w.checkVerified(context.Background())
	actual, _ := os.ReadFile(w.config.OutboundsFile)
	if string(original) != string(actual) {
		t.Fatal("healthy config replaced")
	}
	if w.subscription.GetActiveServer() != nil {
		t.Fatal("unmatched current config mislabelled as first subscription server")
	}
}

func TestVerifiedFailedSwitchDoesNotActivateCandidate(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.verifiedApplier.Validate = func() error { return errors.New("invalid") }
	before, _ := os.ReadFile(w.config.OutboundsFile)
	if _, err := w.SelectVerified(context.Background(), 2); err == nil {
		t.Fatal("invalid candidate accepted")
	}
	after, _ := os.ReadFile(w.config.OutboundsFile)
	if string(before) != string(after) {
		t.Fatal("failed manual transaction was not restored")
	}
	w.subscription.ReconcileActive(mustSingle(t, w.config.OutboundsFile))
	if w.subscription.GetActiveServer() != nil {
		t.Fatal("candidate marked active after failed application")
	}
}

func mustSingle(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	ob, err := xkeen.SingleProxy(path)
	if err != nil {
		t.Fatal(err)
	}
	return ob
}

func TestVerifiedCooldownAndDisabledWatchdog(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	count := 0
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		count++
		return xkeen.ProbeResult{OK: false}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.checkVerified(context.Background())
	w.checkVerified(context.Background())
	first := count
	w.checkVerified(context.Background())
	if count != first+1 {
		t.Fatal("cooldown did not suppress candidate probes")
	}
	w.SetActive(false)
	w.checkVerified(context.Background())
	if count != first+1 {
		t.Fatal("disabled watchdog probed")
	}
}

func TestVerifiedRefreshDoesNotReenablePausedWatchdog(t *testing.T) {
	w := verifiedWatchdog(t)
	w.config.WatchdogAutoStart = true
	data := models.SubscriptionData{Servers: []models.Server{{ID: 0, Name: "NL"}}}
	bytes, _ := json.Marshal(data)
	os.WriteFile(filepath.Join(w.config.DataDir, "subscription.json"), bytes, 0600)
	w.subscription.Load()
	w.active = false
	w.RefreshVerified("")
	if w.IsActive() {
		t.Fatal("refresh enabled watchdog")
	}
}
