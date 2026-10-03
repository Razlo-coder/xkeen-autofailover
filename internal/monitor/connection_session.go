package monitor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"xkeen-panel/internal/xkeen"
)

type connectionSession struct {
	Fingerprint  string    `json:"fingerprint,omitempty"`
	CoreInstance string    `json:"core_instance,omitempty"`
	Since        time.Time `json:"since"`
}

type connectionTracker struct {
	mu        sync.Mutex
	loaded    bool
	current   connectionSession
	persisted connectionSession
}

func (w *Watchdog) connectionSessionPath() string {
	return filepath.Join(w.config.DataDir, "connection-session.json")
}

// Called only after a confirmed active connection, never for candidate pings.
// forceNew covers a deliberate apply even if the endpoint stayed the same.
func (w *Watchdog) confirmConnection(ob map[string]interface{}, at time.Time, forceNew bool) {
	fingerprint, err := xkeen.OutboundFingerprint(ob)
	if err != nil {
		return
	}
	instance := w.coreInstance(w.detector.Runtime().Core)
	t := &w.connection
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.loaded {
		t.loaded = true
		if file, err := os.Open(w.connectionSessionPath()); err == nil {
			data, err := io.ReadAll(io.LimitReader(file, 65537))
			file.Close()
			if err == nil && len(data) <= 65536 {
				var saved connectionSession
				if json.Unmarshal(data, &saved) == nil && instance != "" && saved.CoreInstance == instance && saved.Fingerprint == fingerprint && !saved.Since.IsZero() && !saved.Since.After(at) {
					t.current, t.persisted = saved, saved
				}
			}
		}
	}
	if forceNew || t.current.Since.IsZero() || t.current.Since.After(at) || t.current.Fingerprint != fingerprint || t.current.CoreInstance != instance {
		t.current = connectionSession{Fingerprint: fingerprint, CoreInstance: instance, Since: at.UTC()}
	}
	w.persistConnectionLocked()
}

func (w *Watchdog) endConnection() {
	t := &w.connection
	t.mu.Lock()
	defer t.mu.Unlock()
	// Write an empty record even before first confirmation, so a stop/failure
	// cannot revive an earlier saved session after a panel restart.
	if !t.loaded {
		t.loaded = true
		t.persisted.Fingerprint = "invalidate"
	}
	t.current = connectionSession{}
	w.persistConnectionLocked()
}

func (w *Watchdog) persistConnectionLocked() {
	t := &w.connection
	if t.current == t.persisted {
		return
	}
	data, err := json.Marshal(t.current)
	if err == nil {
		err = os.MkdirAll(w.config.DataDir, 0700)
	}
	if err == nil {
		err = xkeen.AtomicWritePrivate(w.connectionSessionPath(), data)
	}
	if err != nil {
		w.writeLog("[UPTIME] Не удалось сохранить время подключения: %v", err)
		return
	}
	t.persisted = t.current
}

func (w *Watchdog) currentConnectionSince(ob map[string]interface{}, now time.Time) *time.Time {
	fingerprint, err := xkeen.OutboundFingerprint(ob)
	if err != nil {
		return nil
	}
	instance := w.coreInstance(w.detector.Runtime().Core)
	w.connection.mu.Lock()
	defer w.connection.mu.Unlock()
	s := w.connection.current
	if s.Since.IsZero() || s.Since.After(now) || s.Fingerprint != fingerprint || s.CoreInstance != instance {
		return nil
	}
	// An unavailable main process is not evidence that its session is live.
	if instance == "" && w.config.VerifiedFailover.Enabled {
		return nil
	}
	since := s.Since
	return &since
}

func formatUptime(seconds int64) string {
	days, hours, minutes := seconds/86400, seconds/3600%24, seconds/60%60
	if days > 0 {
		return fmt.Sprintf("%d д %d ч %d мин", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%d ч %d мин", hours, minutes)
	}
	if minutes > 0 {
		return fmt.Sprintf("%d мин", minutes)
	}
	return fmt.Sprintf("%d с", seconds)
}
