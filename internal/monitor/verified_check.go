package monitor

import (
	"context"
	"fmt"
)

// Interactive actions cancel network work before waiting for the short-lived
// transaction lock. Queued background scans cannot jump ahead of the action.
func (w *Watchdog) prioritizeManualSelection() func() {
	_, finish := w.prioritizeVerifiedAction()
	return finish
}

func (w *Watchdog) prioritizeVerifiedAction() (uint64, func()) {
	w.verifiedCheckMu.Lock()
	w.manualSelections++
	w.verifiedOperationEpoch++
	if w.verifiedCheckCancel != nil {
		w.verifiedCheckCancel()
	}
	if w.verifiedOperationCancel != nil {
		w.verifiedOperationCancel()
	}
	epoch := w.verifiedOperationEpoch
	w.verifiedCheckMu.Unlock()
	return epoch, func() {
		w.verifiedCheckMu.Lock()
		w.manualSelections--
		w.verifiedCheckMu.Unlock()
	}
}

// All child probes are reaped before the transaction lock is freed.
func (w *Watchdog) lockVerifiedOperation(parent context.Context, interactive bool) (context.Context, func(), error) {
	finishPriority := func() {}
	var epoch uint64
	if interactive {
		epoch, finishPriority = w.prioritizeVerifiedAction()
	} else {
		w.verifiedCheckMu.Lock()
		epoch = w.verifiedOperationEpoch
		w.verifiedCheckMu.Unlock()
	}
	w.operationMu.Lock()
	w.verifiedCheckMu.Lock()
	if parent.Err() != nil || epoch != w.verifiedOperationEpoch || (!interactive && w.manualSelections > 0) {
		w.verifiedCheckMu.Unlock()
		w.operationMu.Unlock()
		finishPriority()
		return nil, nil, context.Canceled
	}
	ctx, cancel := context.WithCancel(parent)
	w.verifiedOperationCancel = cancel
	w.verifiedCheckMu.Unlock()
	return ctx, func() {
		cancel()
		w.verifiedCheckMu.Lock()
		w.verifiedOperationCancel = nil
		w.verifiedCheckMu.Unlock()
		w.operationMu.Unlock()
		finishPriority()
	}, nil
}

func (w *Watchdog) CancelVerifiedWork() {
	w.verifiedCheckMu.Lock()
	defer w.verifiedCheckMu.Unlock()
	w.verifiedOperationEpoch++
	if w.verifiedCheckCancel != nil {
		w.verifiedCheckCancel()
	}
	if w.verifiedOperationCancel != nil {
		w.verifiedOperationCancel()
	}
}

func (w *Watchdog) MarkCoreStopped() {
	w.mu.Lock()
	w.connected = false
	w.lastLatency = -1
	w.failCount = 0
	w.qualityFailCount = 0
	w.mu.Unlock()
	w.publishStatus()
}

func (w *Watchdog) beginVerifiedCheck(ctx context.Context) (context.Context, func(), error) {
	w.verifiedCheckMu.Lock()
	defer w.verifiedCheckMu.Unlock()
	if w.manualSelections > 0 {
		return nil, nil, fmt.Errorf("дождитесь завершения ручного действия")
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
