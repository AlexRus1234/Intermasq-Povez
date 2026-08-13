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

# Внутренняя архитектура Povez

Документ описывает внутреннее устройство Povez: контракт с хост-панелью,
конвенцию тегов PVE, формулу IP-адресации и обоснование принудительного
перезапуска Caddy. Адресат — разработчики, сопровождающие исходный код; для
пользователей и администраторов предназначены [FEATURES.md](FEATURES.md) и
[SETUP.md](SETUP.md).

## Содержание

- [Контракт с Intermasq](#контракт-с-intermasq)
- [Конвенция тегов PVE](#конвенция-тегов-pve)
- [Формула IP-адресации](#формула-ip-адресации)
- [Работа с Caddy](#работа-с-caddy)
- [Хранилище состояния](#хранилище-состояния)
- [Потоки данных](#потоки-данных)
- [Изоляция для тестов](#изоляция-для-тестов)

## Контракт с Intermasq

Povez представляет собой плагин типа sidecar, реализующий контракт
`internal/plugins.Load()` панели (см. `docs/func/ru/plugins.md` репозитория
хоста). Контракт определяет следующее.

- **Расположение.** Каталог `/etc/intermasq/plugins/povez/` содержит
  `manifest.json` и исполняемый файл.
- **Манифест** `manifest.json`:
  ```json
  { "id": "povez", "name": "Povez", "bin": "povez" }
  ```
  Значение `id` используется в пути `/plugins/povez/*` и в имени сокета.
- **Транспорт.** Intermasq передаёт `PLUGIN_SOCKET` — путь Unix-сокета (по
  умолчанию `/run/intermasq/sockets/povez.sock`). Плагин прослушивает этот
  сокет, а панель проксирует на него запросы `/plugins/povez/*` через
  reverse-proxy (аутентификация уже выполнена панелью).
- **Аутентификация обратных вызовов.** Панель передаёт `INTERMASQ_KEY` — секрет
  панели. Povez использует его в качестве заголовка `X-API-Key` для запросов к
  API Intermasq (`/hosts`, `/leases`, `/reload`). Приоритет: переменная
  окружения `INTERMASQ_KEY` выше, чем `config.intermasq_key` (последняя
  применяется для локальной отладки без панели).
- **Жизненный цикл.** При получении `SIGTERM`/`SIGINT` плагин корректно
  завершает работу `http.Server` (`Shutdown`) и удаляет файл сокета. Права
  сокета — `0770` (владелец и группа).

Режим локальной отладки (без панели): если `PLUGIN_SOCKET` не задан, плагин
прослушивает TCP-порт `:5000` (`plugin.tcp_debug_port`) и считывает `config.json`
из текущего каталога.

## Конвенция тегов PVE

Сканер `proxmox.go` считывает поле `tags` конфигурации контейнера или
виртуальной машины. Разделители (`,`, `;`) приводятся к пробелам, после чего
каждое поле приводится к нижнему регистру и сопоставляется с префиксами
(настраиваются через `proxmox.*_prefix`):

| Тег | Результат |
|---|---|
| `port-XX` | `ContainerInfo.Port = "XX"` (обязателен, кроме хостов Caddy) |
| `proto-http` / `proto-https` | `ContainerInfo.Protocol` (по умолчанию `http`) |
| `name-foo` | `ContainerInfo.Name = "foo"` — переопределяет имя из PVE |

Дополнительно: если имя контейнера (после возможного переопределения) содержит
подстроку `caddy`, устанавливается `ContainerInfo.IsCaddy = true`. Для хостов
Caddy создаётся только dnsmasq-запись (без маршрута в Caddy), а их IP
помещается в `dnsmasq.caddy_file` вместо узлового файла.

Если контейнер найдён по MAC, но не является хостом Caddy и не содержит тег
`port-XX`, провижнинг завершается ошибкой «у контейнера нет тега port-XX».

## Формула IP-адресации

```
newIP = <subnet>.<VMID - vmid_base>
```

- `subnet` — поле узла, три октета (например `172.20.5`).
- `vmid_base` — настраиваемое смещение (по умолчанию `98`): VMID `99` → суффикс
  `1`, VMID `100` → `2` и т. д.
- Пример: `subnet=172.20.5`, `VMID=105`, `vmid_base=98` → `172.20.5.7`.

Имя файла dnsmasq для контейнера формируется как
`<dnsmasq.conf_dir>/<RealNode_lower>x<subnet_octet:02d>.conf`
(например `/etc/dnsmasq.d/yadr01x05.conf`), где `subnet_octet` — третий октет
подсети (для `172.20.5` это `5` → `x05`).

Домен контейнера: `<name_lower>.<RealNode_lower><base_domain>`
(например `web01.yadr01.internal`).

## Работа с Caddy

Povez управляет Caddy через admin API (`caddy_url`, по умолчанию `:2019`).

### Структура конфигурации Caddy

Для каждого контейнера создаются два объекта с атрибутом `@id`:

- **route** `proxy-<VMID>-<NodeKey>` — `reverse_proxy` на `<IP>:<port>` с
  сопоставлением по `host`, равному домену. Для восходящего потока HTTPS
  добавляется `transport.tls.insecure_skip_verify` (значение из
  `caddy.upstream_insecure`).
- **TLS policy** `tls-<VMID>-<NodeKey>` — `subjects` равно домену, издатель —
  Step-CA ACME (`caddy.acme_url` + `caddy.ca_roots`); HTTP-01-запрос отключён.

### Логика upsert (`upsertByID`)

1. `GET /id/<id>`:
   - `200` — объект существует → `PUT /id/<id>` (замена);
   - иначе — `POST <createPath>` (создание).
2. Если `POST` возвращает `500`, предполагается отсутствие родительского
   контейнера (`srv0` или `automation.policies`); вызывается `init*` (создание
   родителя), после чего `POST` повторяется.
3. Ответ `404` при `DELETE` трактуется как успех (запись уже удалена).

### Принудительный перезапуск вместо мягкой перезагрузки

После добавления route и TLS Caddy кэширует сертификат в `autosave.json`.
Мягкая перезагрузка (`POST /reload`) **не перечитывает** сертификат с диска и не
применяет сертификат, только что выпущенный Step-CA: Caddy продолжает
использовать кэш, и домен может остаться с самоподписанным или устаревшим
сертификатом.

Решение — принудительный перезапуск:

1. После `installRouteAndCert` плагин ожидает `plugin.cert_settle_seconds`
   (по умолчанию `2 с`), чтобы Caddy записал новый сертификат на диск.
2. Направляется запрос `POST /stop` — Caddy завершает работу.
3. Системный юнит Caddy с параметром `Restart=always` перезапускает процесс.
4. При запуске Caddy считывает сертификат с диска, минуя устаревший кэш.

Перед перезапуском внутренний CA Caddy также отключается
(`local_certs_disabled`), чтобы сертификаты выпускались исключительно через
Step-CA.

В операции `Replay` перезапуск выполняется один раз для каждого затронутого
узла (а не для каждой записи) — после upsert всей группы route/TLS.

## Хранилище состояния

`state.go` хранит таблицу выделенных route/TLS в `STATE_FILE` (по умолчанию
`/etc/intermasq/plugins/povez/routes.json`). Записи имеют вид
`RouteRecord{Domain, TargetIP, TargetPort, Protocol, RouteID, TLSID, Node,
UpdatedAt}`.

- Запись атомарна: данные помещаются во временный файл `<path>.tmp`, после чего
  выполняется `os.Rename`.
- Каталог создаётся через `MkdirAll` (0750), файл — с правами 0660.
- `Upsert` выполняет обновление по `RouteID` (со сменой `UpdatedAt`) либо
  добавление новой записи.
- Используется для отображения в интерфейсе (`GET /api/state`) и для
  восстановления конфигурации (`POST /api/replay`).

## Потоки данных

```
                 ┌─────────────────────────────────────────────────────┐
                 │                     Povez (engine)                  │
                 └─────────────────────────────────────────────────────┘
   GET /leases ──┐ │ │ ┌── AddHost/DeleteHost ──┐
   GET /hosts ───┼─┼─┘ └── Reload ──────────────┘
                 │ │      API Intermasq (X-API-Key = INTERMASQ_KEY)
                 │ │
                 │ └── /cluster/resources, /nodes/.../config ── Proxmox VE
                 │
                 └── upsert route/TLS, POST /stop ── Caddy admin API
                      │
                      └── Step-CA ACME (ca + trusted_roots) ── Step-CA
```

Интерфейс (`index.html`) встроен в плагин и обращается к API плагина по пути
`/plugins/povez/*` (тот же источник, что и у панели), используя bearer-токен из
`localStorage` родительского окна.

## Изоляция для тестов

Engine обращается к внешним зависимостям через интерфейсы, определённые в
`core/interfaces.go`: `PVEFinder` (поиск контейнера по MAC), `HostManager`
(управление dnsmasq через Intermasq), `RouteManager` (установка и удаление
маршрутов и TLS в Caddy, перезапуск узла), `StateBackend` (хранилище таблицы
маршрутов). Благодаря этому внешние сервисы (PVE/Intermasq/Caddy) в тестах
замещаются заглушками на основе `httptest.Server`.

Типизированные ошибки в `core/errors.go` (`ErrContainerNotFound`,
`ErrContainerRunning`, `ErrInvalidIP`) преобразуются слоем API в HTTP-статусы:
404, 409 и 400 соответственно; прочие ошибки — в 500.

Тесты располагаются совместно с кодом (`*_test.go`) и используют stdlib
`testing` и `httptest` (без testify):

- `core/state_test.go` — StateStore (файловая система, без сети);
- `core/caddy_test.go` — генераторы route/TLS, `upsertByID`,
  `DeleteRouteAndTLS`;
- `core/engine_test.go` — `GetPendingDevices` (заглушка Intermasq);
- `core/proxmox_test.go` — `FindByMAC`, разбор тегов (заглушка PVE);
- `api/routes_test.go` — обработчики (проверка методов, валидация, пути с
  ошибками);
- `main_test.go` — `Config.applyDefaults`, `loadConfig`, `intermasqAPIKey`.

Покрытие составляет около 43 %; совместная проверка оркестрации
AddRoute/Provision/ReplayCaddy, требующая одновременной имитации
Caddy+PVE+Intermasq, оставлена для следующего этапа. Запуск:
`go test ./... -race -count=1`.
