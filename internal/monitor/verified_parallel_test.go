package monitor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"xkeen-panel/internal/xkeen"
)

func awaitVerified(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not react to cancellation")
		return nil
	}
}

func TestParallelPingIsBoundedAndCoreActionCancelsAllWorkers(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.config.ProbeConcurrency = 100 // conservative cap applies to old YAML too
	current := mustSingle(t, w.config.OutboundsFile)
	candidates := w.verifiedCandidates(current, nil, false)
	base := append([]verifiedCandidate{}, candidates...)
	for range 4 {
		candidates = append(candidates, base...)
	} // ten jobs, at most eight active
	started := make(chan struct{}, 10)
	var active atomic.Int32
	var maxActive atomic.Int32
	w.verifiedProbe = func(ctx context.Context, _ map[string]interface{}) (xkeen.ProbeResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		started <- struct{}{}
		<-ctx.Done()
		return xkeen.ProbeResult{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		ctx, finish, err := w.lockVerifiedOperation(context.Background(), false)
		if err != nil {
			done <- err
			return
		}
		defer finish()
		done <- w.probeVerifiedCandidates(ctx, candidates, false, func(int, xkeen.ProbeResult, error) bool { t.Error("cancelled result emitted"); return true })
	}()
	for range 8 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("eight probes did not run together")
		}
	}
	// Read APIs used to block behind the entire scan.
	readDone := make(chan error, 1)
	go func() { w.GetAutomation(); w.PolicyServers(); w.GetStatus(); readDone <- nil }()
	if err := awaitVerified(t, readDone); err != nil {
		t.Fatal(err)
	}
	coreDone := make(chan error, 1)
	go func() { release := w.LockCoreOperation(); defer release(); coreDone <- nil }()
	if err := awaitVerified(t, coreDone); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(awaitVerified(t, done), context.Canceled) {
		t.Fatal("scan was not cancelled")
	}
	if active.Load() != 0 || maxActive.Load() != 8 {
		t.Fatalf("active=%d max=%d", active.Load(), maxActive.Load())
	}
}

func TestParallelFailoverUsesFirstFastPriorityAndReapsOtherProbesBeforeApply(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.config.ProbeConcurrency = 8
	higherStarted := make(chan struct{})
	var active atomic.Int32
	w.verifiedProbe = func(ctx context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		active.Add(1)
		defer active.Add(-1)
		if addressOf(ob) == "192.0.2.3" {
			<-higherStarted
			return xkeen.ProbeResult{OK: true, Latency: 10}, nil
		}
		close(higherStarted)
		<-ctx.Done()
		return xkeen.ProbeResult{}, ctx.Err()
	}
	w.verifiedApplier.Validate = func() error {
		if active.Load() != 0 {
			t.Error("config applied while candidate probes still running")
		}
		return nil
	}
	w.verifiedApplier.Probe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 10}, nil
	}
	if err := w.failoverVerified(context.Background(), mustSingle(t, w.config.OutboundsFile)); err != nil {
		t.Fatal(err)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("fast Germany was not chosen while Netherlands was still probing")
	}
	if active.Load() != 0 {
		t.Fatal("candidate worker leaked")
	}
}

func TestOutageSwitchesToFastSamePriorityWithoutWaitingForWholeScan(t *testing.T) {
	w := verifiedWatchdog(t)
	w.config.ProbeConcurrency = 8
	content := "vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#Netherlands%20Slow\n" +
		"vless://00000000-0000-4000-8000-000000000003@192.0.2.3:443?type=tcp#Netherlands%20Fast\n" +
		"vless://00000000-0000-4000-8000-000000000004@192.0.2.4:443?type=tcp#Germany"
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write([]byte(content)) }))
	if _, err := w.subscription.UpdateURL(srv.URL); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	firstStarted := make(chan struct{})
	firstCancelled := make(chan struct{})
	lowerCancelled := make(chan struct{})
	var active atomic.Int32
	w.verifiedProbe = func(ctx context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		active.Add(1)
		defer active.Add(-1)
		switch addressOf(ob) {
		case "192.0.2.2":
			close(firstStarted)
			<-ctx.Done()
			close(firstCancelled)
			return xkeen.ProbeResult{}, ctx.Err()
		case "192.0.2.3":
			<-firstStarted
			return xkeen.ProbeResult{OK: true, Latency: 400}, nil
		default:
			<-ctx.Done()
			close(lowerCancelled)
			return xkeen.ProbeResult{}, ctx.Err()
		}
	}
	w.verifiedApplier.Validate = func() error {
		if active.Load() != 0 {
			t.Error("config applied before other probes were cancelled")
		}
		return nil
	}
	w.verifiedApplier.Probe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 400}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.failoverVerified(ctx, mustSingle(t, w.config.OutboundsFile)); err != nil {
		t.Fatal(err)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("fast Netherlands did not win over an earlier hanging peer")
	}
	select {
	case <-firstCancelled:
	default:
		t.Fatal("hanging probe was not cancelled before applying the server")
	}
	select {
	case <-lowerCancelled:
	default:
		t.Fatal("lower-priority probe was not cancelled before applying the server")
	}
}

func TestFirstOfflineCheckStartsFastRecovery(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.config.MaxFails = 3
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		if addressOf(ob) == "192.0.2.1" {
			return xkeen.ProbeResult{OK: false, Latency: -1}, nil
		}
		return xkeen.ProbeResult{OK: true, Latency: 200}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.checkVerified(context.Background())
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.2" {
		t.Fatal("first offline check did not connect the fast preferred server")
	}
	if w.failCount != 0 || !w.GetStatus().Connected {
		t.Fatal("successful fast recovery did not restore online status")
	}
}

func TestFirstOfflineCheckDoesNotAcceptSlowFallback(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.config.MaxFails = 3
	w.verifiedProbe = func(_ context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		if addressOf(ob) == "192.0.2.3" {
			return xkeen.ProbeResult{OK: true, Latency: 3000}, nil
		}
		return xkeen.ProbeResult{OK: false, Latency: -1}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	w.checkVerified(context.Background())
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.1" {
		t.Fatal("slow fallback was applied after only one failed check")
	}
	w.checkVerified(context.Background())
	w.checkVerified(context.Background())
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("slow fallback was not applied after the configured failure count")
	}
}

func TestOutageUsesNextPreferredCountryBeforeUnprioritizedScanEnds(t *testing.T) {
	w := verifiedWatchdog(t)
	w.config.ProbeConcurrency = 8
	w.config.VerifiedFailover.AllowOtherCountries = true
	content := "vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#Netherlands\n" +
		"vless://00000000-0000-4000-8000-000000000003@192.0.2.3:443?type=tcp#Germany\n" +
		"vless://00000000-0000-4000-8000-000000000004@192.0.2.4:443?type=tcp#France"
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write([]byte(content)) }))
	if _, err := w.subscription.UpdateURL(srv.URL); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	netherlandsStarted := make(chan struct{})
	netherlandsCancelled := make(chan struct{})
	franceStarted := make(chan struct{})
	franceCancelled := make(chan struct{})
	w.verifiedProbe = func(ctx context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		switch addressOf(ob) {
		case "192.0.2.2":
			close(netherlandsStarted)
			<-ctx.Done()
			close(netherlandsCancelled)
			return xkeen.ProbeResult{}, ctx.Err()
		case "192.0.2.3":
			<-netherlandsStarted
			<-franceStarted
			return xkeen.ProbeResult{OK: true, Latency: 200}, nil
		default:
			close(franceStarted)
			<-ctx.Done()
			close(franceCancelled)
			return xkeen.ProbeResult{}, ctx.Err()
		}
	}
	w.verifiedApplier.Validate = func() error {
		select {
		case <-netherlandsCancelled:
		default:
			t.Error("Germany was applied before Netherlands probe was cancelled")
		}
		select {
		case <-franceCancelled:
		default:
			t.Error("Germany was applied before France probe was cancelled")
		}
		return nil
	}
	w.verifiedApplier.Probe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 200}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.failoverVerified(ctx, mustSingle(t, w.config.OutboundsFile)); err != nil {
		t.Fatal(err)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("fast Germany was not selected while Netherlands was still probing")
	}
	select {
	case <-franceCancelled:
	default:
		t.Fatal("lower-priority scan was not cancelled")
	}
}

func TestRefreshInterruptsOldBulkAndAutomaticScans(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "bulk", true: "automatic"}[automatic], func(t *testing.T) {
			w := verifiedWatchdog(t)
			importVerified(t, w)
			w.config.ProbeConcurrency = 3
			started := make(chan struct{}, 3)
			var active atomic.Int32
			w.verifiedProbe = func(ctx context.Context, _ map[string]interface{}) (xkeen.ProbeResult, error) {
				active.Add(1)
				defer active.Add(-1)
				started <- struct{}{}
				<-ctx.Done()
				return xkeen.ProbeResult{}, ctx.Err()
			}
			original, _ := os.ReadFile(w.config.OutboundsFile)
			done := make(chan error, 1)
			go func() {
				if !automatic {
					_, err := w.CheckVerifiedServers(context.Background(), nil)
					done <- err
					return
				}
				ctx, finish, err := w.lockVerifiedOperation(context.Background(), false)
				if err != nil {
					done <- err
					return
				}
				defer finish()
				done <- w.failoverVerified(ctx, mustSingle(t, w.config.OutboundsFile))
			}()
			for range 2 {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("old scan did not start")
				}
			}
			fresh := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if active.Load() != 0 {
					t.Error("subscription downloaded before old workers were stopped")
				}
				rw.Write([]byte("vless://00000000-0000-4000-8000-000000000099@192.0.2.99:443?type=tcp#Netherlands"))
			}))
			defer fresh.Close()
			refreshDone := make(chan error, 1)
			go func() { _, err := w.RefreshVerifiedContext(context.Background(), fresh.URL); refreshDone <- err }()
			if err := awaitVerified(t, refreshDone); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(awaitVerified(t, done), context.Canceled) {
				t.Fatal("old scan was not cancelled")
			}
			servers := w.subscription.GetServers()
			if len(servers) != 1 || servers[0].Address != "192.0.2.99" {
				t.Fatal("fresh list not installed")
			}
			after, _ := os.ReadFile(w.config.OutboundsFile)
			if string(original) != string(after) {
				t.Fatal("cancelled scan applied old server")
			}
		})
	}
}

func TestCoreActionInterruptsHangingSubscriptionDownload(t *testing.T) {
	w := verifiedWatchdog(t)
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer srv.Close()
	done := make(chan error, 1)
	go func() { _, err := w.RefreshVerifiedContext(context.Background(), srv.URL); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("download did not start")
	}
	coreDone := make(chan error, 1)
	go func() { release := w.LockCoreOperation(); defer release(); coreDone <- nil }()
	if err := awaitVerified(t, coreDone); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(awaitVerified(t, done), context.Canceled) {
		t.Fatal("download was not cancelled")
	}
}

func TestDisableInterruptsManualVerificationAndPreventsApply(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	started := make(chan struct{})
	w.verifiedProbe = func(ctx context.Context, _ map[string]interface{}) (xkeen.ProbeResult, error) {
		close(started)
		<-ctx.Done()
		return xkeen.ProbeResult{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := w.SelectVerified(context.Background(), 0); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("manual check did not start")
	}
	disableDone := make(chan error, 1)
	go func() { disableDone <- w.SetAutomationEnabled(false) }()
	if err := awaitVerified(t, disableDone); err != nil {
		t.Fatal(err)
	}
	if awaitVerified(t, done) == nil {
		t.Fatal("cancelled manual selection succeeded")
	}
	if w.IsActive() || addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.1" {
		t.Fatal("disabled automation applied a server")
	}
}
