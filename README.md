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
- **Правила архивации**: визуальный AND/OR-конструктор условий (тема, отправитель,
  домен, вложения, размер, дата, папка — 15 типов условий, вложенность до 3 уровней),
  действия `archive`/`skip`/`archive_and_delete`/`archive_and_mark_read`. Правила можно
  редактировать и в Web UI, и через `rules.yaml` (однонаправленная синхронизация файла в
  БД, отслеживается через fsnotify).
- **Полнотекстовый поиск**: индекс на [Bluge](https://github.com/blugelabs/bluge) по
  теме/телу/отправителю/вложениям, с фильтрами по дате/аккаунту/папке.
- **S3-репликация**: зеркалирование заархивированных писем в любое S3-совместимое
  хранилище (AWS S3, MinIO, ...) с клиентским шифрованием (AES-256-GCM) перед загрузкой.
- **Retention**: трёхстадийный жизненный цикл письма — локально → перенос в S3 →
  окончательное удаление, с настраиваемыми по умолчанию и переопределяемыми на уровне
  аккаунта порогами в днях.
- **Восстановление**: письмо (или пачка писем по результатам поиска) можно вернуть в
  любой IMAP-аккаунт через `APPEND`, с фолбэком на SMTP.
- **Web UI**: Dashboard, Accounts, Archive (поиск + просмотр письма + восстановление),
  Rules, Settings — тёмная/светлая тема, RU/EN-локализация.
- **CLI**: те же операции без браузера — `add-account`, `sync`, `status`, `retention run`,
  `reindex` и т.д. (`marchi --help`, `marchi --lang ru --help`).
- **Master Key**: единый пароль (Argon2id) шифрует Data Encryption Key, из которого
  выводятся отдельные подключи для IMAP-паролей, OAuth2-токенов и S3-объектов — смена
  пароля не требует перешифровывания уже сохранённых данных.
- **Метрики**: `/metrics` в формате Prometheus (счётчики писем, синхронизаций, очереди
  S3, HTTP-latency).

## Быстрый старт (Docker)

Единственная поддерживаемая платформа — Linux (amd64/arm64), запуск через Docker или
systemd.

```bash
git clone https://github.com/yurydemin/marchi.git
cd marchi
docker compose up
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
| `security.master_key_env` | `MARCHI_MASTER_KEY` | Имя переменной окружения для unattended-разблокировки |
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

**Бэкап**: остановите сервис (`systemctl stop marchi` / `docker compose down`) и
скопируйте весь `data_dir` целиком — `.salt`/`.mk-verify`/`.dek` обязательны, без них расшифровать
`marchi.db` и `maildir` невозможно даже зная пароль. Копирование "на живую" не
поддерживается официально: SQLite WAL и запись `.eml`-файлов не атомарны относительно
файловой копии.

**Восстановление**: остановленный сервис, пустой (или новый) `data_dir`, туда
распаковывается бэкап целиком, сервис запускается — Master Key тот же, что был на момент
бэкапа. Если поисковый индекс не скопирован или подозревается в рассинхроне —
`marchi reindex` пересоберёт его из `maildir/` и `marchi.db` без обращения к сети.

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
- [CHANGELOG.md](CHANGELOG.md) — история релизов.

## Лицензия

[MIT](LICENSE). Лицензии зависимостей — [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
