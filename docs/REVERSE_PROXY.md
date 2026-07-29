# Работа за reverse-proxy

Marchi отдаёт собственный самоподписанный TLS по умолчанию (`http.tls.enabled: true`,
`{data_dir}/tls`) и рассчитан на прямой доступ. Если перед ним стоит nginx или Traefik —
например, чтобы получить настоящий сертификат через Let's Encrypt, повесить несколько
self-hosted сервисов на один порт 443, или добавить WAF — учтите три момента ниже, иначе
часть функциональности будет работать неверно или совсем не будет.

## TLS-offload

Пусть сертификат терминирует proxy, а Marchi слушает как обычный HTTP внутри доверенной
сети (Docker-сеть, localhost, VPN):

```yaml
http:
  host: 0.0.0.0 # или конкретный внутренний адрес
  port: 8080
  tls:
    enabled: false
```

Без этого браузер будет видеть двойной TLS (proxy → Marchi тоже поверх HTTPS с
самоподписанным сертификатом) — работать будет, но усложнит конфигурацию proxy без
всякой пользы: сертификат Marchi всё равно не тот, что видит браузер.

## Доверенные прокси-заголовки (`http.trusted_proxies`)

Без этой настройки **каждый запрос будет выглядеть так, будто он пришёл с адреса самого
proxy**, а не реального клиента — потому что по умолчанию Marchi игнорирует
`X-Forwarded-For` (не доверяет ему, пока explicit не сказано, что он приходит от
настоящего proxy — иначе любой клиент мог бы подделать этот заголовок и, например, обойти
блокировку по IP после неудачных попыток разблокировки). Это ломает сразу две вещи:

- **прогрессивную блокировку `/unlock`** (10 неудачных попыток → блок на 15 минут) — она
  трекает по IP, и без реального IP заблокируется/не заблокируется proxy целиком, а не
  конкретный клиент;
- **аудит-лог** (Settings → «Аудит-лог») — колонка IP будет показывать адрес proxy для
  каждой записи, что бесполезно при расследовании.

Укажите адрес (или подсеть) proxy явно:

```yaml
http:
  trusted_proxies:
    - "172.18.0.0/16" # подсеть Docker-сети, где живёт nginx/Traefik
    # - "10.0.0.5"    # или конкретный IP, если proxy не в контейнере
```

Только после этого Marchi начинает читать `X-Forwarded-For`, и только от адресов из
списка — запрос напрямую (не через proxy) с поддельным `X-Forwarded-For` будет
проигнорирован, если сам клиент не входит в `trusted_proxies`.

**Важно:** как только адрес добавлен в `trusted_proxies`, Marchi перестаёт использовать
реальный адрес TCP-соединения от него вообще — только значение `X-Forwarded-For`, и если
запрос от доверенного адреса пришёл без этого заголовка, IP в аудит-логе/блокировке
окажется пустым. Оба конфига ниже (nginx и Traefik) сами всегда проставляют заголовок, так
что для них это не проблема — но не добавляйте в `trusted_proxies` ничего, откуда запросы
могут приходить не через настоящий proxy (например, весь `0.0.0.0/0` или адрес, с которого
вы иногда обращаетесь к Marchi напрямую в обход proxy).

## Path Marchi не поддерживает — только (под)домен или отдельный порт

У Marchi нет поддержки работы из-под URL-префикса (`https://example.com/marchi/`) — все
ссылки и статика (`/static/...`, `/accounts`, `/ws` и т.д.) отдаются от корня без
какого-либо configurable base path, и proxy, который просто урезает префикс
(`rewrite ^/marchi/(.*) /$1`), не перепишет эти абсолютные пути внутри уже отданного HTML.
Результат — сломанные ссылки/CSS/JS.

Правильные варианты:
- отдельный (под)домен: `marchi.example.com` → `proxy_pass` на весь корень `/`;
- отдельный порт на существующем домене без изменения path.

## WebSocket (`/ws`)

Прогресс синхронизации/восстановления/reindex идёт через WebSocket — proxy должен явно
поддерживать апгрейд протокола (см. конфиги ниже, оба это учитывают).

## nginx

```nginx
server {
    listen 443 ssl;
    server_name marchi.example.com;

    ssl_certificate     /etc/letsencrypt/live/marchi.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/marchi.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        # WebSocket upgrade (/ws — прогресс sync/restore/reindex)
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        # Реальный IP клиента — обязательно вместе с http.trusted_proxies
        # выше, иначе настройка на стороне Marchi бесполезна.
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Host $host;
    }
}
```

## Traefik (Docker-labels)

```yaml
services:
  marchi:
    image: ghcr.io/yurydemin/marchi:latest
    # ... volumes/environment как в docker-compose.yml из README ...
    labels:
      - traefik.enable=true
      - traefik.http.routers.marchi.rule=Host(`marchi.example.com`)
      - traefik.http.routers.marchi.tls.certresolver=letsencrypt
      - traefik.http.services.marchi.loadbalancer.server.port=8080
      # WebSocket работает через Traefik без дополнительных labels — он
      # сам определяет апгрейд по заголовкам запроса.
```

Traefik по умолчанию форвардит реальный IP клиента через `X-Forwarded-For` — на стороне
Marchi всё равно нужно явно перечислить IP/подсеть самого Traefik в `http.trusted_proxies`
(см. выше), иначе Marchi этому заголовку не поверит.
