# Разработка и проверка

## Сборка

Требуются Go 1.25+ (использовано 1.26.8), Node.js 22 и npm. Веб-интерфейс встраивается в Go-бинарник, поэтому сначала соберите его:

```sh
cd frontend
npm ci
npm run build
cd ..
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o build/xkeen-panel-aarch64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags='-s -w' -o build/xkeen-panel-armv7 .
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -trimpath -ldflags='-s -w' -o build/xkeen-panel-mipsel .
```

CI выполняет сборку интерфейса, Go-тесты, `go vet`, интеграционные проверки с Xray 26.9.9 и сборку трёх архитектур. Тег `v*` публикует бинарники, установщик, конфиг по умолчанию и SHA256SUMS.

## Основные части

| Файлы | Назначение |
|---|---|
| `internal/monitor/verified.go` | Проверка активного VPN, обновление подписки и последовательный поиск замены |
| `internal/monitor/automation.go` | Валидация, сохранение и применение пользовательских правил |
| `internal/xkeen/verified_policy.go` | Разрешённые страны, приоритет названий и исключения до любых проб |
| `internal/xkeen/proxy_uri.go` | Преобразование стандартных ссылок четырёх протоколов в Xray outbound |
| `internal/xkeen/verified_probe.go` | Изолированная HTTPS-проверка через временный Xray и SOCKS |
| `internal/xkeen/direct.go`, `marked_linux.go` | Загрузка подписки и DNS с обходом перехвата основного VPN |
| `internal/xkeen/verified_apply.go` | Транзакция применения, журнал восстановления и точный откат |
| `internal/xkeen/control.go` | Ожидание фактического перезапуска основного ядра |
| `internal/api/automation.go` | Защищённые GET/PUT `/api/automation` |
| `frontend/src/components/automationCard.tsx` | Редактирование правил без изменения файлов |

## Состояние

`data/automation.json` содержит только пользовательские правила: enabled, country_priority, allow_other_countries, preferred_server_names, excluded_server_names, exclude_name_contains. Файл сохраняется атомарно с правами 0600. Если сохранить не удалось, действующие правила не меняются. Правила загружаются до фоновой работы.

Параметры установки и служебная метка остаются в `config.yaml`; пользовательские предпочтения редактируются в UI. Старый YAML со списком стран без нового поля allow_other_countries сохраняет семантику строгого списка. При отсутствии automation.json используются старые правила YAML.

Идентичность проверяемого подключения определяется параметрами URI, а не отображаемым названием или индексом. Рейтинг и постоянные исключения задаются по названию, чтобы переживать ротацию адреса. Обновление, смена правил, ручной выбор и автоматическое применение сериализованы; в панели одновременно используется не более одного тестового ядра.

## Интеграционные проверки

```sh
TEST_XRAY_BIN=/absolute/path/to/xray go test ./internal/xkeen -count=1 -v
```

Синтетические тесты не содержат пользовательских секретов. Включены проверки приоритета, исключения до проб, смены IP при прежнем названии, сохранения настроек, неподменяемого HTTPS-пути через SOCKS, проверки с реальным Xray, отказа валидатора, отказа запуска, отката и восстановления прерванного переключения.

Опционально можно передать приватные пути `TEST_PRIVATE_SUBSCRIPTION` и `TEST_PRIVATE_OUTBOUNDS`. Файлы только читаются; конфиги разрешённых кандидатов проверяются локальным Xray без соединений с VPN-провайдером. Не добавляйте эти файлы в Git.

Первоначальная версия ядра доработки проверена на Netcraze NC-3811, прошивке 5.1.6, XKeen 2.0 Stable и Xray 26.9.9: основной процесс работает, обе HTTPS-проверки успешны. Новые протоколы и UI проверяются отдельно локально/в CI. Сборка для архитектуры не является подтверждением работы на каждом устройстве: изменение режима перехвата или firewall требует проверки служебной метки и реальных соединений на целевом роутере.

## Происхождение

Исходный проект: https://github.com/Dearonski/xkeen-panel, коммит e0307ff36c10b640da71ad8da0ae3ca70135b200. История и авторство сохраняются. В этом исходном коммите отдельный LICENSE не обнаружен; доработка не объявляет новую лицензию на чужой код и не меняет права исходного автора.

Схемы протоколов сверены с официальной документацией Xray: [VMess](https://xtls.github.io/config/outbounds/vmess.html), [Trojan](https://xtls.github.io/config/outbounds/trojan.html), [Shadowsocks](https://xtls.github.io/config/outbounds/shadowsocks.html).
