package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"xkeen-panel/internal/models"
	"xkeen-panel/internal/xkeen"
)

func TestCurrentNameSurvivesIPRotationAndPanelRestart(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 20}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if _, err := w.SelectVerified(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if w.GetStatus().CurrentServer != "Germany" {
		t.Fatal("manual selection lost its name")
	}
	original, _ := os.ReadFile(w.config.OutboundsFile)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte("vless://00000000-0000-4000-8000-000000000002@192.0.2.5:443?type=tcp#Netherlands\n" +
			"vless://00000000-0000-4000-8000-000000000003@192.0.2.6:443?type=tcp#Germany"))
	}))
	defer srv.Close()
	if _, err := w.RefreshVerified(srv.URL); err != nil {
		t.Fatal(err)
	}
	if w.subscription.GetActiveServer() != nil || w.GetStatus().CurrentServer != "Germany" {
		t.Fatal("rotated subscription replaced the current display name")
	}
	after, _ := os.ReadFile(w.config.OutboundsFile)
	if string(after) != string(original) {
		t.Fatal("refresh changed the VPN config")
	}
	data, err := os.ReadFile(w.verifiedIdentityPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "192.0.2.") || strings.Contains(string(data), "vless://") || strings.Contains(string(data), "00000000-0000-") {
		t.Fatal("display record copied connection secrets")
	}
	newSub := xkeen.NewSubscriptionManager(w.config.DataDir)
	if err := newSub.Load(); err != nil {
		t.Fatal(err)
	}
	restarted := NewWatchdog(w.config, newSub, w.detector)
	restarted.rememberVerifiedCurrent(mustSingle(t, w.config.OutboundsFile))
	if restarted.GetStatus().CurrentServer != "Germany" || restarted.verifiedCurrent == nil || xkeen.PolicyCountry(*restarted.verifiedCurrent) != "DE" {
		t.Fatal("name/country not restored for unchanged configuration")
	}
	// An external edit changes the real endpoint, even while monitoring is off.
	config, _ := xkeen.ReadOutboundsConfig(w.config.OutboundsFile)
	config["outbounds"].([]interface{})[0].(map[string]interface{})["settings"].(map[string]interface{})["address"] = "192.0.2.99"
	if err := xkeen.WriteOutboundsConfig(w.config.OutboundsFile, config); err != nil {
		t.Fatal(err)
	}
	if restarted.GetStatus().CurrentServer == "Germany" {
		t.Fatal("old name displayed for a different connection")
	}
	restarted.rememberVerifiedCurrent(mustSingle(t, w.config.OutboundsFile))
	if restarted.verifiedCurrent != nil {
		t.Fatal("saved identity attached to unrelated connection")
	}
}

func TestStatusUsesActualConfigInsteadOfWrongActiveIndex(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	w.verifiedProbe = func(context.Context, map[string]interface{}) (xkeen.ProbeResult, error) {
		return xkeen.ProbeResult{OK: true, Latency: 20}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if _, err := w.SelectVerified(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := w.subscription.SetActive(0); err != nil {
		t.Fatal(err)
	}
	if w.GetStatus().CurrentServer != "Netherlands" {
		t.Fatal("status trusted an unrelated active index")
	}
	// A failed manual transaction retains the previous name.
	w.verifiedApplier.Validate = func() error { return os.ErrPermission }
	if _, err := w.SelectVerified(context.Background(), 0); err == nil {
		t.Fatal("expected application failure")
	}
	if w.GetStatus().CurrentServer != "Netherlands" {
		t.Fatal("failed selection replaced the current name")
	}
}

func TestCorruptIdentityDoesNotInventCurrentName(t *testing.T) {
	w := verifiedWatchdog(t)
	importVerified(t, w)
	if err := os.WriteFile(w.verifiedIdentityPath(), []byte(`{"name":"False name","fingerprint":"wrong","protocol":"vless"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w.rememberVerifiedCurrent(mustSingle(t, w.config.OutboundsFile))
	if w.verifiedCurrent != nil || w.GetStatus().CurrentServer == "False name" {
		t.Fatal("invalid display record trusted")
	}
}

func TestDisplayIdentityRetriesFailedPersistence(t *testing.T) {
	w := verifiedWatchdog(t)
	ob := mustSingle(t, w.config.OutboundsFile)
	protocol, _ := ob["protocol"].(string)
	w.verifiedCurrent = &models.Server{Name: "Current name", Protocol: protocol}
	// An empty directory at the record path makes the atomic rename fail.
	if err := os.Mkdir(w.verifiedIdentityPath(), 0700); err != nil {
		t.Fatal(err)
	}
	w.updateVerifiedIdentity(ob)
	if w.GetStatus().CurrentServer != "Current name" {
		t.Fatal("disk failure lost the in-memory display name")
	}
	if err := os.Remove(w.verifiedIdentityPath()); err != nil {
		t.Fatal(err)
	}
	w.updateVerifiedIdentity(ob)
	identity, ok := w.loadVerifiedIdentity(ob)
	if !ok || identity.Name != "Current name" {
		t.Fatal("unchanged identity was not retried after disk failure")
	}
}
