package sse

import (
	"context"
	"net/http"

	"xkeen-panel/internal/models"
)

type VerifiedServerChecker interface {
	CheckVerifiedServers(context.Context, func(models.Server)) ([]models.Server, error)
}

func HandleVerifiedStream(checker VerifiedServerChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		send := func(event Event) {
			if data, err := FormatSSE(event); err == nil {
				w.Write(data)
				flusher.Flush()
			}
		}
		_, err := checker.CheckVerifiedServers(r.Context(), func(s models.Server) {
			send(Event{Type: "latency", Data: map[string]int{"id": s.ID, "latency_ms": s.Latency}})
		})
		if err != nil {
			send(Event{Type: "error", Data: map[string]string{"error": err.Error()}})
		}
		send(Event{Type: "done", Data: map[string]bool{"complete": true}})
	}
}
