package api

import (
	"encoding/json"
	"net/http"
)

func (h *Handlers) HandleGetAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.config.VerifiedFailover.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Этот режим автоматизации не включён"})
		return
	}
	writeJSON(w, http.StatusOK, h.watchdog.GetAutomation())
}

func (h *Handlers) HandleSaveAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.config.VerifiedFailover.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Этот режим автоматизации не включён"})
		return
	}
	// An older cached UI can still save its rules without clearing new options.
	settings := h.watchdog.GetAutomation()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Неверный формат настроек"})
		return
	}
	if err := h.watchdog.SaveAutomation(settings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, h.watchdog.GetAutomation())
}
