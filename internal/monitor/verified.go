package monitor

import (
	"context"
	"fmt"
	"os"
	"sort"
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
	w.rememberVerifiedCurrent(ob)
	return nil
}

// Keep the last matched logical name while a subscription rotates its IP.
// Never guess a country/name for a configuration that was not matched before.
// All access is serialized by operationMu.
func (w *Watchdog) rememberVerifiedCurrent(ob map[string]interface{}) {
	var saved verifiedIdentity
	var haveSaved bool
	if w.verifiedCurrentOB == nil || !xkeen.SameOutbound(ob, w.verifiedCurrentOB) {
		w.manualSlowChoice = false
		saved, haveSaved = w.loadVerifiedIdentity(ob)
		if haveSaved {
			w.manualSlowChoice = saved.ManualSlowChoice
		}
	}
	w.subscription.ReconcileActive(ob)
	if server := w.subscription.GetActiveServer(); server != nil {
		w.verifiedCurrent = server
		w.verifiedCurrentOB = ob
	} else if w.verifiedCurrentOB == nil || !xkeen.SameOutbound(ob, w.verifiedCurrentOB) {
		w.verifiedCurrent = nil
		w.verifiedCurrentOB = nil
		if haveSaved {
			w.verifiedCurrent = &models.Server{Name: saved.Name, Protocol: saved.Protocol,
				Country: saved.Country, CountryOverride: saved.CountryOverride}
			w.verifiedCurrentOB = ob
		}
	}
	w.updateVerifiedIdentity(ob)
}

func (w *Watchdog) acceptableQuality(result xkeen.ProbeResult) bool {
	p := w.config.VerifiedFailover
	return result.OK && (!p.QualityEnabled || (result.Latency >= 0 && result.Latency <= p.QualityThresholdMs))
}

func (w *Watchdog) checkVerified(ctx context.Context) {
	ctx, finish, err := w.lockVerifiedOperation(ctx, false)
	if err != nil {
		return
	}
	defer finish()
	if !w.IsActive() || xkeen.IsRestarting() || ctx.Err() != nil {
		return
	}
	w.configureVerified()
	ob, err := xkeen.SingleProxy(w.config.OutboundsFile)
	if err != nil {
		w.writeLog("[VERIFY] %v", err)
		return
	}
	w.rememberVerifiedCurrent(ob)
	result, probeErr := w.verifiedProbe(ctx, ob)
	if ctx.Err() != nil {
		return
	}
	running := w.verifiedApplier.Running()
	ok := probeErr == nil && result.OK && running
	poor := ok && !w.acceptableQuality(result)
	w.mu.Lock()
	w.lastCheck = time.Now()
	w.connected = ok
	w.lastLatency = result.Latency
	if ok {
		w.failCount = 0
		if poor {
			if w.manualSlowChoice {
				w.qualityFailCount = 0
			} else {
				w.qualityFailCount++
			}
		} else {
			w.qualityFailCount = 0
		}
	} else {
		w.failCount++
		w.qualityFailCount = 0
		w.lastLatency = -1
	}
	fails := w.failCount
	qualityFails := w.qualityFailCount
	w.mu.Unlock()
	if ok {
		w.confirmConnection(ob, time.Now(), false)
	} else {
		w.endConnection()
	}
	w.publishStatus()
	if ok && !poor {
		w.writeLog("[VERIFY] VPN работает: HTTPS %d/%d, %d мс", result.Successes, result.Total, result.Latency)
		w.returnVerifiedPriority(ctx, ob)
		return
	}
	if poor {
		if w.manualSlowChoice {
			// The owner deliberately accepted this latency. Do not undo that
			// choice because of quality alone; keep testing for a healthy priority.
			w.writeLog("[QUALITY] Задержка %d мс, ручной выбор сохранён", result.Latency)
			w.returnVerifiedPriority(ctx, ob)
			return
		}
		w.writeLog("[QUALITY] Задержка %d мс выше %d мс, проверка %d/%d", result.Latency, w.config.VerifiedFailover.QualityThresholdMs, qualityFails, w.config.VerifiedFailover.QualityFailCount)
		if qualityFails < w.config.VerifiedFailover.QualityFailCount {
			return
		}
	} else {
		w.writeLog("[VERIFY] VPN не подтвердился: HTTPS %d/%d, ядро=%v, попытка %d/%d", result.Successes, result.Total, running, fails, w.config.MaxFails)
		if probeErr != nil {
			w.writeLog("[VERIFY] Ошибка проверки: %v", probeErr)
		}
		if fails < w.config.MaxFails {
			return
		}
	}
	if time.Since(w.lastVerifiedAttempt) < time.Duration(w.config.VerifiedFailover.RetryIntervalSec)*time.Second {
		return
	}
	w.lastVerifiedAttempt = time.Now()
	// A fresh subscription is helpful, but recovery still works from the cached
	// list if the provider is unreachable. Never apply its first entry blindly.
	if _, err := w.subscription.RefreshContext(ctx); err != nil {
		w.writeLog("[VERIFY] Обновление недоступно, использую сохранённые серверы: %v", err)
	}
	w.rememberVerifiedCurrent(ob)
	if err := w.failoverVerified(ctx, ob); err != nil {
		w.writeLog("[VERIFY] %v", err)
	}
}

func (w *Watchdog) returnVerifiedPriority(ctx context.Context, current map[string]interface{}) {
	p := w.config.VerifiedFailover
	if !p.ReturnToPriority || w.verifiedCurrent == nil || !xkeen.PolicyHasHigherPriority(*w.verifiedCurrent, p) {
		return
	}
	interval := time.Duration(p.PriorityCheckSec) * time.Second
	if time.Since(w.lastPriorityAttempt) < interval || time.Since(w.lastVerifiedSwitch) < interval {
		return
	}
	w.lastPriorityAttempt = time.Now()
	logicalCurrent := *w.verifiedCurrent
	w.writeLog("[PRIORITY] Обновляю подписку и проверяю более приоритетные серверы")
	if _, err := w.subscription.RefreshContext(ctx); err != nil {
		w.writeLog("[PRIORITY] Обновление недоступно, использую сохранённый список: %v", err)
	}
	w.rememberVerifiedCurrent(current)
	if w.verifiedCurrent != nil {
		logicalCurrent = *w.verifiedCurrent
	}
	if err := w.tryVerifiedCandidates(ctx, current, &logicalCurrent); err != nil {
		w.writeLog("[PRIORITY] %v", err)
	}
}

func (w *Watchdog) failoverVerified(ctx context.Context, current map[string]interface{}) error {
	return w.tryVerifiedCandidates(ctx, current, nil)
}

func (w *Watchdog) tryVerifiedCandidates(ctx context.Context, current map[string]interface{}, higherThan *models.Server) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	candidates := w.verifiedCandidates(current, higherThan, true)
	w.mu.RLock()
	allowSlowFallback := higherThan == nil && !w.connected
	w.mu.RUnlock()
	type slowCandidate struct {
		candidate verifiedCandidate
		latency   int
		order     int
	}
	var slow []slowCandidate
	rememberSlow := func(candidate verifiedCandidate, latency, order int) {
		if !allowSlowFallback || latency < 0 || latency > 7999 {
			return
		}
		for _, item := range slow {
			if item.candidate.server.RawURI == candidate.server.RawURI {
				return
			}
		}
		slow = append(slow, slowCandidate{candidate: candidate, latency: latency, order: order})
	}
	order := 0
	for len(candidates) > 0 {
		if ctx.Err() != nil || !w.IsActive() {
			return fmt.Errorf("автопереключение отменено")
		}
		picked := -1
		var result xkeen.ProbeResult
		err := w.probeVerifiedCandidates(ctx, candidates, true, func(i int, r xkeen.ProbeResult, probeErr error) bool {
			server := candidates[i].server
			server.Latency = -1
			if probeErr == nil && r.OK {
				server.Latency = r.Latency
			}
			w.subscription.UpdateLatencies([]models.Server{server})
			if probeErr == nil && r.OK && !w.acceptableQuality(r) {
				rememberSlow(candidates[i], r.Latency, order+i)
				if r.Latency >= 0 {
					w.writeLog("[QUALITY] %s слишком медленный: %d мс", server.Name, r.Latency)
				}
				return true
			}
			if probeErr != nil || !r.OK {
				w.blacklistServer(server.RawURI)
				return true
			}
			picked, result = i, r
			return false
		})
		if err != nil {
			return err
		}
		if picked < 0 {
			break
		}
		server, candidate := candidates[picked].server, candidates[picked].outbound
		order += picked + 1
		candidates = candidates[picked+1:]
		if err := ctx.Err(); err != nil {
			return err
		}
		if !w.IsActive() {
			return fmt.Errorf("автопереключение выключено")
		}
		if err := w.verifiedApplier.ApplyChecked(ctx, candidate, func(confirmed xkeen.ProbeResult) error {
			result = confirmed
			if !w.acceptableQuality(confirmed) {
				if confirmed.OK {
					rememberSlow(verifiedCandidate{server: server, outbound: candidate}, confirmed.Latency, order-1)
				}
				server.Latency = confirmed.Latency
				w.subscription.UpdateLatencies([]models.Server{server})
				return fmt.Errorf("задержка после перезапуска %d мс превышает допустимую", confirmed.Latency)
			}
			return nil
		}); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.writeLog("[VERIFY] %s не применён: %v", server.Name, err)
			w.blacklistServer(server.RawURI)
			// A pending journal means rollback needs attention: stop this round.
			if err := w.verifiedApplier.Recover(); err != nil {
				return err
			}
			continue
		}
		w.commitVerifiedCandidate(server, candidate, result)
		w.writeLog("[VERIFY] Переключено и подтверждено: %s", server.Name)
		return nil
	}
	if allowSlowFallback && len(slow) > 0 {
		// Prefer the lowest measured delay, retaining policy order on ties.
		sort.SliceStable(slow, func(i, j int) bool {
			if slow[i].latency != slow[j].latency {
				return slow[i].latency < slow[j].latency
			}
			return slow[i].order < slow[j].order
		})
		for _, item := range slow {
			if ctx.Err() != nil || !w.IsActive() {
				return fmt.Errorf("автопереключение отменено")
			}
			server, candidate := item.candidate.server, item.candidate.outbound
			var confirmed xkeen.ProbeResult
			if err := w.verifiedApplier.ApplyChecked(ctx, candidate, func(r xkeen.ProbeResult) error {
				confirmed = r
				if r.Latency < 0 || r.Latency > 7999 {
					return fmt.Errorf("задержка после перезапуска превышает 7999 мс")
				}
				return nil
			}); err != nil {
				w.writeLog("[VERIFY] Медленный запасной %s не применён: %v", server.Name, err)
				w.blacklistServer(server.RawURI)
				if err := w.verifiedApplier.Recover(); err != nil {
					return err
				}
				continue
			}
			w.commitVerifiedCandidate(server, candidate, confirmed)
			w.writeLog("[VERIFY] Рабочих быстрых серверов нет; подключён медленный запасной %s: %d мс", server.Name, confirmed.Latency)
			return nil
		}
	}
	if higherThan != nil {
		return fmt.Errorf("более приоритетных серверов с допустимым качеством нет; текущее соединение сохранено")
	}
	return fmt.Errorf("серверов с допустимым качеством по выбранным правилам нет; текущая конфигурация сохранена, попытка будет повторена")
}

func (w *Watchdog) commitVerifiedCandidate(server models.Server, candidate map[string]interface{}, result xkeen.ProbeResult) {
	server.Latency = result.Latency
	w.subscription.UpdateLatencies([]models.Server{server})
	if _, err := w.subscription.SetActiveByRawURI(server.RawURI); err != nil {
		w.writeLog("[VERIFY] Сервер применён, состояние подписки не сохранено: %v", err)
	}
	w.mu.Lock()
	w.failCount = 0
	w.qualityFailCount = 0
	w.connected = true
	w.lastLatency = result.Latency
	w.lastCheck = time.Now()
	w.mu.Unlock()
	w.lastVerifiedSwitch = time.Now()
	w.confirmConnection(candidate, w.lastVerifiedSwitch, true)
	w.rememberVerifiedCurrent(candidate)
	w.publishStatus()
}

// RefreshVerified refreshes metadata only, keeping a working active config.
// The same lock serializes refresh, manual selection and automatic failover.
func (w *Watchdog) RefreshVerified(newURL string) ([]models.Server, error) {
	return w.RefreshVerifiedContext(context.Background(), newURL)
}

func (w *Watchdog) RefreshVerifiedContext(ctx context.Context, newURL string) ([]models.Server, error) {
	ctx, finish, err := w.lockVerifiedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer finish()
	var servers []models.Server
	if ob, e := xkeen.SingleProxy(w.config.OutboundsFile); e == nil {
		w.rememberVerifiedCurrent(ob)
	}
	hadServers := len(w.subscription.GetServers()) > 0
	if newURL != "" {
		servers, err = w.subscription.UpdateURLContext(ctx, newURL)
	} else {
		servers, err = w.subscription.RefreshContext(ctx)
	}
	if ob, e := xkeen.SingleProxy(w.config.OutboundsFile); e == nil {
		w.rememberVerifiedCurrent(ob)
	}
	if err == nil {
		w.lastVerifiedAttempt = time.Time{}
	}
	if err == nil && newURL != "" && !hadServers && w.config.WatchdogAutoStart {
		w.SetActive(true)
	}
	return servers, err
}

func (w *Watchdog) SelectVerified(ctx context.Context, id int) (*models.Server, error) {
	ctx, finishSelection, err := w.lockVerifiedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer finishSelection()
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
	if err := w.verifiedApplier.ApplyChecked(ctx, ob, func(confirmed xkeen.ProbeResult) error {
		result = confirmed
		return nil // A deliberate manual choice may be slow; show its quality.
	}); err != nil {
		return nil, err
	}
	w.ClearBlacklist(chosen.RawURI)
	chosen.Latency = result.Latency
	w.subscription.UpdateLatencies([]models.Server{*chosen})
	selected, err := w.subscription.SetActiveByRawURI(chosen.RawURI)
	w.rememberVerifiedCurrent(ob)
	w.manualSlowChoice = result.OK && !w.acceptableQuality(result)
	w.updateVerifiedIdentity(ob)
	w.lastVerifiedSwitch = time.Now()
	w.confirmConnection(ob, w.lastVerifiedSwitch, true)
	w.mu.Lock()
	w.failCount = 0
	w.qualityFailCount = 0
	w.connected = true
	w.lastLatency = result.Latency
	w.lastCheck = time.Now()
	w.mu.Unlock()
	w.publishStatus()
	return selected, err
}

func (w *Watchdog) CheckVerifiedServers(ctx context.Context, emit func(models.Server)) ([]models.Server, error) {
	ctx, finishCheck, err := w.beginVerifiedCheck(ctx)
	if err != nil {
		return nil, err
	}
	defer finishCheck()
	ctx, finishOperation, err := w.lockVerifiedOperation(ctx, false)
	if err != nil {
		return nil, err
	}
	defer finishOperation()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.configureVerified()
	current, err := xkeen.SingleProxy(w.config.OutboundsFile)
	if err != nil {
		return nil, err
	}
	var checked []models.Server
	candidates := w.verifiedCandidates(current, nil, false)
	err = w.probeVerifiedCandidates(ctx, candidates, false, func(i int, r xkeen.ProbeResult, probeErr error) bool {
		s := candidates[i].server
		s.Latency = -1
		if probeErr == nil && r.OK {
			s.Latency = r.Latency
		}
		checked = append(checked, s)
		w.subscription.UpdateLatencies([]models.Server{s})
		if emit != nil {
			emit(s)
		}
		return true
	})
	return checked, err
}

// LockCoreOperation prevents UI core changes from racing a failover transaction.
func (w *Watchdog) LockCoreOperation() func() {
	finish := w.prioritizeManualSelection()
	w.operationMu.Lock()
	return func() {
		w.operationMu.Unlock()
		finish()
	}
}
