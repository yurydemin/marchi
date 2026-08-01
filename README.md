# Marchi

**Marchi** — self-hosted сервис архивации электронной почты. Подключает произвольные
IMAP-аккаунты, архивирует письма в фоне по настраиваемым правилам, даёт полнотекстовый
поиск по архиву, умеет реплицировать архив в S3-совместимое хранилище и восстанавливать
письма обратно в почтовый ящик.

Работает на одном бинарнике без внешних зависимостей (кроме опционального S3), хранит
все данные локально (SQLite + Maildir), шифрует пароли/токены/S3-ключи на диске.

## Возможности

- **IMAP-аккаунты**: пароль или OAuth2 (Google/Microsoft, BYO-приложение — свой
  `client_id`/`client_secret`, Marchi не поставляет общий клиент).
- **Gmail API-коннектор**: альтернатива IMAP для Google-аккаунтов — синк через нативный
  Gmail REST API с дельта-синхронизацией (History API) вместо полного обхода UID; пока
  только через REST (`POST /api/v1/accounts/oauth2` с `"connector_type": "gmail_api"`),
  без формы в Web UI. Подробности — [docs/api/openapi.yaml](docs/api/openapi.yaml).
- **MS Graph-коннектор**: аналогично для Microsoft 365/Exchange Online — синк через
  Microsoft Graph REST API с реальными иерархическими папками (не как у Gmail — с метками)
  и дельта-синхронизацией per-folder. `"connector_type": "ms_graph"` в том же эндпоинте.
- **Правила архивации**: визуальный AND/OR-конструктор условий (тема, отправитель,
  домен, вложения, размер, дата, папка — 15 типов условий, вложенность до 3 уровней),
  действия `archive`/`skip`/`archive_and_delete`/`archive_and_mark_read`. Правила можно
  редактировать и в Web UI, и через `rules.yaml` (однонаправленная синхронизация файла в
  БД, отслеживается через fsnotify). «Проверить на архиве» — dry-run нового условия
  против уже заархивированных писем, до сохранения правила. Каждое правило накапливает
  счётчик срабатываний и дату последнего совпадения, видимые прямо в списке правил.
- **Полнотекстовый поиск**: индекс на [Bluge](https://github.com/blugelabs/bluge) по
  теме/телу/отправителю/вложениям, с фильтрами по дате/аккаунту/папке.
- **S3-репликация**: зеркалирование заархивированных писем в любое S3-совместимое
  хранилище (AWS S3, MinIO, ...) с клиентским шифрованием (AES-256-GCM) перед загрузкой.
- **Retention**: трёхстадийный жизненный цикл письма — локально → перенос в S3 →
  окончательное удаление, с настраиваемыми по умолчанию и переопределяемыми на уровне
  аккаунта порогами в днях.
- **Восстановление**: письмо (или пачка писем по результатам поиска) можно вернуть в
  любой IMAP-аккаунт через `APPEND`, с фолбэком на SMTP — как по выбору, так и целиком
  весь аккаунт/папку, опционально сузив по диапазону дат.
- **Массовые операции над результатами поиска**: удаление и перенос в другую папку (в
  рамках того же аккаунта) выбранных писем, экспорт выбранного (или всего результата
  поиска) в `.zip` (по письму на файл) или в один файл `mbox`.
- **Импорт**: перенос почты из `mbox`, Maildir или отдельных `.eml` в архив —
  `marchi import` (CLI; идемпотентно — повторный запуск докачивает только новое,
  дедупликация по Message-ID).
- **Уведомления о сбоях**: webhook (с HMAC-подписью тела) и/или письмо через отдельный
  исходящий SMTP-relay — при сбое синхронизации/retention, росте очереди S3, нехватке
  места на диске.
- **Аудит-лог**: разблокировки, CRUD правил, восстановление, удаление писем — с IP и
  временем, просмотр в Settings.
- **Web UI**: Dashboard, Accounts, Archive (поиск + просмотр письма + восстановление),
  Rules, Settings — тёмная/светлая тема, RU/EN-локализация, адаптивная вёрстка для
  мобильных экранов.
- **CLI**: те же операции без браузера — `add-account`, `sync`, `status`, `retention run`,
  `reindex`, `import`, `backup` и т.д. (`marchi --help`, `marchi --lang ru --help`).
- **Master Key**: единый пароль (Argon2id) шифрует Data Encryption Key, из которого
  выводятся отдельные подключи для IMAP-паролей, OAuth2-токенов и S3-объектов — смена
  пароля не требует перешифровывания уже сохранённых данных.
- **Метрики**: `/metrics` в формате Prometheus (счётчики писем, синхронизаций, очереди
  S3, HTTP-latency) — опционально закрывается bearer-токеном (`security.metrics_token` в
  `config.yaml` или `MARCHI_METRICS_TOKEN`), если отдаётся за пределы доверенной сети.
- **`/healthz`**: всегда `200`, пока жив процесс, без гейта разблокировки — для
  Docker/Kubernetes healthcheck'ов, которым не нужно (и не должно) перезапускать
  контейнер только из-за того, что vault ещё заперт.

## Быстрый старт (Docker)

Единственная поддерживаемая платформа — Linux (amd64/arm64), запуск через Docker или
systemd.

```bash
git clone https://github.com/yurydemin/marchi.git
cd marchi
docker compose up
```

`docker-compose.yml` ссылается на готовый образ `ghcr.io/yurydemin/marchi:latest` — Docker
сам скачает его при первом запуске, собирать из исходников не нужно (хотя `git clone`
всё равно удобен ради самого `docker-compose.yml`). Без compose — тот же образ можно
запустить и напрямую:

```bash
docker pull ghcr.io/yurydemin/marchi:latest   # или конкретная версия, например :0.10.0
docker run -p 8443:8080 -v marchi-data:/data ghcr.io/yurydemin/marchi:latest
```

Откройте `https://localhost:8443` (самоподписанный TLS-сертификат — браузер спросит
подтверждение). При первом запуске сервис попросит задать пароль Master Key (минимум 12
символов) — это единственный секрет, который нужно запомнить.

Данные (SQLite, Maildir, поисковый индекс, TLS-сертификат) живут в именованном Docker
volume `marchi-data`, переживают `docker compose down && up`.

Хотите потестировать S3-репликацию локально (MinIO, без реального облачного аккаунта)?

```bash
docker compose --profile s3 up
```

MinIO-консоль будет на `http://localhost:9001` (логин/пароль — `marchiadmin`/`marchisecret`,
см. `docker-compose.yml`; смените перед любым использованием за пределами локального теста).

### Автоматическая разблокировка при рестарте контейнера

Без этого сервис после каждого `docker compose restart` требует зайти в браузер и
разблокировать вручную. Чтобы разблокировался сам:

```bash
cp .env.example .env
# впишите MARCHI_MASTER_KEY=ваш-пароль в .env
docker compose up
```

`.env` в `.gitignore` — реальный пароль туда, в репозиторий не попадёт.

## Быстрый старт (systemd)

Полная пошаговая инструкция — [build/systemd/README.md](build/systemd/README.md).
Коротко:

```bash
go build -o marchi ./cmd/marchi
sudo install -o root -g root -m 0755 marchi /usr/local/bin/marchi
sudo useradd --system --home-dir /var/lib/marchi --shell /usr/sbin/nologin marchi
sudo mkdir -p /var/lib/marchi /etc/marchi
sudo chown marchi:marchi /var/lib/marchi
sudo install -o root -g marchi -m 0640 build/systemd/config.yaml.example /etc/marchi/config.yaml
sudo install -o root -g root -m 0644 build/systemd/marchi.service /etc/systemd/system/marchi.service
sudo systemctl daemon-reload
sudo systemctl enable --now marchi
```

Готовые бинарники под `linux/amd64` и `linux/arm64` — на странице
[Releases](https://github.com/yurydemin/marchi/releases).

## Первый запуск (zero-config)

Без единой правки конфига сервис поднимается на `https://127.0.0.1:8080` с
самоподписанным TLS, всеми данными под `./data` (или `app.data_dir` из конфига) и в
заблокированном состоянии — синхронизация не начнётся, пока не задан Master Key.

При первом открытии Web UI (или первой CLI-команде, требующей ключ) — форма задаёт новый
пароль. При каждом следующем запуске — тот же пароль его разблокирует. Процесс,
запущенный без `MARCHI_MASTER_KEY` в окружении, стартует заблокированным и ждёт ручной
разблокировки через браузер.

## Конфигурация

`config.yaml` (путь передаётся флагом `--config`, по умолчанию `./config.yaml`) — все
поля опциональны, значения по умолчанию покрывают zero-config запуск. Переменные
окружения имеют приоритет над файлом. Самое важное:

| Ключ | По умолчанию | Назначение |
|---|---|---|
| `app.data_dir` | `./data` | Корень для БД, Maildir, индекса, TLS-сертификата, логов |
| `http.host` / `http.port` | `127.0.0.1` / `8080` | Адрес Web UI. В Docker нужно `0.0.0.0`, иначе порт не будет доступен снаружи контейнера |
| `http.tls.enabled` / `auto_cert` | `true` / `true` | Самоподписанный TLS, генерируется в `{data_dir}/tls` |
| `http.trusted_proxies` | не задано | IP/подсети reverse-proxy, чьему `X-Forwarded-For` можно доверять — см. [Работа за reverse-proxy](docs/REVERSE_PROXY.md) |
| `security.master_key_env` | `MARCHI_MASTER_KEY` | Имя переменной окружения для unattended-разблокировки |
| `app.log_output` | `both` | Куда пишутся логи: `file` (только `{data_dir}/logs`), `stdout` (только консоль — физически stderr, чтобы не засорять stdout вывод команд вроде `config show`; виден в `docker logs`/`journalctl -u marchi`), `both` |
| `sync.default_schedule` | `0 */6 * * *` | Cron-расписание автосинхронизации по умолчанию (переопределяется на уровне аккаунта) |
| `storage.cache.max_size_gb` | `10` | Лимит byte-budget LRU-кэша для ленивой загрузки писем из S3 |

`marchi config show` печатает итоговую конфигурацию (дефолты + файл + окружение) —
удобно проверить, что реально применилось.

## Резервное копирование и восстановление

Всё состояние — в `app.data_dir`:

```
data/
  marchi.db          # SQLite: аккаунты, правила, метаданные писем, логи
  marchi.db-wal       # WAL-журнал (может присутствовать)
  .salt / .mk-verify / .dek  # производные Master Key — без них данные не расшифровать
  maildir/            # оригинальные .eml по аккаунтам/папкам
  index/              # поисковый индекс Bluge (перестраивается через `marchi reindex`)
  tls/                # самоподписанный сертификат
  logs/               # ротируемые логи приложения
```

**Бэкап без остановки сервиса** (рекомендуемый способ): `marchi backup run <dest-dir>`
пишет консистентный снапшот работающего архива — SQLite через собственный Online Backup
API (не копия файла: WAL гарантирует, что чтение для бэкапа не блокирует и не блокируется
записью), плюс `maildir/` (в виде `maildir.tar.gz`) и `.salt`/`.mk-verify`/`.dek`. Ничего
не расшифровывается по пути — сохранённые пароли/токены остаются зашифрованными той же
самой обёрткой, что и в оригинале. `<dest-dir>` должен быть новым/пустым — команда не
перезаписывает существующий бэкап молча. Пароль Master Key для этого не нужен.

`marchi backup verify <dest-dir>` сверяет SHA-256 каждого файла (записаны в
`manifest.json` при создании) и прогоняет `PRAGMA integrity_check` на скопированной базе —
стоит выполнять сразу после `backup run`, а не только в момент реального восстановления.

**Восстановление**: остановленный сервис, пустой (или новый) `data_dir`, туда
распаковывается бэкап целиком (`maildir.tar.gz` — обратно в `maildir/`, `marchi.db` и
`.salt`/`.mk-verify`/`.dek` — как есть), сервис запускается — Master Key тот же, что был на
момент бэкапа. Если поисковый индекс не скопирован или подозревается в рассинхроне —
`marchi reindex` пересоберёт его из `maildir/` и `marchi.db` без обращения к сети.

**Ручной способ** (без CLI-команды, но так же надёжно): остановите сервис
(`systemctl stop marchi` / `docker compose down`) и скопируйте весь `data_dir` целиком —
`.salt`/`.mk-verify`/`.dek` обязательны, без них расшифровать `marchi.db` и `maildir`
невозможно даже зная пароль. В отличие от `marchi backup run`, копирование файлов на живую
здесь не поддерживается: SQLite WAL и запись `.eml`-файлов не атомарны относительно
простой файловой копии.

**Дополнительная защита** от потери исходных писем (не то же самое, что резервная копия
самого Marchi): пока включена S3-репликация, копия каждого письма уже лежит в S3
независимо от локального `data_dir`.

## Технологии

Go 1.25, Fiber (HTTP), `modernc.org/sqlite` (чистый Go, без CGO), Bluge (поиск),
`emersion/go-imap` + `emersion/go-message` (IMAP/MIME), `aws-sdk-go-v2` (S3),
`wneessen/go-mail` (SMTP-фолбэк восстановления), HTMX + Tailwind CSS (Web UI, без Node в
рантайме), `go-i18n` (RU/EN), Cobra (CLI), Prometheus client (метрики).

## Документация

- [User Guide](docs/USER_GUIDE.md) — пошаговое руководство пользователя.
- [OpenAPI-спецификация](docs/api/openapi.yaml) — REST API `/api/v1/*`.
- [Работа за reverse-proxy](docs/REVERSE_PROXY.md) — nginx/Traefik, TLS-offload,
  доверенные прокси-заголовки, ограничения по path.
- [SECURITY.md](SECURITY.md) — как сообщить об уязвимости.
- [CHANGELOG.md](CHANGELOG.md) — история релизов.

## Лицензия

[MIT](LICENSE). Лицензии зависимостей — [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
