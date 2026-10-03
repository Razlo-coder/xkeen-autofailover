package monitor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"xkeen-panel/internal/models"
	"xkeen-panel/internal/xkeen"
)

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

func normalizeAutomation(s models.AutomationSettings) (models.AutomationSettings, error) {
	if s.SourcePriority != "" && s.SourcePriority != "all" && s.SourcePriority != "first" && s.SourcePriority != "second" {
		return s, fmt.Errorf("неизвестный приоритет подписок")
	}
	if s.SourcePriority == "" {
		s.SourcePriority = "all"
	}
	if !models.ValidConnectionSettings(s.QualityThresholdMs, s.QualityFailCount, s.PriorityCheckSec) {
		return s, fmt.Errorf("порог задержки: 100–60000 мс; плохих проверок: 1–10; интервал возврата: 60–86400 секунд")
	}
	normalize := func(values []string, countries bool) ([]string, error) {
		if len(values) > 300 {
			return nil, fmt.Errorf("слишком много правил: максимум 300")
		}
		result := []string{}
		seen := map[string]bool{}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if countries {
				value = strings.ToUpper(value)
			}
			if value == "" {
				continue
			}
			if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
				return nil, fmt.Errorf("неверное значение правила")
			}
			if countries && !countryCode.MatchString(value) {
				return nil, fmt.Errorf("выберите страну с двухбуквенным кодом")
			}
			key := strings.ToLower(value)
			if !seen[key] {
				result = append(result, value)
				seen[key] = true
			}
		}
		return result, nil
	}
	var err error
	if s.CountryPriority, err = normalize(s.CountryPriority, true); err != nil {
		return s, err
	}
	if !s.AllowOtherCountries && len(s.CountryPriority) == 0 {
		return s, fmt.Errorf("выберите хотя бы одну страну или разрешите остальные")
	}
	if s.PreferredServerNames, err = normalize(s.PreferredServerNames, false); err != nil {
		return s, err
	}
	if s.ExcludedServerNames, err = normalize(s.ExcludedServerNames, false); err != nil {
		return s, err
	}
	if s.ExcludeNameContains, err = normalize(s.ExcludeNameContains, false); err != nil {
		return s, err
	}
	return s, nil
}

func (w *Watchdog) automationLocked() models.AutomationSettings {
	p := w.config.VerifiedFailover
	copyList := func(v []string) []string { return append([]string{}, v...) }
	return models.AutomationSettings{Enabled: w.config.WatchdogAutoStart, SourcePriority: p.SourcePriority, CountryPriority: copyList(p.CountryPriority),
		AllowOtherCountries: p.AllowOtherCountries, PreferredServerNames: copyList(p.PreferredServerNames),
		ExcludedServerNames: copyList(p.ExcludedServerNames), ExcludeNameContains: copyList(p.ExcludeNameContains),
		QualityEnabled: p.QualityEnabled, QualityThresholdMs: p.QualityThresholdMs, QualityFailCount: p.QualityFailCount,
		ReturnToPriority: p.ReturnToPriority, PriorityCheckSec: p.PriorityCheckSec}
}

func (w *Watchdog) applyAutomationLocked(s models.AutomationSettings) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.config.WatchdogAutoStart = s.Enabled
	w.config.VerifiedFailover.SourcePriority = s.SourcePriority
	w.config.VerifiedFailover.CountryPriority = s.CountryPriority
	w.config.VerifiedFailover.AllowOtherCountries = s.AllowOtherCountries
	w.config.VerifiedFailover.PreferredServerNames = s.PreferredServerNames
	w.config.VerifiedFailover.ExcludedServerNames = s.ExcludedServerNames
	w.config.VerifiedFailover.ExcludeNameContains = s.ExcludeNameContains
	w.config.VerifiedFailover.QualityEnabled = s.QualityEnabled
	w.config.VerifiedFailover.QualityThresholdMs = s.QualityThresholdMs
	w.config.VerifiedFailover.QualityFailCount = s.QualityFailCount
	w.config.VerifiedFailover.ReturnToPriority = s.ReturnToPriority
	w.config.VerifiedFailover.PriorityCheckSec = s.PriorityCheckSec
	w.qualityFailCount = 0
}

// LoadAutomation runs before background work. Existing YAML rules migrate
// naturally when no preferences file exists; corrupt saved rules fail closed.
func (w *Watchdog) LoadAutomation() error {
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	data, err := os.ReadFile(filepath.Join(w.config.DataDir, "automation.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Overlay older files onto YAML/defaults instead of resetting new fields.
	s := w.automationLocked()
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("настройки автоматизации: %w", err)
	}
	s, err = normalizeAutomation(s)
	if err != nil {
		return err
	}
	w.applyAutomationLocked(s)
	return nil
}

func (w *Watchdog) GetAutomation() models.AutomationSettings {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.automationLocked()
}

func (w *Watchdog) saveAutomationLocked(s models.AutomationSettings) error {
	normalized, err := normalizeAutomation(s)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(w.config.DataDir, 0700); err != nil {
		return err
	}
	if err := xkeen.AtomicWritePrivate(filepath.Join(w.config.DataDir, "automation.json"), data); err != nil {
		return fmt.Errorf("настройки не сохранены: %w", err)
	}
	w.applyAutomationLocked(normalized)
	w.SetActive(normalized.Enabled && len(w.subscription.GetServers()) > 0)
	return nil
}

func (w *Watchdog) SaveAutomation(s models.AutomationSettings) error {
	finish := w.prioritizeManualSelection()
	defer finish()
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	return w.saveAutomationLocked(s)
}

func (w *Watchdog) SetAutomationEnabled(enabled bool) error {
	finish := w.prioritizeManualSelection()
	defer finish()
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	s := w.automationLocked()
	s.Enabled = enabled
	return w.saveAutomationLocked(s)
}

func (w *Watchdog) PolicyServers() []models.Server {
	w.mu.RLock()
	policy := w.config.VerifiedFailover
	w.mu.RUnlock()
	servers := w.subscription.GetServers()
	for i := range servers {
		reason := xkeen.PolicyExclusion(servers[i], policy)
		eligible := reason == ""
		servers[i].AutomaticEligible = &eligible
		servers[i].ExclusionReason = reason
	}
	return servers
}
