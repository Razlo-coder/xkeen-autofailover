package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xkeen-panel/internal/xkeen"
)

func TestSelectVerifiedAutoGroupUsesFastestReachableMember(t *testing.T) {
	w := verifiedWatchdog(t)
	w.config.VerifiedFailover.AllowOtherCountries = true
	w.config.ProbeConcurrency = 8
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Join([]string{
			"vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#%E2%9A%A1%20%D0%90%D0%B2%D1%82%D0%BE%20%C2%B7%201",
			"vless://00000000-0000-4000-8000-000000000003@192.0.2.3:443?type=tcp#%E2%9A%A1%20%D0%90%D0%B2%D1%82%D0%BE%20%C2%B7%202",
			"vless://00000000-0000-4000-8000-000000000004@192.0.2.4:443?type=tcp#%E2%9A%A1%20%D0%90%D0%B2%D1%82%D0%BE%2B%20%C2%B7%201",
		}, "\n")))
	}))
	defer provider.Close()
	if _, err := w.subscription.UpdateSourceContext(context.Background(), 1, provider.URL); err != nil {
		t.Fatal(err)
	}
	w.verifiedProbe = func(_ context.Context, outbound map[string]interface{}) (xkeen.ProbeResult, error) {
		delay := 250
		if addressOf(outbound) == "192.0.2.3" {
			delay = 90
		}
		return xkeen.ProbeResult{OK: true, Latency: delay}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	selected, err := w.SelectVerifiedGroup(context.Background(), 1, "⚡ Авто")
	if err != nil {
		t.Fatal(err)
	}
	if selected.SourceID != 1 || selected.GroupName != "⚡ Авто" || addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatalf("wrong group member selected: %+v", selected)
	}
	if _, err := w.SelectVerifiedGroup(context.Background(), 1, "⚡ Авто+"); err != nil {
		t.Fatal("other auto group should remain selectable:", err)
	}
}

func TestAutoGroupFailoverTriesSiblingBeforeOtherPriority(t *testing.T) {
	w := verifiedWatchdog(t)
	w.config.VerifiedFailover.AllowOtherCountries = true
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Join([]string{
			"vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp#%E2%9A%A1%20%D0%90%D0%B2%D1%82%D0%BE%20%C2%B7%201",
			"vless://00000000-0000-4000-8000-000000000003@192.0.2.3:443?type=tcp#%E2%9A%A1%20%D0%90%D0%B2%D1%82%D0%BE%20%C2%B7%202",
			"vless://00000000-0000-4000-8000-000000000004@192.0.2.4:443?type=tcp#NL%20Netherlands",
		}, "\n")))
	}))
	defer provider.Close()
	if _, err := w.subscription.UpdateSourceContext(context.Background(), 1, provider.URL); err != nil {
		t.Fatal(err)
	}
	w.verifiedProbe = func(_ context.Context, outbound map[string]interface{}) (xkeen.ProbeResult, error) {
		switch addressOf(outbound) {
		case "192.0.2.2":
			return xkeen.ProbeResult{OK: true, Latency: 150}, nil
		case "192.0.2.3":
			return xkeen.ProbeResult{OK: true, Latency: 90}, nil
		default:
			return xkeen.ProbeResult{OK: true, Latency: 50}, nil
		}
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if _, err := w.SelectVerifiedGroup(context.Background(), 1, "⚡ Авто"); err != nil {
		t.Fatal(err)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.3" {
		t.Fatal("fastest group member was not selected")
	}
	w.verifiedProbe = func(_ context.Context, outbound map[string]interface{}) (xkeen.ProbeResult, error) {
		if addressOf(outbound) == "192.0.2.3" {
			return xkeen.ProbeResult{OK: false, Latency: -1}, nil
		}
		if addressOf(outbound) == "192.0.2.2" {
			return xkeen.ProbeResult{OK: true, Latency: 180}, nil
		}
		return xkeen.ProbeResult{OK: true, Latency: 40}, nil
	}
	w.verifiedApplier.Probe = w.verifiedProbe
	if err := w.failoverVerified(context.Background(), mustSingle(t, w.config.OutboundsFile)); err != nil {
		t.Fatal(err)
	}
	if addressOf(mustSingle(t, w.config.OutboundsFile)) != "192.0.2.2" {
		t.Fatal("automatic failover left the Auto group while a sibling still worked")
	}
}
