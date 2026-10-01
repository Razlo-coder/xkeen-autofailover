package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
	"xkeen-panel/internal/auth"
	"xkeen-panel/internal/geoip"
	"xkeen-panel/internal/models"
	"xkeen-panel/internal/monitor"
	"xkeen-panel/internal/server"
	"xkeen-panel/internal/sse"
	"xkeen-panel/internal/xkeen"

	"gopkg.in/yaml.v3"
)

func main() {
	configPath := flag.String("config", "config.yaml", "путь к конфигурационному файлу")
	preflight := flag.Bool("preflight", false, "проверить настройки и конфигурацию XKeen без изменений")
	verifyNow := flag.Bool("verify-now", false, "однократно проверить текущее VPN-подключение без изменения файлов")
	flag.Parse()

	// Load the configuration
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Ошибка загрузки конфига: %v", err)
	}

	// User manager
	userManager := auth.NewUserManager(cfg.DataDir)
	if err := userManager.Load(); err != nil {
		log.Fatalf("Ошибка загрузки пользователя: %v", err)
	}

	// Subscription manager
	subManager := xkeen.NewSubscriptionManager(cfg.DataDir)
	if cfg.VerifiedFailover.Enabled {
		subManager.SetHTTPClient(xkeen.DirectHTTPClient(cfg.VerifiedFailover.BypassMark))
	}
	if err := subManager.Load(); err != nil {
		log.Printf("Предупреждение: ошибка загрузки подписки: %v", err)
	}

	// Detect the XKeen layout: generation (S05xkeen/S24xray), core, mode, version
	detector := xkeen.NewDetector("", cfg.XKeenPath, cfg.InitScript,
		cfg.XrayConfigDir, cfg.RoutingFile, cfg.MihomoConfig, cfg.XkeenJSON)
	rt := detector.Runtime()
	if *verifyNow {
		ob, err := xkeen.SingleProxy(cfg.OutboundsFile)
		if err != nil {
			log.Fatal(err)
		}
		p := xkeen.VPNProber{Binary: rt.CoreBin, Mark: cfg.VerifiedFailover.BypassMark, URLs: cfg.HealthCheckURLs, Timeout: time.Duration(cfg.VerifiedFailover.ProbeTimeoutSec) * time.Second}
		r, err := p.Probe(context.Background(), ob)
		running := xkeen.IsRunning(xkeen.CoreXray)
		log.Printf("VPN: HTTPS %d/%d; основное ядро=%v", r.Successes, r.Total, running)
		if err != nil {
			log.Printf("Ошибка проверки: %v", err)
		}
		if err != nil || !r.OK || !running {
			os.Exit(2)
		}
		return
	}
	if *preflight {
		if !rt.Installed || rt.Core != xkeen.CoreXray {
			log.Fatal("Нужен установленный XKeen с ядром Xray")
		}
		if _, err := xkeen.SingleProxy(cfg.OutboundsFile); err != nil {
			log.Fatal(err)
		}
		if output, err := xkeen.ValidateXray(rt); err != nil {
			log.Fatalf("Проверка Xray: %s", xkeen.TailLines(output, 4))
		}
		log.Printf("Настройки проверены: ядро=%s, страны=%v, исключения=%v", rt.Core, cfg.VerifiedFailover.CountryPriority, cfg.VerifiedFailover.ExcludeNameContains)
		return
	}
	if !rt.Installed {
		log.Printf("XKeen не найден (%s) — управление ядром недоступно", rt.InitScript)
	} else {
		log.Printf("XKeen %s (поколение %d), ядро %s, режим %s", rt.Version, rt.Generation, rt.Core, rt.Mode)
	}

	// Pool state: the tag the routing rules were written against is what makes a
	// correct return to a single outbound possible
	poolStore := xkeen.NewPoolStore(cfg.DataDir)
	if err := poolStore.Load(); err != nil {
		log.Printf("Предупреждение: не удалось загрузить состояние пула: %v", err)
	}

	// An install from before the api block was fixed cannot pin a pool node until
	// the file is migrated, and a pool in sync never reaches the refresh path
	if !cfg.VerifiedFailover.Enabled {
		if migrated, err := xkeen.EnsureAPIConfig(poolStore.Get(), cfg.XrayAPIAddr); err != nil {
			log.Printf("Не удалось обновить api-блок Xray: %v", err)
		} else if migrated {
			log.Printf("api-блок Xray приведён к текущей форме — перезапускаю ядро")
			xkeen.Restart(rt.Dispatcher)
		}
	}

	// Watchdog and SSE
	watchdog := monitor.NewWatchdog(cfg, subManager, detector)
	eventBus := sse.NewEventBus()
	watchdog.SetEventBus(eventBus)
	watchdog.SetPoolStore(poolStore)
	if cfg.VerifiedFailover.Enabled {
		if err := watchdog.LoadAutomation(); err != nil {
			log.Fatalf("Настройки автоматизации: %v", err)
		}
		if err := watchdog.PrepareVerified(); err != nil {
			log.Fatalf("Проверяемое переключение: %v", err)
		}
	}

	// Package xkeen logs to stdout by default, which the init script discards —
	// send its pool and restart events to the panel log instead
	xkeen.Log = watchdog.Log

	// GeoIP reuses the geoip.dat already installed for Xray
	var geoMatcher *geoip.Matcher
	if geoPath := geoip.FindDat(cfg.GeoIPPath); geoPath == "" {
		log.Printf("GeoIP: geoip.dat не найден (%s) — гео-фильтр по IP отключён, используется определение по имени", cfg.GeoIPPath)
	} else if matcher, err := geoip.Load(geoPath, cfg.AutoSwitchAvoidCountries); err != nil {
		log.Printf("GeoIP: ошибка загрузки %s: %v — гео-фильтр по IP отключён", geoPath, err)
	} else {
		geoMatcher = matcher
		watchdog.SetGeoIP(matcher)
		log.Printf("GeoIP: загружен %s (избегаемые страны: %v)", geoPath, cfg.AutoSwitchAvoidCountries)
	}

	// Autostart the watchdog for unattended operation
	if cfg.WatchdogAutoStart && (len(subManager.GetServers()) > 0 || cfg.VerifiedFailover.Enabled) {
		watchdog.SetActive(true)
		log.Printf("Watchdog включён автоматически (watchdog_auto_start)")
	}

	// Publish restart events over SSE
	xkeen.OnRestartStateChange = func(restarting bool) {
		eventBus.Publish(sse.Event{
			Type: "restart",
			Data: map[string]bool{"restarting": restarting},
		})
		eventBus.Publish(sse.Event{
			Type: "status",
			Data: watchdog.GetStatus(),
		})
	}

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the watchdog
	go watchdog.Start(ctx)

	// Periodic subscription refresh
	if cfg.SubscriptionRefreshInterval > 0 {
		go runSubscriptionRefresh(ctx, cfg, subManager, watchdog, detector, poolStore, geoMatcher, eventBus)
	}

	// Frontend assets
	var frontendFS fs.FS
	distFS, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		log.Printf("Предупреждение: встроенный фронтенд недоступен: %v", err)
	} else {
		frontendFS = distFS
	}

	// HTTP server
	srv := server.New(cfg, userManager, subManager, watchdog, detector, poolStore, geoMatcher, eventBus, frontendFS)
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: srv.Handler(),
	}

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Получен сигнал завершения, останавливаем сервер...")
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("Ошибка при остановке сервера: %v", err)
		}
	}()

	log.Printf("XKeen Panel v2 запущена на порту %d (xkeen=%s, outbounds=%s)", cfg.Port, cfg.XKeenPath, cfg.OutboundsFile)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Ошибка сервера: %v", err)
	}

	log.Println("Сервер остановлен")
}

// runSubscriptionRefresh refreshes the subscription on a timer. The core is
// restarted only when the active server was actually replaced (it vanished from
// the subscription) — a refresh must not drop a working connection.
func runSubscriptionRefresh(ctx context.Context, cfg *models.Config, sm *xkeen.SubscriptionManager, wd *monitor.Watchdog, det *xkeen.Detector, pool *xkeen.PoolStore, matcher *geoip.Matcher, bus *sse.EventBus) {
	interval := time.Duration(cfg.SubscriptionRefreshInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cfg.VerifiedFailover.Enabled {
				if servers, err := wd.RefreshVerified(""); err != nil {
					wd.Log("[AUTO-UPDATE] %v", err)
				} else {
					wd.Log("[AUTO-UPDATE] Подписка обновлена (%d серверов)", len(servers))
					bus.Publish(sse.Event{Type: "subscription", Data: map[string]bool{"updated": true}})
				}
				continue
			}
			prevURI := ""
			if a := sm.GetActiveServer(); a != nil {
				prevURI = a.RawURI
			}

			var err error
			for attempt := 0; attempt < 2; attempt++ {
				if _, err = sm.Refresh(); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
			}
			if err != nil {
				wd.Log("[AUTO-UPDATE] Подписка не обновилась: %v", err)
				continue
			}

			active := sm.GetActiveServer()
			newURI := ""
			if active != nil {
				newURI = active.RawURI
			}

			rt := det.Runtime()

			// A pool is generated from subscription URIs, so a rotated server
			// leaves it pointing at an endpoint that no longer answers. Sync on
			// every refresh, not only when the active server changed.
			if top := det.Topology(); top.Mode == xkeen.TopologyPool {
				state := pool.Get()
				state.BalancerTag = top.BalancerTag
				if len(top.Selectors) > 0 {
					state.Selector = top.Selectors[0]
				}

				res, err := xkeen.RefreshPool(rt, cfg.OutboundsFile, cfg.XrayAPIAddr, sm.GetServers(), state,
					xkeen.PoolSelectionFromConfig(cfg, matcher))
				switch {
				case err != nil:
					wd.Log("[AUTO-UPDATE] Пул не синхронизирован: %v", err)
				case res.Changed:
					det.InvalidateTopology()
					wd.Log("[AUTO-UPDATE] Пул обновлён: +%d, -%d, заменено %d%s", len(res.Added), len(res.Removed), len(res.Replaced), liveSuffix(res))
				}
			} else if active != nil && newURI != prevURI {
				// Single-outbound mode: the active server left the subscription.
				// Do not blindly restart onto servers[0] — it may sit in an
				// avoided country; let the watchdog's geo filter choose.
				if target := wd.AllowedActiveOrBest(); target != nil {
					if err := xkeen.ApplyServer(rt, cfg.OutboundsFile, target); err != nil {
						log.Printf("[AUTO-UPDATE] Ошибка конфига: %v", err)
					} else {
						xkeen.Restart(rt.Dispatcher)
						log.Printf("[AUTO-UPDATE] Активный сервер заменён на %s, ядро перезапущено", target.Name)
					}
				}
			}

			bus.Publish(sse.Event{Type: "subscription", Data: map[string]bool{"updated": true}})
			wd.Log("[AUTO-UPDATE] Подписка обновлена (%d серверов)", len(sm.GetServers()))
		}
	}
}

// liveSuffix says whether the pool update avoided a restart.
func liveSuffix(res xkeen.SyncResult) string {
	if res.Live {
		return " (без перезапуска)"
	}
	if res.Restarted {
		return " (с перезапуском ядра)"
	}
	return ""
}

func loadConfig(path string) (*models.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &models.Config{
		Port:          3000,
		DataDir:       "data",
		XKeenPath:     "/opt/sbin/xkeen",
		OutboundsFile: "/opt/etc/xray/configs/04_outbounds.json",
		XrayAPIAddr:   "127.0.0.1:10085",
		CheckInterval: 120,
		CheckURL:      "https://www.google.com",
		MaxFails:      3,
		LogFile:       "xkeen-panel.log",
		VerifiedFailover: models.VerifiedFailoverConfig{
			AllowOtherCountries: true,
			BypassMark:          255, ProbeTimeoutSec: 8, RetryIntervalSec: 120,
		},

		ProbeTimeoutMs:     2000,
		ProbeConcurrency:   20,
		LatencyAutoSwitch:  true,
		LatencyThresholdMs: 1000,
		LatencySwitchCount: 3,
		BlacklistTTLSec:    300,
		WatchdogAutoStart:  true,

		SubscriptionRefreshInterval: 1800,

		PoolMaxNodes:             xkeen.DefaultPoolMaxNodes,
		HealthCheckURLs:          monitor.DefaultHealthURLs,
		HealthCheckEvery:         5,
		HealthFailThreshold:      2,
		HealthQuorum:             2,
		GeoIPPath:                "/opt/etc/xray/dat/geoip_v2fly.dat",
		AutoSwitchAvoidCountries: []string{"RU", "BY"},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	// Older installations used CountryPriority as a strict allowlist. Preserve
	// that choice unless the new explicit checkbox was saved in YAML/the UI.
	var legacy struct {
		Verified struct {
			Allow *bool `yaml:"allow_other_countries"`
		} `yaml:"verified_failover"`
	}
	if yaml.Unmarshal(data, &legacy) == nil && legacy.Verified.Allow == nil && len(cfg.VerifiedFailover.CountryPriority) > 0 {
		cfg.VerifiedFailover.AllowOtherCountries = false
	}
	if cfg.VerifiedFailover.Enabled {
		if cfg.CheckInterval < 10 || cfg.MaxFails < 2 {
			return nil, fmt.Errorf("check_interval должен быть >= 10, max_fails >= 2")
		}
		p := cfg.VerifiedFailover
		if (!p.AllowOtherCountries && len(p.CountryPriority) == 0) || p.BypassMark < 1 || p.ProbeTimeoutSec < 1 || p.ProbeTimeoutSec > 60 || p.RetryIntervalSec < 30 {
			return nil, fmt.Errorf("неверные параметры verified_failover")
		}
		if cfg.SubscriptionRefreshInterval < 0 {
			return nil, fmt.Errorf("интервал подписки не может быть отрицательным")
		}
		hosts := map[string]bool{}
		for _, target := range cfg.HealthCheckURLs {
			u, e := url.Parse(target)
			if e != nil || u.Scheme != "https" || u.Hostname() == "" {
				return nil, fmt.Errorf("health_check_urls должны содержать HTTPS-адреса")
			}
			hosts[u.Hostname()] = true
		}
		if len(hosts) < 2 {
			return nil, fmt.Errorf("нужны как минимум два независимых HTTPS-адреса для проверки")
		}
	}

	return cfg, nil
}
