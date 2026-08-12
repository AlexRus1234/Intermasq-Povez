<!--
Povez - Intermasq provisioning plugin
Copyright (C) 2026 AlexRus1234

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
-->

# Архитектура Povez

Детальное описание того, как Povez устроен внутри: контракт с матерью, конвенция
тегов PVE, формула IP-адресации и rationale «NUKE»-рестарта Caddy.

---

## Контракт с Intermasq

Povez — sidecar-плагин по контракту `internal/plugins.Load()` панели (см.
`docs/func/ru/plugins.md` матери). Контракт определяет:

- **Расположение.** `/etc/intermasq/plugins/povez/` с `manifest.json` и
  бинарником.
- **Манифест** `manifest.json`:
  ```json
  { "id": "povez", "name": "Povez", "bin": "povez" }
  ```
  `id` используется в URL `/plugins/povez/*` и имени сокета.
- **Транспорт.** Мать экспортирует `PLUGIN_SOCKET` (путь Unix-сокета, по
  умолчанию `/run/intermasq/sockets/povez.sock`). Плагин слушает этот сокет,
  мать проксирует на него `/plugins/povez/*` через reverse-proxy (auth уже
  проверен панелью).
- **Аутентификация обратных вызовов.** Мать экспортирует `INTERMASQ_KEY` —
  секрет панели. Povez использует его как `X-API-Key` для запросов к API
  матери (`/hosts`, `/leases`, `/reload`). Приоритет: env `INTERMASQ_KEY` >
  `config.intermasq_key` (последний — для локальной отладки без матери).
- **Жизненный цикл.** На `SIGTERM`/`SIGINT` плагин корректно завершает
  `http.Server` (`Shutdown`) и удаляет socket-файл. Права сокета — `0770`
  (владелец + группа).

Локальный режим отладки (без матери): если `PLUGIN_SOCKET` не задан, плагин
слушает TCP `:5000` (`plugin.tcp_debug_port`) и читает `config.json` из CWD.

---

## Конвенция тегов PVE

Сканер `proxmox.go` читает поле `tags` конфига контейнера/ВМ. Разделители
приводятся к пробелам (`,`, `;`), затем каждое поле приводится к нижнему
регистру и сопоставляется с префиксами (конфигурируются в `proxmox.*_prefix`):

| Тег | Эффект |
|---|---|
| `port-XX` | `ContainerInfo.Port = "XX"` (обязателен, кроме Caddy-хостов) |
| `proto-http` / `proto-https` | `ContainerInfo.Protocol` (default `http`) |
| `name-foo` | `ContainerInfo.Name = "foo"` — переопределяет имя из PVE |

Дополнительно: если имя контейнера (после возможного override) содержит
подстроку `caddy`, `ContainerInfo.IsCaddy = true`. Для Caddy-хостов создаётся
только dnsmasq-запись (без Caddy route), а их IP пишется в
`dnsmasq.caddy_file` вместо нодового файла.

Если контейнер найден по MAC, но не Caddy и без тега `port-XX` — провижнинг
возвращает ошибку «у контейнера нет тега port-XX».

---

## Формула IP-адресации

```
newIP = <subnet>.<VMID - vmid_base>
```

- `subnet` — поле ноды, 3 октета (например `172.20.5`).
- `vmid_base` — настраиваемое смещение (default `98`): VMID `99` → суффикс
  `1`, VMID `100` → `2`, и т.д.
- Пример: `subnet=172.20.5`, `VMID=105`, `vmid_base=98` → `172.20.5.7`.

Имя файла dnsmasq для контейнера строится как
`<dnsmasq.conf_dir>/<RealNode_lower>x<subnet_octet:02d>.conf`
(например `/etc/dnsmasq.d/yadr01x05.conf`), где `subnet_octet` — 3-й октет
подсети (для `172.20.5` это `5` → `x05`).

Домен контейнера: `<name_lower>.<RealNode_lower><base_domain>`
(например `web01.yadr01.internal`).

---

## Работа с Caddy

Povez управляет Caddy через admin API (`caddy_url`, default `:2019`).

### Структура конфига Caddy

Для каждого контейнера создаются два объекта с `@id`:

- **route** `proxy-<VMID>-<NodeKey>` — `reverse_proxy` на `<IP>:<port>`,
  матч по `host` = домену. Для https upstream добавляется
  `transport.tls.insecure_skip_verify` (значение из `caddy.upstream_insecure`).
- **TLS policy** `tls-<VMID>-<NodeKey>` — `subjects` = домен, issuer = Step-CA
  ACME (`caddy.acme_url` + `caddy.ca_roots`), HTTP-01 челлендж отключён.

### Upsert-логика (`upsertByID`)

1. `GET /id/<id>`:
   - `200` → объект существует → `PUT /id/<id>` (замена).
   - иначе → `POST <createPath>` (создание).
2. Если `POST` вернул `500` — значит отсутствует родительский контейнер
   (`srv0` или `automation.policies`); вызывается `init*` (создание
   родителя), затем `POST` повторяется.
3. `404` при `DELETE` трактуется как успех (запись уже удалена).

### Почему «NUKE»-рестарт, а не reload

После добавления route+TLS Caddy кэширует сертификат в `autosave.json`. Soft
reload (`POST /reload`) **не перечитывает** сертификат с диска и не применяет
свежий cert, только что выпущенный Step-CA — Caddy продолжает пользоваться
кэшем, и домен может остаться с самоподписанным/устаревшим сертификатом.

Решение — жёсткий рестарт:

1. После `installRouteAndCert` плагин ждёт `plugin.cert_settle_seconds`
   (default `2s`), чтобы Caddy успел записать свежий cert на диск.
2. Шлёт `POST /stop` — Caddy завершается.
3. Systemd-юнит Caddy с `Restart=always` поднимает процесс заново.
4. При старте Caddy читает сертификат с диска, минуя плохой кэш.

Перед рестартом также отключается внутренний CA Caddy (`local_certs_disabled`)
чтобы сертификаты выпускались только через Step-CA.

В `Replay` рестарт делается один раз на каждую затронутую ноду (а не на каждую
запись) — после upsert'а всей пачки route/TLS.

---

## State store

`state.go` хранит таблицу выделенных route/TLS в `STATE_FILE`
(default `/etc/intermasq/plugins/povez/routes.json`). Записи —
`RouteRecord{Domain, TargetIP, TargetPort, Protocol, RouteID, TLSID, Node,
UpdatedAt}`.

- Запись атомарна: пишем в `<path>.tmp`, затем `os.Rename`.
- Каталог создается через `MkdirAll` (0750), файл — 0660.
- `Upsert` обновляет по `RouteID` (переставляет `UpdatedAt`), иначе добавляет.
- Используется для отображения в UI (`GET /api/state`) и для восстановления
  (`POST /api/replay`).

---

## Потоки данных

```
                 ┌─────────────────────────────────────────────────────┐
                 │                     Povez (engine)                  │
                 └─────────────────────────────────────────────────────┘
   GET /leases ──┐ │ │ ┌── AddHost/DeleteHost ──┐
   GET /hosts ───┼─┼─┘ └── Reload ──────────────┘
                 │ │      Intermasq API (X-API-Key = INTERMASQ_KEY)
                 │ │
                 │ └── /cluster/resources, /nodes/.../config ── Proxmox VE
                 │
                 └── upsert route/TLS, POST /stop ── Caddy admin API
                      │
                      └── Step-CA ACME (ca + trusted_roots) ── Step-CA
```

UI (`index.html`) встроен в плагин и ходит в API плагина через
`/plugins/povez/*` (тот же origin, что и мать), беря bearer-токен из
`localStorage` родительского окна.

---

## Тестирование

Тесты — co-located `*_test.go`, stdlib `testing` + `httptest` (без testify).
Внешние сервисы (PVE/Intermasq/Caddy) мокаются `httptest.Server`:

- `core/state_test.go` — StateStore (FS, без сети).
- `core/caddy_test.go` — генераторы route/TLS, upsertByID, DeleteRouteAndTLS.
- `core/engine_test.go` — GetPendingDevices (mock Intermasq).
- `core/proxmox_test.go` — FindByMAC, парсинг тегов (mock PVE).
- `api/routes_test.go` — хендлеры (method guards, валидация, error-пути).
- `main_test.go` — Config.applyDefaults, loadConfig, intermasqAPIKey.

Покрытие ~43% (orchestration AddRoute/Provision/ReplayCaddy требует совместного
мока Caddy+PVE+Intermasq и оставлен на следующий проход). Запуск:
`go test ./... -race -count=1`.
