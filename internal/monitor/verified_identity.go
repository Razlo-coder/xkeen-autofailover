package monitor

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"xkeen-panel/internal/xkeen"
)

// Persist only display/policy metadata and a digest of the connection. Endpoint,
// credentials and the subscription URI are not copied into this file.
type verifiedIdentity struct {
	Fingerprint      string `json:"fingerprint"`
	Name             string `json:"name"`
	Protocol         string `json:"protocol"`
	SourceID         int    `json:"source_id,omitempty"`
	Country          string `json:"country,omitempty"`
	CountryOverride  string `json:"country_override,omitempty"`
	ManualSlowChoice bool   `json:"manual_slow_choice,omitempty"`
}

func (w *Watchdog) verifiedIdentityPath() string {
	return filepath.Join(w.config.DataDir, "verified-current.json")
}

func (w *Watchdog) loadVerifiedIdentity(ob map[string]interface{}) (verifiedIdentity, bool) {
	var identity verifiedIdentity
	fingerprint, err := xkeen.OutboundFingerprint(ob)
	if err != nil {
		return identity, false
	}
	file, err := os.Open(w.verifiedIdentityPath())
	if err != nil {
		return identity, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &identity) != nil || identity.Name == "" || identity.Fingerprint != fingerprint {
		return verifiedIdentity{}, false
	}
	protocol, _ := ob["protocol"].(string)
	if identity.Protocol != protocol {
		return verifiedIdentity{}, false
	}
	return identity, true
}

// operationMu serializes writers. GetStatus reads the display snapshot under mu
// and validates its digest against the current config before displaying a name.
func (w *Watchdog) updateVerifiedIdentity(ob map[string]interface{}) {
	var identity verifiedIdentity
	if s := w.verifiedCurrent; s != nil {
		if fingerprint, err := xkeen.OutboundFingerprint(ob); err == nil {
			identity = verifiedIdentity{Fingerprint: fingerprint, Name: s.Name, Protocol: s.Protocol, SourceID: s.SourceID,
				Country: s.Country, CountryOverride: s.CountryOverride, ManualSlowChoice: w.manualSlowChoice}
		}
	}
	w.mu.Lock()
	w.verifiedDisplay = identity
	w.mu.Unlock()
	if identity.Name == "" || identity == w.verifiedPersisted {
		return
	}
	data, err := json.MarshalIndent(identity, "", "  ")
	if err == nil {
		err = os.MkdirAll(w.config.DataDir, 0700)
	}
	if err == nil {
		err = xkeen.AtomicWritePrivate(w.verifiedIdentityPath(), data)
	}
	if err != nil {
		w.writeLog("[VERIFY] Название активно в памяти, но не сохранено для перезапуска: %v", err)
		return
	}
	w.verifiedPersisted = identity
}
