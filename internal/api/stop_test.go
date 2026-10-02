package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xkeen-panel/internal/models"
	"xkeen-panel/internal/monitor"
	"xkeen-panel/internal/xkeen"
)

func TestStopStillRunsWhenAutomationCannotBeSaved(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses a synthetic Unix dispatcher")
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "called")
	dispatcher := filepath.Join(dir, "xkeen")
	if err := os.WriteFile(dispatcher, []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$1\" > %q\n", trace)), 0700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "data-is-a-file")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := models.DefaultVerifiedFailover()
	policy.Enabled = true
	cfg := &models.Config{DataDir: blocked, VerifiedFailover: policy, WatchdogAutoStart: true}
	sub := xkeen.NewSubscriptionManager(dir)
	det := xkeen.NewDetector(dir, dispatcher, "", "", "", "", "")
	wd := monitor.NewWatchdog(cfg, sub, det)
	wd.SetActive(true)
	h := NewHandlers(cfg, sub, wd, det, nil, nil)
	rw := httptest.NewRecorder()
	h.HandleStop(rw, httptest.NewRequest(http.MethodPost, "/api/xkeen/stop", nil))
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "warning") {
		t.Fatalf("stop response: %d %s", rw.Code, rw.Body.String())
	}
	called, err := os.ReadFile(trace)
	if err != nil || string(called) != "-stop" {
		t.Fatal("failure saving rules prevented the stop command")
	}
	if wd.IsActive() || wd.GetStatus().Connected {
		t.Fatal("stop left automation/connection active")
	}
}
