package monitor

import (
	"context"
	"fmt"
	"os"
	"time"

	"xkeen-panel/internal/models"
	"xkeen-panel/internal/xkeen"
)

func (w *Watchdog) configureVerified() {
	if w.verifiedProbe != nil {
		return
	}
	rt := w.detector.Runtime()
	p := &xkeen.VPNProber{Binary: rt.CoreBin, Mark: w.config.VerifiedFailover.BypassMark,
		URLs: w.config.HealthCheckURLs, Timeout: time.Duration(w.config.VerifiedFailover.ProbeTimeoutSec) * time.Second}
	w.verifiedProbe = p.Probe
	w.verifiedApplier = &xkeen.VerifiedApplier{Path: w.config.OutboundsFile, DataDir: w.config.DataDir,
		Validate: func() error {
			output, err := xkeen.ValidateXray(rt)
			if err != nil {
				return fmt.Errorf("%s", xkeen.TailLines(output, 4))
			}
			return nil
		},
		Restart: func() error {
			_, err := xkeen.RestartAndWait(rt.Dispatcher)
			return err
		},
		Running: func() bool { return xkeen.IsRunning(xkeen.CoreXray) }, Probe: w.verifiedProbe,
	}
}

// PrepareVerified verifies the supported layout and recovers an interrupted
// transaction before timers and the web API can change anything.
func (w *Watchdog) PrepareVerified() error {
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	rt := w.detector.Runtime()
	if rt.Core != xkeen.CoreXray || !rt.Installed {
		return fmt.Errorf("для проверяемого переключения нужен установленный XKeen с Xray")
	}
	if _, err := os.Stat(rt.CoreBin); err != nil {
		return fmt.Errorf("ядро Xray не найдено: %w", err)
	}
	w.configureVerified()
	if err := w.verifiedApplier.Recover(); err != nil {
		return fmt.Errorf("восстановление прерванного переключения: %w", err)
	}
	ob, err := xkeen.SingleProxy(w.config.OutboundsFile)
	if err != nil {
		return err
	}
	w.subscription.ReconcileActive(ob)
	return nil
}

func (w *Watchdog) checkVerified(ctx context.Context) {
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	if !w.IsActive() || xkeen.IsRestarting() || ctx.Err() != nil {
		return
	}
	w.configureVerified()
	ob, err := xkeen.SingleProxy(w.config.OutboundsFile)
	if err != nil {
		w.writeLog("[VERIFY] %v", err)
		return
	}
	w.subscription.ReconcileActive(ob)
	result, probeErr := w.verifiedProbe(ctx, ob)
	if ctx.Err() != nil {
		return
	}
	running := w.verifiedApplier.Running()
	ok := probeErr == nil && result.OK && running
	w.mu.Lock()
	w.lastCheck = time.Now()
	w.connected = ok
	w.lastLatency = result.Latency
	if ok {
		w.failCount = 0
	} else {
		w.failCount++
		w.lastLatency = -1
	}
	fails := w.failCount
	w.mu.Unlock()
	w.publishStatus()
	if ok {
		w.writeLog("[VERIFY] VPN работает: HTTPS %d/%d", result.Successes, result.Total)
		return
	}
	w.writeLog("[VERIFY] VPN не подтвердился: HTTPS %d/%d, ядро=%v, попытка %d/%d", result.Successes, result.Total, running, fails, w.config.MaxFails)
	if probeErr != nil {
		w.writeLog("[VERIFY] Ошибка проверки: %v", probeErr)
	}
	if fails < w.config.MaxFails {
		return
	}
	if time.Since(w.lastVerifiedAttempt) < time.Duration(w.config.VerifiedFailover.RetryIntervalSec)*time.Second {
		return
	}
	w.lastVerifiedAttempt = time.Now()
	// A fresh subscription is helpful, but recovery still works from the cached
	// list if the provider is unreachable. Never apply its first entry blindly.
	if _, err := w.subscription.Refresh(); err != nil {
		w.writeLog("[VERIFY] Обновление недоступно, использую сохранённые серверы: %v", err)
	}
	w.subscription.ReconcileActive(ob)
	if err := w.failoverVerified(ctx, ob); err != nil {
		w.writeLog("[VERIFY] %v", err)
	}
}

func (w *Watchdog) failoverVerified(ctx context.Context, current map[string]interface{}) error {
	// If rollback previously failed, restore it before attempting another write;
	// otherwise the new transaction could overwrite the only recovery copy.
	if err := w.verifiedApplier.Recover(); err != nil {
		return err
	}
	if ob, err := xkeen.SingleProxy(w.config.OutboundsFile); err == nil {
		current = ob
	} else {
		return err
	}
	for _, server := range xkeen.PolicyCandidates(w.subscription.GetServers(), w.config.VerifiedFailover) {
		if ctx.Err() != nil || !w.IsActive() {
			return fmt.Errorf("автопереключение отменено")
		}
		if w.isBlacklisted(server.RawURI) {
			continue
		}
		candidate, err := xkeen.OutboundForServer(current, &server)
		if err != nil {
			continue
		}
		// The failed current endpoint is held out on this recovery round. If it
		// recovers naturally, the next normal check will retain it.
		if xkeen.SameOutbound(candidate, current) && w.verifiedApplier.Running() {
			continue
		}
		w.writeLog("[VERIFY] Проверяю %s", server.Name)
		result, err := w.verifiedProbe(ctx, candidate)
		server.Latency = result.Latency
		w.subscription.UpdateLatencies([]models.Server{server})
		if err != nil || !result.OK {
			w.blacklistServer(server.RawURI)
			continue
		}
		if !w.IsActive() {
			return fmt.Errorf("автопереключение выключено")
		}
		if err := w.verifiedApplier.Apply(ctx, candidate); err != nil {
			w.writeLog("[VERIFY] %s не применён: %v", server.Name, err)
			w.blacklistServer(server.RawURI)
			// A pending journal means rollback needs attention: stop this round.
			if err := w.verifiedApplier.Recover(); err != nil {
				return err
			}
			continue
		}
		if _, err := w.subscription.SetActiveByRawURI(server.RawURI); err != nil {
			w.writeLog("[VERIFY] Сервер применён, состояние подписки не сохранено: %v", err)
		}
		w.mu.Lock()
		w.failCount = 0
		w.connected = true
		w.lastLatency = result.Latency
		w.mu.Unlock()
		w.writeLog("[VERIFY] Переключено и подтверждено: %s", server.Name)
		w.publishStatus()
		return nil
	}
	return fmt.Errorf("доступных серверов по выбранным правилам нет; текущая конфигурация сохранена, попытка будет повторена")
}

// RefreshVerified refreshes metadata only, keeping a working active config.
// The same lock serializes refresh, manual selection and automatic failover.
func (w *Watchdog) RefreshVerified(newURL string) ([]models.Server, error) {
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	var servers []models.Server
	hadServers := len(w.subscription.GetServers()) > 0
	var err error
	if newURL != "" {
		servers, err = w.subscription.UpdateURL(newURL)
	} else {
		servers, err = w.subscription.Refresh()
	}
	if ob, e := xkeen.SingleProxy(w.config.OutboundsFile); e == nil {
		w.subscription.ReconcileActive(ob)
	}
	if err == nil && newURL != "" && !hadServers && w.config.WatchdogAutoStart {
		w.SetActive(true)
	}
	return servers, err
}

func (w *Watchdog) SelectVerified(ctx context.Context, id int) (*models.Server, error) {
	finishSelection := w.prioritizeManualSelection()
	defer finishSelection()
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if xkeen.IsRestarting() {
		return nil, fmt.Errorf("дождитесь завершения перезапуска")
	}
	w.configureVerified()
	if err := w.verifiedApplier.Recover(); err != nil {
		return nil, err
	}
	var chosen *models.Server
	for _, server := range xkeen.PolicyCandidates(w.subscription.GetServers(), w.config.VerifiedFailover) {
		if server.ID == id {
			s := server
			chosen = &s
			break
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("сервер исключён правилами автоматического выбора")
	}
	current, err := xkeen.SingleProxy(w.config.OutboundsFile)
	if err != nil {
		return nil, err
	}
	ob, err := xkeen.OutboundForServer(current, chosen)
	if err != nil {
		return nil, err
	}
	result, err := w.verifiedProbe(ctx, ob)
	if err != nil || !result.OK {
		return nil, fmt.Errorf("VPN-соединение выбранного сервера не подтвердилось")
	}
	if err := w.verifiedApplier.Apply(ctx, ob); err != nil {
		return nil, err
	}
	w.ClearBlacklist(chosen.RawURI)
	return w.subscription.SetActiveByRawURI(chosen.RawURI)
}

func (w *Watchdog) CheckVerifiedServers(ctx context.Context, emit func(models.Server)) ([]models.Server, error) {
	ctx, finishCheck, err := w.beginVerifiedCheck(ctx)
	if err != nil {
		return nil, err
	}
	defer finishCheck()
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.configureVerified()
	current, err := xkeen.SingleProxy(w.config.OutboundsFile)
	if err != nil {
		return nil, err
	}
	var checked []models.Server
	for _, s := range xkeen.PolicyCandidates(w.subscription.GetServers(), w.config.VerifiedFailover) {
		if err := ctx.Err(); err != nil {
			return checked, err
		}
		ob, err := xkeen.OutboundForServer(current, &s)
		if err != nil {
			continue
		}
		r, err := w.verifiedProbe(ctx, ob)
		if err := ctx.Err(); err != nil {
			w.subscription.UpdateLatencies(checked)
			return checked, err
		}
		s.Latency = -1
		if err == nil && r.OK {
			s.Latency = r.Latency
		}
		checked = append(checked, s)
		if emit != nil {
			emit(s)
		}
	}
	w.subscription.UpdateLatencies(checked)
	return checked, nil
}

// LockCoreOperation prevents UI core changes from racing a failover transaction.
func (w *Watchdog) LockCoreOperation() func() {
	w.operationMu.Lock()
	return w.operationMu.Unlock
}
