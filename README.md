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

**Русский** | [English](README.en.md) |

<div align="center">

<h1>Povez</h1>

**Плагин автопровижнинга для [Intermasq](https://git.alexrus1234.ru/AlexRus1234/Intermasq)**

Povez связывает Intermasq, Proxmox VE и Caddy: по MAC-адресу нового контейнера
плагин автоматически настраивает DNS-запись в dnsmasq и reverse-proxy +
TLS-сертификат в Caddy. Запускается матерью как sidecar-процесс по контракту
`/etc/intermasq/plugins/`, общается с панелью через Unix-сокет.

[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg?style=flat-square)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8.svg?style=flat-square)](https://go.dev/)
[![Platform](https://img.shields.io/badge/Linux-any-1793D1.svg?style=flat-square)](#быстрый-старт)
[![Intermasq](https://img.shields.io/badge/Intermasq-plugin-6f42c1.svg?style=flat-square)](https://git.alexrus1234.ru/AlexRus1234/Intermasq)

</div>

---

## Содержание

- [Возможности](#возможности)
- [Быстрый старт](#быстрый-старт)
- [Конфигурация](#конфигурация)
- [API плагина](#api-плагина)
- [Конвенция тегов PVE](#конвенция-тегов-pve)
- [Как это работает](#как-это-работает)
- [Структура проекта](#структура-проекта)
- [Технологический стек](#технологический-стек)
- [Лицензия](#лицензия)

> Детали архитектуры — конвенция тегов PVE, формула IP-адресации, rationale
> «NUKE»-рестарта Caddy и контракт манифеста — вынесены в
> [`docs/architecture.md`](docs/architecture.md).

Проект разработан в соответствии с заранее определённой архитектурой; при
подготовке исходного кода использовался ИИ-ассистент.[^1]

---

## Возможности

- **Обнаружение** новых контейнеров: сравнение ARP/dhcp-аренд матери с уже
  зарегистрированными хостами → список «pending» MAC-адресов.
- **Авто-провижнинг** по MAC: поиск контейнера в Proxmox VE (LXC/QEMU), расчёт
  IP по подсети ноды, DNS-запись в dnsmasq, reverse-proxy route + TLS-политика
  в Caddy.
- **DNS-only режим** (`dnsOnly`): добавить только dnsmasq-запись без Caddy.
- **Deprovisioning**: удаление route+TLS из Caddy и хоста из dnsmasq для
  остановленного контейнера.
- **Replay**: восстановление конфигурации Caddy из локального `routes.json`
  после сброса/переподъёма Caddy (upsert всех записей + один restart на ноду).
- **Step-CA ACME**: сертификаты выпускаются через внутренний Step-CA,
  HTTP-01 челлендж отключён.
- **Контракт Intermasq**: `manifest.json`, Unix-сокет (`PLUGIN_SOCKET`),
  обратные вызовы в API матери через `INTERMASQ_KEY`, корректное завершение
  по SIGTERM.

---

## Быстрый старт

### Требования

| Компонент | Версия | Назначение |
|---|---|---|
| Go | 1.25+ | сборка плагина |
| Intermasq | последняя | хост-панель (контракт плагинов) |
| Proxmox VE | 7+ | источник данных о контейнерах |
| Caddy | 2.7+ | reverse-proxy + TLS (admin API `:2019`) |
| Step-CA | любой | ACME-центр для сертификатов |
| dnsmasq | любой | управляется матерью |

### Сборка

```bash
git clone <repo-url> povez
cd povez
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o povez .
```

### Установка в Intermasq

```bash
sudo mkdir -p /etc/intermasq/plugins/povez
sudo cp povez        /etc/intermasq/plugins/povez/povez
sudo cp manifest.json /etc/intermasq/plugins/povez/manifest.json
sudo cp config.example.json /etc/intermasq/plugins/povez/config.json
sudo nano /etc/intermasq/plugins/povez/config.json   # заполнить ключи/ноды
sudo systemctl restart intermasq
```

Каталог плагина должен содержать `manifest.json`, бинарник `povez` и
`config.json` (или задать путь через `CONFIG_PATH`). После рестарта панели
плагин появится в меню и доступен на `/plugins/povez/`.

> Автоперезагрузка плагинов не поддерживается — после изменения манифеста или
> бинарника перезапустите панель.

---

## Конфигурация

Конфиг читается из `config.json` (путь переопределяется через `CONFIG_PATH`).
Секция `nodes` обязательна; остальные секции имеют разумные дефолты и могут
быть опущены. См. `config.example.json`.

### Основные поля

| Поле | Тип | Описание |
|---|---|---|
| `intermasq_url` | string | URL API матери (например `http://172.20.0.1:8080/api`) |
| `intermasq_key` | string | API-ключ матери. **Приоритет ниже `INTERMASQ_KEY` env** |
| `base_domain` | string | суффикс домена (например `.internal`) |
| `nodes` | map | конфиг нод PVE (см. ниже) |

### Конфиг ноды

| Поле | Описание |
|---|---|
| `subnet` | подсеть контейнеров вида `172.20.5` (3 октета) |
| `caddy_url` | URL admin API Caddy на ноде (`http://host:2019`) |
| `pve_url` | URL API Proxmox (`https://host:8006/api2/json`) |
| `pve_token_id` | ID токена PVE (`user@pam!token`) |
| `pve_secret` | секрет токена PVE |

### Опциональные секции (с дефолтами)

| Секция.поле | Default | Описание |
|---|---|---|
| `caddy.acme_url` | `https://172.20.0.1:9000/acme/acme/directory` | Step-CA ACME directory |
| `caddy.ca_roots` | `/etc/caddy/root_ca.crt` | путь к root CA PEM |
| `caddy.listen` | `:443` | порт Caddy |
| `caddy.upstream_insecure` | `true` | `insecure_skip_verify` для https upstream |
| `dnsmasq.conf_dir` | `/etc/dnsmasq.d` | каталог include-файлов dnsmasq |
| `dnsmasq.caddy_file` | `/etc/dnsmasq.d/caddy.conf` | файл хоста Caddy |
| `http.timeout_seconds` | `10` | общий таймаут HTTP-клиентов |
| `proxmox.port_prefix` | `port-` | префикс тега порта |
| `proxmox.proto_prefix` | `proto-` | префикс тега протокола |
| `proxmox.name_prefix` | `name-` | префикс тега имени |
| `proxmox.vmid_base` | `98` | базовый VMID для расчёта IP-суффикса |
| `proxmox.insecure_skip_verify` | `true` | пропуск проверки TLS PVE |
| `plugin.tcp_debug_port` | `:5000` | TCP-порт локальной отладки (без матери) |
| `plugin.cert_settle_seconds` | `2` | пауза перед рестартом Caddy (выпуск cert) |

### Переменные окружения

| Переменная | Назначение |
|---|---|
| `PLUGIN_SOCKET` | путь Unix-сокета (прокидывается матерью; без него — TCP-режим отладки) |
| `INTERMASQ_KEY` | API-ключ матери (приоритет над `config.intermasq_key`) |
| `STATE_FILE` | путь к `routes.json` (default `/etc/intermasq/plugins/povez/routes.json`) |
| `CONFIG_PATH` | путь к config.json (default `config.json` в CWD) |

---

## API плагина

Все эндпоинты монтируются матерью под `/plugins/povez/*` (auth проверяется
панелью до проксирования).

| Метод | Путь | Описание |
|---|---|---|
| `GET` | `/` | UI (`index.html`, Vue 3 SPA) |
| `GET` | `/health` | healthcheck (`{status, plugin, version}`) |
| `GET` | `/api/pending` | список неизвестных MAC (lease без host-записи) |
| `POST` | `/api/provision` | провижнинг: `{mac, dnsOnly}` |
| `DELETE`/`POST` | `/api/deprovision` | удаление конфигов: `{mac}` |
| `GET` | `/api/state` | содержимое `routes.json` |
| `POST` | `/api/replay` | восстановление Caddy из `routes.json` |

---

## Конвенция тегов PVE

Povez читает теги контейнера/ВМ из Proxmox (поле `tags`, разделители `,`/`;`/пробел):

| Тег | Назначение |
|---|---|
| `port-XX` | порт upstream-сервиса контейнера (обязателен, кроме Caddy-хостов) |
| `proto-http` / `proto-https` | протокол upstream (default `http`) |
| `name-foo` | переопределение имени (используется в домене) |

Если имя контейнера содержит подстроку `caddy`, он считается Caddy-хостом:
для него создаётся только dnsmasq-запись (без Caddy route).

Подробности — в [`docs/architecture.md`](docs/architecture.md).

---

## Как это работает

1. **Обнаружение.** `GET /api/pending` сравнивает `/leases` матери с
   `/hosts` — устройства, которых ещё нет в dnsmasq, попадают в список.
2. **Provision.** По MAC ищется контейнер в PVE (`/cluster/resources` + конфиг
   сети), из тегов берётся порт/протокол, считается IP
   (`<subnet>.<VMID - vmid_base>`), в dnsmasq пишется host-запись, в Caddy —
   reverse_proxy route + TLS-политика (Step-CA ACME), после чего Caddy
   жёстко перезапускается (`POST /stop` → `Restart=always`) для применения
   свежего сертификата.
3. **Deprovision.** Проверяется, что контейнер `stopped`, затем удаляются
   route+TLS из Caddy и host из dnsmasq.
4. **Replay.** Все записи из `routes.json` upsert'ятся в Caddy (PUT/POST по
   `@id`), затем один `/stop` на каждую затронутую ноду.

> «NUKE»-рестарт вместо мягкого reload — намеренный workaround: Caddy кэширует
> сертификат в `autosave.json`, и мягкий reload не подхватывает свежий cert от
> Step-CA. Жёсткий рестарт заставляет читать cert с диска. Детали — в
> `docs/architecture.md`.

---

## Структура проекта

```
povez/
├── main.go              # точка входа: конфиг, клиенты, server, listener
├── manifest.json        # {id, name, bin} — контракт Intermasq
├── config.example.json  # шаблон конфига (→ config.json)
├── index.html           # UI (Vue 3 SPA)
├── go.mod
├── api/
│   └── routes.go        # HTTP-хендлеры + method guards + DTO
├── core/
│   ├── caddy.go         # Caddy admin API (upsert route/TLS, restart)
│   ├── engine.go        # оркестратор: Provision / Deprovision / Replay
│   ├── intermasq.go     # HTTP-клиент к API матери
│   ├── proxmox.go       # PVE клиент (scan by MAC, парсинг тегов)
│   └── state.go         # JSON state store (atomic write)
├── docs/
│   └── architecture.md  # детали архитектуры
├── .forgejo/workflows/  # CI: build/test/publish + mirror
├── LICENSE              # AGPL-3.0
└── README.md / README.en.md
```

---

## Технологический стек

- **Go 1.25** — stdlib only, без внешних зависимостей
- **Vue 3 + Bootstrap 5** — UI (CDN, без сборщика)
- **log/slog** — структурное логирование
- **httptest** — тесты (stdlib `testing`, без testify)
- **Forgejo Actions** — CI на `fedora:44`

---

## Лицензия

Copyright (C) 2026 AlexRus1234. Распространяется по лицензии
**GNU Affero General Public License v3.0**. Полный текст — в [`LICENSE`](LICENSE).

---

[^1]: ИИ-ассистент использовался для подготовки исходного кода по заранее
      заданной архитектуре; все проектные решения, поведение и совместимость
      с контрактом Intermasq проверялись автором.
