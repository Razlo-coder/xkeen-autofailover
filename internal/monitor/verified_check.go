package monitor

import (
	"context"
	"fmt"
)

// Manual selection cancels the bulk ping before waiting for operationMu. Its
// own verification still uses the same lock, so probe cores never overlap.
func (w *Watchdog) prioritizeManualSelection() func() {
	w.verifiedCheckMu.Lock()
	w.manualSelections++
	if w.verifiedCheckCancel != nil {
		w.verifiedCheckCancel()
	}
	w.verifiedCheckMu.Unlock()
	return func() {
		w.verifiedCheckMu.Lock()
		w.manualSelections--
		w.verifiedCheckMu.Unlock()
	}
}

func (w *Watchdog) beginVerifiedCheck(ctx context.Context) (context.Context, func(), error) {
	w.verifiedCheckMu.Lock()
	defer w.verifiedCheckMu.Unlock()
	if w.manualSelections > 0 {
		return nil, nil, fmt.Errorf("дождитесь выбора сервера")
	}
	if w.verifiedCheckCancel != nil {
		return nil, nil, fmt.Errorf("пинг уже выполняется")
	}
	ctx, cancel := context.WithCancel(ctx)
	w.verifiedCheckCancel = cancel
	return ctx, func() {
		cancel()
		w.verifiedCheckMu.Lock()
		w.verifiedCheckCancel = nil
		w.verifiedCheckMu.Unlock()
	}, nil
}
