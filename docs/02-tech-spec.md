# Техническое задание: Marchi Go — Система архивации электронной почты

**Версия:** 2.1  
**Дата:** 2026-07-06 (обновлено 2026-07-18: язык реализации Go 1.22+ → Go 1.25+, см. ниже)  
**Статус:** Утверждено для разработки  
**Язык реализации:** Go 1.25+  
**Целевая платформа:** Linux, macOS, Windows (x86_64, ARM64)

---

## 1. Общие положения

### 1.1. Цель и область применения

Разработать self-hosted приложение для архивации электронной почты с произвольных IMAP-ящиков (Gmail, Yandex, Mail.ru, Microsoft 365, корпоративные серверы), с гибкой настройкой правил архивации, полнотекстовым поиском, восстановлением писем обратно в почтовые ящики через IMAP APPEND, а также асинхронной репликацией архива в S3-совместимое облачное хранилище.

Приложение представляет собой **единый бинарный файл** без внешних зависимостей. Целевой пользователь — частное лицо или малый бизнес.

### 1.2. Глоссарий

| Термин | Определение |
|--------|-------------|
| **Maildir** | Формат хранения писем «одно письмо — один файл .eml» по спецификации qmail. |
| **S3-совместимое хранилище** | Облако с API Amazon S3: AWS S3, MinIO, Wasabi, Yandex Object Storage, Selectel, Ceph. |
| **Hot Storage** | Локальное хранилище писем на диске пользователя. |
| **Warm Storage** | S3-хранилище для резервной копии. |
| **Lazy Load** | Подгрузка содержимого письма из S3 в локальный кэш только при запросе пользователя. |
| **Master Key** | Ключ AES-256-GCM, производный от пароля пользователя через Argon2id, для шифрования учётных данных и S3-объектов. |
| **UIDVALIDITY / UID** | IMAP-метаданные для инкрементальной синхронизации. |
| **Bluge** | Встраиваемый полнотекстовый поисковый движок на чистом Go. |
| **Retention Policy** | Правило, определяющее срок хранения письма локально перед вытеснением в S3 или удалением. |
| **Single Writer Pattern** | Все операции записи в SQLite выполняются через единый канал (одна goroutine-писатель) для предотвращения deadlock и WAL contention. |

### 1.3. Жёсткие ограничения

1. **Один бинарник**: приложение компилируется в один исполняемый файл без внешних зависимостей.
2. **SQLite единственная БД**: все метаданные хранятся в SQLite с включённым WAL mode.
3. **Maildir**: все письма хранятся в оригинальном виде (.eml) без модификации заголовков.
4. **IMAP4rev1 / SMTP**: базовые протоколы. Gmail API и Microsoft Graph — не используются в MVP.
5. **S3 — обязательный модуль**: система должна поддерживать S3-репликацию. Работа без S3 возможна только если S3 не сконфигурирован, но архитектура предполагает его наличие.

---

## 2. Архитектура системы

### 2.1. High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Web UI (HTTP/HTTPS)                      │
│              HTMX + Go html/template                        │
└──────────────────────────┬──────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────┐
│              Go Backend (Fiber + Cobra CLI)                   │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │ Account Mgr    │  │ Sync Engine  │  │ Rule Engine      │  │
│  │ - IMAP creds   │  │ - IMAP IDLE  │  │ - YAML/Web rules │  │
│  │ - OAuth2       │  │ - Incremental│  │ - Skip/Archive   │  │
│  │ - S3 creds     │  │ - Dedup      │  │ - Retention      │  │
│  └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘  │
│         │                 │                    │            │
│  ┌──────▼─────────────────▼────────────────────▼─────────┐  │
│  │              Storage Abstraction Layer               │  │
│  │  ┌──────────────┐  ┌─────────────┐  ┌───────────┐  │  │
│  │  │ SQLite (meta)│  │ Maildir     │  │ Bluge     │  │  │
│  │  │ - accounts   │  │ - .eml      │  │ - index   │  │  │
│  │  │ - folders    │  │ - hot       │  │ - content │  │  │
│  │  │ - uids       │  │   storage   │  │   + att   │  │  │
│  │  └──────────────┘  └──────┬──────┘  └───────────┘  │  │
│  │                           │                        │  │
│  │  ┌────────────────────────▼─────────────────────┐  │  │
│  │  │         S3 Async Uploader / Mirror           │  │  │
│  │  │  - Multipart upload                        │  │  │
│  │  │  - Client-side encryption (AES-256-GCM)    │  │  │
│  │  │  - Lazy Load Cache Manager                 │  │  │
│  │  └────────────────────────────────────────────┘  │  │
│  └──────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────┘
```

### 2.2. Компоненты системы

| Компонент | Технология | Назначение |
|-----------|-----------|------------|
| **HTTP-сервер** | `gofiber/fiber` | REST API, статические файлы UI, WebSocket для прогресса. |
| **CLI** | `spf13/cobra` | Управление: `add-account`, `sync`, `restore`, `config`, `reset-master-key`. |
| **IMAP-клиент** | `emersion/go-imap` | Инкрементальная синхронизация, IDLE, UIDPLUS, CONDSTORE. |
| **SMTP-клиент** | `go-mail/mail` | Fallback восстановления через SMTP, если IMAP APPEND не поддерживается. |
| **S3-клиент** | `aws-sdk-go-v2/service/s3` | Загрузка, скачивание, multipart, presigned URLs, S3-compatible endpoints. |
| **БД метаданных** | `modernc.org/sqlite` (чистый Go, без CGO) | Аккаунты, папки, UID-состояния, правила, журналы, очередь S3. |
| **Поиск** | `blugelabs/bluge` | Полнотекстовый индекс Subject, From, To, Cc, Body, имена вложений. |
| **Шифрование** | `golang.org/x/crypto` (Argon2id, AES-256-GCM) | Master Key derivation, шифрование credentials, client-side encryption для S3. |
| **HTML Sanitizer** | `microcosm-cc/bluemonday` | Очистка HTML писем перед рендерингом в Web UI. |
| **Кэш** | `hashicorp/golang-lru` | Локальный LRU-кэш для файлов, вытесненных в S3. |
| **Scheduler** | `robfig/cron` + `panjf2000/ants` (goroutine pool) | Фоновая синхронизация по расписанию, ограничение concurrency. |
| **Single Writer** | Канал Go + 1 goroutine | Все INSERT/UPDATE/DELETE в SQLite проходят через один канал. |

### 2.3. Потоки данных (Data Flow)

**Поток 1: Архивация (основной)**
1. Scheduler запускает Sync Engine для аккаунта через goroutine pool.
2. IMAP Client подключается, получает список папок.
3. Для каждой папки: сравниваем `UIDVALIDITY` с SQLite. Если изменился — полная ресинхронизация.
4. Запрашиваем UID-ы выше последнего заархивированного.
5. Rule Engine проверяет письмо (заголовки, размер, вложения). Если `skip` — пропускаем, UID не фиксируется.
6. Письмо записывается в локальный Maildir (`tmp/` → `new/`).
7. Bluge индексирует метаданные и содержимое (имена вложений и MIME-типы индексируются; содержимое вложений не индексируется).
8. Метаданные записываются в SQLite через Single Writer Pattern.
9. S3 Async Uploader ставит файл в очередь `s3_upload_queue` (SQLite).
10. Только после успешных шагов 6-8 UID фиксируется как заархивированный. Если ошибка на любом шаге — письмо не считается заархивированным, UID не обновляется, транзакция откатывается.

**Поток 2: Просмотр письма**
1. Web UI запрашивает письмо по ID.
2. Backend проверяет SQLite: `storage_location = local | s3`.
3. Если `local` — читаем .eml из Maildir, отдаём.
4. Если `s3` — проверяем LRU-кэш. Если есть — отдаём. Если нет — скачиваем из S3 в кэш, расшифровываем (если включено шифрование), затем отдаём. TTL кэша — 30 дней или до вытеснения по LRU.

**Поток 3: Восстановление**
1. Пользователь выбирает письмо(а) и целевой ящик.
2. Backend проверяет локальное наличие. Если нет — lazy load из S3 в кэш (приоритетный запрос, не вытесняет другие кэшированные файлы).
3. IMAP Client подключается к целевому ящику.
4. Выполняется `IMAP APPEND` в целевую папку с сохранением оригинальных флагов и даты (`INTERNALDATE`).
5. Если APPEND не поддерживается — fallback на SMTP пересылку.
6. Результат записывается в `restore_logs`.

**Поток 4: Retention (фоновый)**
1. Cron-задача `retention` запускается ежедневно в 03:00.
2. Для каждого письма, у которого `retention_local_days` истёк и `storage_location = local`:
   - Загружаем в S3 (если ещё не загружено).
   - Удаляем локальный .eml.
   - Обновляем `storage_location = s3` через Single Writer.
3. Для каждого письма, у которого `retention_s3_days` истёк: удаляем из S3 и из SQLite (каскадно).

---

## 3. Функциональные требования

### 3.1. Управление учётными записями (Account Management)

**FR-AM-01:** Поддержка добавления произвольного IMAP-аккаунта с указанием:
- Email-адреса (уникальный идентификатор)
- IMAP-сервер, порт, SSL/TLS или STARTTLS
- Аутентификация: plain password (для корпоративных IMAP) или OAuth2 (для Gmail и Microsoft 365)
- Произвольное отображаемое имя

**FR-AM-02:** Хранение credentials в SQLite в зашифрованном виде (AES-256-GCM) с использованием Master Key.

**FR-AM-03:** Поддержка неограниченного количества аккаунтов (лимит — ресурсы железа).

**FR-AM-04:** Проверка подключения (Test Connection) при добавлении/редактировании аккаунта. При ошибке — вывод детального сообщения (неверный пароль, нет IMAP, SSL-ошибка).

**FR-AM-05:** Отключение аккаунта без удаления архива (пауза синхронизации). Флаг `is_active = 0`.

**FR-AM-06:** Удаление аккаунта с каскадным удалением заархивированных писем, метаданных, записей в Bluge и S3-объектов. Требуется подтверждение пользователя.

### 3.2. Синхронизация и архивация (Sync Engine)

**FR-SE-01:** Инкрементальная синхронизация по UID. Система отслеживает `UIDVALIDITY` и `LAST_UID` для каждой папки каждого аккаунта в SQLite.

**FR-SE-02:** При изменении `UIDVALIDITY` — автоматическая полная ресинхронизация папки с дедупликацией по `Message-ID` + `Date` + `From` (если письмо уже есть в SQLite — пропускаем запись, но добавляем ссылку на новую папку/аккаунт).

**FR-SE-03:** Поддержка выбора папок для архивации. По умолчанию архивируются все папки кроме `Trash`, `Spam`, `Junk`. Список исключаемых папок настраивается per-account.

**FR-SE-04:** Сохранение оригинального .eml без модификации (RFC 5322).

**FR-SE-05:** Извлечение и индексация вложений: имена файлов, MIME-типы, размеры. Содержимое вложений не индексируется.

**FR-SE-06:** Фоновая синхронизация по расписанию (cron-выражение per account, по умолчанию `0 */6 * * *`) и ручной запуск через Web UI или CLI.

**FR-SE-07:** WebSocket-прогресс для длительных операций синхронизации: текущий UID, всего писем, скорость (писем/мин), ошибки.

**FR-SE-08:** Graceful degradation: если IMAP-сервер не поддерживает IDLE — используется polling с интервалом 60 секунд.

### 3.3. Правила архивации (Archive Rules)

**FR-RE-01:** Движок правил применяется на этапе получения письма (до записи в хранилище). Правила хранятся в SQLite.

**FR-RE-02:** Условия (AND/OR логика, вложенность до 3 уровней):
- `from_contains` (regex)
- `from_domain` (string)
- `from_exact` (string)
- `to_contains` (regex)
- `to_domain` (string)
- `subject_contains` (regex)
- `has_attachments` (boolean)
- `attachment_type` (MIME, например `application/pdf`)
- `size_greater_than` (bytes)
- `size_less_than` (bytes)
- `date_after` (ISO 8601)
- `date_before` (ISO 8601)
- `folder_is` (string)
- `folder_is_not` (string)
- `account_is` (account_id)

**FR-RE-03:** Действия (выполняется первое совпавшее правило, сортировка по `priority`):
- `archive` (записать в хранилище)
- `skip` (не архивировать)
- `archive_and_delete` (архивировать и удалить с сервера источника)
- `archive_and_mark_read` (архивировать и пометить прочитанным на сервере)

**FR-RE-04:** Retention Policies (правила хранения) — отдельный блок настроек per rule:
- `keep_local_days`: сколько дней письмо остаётся в локальном Maildir. По умолчанию 365. `0` — вытеснять сразу в S3. `null` — хранить локально всегда.
- `move_to_s3_after_days`: после истечения срока — вытеснение в S3 (удаление локального .eml, флаг `storage_location = s3`).
- `delete_from_s3_after_days`: полное удаление из S3 и SQLite. По умолчанию 2555 (7 лет). `null` — не удалять.

**FR-RE-05:** Управление правилами через Web UI (визуальный конструктор) и через YAML-файл `rules.yaml`. При изменении `rules.yaml` система перезагружает правила без перезапуска (через fsnotify или по таймеру раз в 30 секунд).

### 3.4. Хранилище данных (Storage Layer)

**FR-ST-01:** Локальное хранилище — Maildir:
```
{data_dir}/
  accounts/
    {account_id}/
      mail/
        {folder_safe_name}/
          cur/
          new/
          tmp/
```
Имена файлов: `{unix_time}.{pid}_{counter}.{host}:2,{flags}` (например, `1720123456.12345_1.local:2,S`).

**FR-ST-02:** SQLite — единственная база данных. Включён WAL mode (`PRAGMA journal_mode = WAL`). Все записи выполняются через Single Writer Pattern (одна goroutine-писатель, остальные отправляют запросы через канал).

**FR-ST-03:** SQLite-схема обязана включать таблицы:
- `accounts` (id, email, display_name, imap_host, imap_port, imap_tls, imap_username, imap_password_encrypted, oauth2_provider, oauth2_token_encrypted, is_active, created_at, updated_at)
- `folders` (id, account_id, folder_name, uidvalidity, last_uid, sync_enabled, UNIQUE(account_id, folder_name))
- `emails` (id, message_id, account_id, folder_id, uid, subject, from_addr, to_addrs, cc_addrs, date, size, has_attachments, flags, storage_location, local_path, s3_key, s3_etag, s3_sha256, created_at, updated_at, UNIQUE(account_id, folder_id, uid))
- `attachments` (id, email_id, filename, mime_type, size, content_id, local_path, s3_key, created_at)
- `rules` (id, name, priority, conditions_json, action, retention_local_days, retention_s3_days, is_active, created_at)
- `sync_logs` (id, account_id, started_at, ended_at, emails_processed, emails_archived, emails_skipped, bytes_downloaded, errors, status, error_msg)
- `restore_logs` (id, email_id, target_account_id, target_folder, method, status, error_msg, created_at)
- `s3_upload_queue` (id, email_id, attachment_id, s3_key, local_path, status, retry_count, error_msg, created_at, updated_at)

**FR-ST-04:** Master Key: при первом запуске система требует от пользователя ввода пароля (минимум 12 символов). От пароля производится ключ через Argon2id (salt генерируется случайно и хранится в `{data_dir}/.salt`). Master Key не хранится на диске. При перезапуске — повторный ввод пароля. Для unattended-запуска допускается переменная окружения `MAILVAULT_MASTER_KEY` с предупреждением в логах.

**FR-ST-05:** Master Key используется для:
- Шифрования IMAP-паролей и OAuth2 токенов перед записью в SQLite.
- Client-side encryption файлов перед загрузкой в S3.
- Master Key не передаётся в S3. Потеря Master Key = полная потеря доступа к зашифрованным данным в S3.

### 3.5. Полнотекстовый поиск (Search Engine)

**FR-SR-01:** Используется Bluge. Индекс хранится в `{data_dir}/index/`. Индекс не реплицируется в S3. При восстановлении из бэкапа — индекс перестраивается через полную переиндексацию всех локальных .eml.

**FR-SR-02:** Индексация полей:
- `message_id` (keyword, exact match)
- `subject` (text, analyzed, fuzzy)
- `from` (text + keyword)
- `to`, `cc` (text + keyword)
- `body` (text, analyzed, fuzzy) — только text/plain и text/html без тегов
- `attachment_names` (keyword)
- `date` (datetime)
- `account_id`, `folder_id` (keyword, filter)
- `has_attachments` (boolean)
- `size` (numeric)

**FR-SR-03:** Поисковые возможности:
- Простой текстовый поиск по всем проиндексированным полям.
- Фильтры: дата (from/to), отправитель, получатель, наличие вложений, аккаунт, папка.
- Сортировка: по релевантности (default), по дате (asc/desc).
- Пагинация: offset/limit, default limit = 50, max limit = 500.

**FR-SR-04:** Переиндексация: API endpoint `POST /api/v1/admin/reindex` и CLI команда `reindex`. Удаляет текущий индекс Bluge, перечитывает все локальные .eml и пересоздаёт индекс. WebSocket-прогресс.

### 3.6. Просмотр и экспорт (Viewer & Export)

**FR-VW-01:** Просмотр письма в Web UI:
- Заголовки: From, To, Cc, Subject, Date, Message-ID.
- Тело письма: HTML очищается через `bluemonday` (удаление script, onclick, внешних ресурсов, data URI). Fallback на text/plain.
- Список вложений с возможностью скачивания.
- Навигация: prev/next в контексте текущего поиска/папки.

**FR-VW-02:** Скачивание отдельного письма в формате .eml (оригинал, без изменений).

**FR-VW-03:** Bulk-экспорт:
- Выбор нескольких писем или результата поиска.
- Формат: `.zip` с сохранением структуры папок (`{account_email}/{folder_name}/{YYYY-MM}/{Subject}-{MessageID}.eml`).
- Экспорт в `.mbox` не поддерживается в MVP.

### 3.7. Восстановление (Restore Engine)

**FR-RS-01:** Восстановление одиночного или множественного выбора писем в указанный ящик и папку.

**FR-RS-02:** Метод восстановления:
- **Primary:** IMAP `APPEND` с оригинальным `INTERNALDATE` и флагами (`\Seen`, `\Answered` и т.д.).
- **Fallback:** SMTP отправка на адрес ящика (с сохранением оригинальных заголовков, но `INTERNALDATE` будет текущим).

**FR-RS-03:** Перед восстановлением письмо должно быть доступно локально. Если оно вытеснено в S3 — автоматический lazy load в локальный кэш (приоритетный запрос, не вытесняет существующий кэш).

**FR-RS-04:** Журналирование всех операций восстановления в `restore_logs`.

**FR-RS-05:** Восстановление выполняется с оригинальным `Message-ID`. Если целевой сервер отклоняет дублирующийся Message-ID — операция помечается как failed, ошибка записывается в `restore_logs`.

### 3.8. Облачное хранилище S3 (Cloud Storage)

**FR-S3-01:** S3 — обязательный модуль для конфигурации. Если S3 не настроен — система работает в локальном режиме, но UI должен показывать предупреждение о отсутствии резервной копии.

**FR-S3-02:** Конфигурация S3-подключения:
- Endpoint (произвольный)
- Region
- Bucket name
- Access Key / Secret Key (шифруются Master Key)
- Path style (true для MinIO/CEPH, false для AWS/Yandex)
- TLS (skip verify — boolean для self-signed)
- Storage Class (STANDARD, STANDARD_IA, GLACIER)

**FR-S3-03:** Режим работы: **Mirror** (единственный режим в MVP). Каждый заархивированный .eml асинхронно загружается в S3. Локальная копия остаётся до срабатывания Retention Policy. S3 — disaster recovery + вытеснение по retention.

**FR-S3-04:** Object Layout в S3:
```
s3://{bucket}/marchi/v1/accounts/{account_id}/emails/{yyyy}/{mm}/{dd}/{sha256[:2]}/{sha256}.eml
s3://{bucket}/marchi/v1/accounts/{account_id}/attachments/{email_sha256}/{filename}
```
`sha256` — SHA-256 от оригинального содержимого .eml (до шифрования). Это гарантирует дедупликацию на уровне S3.

**FR-S3-05:** Client-side encryption: перед загрузкой в S3 каждый .eml шифруется AES-256-GCM с использованием Master Key (или производного ключа через HKDF). В S3 хранится ciphertext. Метаданные `x-amz-meta-marchi-iv` и `x-amz-meta-marchi-tag` содержат IV и auth tag.

**FR-S3-06:** Асинхронная загрузка:
- Очередь на основе SQLite (`s3_upload_queue`).
- Worker pool (4 горутины по умолчанию, настраивается).
- Retry с экспоненциальным backoff (base 2, max 5 попыток, max delay 1 час).
- WebSocket-уведомления об ошибках загрузки.
- При успешной загрузке: обновление `s3_etag`, `s3_sha256`, удаление записи из очереди.

**FR-S3-07:** Lazy Load Cache:
- LRU-кэш с ограничением по размеру (default 10 GB, настраивается).
- При запросе письма: проверка кэша → скачивание из S3 → расшифровка → запись в кэш → отдача.
- При bulk-экспорте: последовательное скачивание, concurrency = 2 (чтобы не исчерпать RAM).

**FR-S3-08:** Проверка целостности: при загрузке в S3 вычисляется SHA-256 от оригинального содержимого. Сохраняется в `s3_sha256` (SQLite) и `x-amz-meta-marchi-sha256`. При скачивании — верификация SHA-256 после расшифровки.

**FR-S3-09:** Удаление из S3: при удалении письма из архива (ручное или по retention) — каскадное удаление из S3 через очередь `s3_upload_queue` (статус `pending_delete`).

### 3.9. Веб-интерфейс (Web UI)

**FR-WU-01:** Web UI реализован на HTMX + Go `html/template` + Tailwind CSS.

**FR-WU-02:** Обязательные экраны:
- **Dashboard:** статистика (всего писем, по аккаунтам, объём локальный/S3, статус последней синхронизации, размер очереди S3).
- **Accounts:** список, добавление, редактирование, тест подключения, включение/выключение синхронизации.
- **Rules:** визуальный конструктор правил (формы с AND/OR, drag-and-drop приоритета).
- **Archive:** дерево папок, поиск, таблица писем, просмотр письма.
- **Restore:** выбор писем через поиск, выбор целевого аккаунта/папки, прогресс восстановления.
- **Settings:** смена Master Key, S3 config, глобальные retention defaults, логи синхронизации, кнопка переиндексации.

**FR-WU-03:** Адаптивная вёрстка: корректное отображение на экранах от 320px до 4K.

**FR-WU-04:** Поддержка тёмной и светлой темы (переключатель, сохранение в localStorage).

**FR-WU-05:** Локализация: русский и английский языки. Фреймворк i18n — `nicksnyder/go-i18n`. Язык определяется по заголовку `Accept-Language`, с возможностью принудительного выбора.

### 3.10. API

**FR-API-01:** REST API с JSON. Версионирование: `/api/v1/`.

**FR-API-02:** Обязательные эндпоинты:
- `GET /api/v1/accounts` — список
- `POST /api/v1/accounts` — создание
- `PUT /api/v1/accounts/{id}` — обновление
- `DELETE /api/v1/accounts/{id}` — удаление
- `POST /api/v1/accounts/{id}/test` — проверка подключения
- `POST /api/v1/accounts/{id}/sync` — ручной запуск синхронизации
- `GET /api/v1/accounts/{id}/folders` — список папок
- `GET /api/v1/accounts/{id}/sync-status` — статус последней синхронизации
- `GET /api/v1/rules` — список правил
- `POST /api/v1/rules` — создание
- `PUT /api/v1/rules/{id}` — обновление
- `DELETE /api/v1/rules/{id}` — удаление
- `GET /api/v1/search?q=...&from=...&to=...&account_id=...&folder_id=...&has_attachments=...&offset=...&limit=...` — поиск
- `GET /api/v1/emails/{id}` — метаданные + body preview
- `GET /api/v1/emails/{id}/download` — скачать .eml
- `GET /api/v1/emails/{id}/attachments/{att_id}/download` — скачать вложение
- `POST /api/v1/restore` — body: `{email_ids: [], target_account_id, target_folder}`
- `POST /api/v1/export` — body: `{email_ids: []}`, возвращает `job_id`
- `GET /api/v1/jobs/{job_id}/status` — статус долгой операции (export, restore, reindex)
- `POST /api/v1/admin/reindex` — запуск переиндексации
- `GET /api/v1/stats` — общая статистика
- `GET /api/v1/logs/sync` — журналы синхронизации (пагинация)
- `GET /api/v1/logs/restore` — журналы восстановления
- `GET /api/v1/s3-queue` — статус очереди загрузки в S3

**FR-API-03:** WebSocket endpoint `/ws` для real-time прогресса: синхронизация, восстановление, экспорт, переиндексация. Формат сообщений — JSON с полями `type`, `job_id`, `progress_percent`, `message`.

**FR-API-04:** Prometheus metrics endpoint `/metrics` (без авторизации, но только на localhost):
- `marchi_emails_total`
- `marchi_storage_local_bytes`
- `marchi_storage_s3_bytes`
- `marchi_sync_duration_seconds`
- `marchi_s3_upload_queue_size`
- `marchi_s3_upload_failed_total`

---

## 4. Нефункциональные требования

### 4.1. Производительность

**NFR-PF-01:** Скорость синхронизации: не менее 50 писем/сек на канале 100 Мбит/с (для писем средним размером 50 КБ).

**NFR-PF-02:** Время отклика поиска: < 200 мс для архива до 100 000 писем на SSD; < 1 сек для 1 000 000 писем.

**NFR-PF-03:** Время открытия письма в Web UI: < 100 мс для локальных писем; < 3 сек для писем из S3 (с учётом lazy load + расшифровка).

**NFR-PF-04:** Потребление RAM: < 512 МБ в простое; < 2 ГБ при активной синхронизации 10 аккаунтов.

**NFR-PF-05:** SQLite WAL mode + Single Writer Pattern обеспечивают корректную работу при параллельной синхронизации до 10 аккаунтов. При превышении — очередь записи не должна приводить к таймаутам (> 5 сек на операцию).

### 4.2. Надёжность и отказоустойчивость

**NFR-RL-01:** Система работает без доступа к интернету при отключенном S3 (локальный режим). При настроенном S3 — offline работа возможна, но с предупреждением о невозможности загрузки в облако.

**NFR-RL-02:** При обрыве соединения в процессе синхронизации — докачка с места обрыва (по UID) без потери данных.

**NFR-RL-03:** Атомарность записи: письмо считается заархивированным только после успешной записи .eml, SQLite и Bluge. Если ошибка на любом шаге — UID не фиксируется, повторная синхронизация загрузит письмо заново.

**NFR-RL-04:** Ротация текстовых логов: `{data_dir}/logs/marchi-{YYYY-MM-DD}.log`, хранение последних 30 дней, максимальный размер файла 100 МБ.

**NFR-RL-05:** Graceful shutdown: при SIGINT/SIGTERM — завершение текущих операций записи (flush SQLite WAL, закрытие Bluge index, закрытие IMAP IDLE), таймаут 30 секунд. Если не завершилось — force exit.

### 4.3. Безопасность

**NFR-SC-01:** Master Key не хранится на диске в открытом виде. При запуске — интерактивный запрос пароля (CLI) или ввод через Web UI. Переменная окружения `MAILVAULT_MASTER_KEY` допустима только с предупреждением `SECURITY WARNING` в логах.

**NFR-SC-02:** Все sensitive-данные (IMAP пароли, OAuth2 токены, S3 keys) шифруются AES-256-GCM перед записью в SQLite.

**NFR-SC-03:** Client-side encryption для S3 обязательна. Данные шифруются перед отправкой. Ключ — Master Key (производный через HKDF). Потеря Master Key = полная потеря доступа к данным в S3. Система выводит предупреждение при первой настройке S3.

**NFR-SC-04:** HTTPS для Web UI: автогенерация self-signed сертификата при первом запуске. Пользователь может заменить его на свой через конфигурацию (`tls.cert_file`, `tls.key_file`).

**NFR-SC-05:** XSS-защита: обязательная санитизация HTML писем через `bluemonday` перед рендерингом. CSP-заголовок: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:;`.

**NFR-SC-06:** CSRF-токены для всех POST/PUT/DELETE операций Web UI.

**NFR-SC-07:** Rate limiting: 100 req/min для auth-эндпоинтов, 1000 req/min для остального API. Реализуется через `gofiber/fiber` middleware.

### 4.4. Удобство развёртывания

**NFR-DP-01:** Один бинарный файл для каждой платформы: Linux amd64/arm64, macOS amd64/arm64, Windows amd64.

**NFR-DP-02:** Zero-config запуск: `./marchi` создаёт `{cwd}/data/` и запускает веб-интерфейс на `localhost:8080`.

**NFR-DP-03:** Конфигурация через `config.yaml` или переменные окружения (`MAILVAULT_DATA_DIR`, `MAILVAULT_HTTP_PORT`, `MAILVAULT_LOG_LEVEL`). Переменные окружения имеют приоритет над YAML.

**NFR-DP-04:** Systemd unit файл в комплекте (`marchi.service`). Windows Service — команда `marchi service install`.

**NFR-DP-05:** Docker-файл и docker-compose.yml — для пользователей, предпочитающих контейнеризацию. Docker не является обязательным способом запуска.

---

## 5. Требования к данным

### 5.1. Формат хранения писем

- **Raw format:** RFC 5322 (.eml) — без изменений.
- **Локальная структура:**
  ```
  {data_dir}/
    accounts/
      {account_id}/
        mail/
          {folder_safe_name}/
            cur/
            new/
            tmp/
  ```
- **Именование файлов:** `{unix_time}.{pid}_{counter}.{host}:2,{flags}` (Maildir spec). Пример: `1720123456.12345_1.localhost:2,S`
- **Дедупликация:** локальная дедупликация не выполняется. Каждый аккаунт/папка имеет собственный файл. Дедупликация выполняется только на уровне S3 (по SHA-256).

### 5.2. Схема базы данных (SQLite)

```sql
-- accounts
CREATE TABLE accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL UNIQUE,
    display_name TEXT,
    imap_host TEXT NOT NULL,
    imap_port INTEGER NOT NULL DEFAULT 993,
    imap_tls INTEGER NOT NULL DEFAULT 1, -- 0=none, 1=ssl, 2=starttls
    imap_username TEXT,
    imap_password_encrypted BLOB, -- AES-GCM
    oauth2_provider TEXT, -- google, microsoft, null
    oauth2_token_encrypted BLOB,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- folders
CREATE TABLE folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    folder_name TEXT NOT NULL, -- IMAP UTF-7 decoded
    uidvalidity INTEGER NOT NULL DEFAULT 0,
    last_uid INTEGER NOT NULL DEFAULT 0,
    sync_enabled INTEGER NOT NULL DEFAULT 1,
    UNIQUE(account_id, folder_name)
);

-- emails
CREATE TABLE emails (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    uid INTEGER NOT NULL,
    subject TEXT,
    from_addr TEXT,
    to_addrs TEXT, -- JSON array
    cc_addrs TEXT, -- JSON array
    date DATETIME,
    size INTEGER NOT NULL DEFAULT 0,
    has_attachments INTEGER NOT NULL DEFAULT 0,
    flags TEXT, -- JSON array of IMAP flags
    storage_location TEXT NOT NULL DEFAULT 'local', -- local, s3
    local_path TEXT,
    s3_key TEXT,
    s3_etag TEXT,
    s3_sha256 TEXT, -- SHA-256 оригинального содержимого (до шифрования)
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(account_id, folder_id, uid)
);

-- attachments
CREATE TABLE attachments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER NOT NULL REFERENCES emails(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    mime_type TEXT,
    size INTEGER,
    content_id TEXT,
    local_path TEXT,
    s3_key TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- rules
CREATE TABLE rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    conditions_json TEXT NOT NULL, -- serialized rule tree
    action TEXT NOT NULL DEFAULT 'archive', -- archive, skip, archive_and_delete, archive_and_mark_read
    retention_local_days INTEGER, -- null = forever
    retention_s3_days INTEGER, -- null = forever
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- s3_upload_queue
CREATE TABLE s3_upload_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER REFERENCES emails(id) ON DELETE CASCADE,
    attachment_id INTEGER REFERENCES attachments(id) ON DELETE CASCADE,
    s3_key TEXT NOT NULL,
    local_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending', -- pending, uploading, done, failed, pending_delete
    retry_count INTEGER NOT NULL DEFAULT 0,
    error_msg TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    CHECK (email_id IS NOT NULL OR attachment_id IS NOT NULL)
);

-- sync_logs
CREATE TABLE sync_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    started_at DATETIME NOT NULL,
    ended_at DATETIME,
    emails_processed INTEGER NOT NULL DEFAULT 0,
    emails_archived INTEGER NOT NULL DEFAULT 0,
    emails_skipped INTEGER NOT NULL DEFAULT 0,
    bytes_downloaded INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'running', -- running, completed, failed, cancelled
    error_msg TEXT
);

-- restore_logs
CREATE TABLE restore_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER NOT NULL REFERENCES emails(id),
    target_account_id INTEGER NOT NULL REFERENCES accounts(id),
    target_folder TEXT NOT NULL,
    method TEXT NOT NULL DEFAULT 'imap_append', -- imap_append, smtp
    status TEXT NOT NULL DEFAULT 'pending',
    error_msg TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Индексы
CREATE INDEX idx_emails_account ON emails(account_id);
CREATE INDEX idx_emails_folder ON emails(folder_id);
CREATE INDEX idx_emails_date ON emails(date);
CREATE INDEX idx_emails_message_id ON emails(message_id);
CREATE INDEX idx_emails_storage ON emails(storage_location);
CREATE INDEX idx_s3_queue_status ON s3_upload_queue(status);
CREATE INDEX idx_sync_logs_account ON sync_logs(account_id);
CREATE INDEX idx_sync_logs_status ON sync_logs(status);
CREATE INDEX idx_restore_logs_email ON restore_logs(email_id);
```

### 5.3. Индексы и поисковая схема (Bluge)

Bluge Document Mapping (один документ на письмо):
```go
doc := bluge.NewDocument(emailID).
    AddField(bluge.NewKeywordField("message_id", msgID).StoreValue().Aggregatable()).
    AddField(bluge.NewTextField("subject", subject).StoreValue().SearchTermPositions().HighlightMatches()).
    AddField(bluge.NewTextField("from", from).StoreValue()).
    AddField(bluge.NewKeywordField("from_keyword", from).StoreValue().Aggregatable()).
    AddField(bluge.NewTextField("to", to).StoreValue()).
    AddField(bluge.NewTextField("cc", cc).StoreValue()).
    AddField(bluge.NewTextField("body", plainBody).StoreValue().SearchTermPositions()).
    AddField(bluge.NewKeywordField("attachment_names", strings.Join(attNames, " ")).StoreValue()).
    AddField(bluge.NewDateTimeRangeField("date", date, date).StoreValue().Aggregatable()).
    AddField(bluge.NewKeywordField("account_id", accountID).StoreValue().Aggregatable()).
    AddField(bluge.NewKeywordField("folder_id", folderID).StoreValue().Aggregatable()).
    AddField(bluge.NewKeywordField("has_attachments", hasAtt).StoreValue().Aggregatable()).
    AddField(bluge.NewNumericField("size", size).StoreValue().Aggregatable())
```

### 5.4. S3 Object Layout

```
s3://{bucket}/
  marchi/
    v1/
      accounts/{account_id}/
        emails/
          {yyyy}/
            {mm}/
              {dd}/
                {sha256[:2]}/
                  {sha256}.eml
        attachments/
          {email_sha256}/
            {filename}
      metadata/
        manifest.json -- список аккаунтов, версия формата, дата создания
```

**Правила именования:**
- `sha256` — SHA-256 от оригинального содержимого .eml (до client-side encryption).
- `{email_sha256}` — SHA-256 родительского письма для группировки вложений.
- Файлы в S3 — ciphertext (AES-256-GCM). Метаданные объекта: `x-amz-meta-marchi-iv`, `x-amz-meta-marchi-tag`, `x-amz-meta-marchi-sha256`.

---

## 6. План разработки

### 6.1. Фаза 1: Core Engine (MVP) — Недели 1-3
**Цель:** Работающий CLI для архивации.

- [ ] Проектная структура Go: `cmd/`, `internal/{account,sync,storage,config,api,crypto}`.
- [ ] Master Key и шифрование: Argon2id + AES-256-GCM.
- [ ] SQLite-схема и миграции (`golang-migrate/migrate`).
- [ ] Single Writer Pattern для SQLite.
- [ ] IMAP Client: подключение, список папок, инкрементальная синхронизация UID.
- [ ] Maildir writer.
- [ ] CLI: `add-account`, `list-accounts`, `sync`, `status`, `logs`.
- [ ] Логирование в файл (`uber-go/zap`).
- **Критерий приёма:** `marchi add-account` + `marchi sync` создаёт локальную папку с .eml файлами, SQLite содержит метаданные, WAL mode работает.

### 6.2. Фаза 2: Web UI и Поиск — Недели 4-6
**Цель:** Веб-интерфейс и полнотекстовый поиск.

- [ ] HTTP-сервер на Fiber, REST API v1.
- [ ] Web UI на HTMX + Tailwind + Go templates.
- [ ] Bluge: индексация при синхронизации, поисковый API.
- [ ] Просмотр письма: заголовки, HTML через `bluemonday`, text/plain fallback.
- [ ] Скачивание .eml и вложений.
- [ ] Dashboard со статистикой.
- [ ] WebSocket для прогресса sync.
- **Критерий приёма:** Пользователь открывает браузер, видит список писем, выполняет поиск, скачивает .eml.

### 6.3. Фаза 3: Правила, S3 и Восстановление — Недели 7-10
**Цель:** Полноценный продукт с облаком и восстановлением.

- [ ] Rule Engine: парсер YAML, валидация, применение при sync.
- [ ] Retention policies: cron-задача для вытеснения старых писем в S3.
- [ ] S3 Client: конфигурация, multipart upload, очередь, retry, lazy load cache.
- [ ] Client-side encryption для S3.
- [ ] Restore Engine: IMAP APPEND + fallback SMTP.
- [ ] Bulk-экспорт в .zip.
- [ ] OAuth2 для Gmail и Microsoft 365.
- [ ] Настройки S3 в Web UI.
- **Критерий приёма:** Пользователь настраивает правило, подключает Yandex S3, видит зеркалирование, восстанавливает письмо обратно в ящик.

### 6.4. Фаза 4: Полировка и Релиз — Недели 11-12
**Цель:** Production-ready.

- [ ] Prometheus metrics endpoint.
- [ ] Systemd/Windows Service wrapper.
- [ ] Docker-файл.
- [ ] Локализация (ru/en).
- [ ] Тёмная/светлая тема.
- [ ] Тесты: unit (>70% coverage), integration (mock IMAP), e2e (Playwright).
- [ ] Документация: README, API docs (OpenAPI/Swagger), User Guide.
- [ ] CI/CD: GitHub Actions (build, test, release binaries для 6 платформ).
- **Критерий приёма:** Релиз v1.0.0 с бинарниками, тестами, документацией.

---

## 7. Требования к команде разработки (Агенты)

### 7.1. Компетенции и ответственность

| Компетенция | Разделы ТЗ | Субагент |
|-------------|-----------|-------------|
| **Go-разработчик** | 2. Архитектура, 3.1 Account Management, 3.2 Sync Engine, 3.3 Rules Engine, 3.4 Storage Layer, 3.5 Search Engine, 3.7 Restore Engine, 3.8 S3 Cloud Storage, 3.10 API, 4. Нефункциональные требования, 5. Требования к данным, 6. План разработки | golang-pro, backend-developer |
| **Специалист по протоколам электронной почты** | 3.1 Account Management (OAuth2, IMAP), 3.2 Sync Engine (IMAP logic), 3.7 Restore Engine (IMAP APPEND, SMTP), 1.2 Глоссарий | backend-developer |
| **Frontend-разработчик** | 3.9 Web UI, 3.6 Viewer & Export, 3.10 API (WebSocket интеграция), 4.3 Безопасность (CSP, CSRF) | frontend-developer |
| **DevOps / SRE** | 4.5 Удобство развёртывания, 6.4 Фаза 4 (CI/CD, релиз), 2.2 Компоненты (cross-compilation) | devops-engineer, sre-engineer, deployment-engineer, build-engineer |
| **Специалист по информационной безопасности** | 3.4 Storage Layer (Master Key, шифрование), 3.8 S3 (client-side encryption), 4.3 Безопасность, NFR-SC | security-engineer |
| **QA / Инженер по тестированию** | 6.4 Фаза 4 (тесты), 3.2 Sync Engine (mock IMAP), 3.10 API (integration tests), 4. Нефункциональные (нагрузка) | qa-expert, test-automator |

---

## 8. Приложения

### 8.1. Пример конфигурации (config.yaml)

```yaml
app:
  data_dir: "./data"
  log_level: "info"
  log_format: "json"

http:
  host: "0.0.0.0"
  port: 8080
  tls:
    enabled: true
    auto_cert: true
    # cert_file: "/path/to/cert.pem"
    # key_file: "/path/to/key.pem"

security:
  master_key_env: "MAILVAULT_MASTER_KEY"
  argon2:
    memory: 65536
    iterations: 3
    parallelism: 4

database:
  type: "sqlite"
  sqlite:
    path: "./data/marchi.db"

search:
  index_path: "./data/index"

storage:
  maildir_path: "./data/maildir"
  cache:
    enabled: true
    max_size_gb: 10
    path: "./data/cache"

s3:
  enabled: false
  endpoint: "https://s3.yandexcloud.net"
  region: "ru-central1"
  bucket: "my-mail-archive"
  access_key_encrypted: ""
  secret_key_encrypted: ""
  path_style: false
  storage_class: "STANDARD"
  encryption:
    enabled: true
    algorithm: "AES-256-GCM"
  upload_workers: 4

sync:
  default_schedule: "0 */6 * * *"
  max_concurrent_accounts: 5
```

### 8.2. ER-диаграмма (текстовая, Crow's Foot)

```
accounts ||--o{ folders : "has many"
accounts ||--o{ emails : "has many"
accounts ||--o{ sync_logs : "has many"
folders ||--o{ emails : "contains"
emails ||--o{ attachments : "has many"
emails ||--o{ s3_upload_queue : "queued for"
emails ||--o{ restore_logs : "restored via"
accounts ||--o{ restore_logs : "target for"
```

---

## 9. Проверка на логические несостыковки (Self-Check)

| Проверка | Статус | Комментарий |
|----------|--------|-------------|
| **S3 vs Offline** | ✅ OK | FR-S3-01: S3 конфигурируется, но если не настроен — локальный режим. NFR-RL-01: offline работа возможна. Нет противоречия. |
| **SQLite vs Concurrency** | ✅ OK | FR-ST-02: Single Writer Pattern + WAL mode. NFR-PF-05: 10 аккаунтов. Запись сериализована, чтение параллельно. |
| **Bluge vs S3** | ✅ OK | FR-SR-01: Bluge индексирует при получении (шаг 7 потока 1), до асинхронной загрузки в S3. Просмотр S3-писем — через lazy load, не требует переиндексации. |
| **Восстановление vs S3** | ✅ OK | FR-RS-03: lazy load перед восстановлением (приоритетный запрос). FR-S3-07: LRU-кэш обеспечивает наличие файла. Нет ситуации «в S3 — восстановить нельзя». |
| **Retention vs Mirror** | ✅ OK | FR-S3-03: режим Mirror — локальная копия остаётся. FR-RE-04: retention вытесняет в S3 после срока. Логика: сначала mirror, потом retention вытесняет. |
| **Master Key vs S3 Encryption** | ✅ OK | FR-S3-05: client-side encryption через Master Key. NFR-SC-03: обязательно. FR-ST-05: Master Key не в S3. Потеря ключа = потеря данных. Предупреждение выводится. |
| **HTML Sanitizer vs XSS** | ✅ OK | FR-VW-01: `bluemonday`. NFR-SC-05: CSP + XSS. Defense in depth, не противоречие. |
| **IMAP APPEND vs Message-ID** | ✅ OK | FR-RS-05: восстановление с оригинальным Message-ID. Если сервер отклоняет — failed, логируется. Нет требования «всегда успешно». |
| **S3 Object Layout vs SHA-256** | ✅ OK | FR-S3-04: layout по SHA-256. FR-S3-08: SHA-256 оригинала (до шифрования). FR-S3-05: в S3 ciphertext. Проверка целостности после расшифровки. |
| **Single Writer vs API** | ✅ OK | FR-API-02: все mutating операции. FR-ST-02: Single Writer Pattern. API-запросы на изменение данных отправляются в канал писателя. |
| **OAuth2 vs Plain Password** | ✅ OK | FR-AM-01: OAuth2 для Gmail/Microsoft, plain password для остальных. Это не выбор разработчика, а разные протоколы для разных провайдеров. |
| **WAL vs Graceful Shutdown** | ✅ OK | NFR-RL-05: при SIGINT — flush WAL, закрытие Bluge. WAL mode гарантирует целостность при аварийном завершении. |
| **S3 Queue vs SQLite** | ✅ OK | FR-S3-06: очередь в SQLite (`s3_upload_queue`). FR-ST-03: схема включает эту таблицу. Single Writer Pattern охватывает и эту таблицу. |
| **Rules YAML vs Web UI** | ✅ OK | FR-RE-05: правила управляются через Web UI и YAML. Система перезагружает YAML без рестарта. Нет конфликта: оба источника пишут в SQLite, Single Writer сериализует. |
| **Attachment Index vs Bluge** | ✅ OK | FR-SE-05: индексируются имена и MIME-типы. FR-SR-02: `attachment_names` — keyword field. Содержимое вложений не индексируется — нет расхождения. |
| **Config vs Env Vars** | ✅ OK | NFR-DP-03: env vars имеют приоритет над YAML. Это стандартная иерархия конфигурации, не противоречие. |

---

## 10. Будущие улучшения (Backlog для v2)

Следующие возможности были исключены из MVP и перенесены в бэклог. При возврате к ним — обновить ТЗ и проверить на согласованность с текущей архитектурой.

- **PostgreSQL-адаптер:** переход с SQLite на PostgreSQL для многопользовательского режима (> 1 000 000 писем).
- **JMAP поддержка:** альтернатива IMAP для современных провайдеров (Fastmail).
- **Gmail API / Microsoft Graph:** push-уведомления вместо IMAP IDLE/polling.
- **S3-режимы Tiered и S3-only:** вытеснение старых писем в S3 без локальной копии; хранение только в S3 с локальным кэшем.
- **Индексация содержимого вложений:** извлечение текста из PDF/DOCX и индексация в Bluge.
- **Экспорт в .mbox:** формат для совместимости с Thunderbird, Apple Mail.
- **Восстановление как новое письмо:** генерация нового Message-ID при восстановлении для обхода дублирования на сервере.
- **Локальная дедупликация через hard links:** shared Maildir с symlinks/hard links для экономии места при дублировании писем.
- **Плагин Paperless-ngx:** отправка вложений в документооборот одной кнопкой.
- **Шифрование at-rest для локальных .eml:** AES-256-GCM не только для S3, но и для локального Maildir.
- **Мобильное приложение:** нативные iOS/Android клиенты для просмотра архива.
- **Федерация (multi-node):** распределённый архив с несколькими инстансами.
