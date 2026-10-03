package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"xkeen-panel/internal/models"
	"xkeen-panel/internal/xkeen"
)

func TestOlderAutomationFileKeepsNewDefaultsAndSavedOptOut(t *testing.T) {
	w := verifiedWatchdog(t)
	path := filepath.Join(w.config.DataDir, "automation.json")
	old := `{"enabled":true,"country_priority":["NL","DE"],"allow_other_countries":false,"preferred_server_names":[],"excluded_server_names":[],"exclude_name_contains":["Extra Whitelist2"]}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.LoadAutomation(); err != nil {
		t.Fatal(err)
	}
	s := w.GetAutomation()
	if !s.QualityEnabled || s.QualityThresholdMs != 1500 || s.QualityFailCount != 3 || !s.ReturnToPriority || s.PriorityCheckSec != 300 {
		t.Fatal("older file cleared new defaults")
	}
	s.QualityEnabled = false
	s.ReturnToPriority = false
	data, _ := json.Marshal(s)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.LoadAutomation(); err != nil {
		t.Fatal(err)
	}
	if w.GetAutomation().QualityEnabled || w.GetAutomation().ReturnToPriority {
		t.Fatal("explicit opt-out was overwritten by defaults")
	}
}

func TestAutomationPersistsAndKeepsRotatedNamePreferences(t *testing.T) {
	w := verifiedWatchdog(t)
	settings := w.GetAutomation()
	settings.Enabled = true
	settings.SourcePriority = "second"
	settings.CountryPriority = []string{"de", "NL", "de"}
	settings.AllowOtherCountries = true
	settings.PreferredServerNames = []string{"Amsterdam Extra"}
	settings.ExcludedServerNames = []string{"Unsupported"}
	settings.ExcludeNameContains = []string{"Whitelist2"}
	settings.QualityThresholdMs = 1200
	settings.QualityFailCount = 2
	settings.ReturnToPriority = false
	settings.PriorityCheckSec = 600
	if err := w.SaveAutomation(settings); err != nil {
		t.Fatal(err)
	}
	original := w.GetAutomation()
	w.config.VerifiedFailover.CountryPriority = nil
	w.config.VerifiedFailover.ExcludeNameContains = nil
	if err := w.LoadAutomation(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.GetAutomation(), original) {
		t.Fatal("saved rules did not survive reload")
	}
	if !reflect.DeepEqual(original.CountryPriority, []string{"DE", "NL"}) {
		t.Fatal("country normalization or de-duplication failed")
	}
	if original.SourcePriority != "second" {
		t.Fatal("subscription preference was not persisted")
	}
	copy := w.GetAutomation()
	copy.CountryPriority[0] = "RU"
	if w.GetAutomation().CountryPriority[0] != "DE" {
		t.Fatal("API leaked mutable settings")
	}
	if err := w.SetAutomationEnabled(false); err != nil {
		t.Fatal(err)
	}
	if w.GetAutomation().Enabled || w.IsActive() {
		t.Fatal("disable was not persisted")
	}
	servers := []models.Server{{Name: "Amsterdam Extra", Country: "NL", Address: "192.0.2.2", Protocol: "vless"}, {Name: "Other", Country: "NL", Protocol: "vless"}, {Name: "Old Whitelist2", Country: "DE", Protocol: "vless"}, {Name: "Berlin", Country: "DE", Protocol: "vless"}, {Name: "Unsupported", Country: "DE", Protocol: "vless"}, {Name: "Unknown", Protocol: "trojan"}}
	candidates := xkeen.PolicyCandidates(servers, w.config.VerifiedFailover)
	var names []string
	for _, s := range candidates {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"Berlin", "Amsterdam Extra", "Other", "Unknown"}) {
		t.Fatalf("policy order/exclusion: %v", names)
	}
}

func TestAutomationInvalidOrUnsavedRulesDoNotReplaceLiveRules(t *testing.T) {
	w := verifiedWatchdog(t)
	before := w.GetAutomation()
	badPriority := before
	badPriority.SourcePriority = "unknown"
	if err := w.SaveAutomation(badPriority); err == nil {
		t.Fatal("unknown subscription preference accepted")
	}
	if err := w.SaveAutomation(models.AutomationSettings{}); err == nil {
		t.Fatal("empty strict allowlist accepted")
	}
	if !reflect.DeepEqual(before, w.GetAutomation()) {
		t.Fatal("invalid settings changed live rules")
	}
	if err := os.MkdirAll(filepath.Join(w.config.DataDir, "automation.json"), 0700); err != nil {
		t.Fatal(err)
	}
	changed := before
	changed.CountryPriority = []string{"US"}
	if err := w.SaveAutomation(changed); err == nil {
		t.Fatal("unwritable preferences file accepted")
	}
	if !reflect.DeepEqual(before, w.GetAutomation()) {
		t.Fatal("failed persistence changed live rules")
	}
}
