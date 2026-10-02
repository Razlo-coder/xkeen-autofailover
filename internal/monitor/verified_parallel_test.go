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

func TestParallelFailoverKeepsPriorityAndReapsOtherProbesBeforeApply(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.config.ProbeConcurrency = 8
	lowerReady := make(chan struct{})
	var active atomic.Int32
	w.verifiedProbe = func(ctx context.Context, ob map[string]interface{}) (xkeen.ProbeResult, error) {
		active.Add(1)
		defer active.Add(-1)
		if addressOf(ob) == "192.0.2.3" {
			close(lowerReady)
			return xkeen.ProbeResult{OK: true, Latency: 10}, nil
		}
		select {
		case <-lowerReady:
			return xkeen.ProbeResult{OK: true, Latency: 50}, nil
		case <-ctx.Done():
			return xkeen.ProbeResult{}, ctx.Err()
		}
	}
	w.verifiedApplier.Validate = func() error {
		if active.Load() != 0 {
			t.Error("config applied while candidate probes still running")
		}
		return nil
	}
	w.verifiedApplier.Probe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 50}, nil
	}
	if err := w.failoverVerified(context.Background(), mustSingle(t, w.config.OutboundsFile)); err != nil {
		t.Fatal(err)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.2" {
		t.Fatal("faster lower-priority Germany displaced Netherlands")
	}
	if active.Load() != 0 {
		t.Fatal("candidate worker leaked")
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
