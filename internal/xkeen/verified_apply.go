package xkeen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// VerifiedApplier keeps a durable rollback journal until configuration
// validation, restart completion and a second VPN probe all succeed.
type VerifiedApplier struct {
	Path     string
	DataDir  string
	Validate func() error
	Restart  func() error
	Running  func() bool
	Probe    func(context.Context, map[string]interface{}) (ProbeResult, error)
}

func (a *VerifiedApplier) journal() string { return filepath.Join(a.DataDir, "failover-pending.json") }

func (a *VerifiedApplier) removeJournal() error {
	if err := os.Remove(a.journal()); err != nil {
		return err
	}
	return syncParent(a.journal())
}

func (a *VerifiedApplier) Apply(ctx context.Context, outbound map[string]interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	old, err := os.ReadFile(a.Path)
	if err != nil {
		return err
	}
	cfg, err := ReadOutboundsConfig(a.Path)
	if err != nil {
		return err
	}
	obs := asSlice(cfg["outbounds"])
	idx, _ := findProxyOutbound(obs)
	if idx < 0 || countProxyOutbounds(obs) != 1 {
		return fmt.Errorf("ожидается один proxy outbound")
	}
	obs[idx] = outbound
	if err := os.MkdirAll(a.DataDir, 0700); err != nil {
		return err
	}
	// Keep exact original bytes, including comments, for rollback.
	if err := writeFileAtomic(a.journal(), old, 0600); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		os.Remove(a.journal())
		return err
	}
	if err := writeFileAtomic(a.Path+".bak", old, 0600); err != nil {
		os.Remove(a.journal())
		return err
	}
	if err := writeFileAtomic(a.Path, data, 0600); err != nil {
		return a.rollback(err, false)
	}
	if err := a.Validate(); err != nil {
		return a.rollback(fmt.Errorf("конфигурация отвергнута: %w", err), false)
	}
	if err := ctx.Err(); err != nil {
		return a.rollback(err, false)
	}
	if err := a.Restart(); err != nil {
		return a.rollback(fmt.Errorf("перезапуск не завершён: %w", err), true)
	}
	if !a.Running() {
		return a.rollback(fmt.Errorf("основное ядро Xray не запустилось"), true)
	}
	actual, err := SingleProxy(a.Path)
	if err != nil {
		return a.rollback(err, true)
	}
	if !SameOutbound(actual, outbound) {
		return a.rollback(fmt.Errorf("конфигурация изменена во время переключения"), true)
	}
	result, err := a.Probe(ctx, actual)
	if err != nil || !result.OK || !a.Running() {
		return a.rollback(fmt.Errorf("VPN не подтвердился после перезапуска"), true)
	}
	if err := a.removeJournal(); err != nil {
		// Keeping a pending journal after reporting success would undo the
		// successful switch at the next boot. Fail and restore instead.
		return a.rollback(fmt.Errorf("не удалось завершить журнал переключения"), true)
	}
	return nil
}

func (a *VerifiedApplier) rollback(cause error, restart bool) error {
	old, err := os.ReadFile(a.journal())
	if os.IsNotExist(err) {
		old, err = os.ReadFile(a.Path + ".bak")
		if err == nil {
			err = writeFileAtomic(a.journal(), old, 0600)
		}
	}
	if err != nil {
		return fmt.Errorf("%v; резервная копия недоступна: %w", cause, err)
	}
	if err := writeFileAtomic(a.Path, old, 0600); err != nil {
		return fmt.Errorf("%v; откат файла не удался: %w", cause, err)
	}
	if restart {
		if err := a.Restart(); err != nil {
			return fmt.Errorf("%v; файл восстановлен, перезапуск при откате не удался: %w", cause, err)
		}
	}
	if err := a.removeJournal(); err != nil {
		return fmt.Errorf("%v; файл восстановлен, журнал не удалён: %w", cause, err)
	}
	return fmt.Errorf("%v; предыдущая конфигурация восстановлена", cause)
}

// Recover is called before the watchdog on startup. If power failed during a
// switch, the previous configuration is restored and the journal is retained
// until restarting it succeeds.
func (a *VerifiedApplier) Recover() error {
	if _, err := os.Stat(a.journal()); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	old, err := os.ReadFile(a.journal())
	if err != nil {
		return err
	}
	if err := writeFileAtomic(a.Path, old, 0600); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if err := a.Restart(); err != nil {
		return err
	}
	if a.Running != nil && !a.Running() {
		return fmt.Errorf("основное ядро не запущено после восстановления")
	}
	return a.removeJournal()
}

// AtomicWritePrivate is also used for private subscription state.
func AtomicWritePrivate(path string, data []byte) error { return writeFileAtomic(path, data, 0600) }
