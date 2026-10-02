package xkeen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"xkeen-panel/internal/models"
)

func loadSub(t *testing.T, servers []models.Server, activeID int) *SubscriptionManager {
	t.Helper()
	dir := t.TempDir()
	data := models.SubscriptionData{Servers: servers, ActiveID: activeID}
	b, err := json.MarshalIndent(&data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subscription.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	sm := NewSubscriptionManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}
	return sm
}

func TestSetActiveByRawURI(t *testing.T) {
	sm := loadSub(t, []models.Server{
		{ID: 0, RawURI: "vless://a@1.1.1.1:443#A", Active: true},
		{ID: 1, RawURI: "vless://b@2.2.2.2:443#B"},
	}, 0)

	s, err := sm.SetActiveByRawURI("vless://b@2.2.2.2:443#B")
	if err != nil {
		t.Fatalf("SetActiveByRawURI: %v", err)
	}
	if s.RawURI != "vless://b@2.2.2.2:443#B" {
		t.Errorf("активирован не тот сервер: %q", s.RawURI)
	}
	if got := sm.GetActiveServer(); got == nil || got.RawURI != "vless://b@2.2.2.2:443#B" {
		t.Error("активный сервер не установился")
	}
	if _, err := sm.SetActiveByRawURI("vless://missing"); err == nil {
		t.Error("ожидалась ошибка для несуществующего RawURI")
	}
}

func TestReconcilePreservesSelectedAliasForSameConnection(t *testing.T) {
	servers, err := ParseSubscription("vless://00000000-0000-4000-8000-000000000001@192.0.2.1:443?type=tcp#First%20name\nvless://00000000-0000-4000-8000-000000000001@192.0.2.1:443?type=tcp#Chosen%20name")
	if err != nil {
		t.Fatal(err)
	}
	sm := loadSub(t, servers, 1)
	outbound, err := OutboundForServer(map[string]interface{}{"protocol": "vless", "tag": "vpn"}, &servers[1])
	if err != nil {
		t.Fatal(err)
	}
	sm.ReconcileActive(outbound)
	if selected := sm.GetActiveServer(); selected == nil || selected.Name != "Chosen name" {
		t.Fatal("same connection reset the selected display name to the first entry")
	}
	if selected := sm.MatchConfiguredServer(outbound); selected == nil || selected.Name != "Chosen name" {
		t.Fatal("status lookup lost selected alias")
	}
}

// Regression: GetData must not hand out an alias of the internal server slice.
func TestGetDataDeepCopy(t *testing.T) {
	sm := loadSub(t, []models.Server{
		{ID: 0, RawURI: "u0", Country: "NL"},
		{ID: 1, RawURI: "u1"},
	}, 0)

	d := sm.GetData()
	d.Servers[0].Country = "MUTATED"

	if got := sm.GetServers(); got[0].Country != "NL" {
		t.Errorf("GetData вернул алиас: внутренний Country = %q, want NL", got[0].Country)
	}
}
