package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOlderConfigGainsConnectionDefaultsAndPreservesStrictCountries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := "verified_failover:\n  enabled: true\n  country_priority: [NL, DE]\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.VerifiedFailover
	if p.AllowOtherCountries || !p.QualityEnabled || p.QualityThresholdMs != 1500 || p.QualityFailCount != 3 || !p.ReturnToPriority || p.PriorityCheckSec != 300 {
		t.Fatalf("upgrade defaults or old country policy lost: %+v", p)
	}
	if err := os.WriteFile(path, []byte(old+"  quality_enabled: false\n  return_to_priority: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(path)
	if err != nil || cfg.VerifiedFailover.QualityEnabled || cfg.VerifiedFailover.ReturnToPriority {
		t.Fatal("explicit opt-out ignored")
	}
}
