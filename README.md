> **Это зеркало репозитория. Оригинал находится по адресу:**
> [https://git.alexrus1234.ru/AlexRus1234/Intermasq-Povez](https://git.alexrus1234.ru/AlexRus1234/Intermasq-Povez)

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

**Плагин автоматического назначения для [Intermasq](https://git.alexrus1234.ru/AlexRus1234/Intermasq)**

Povez связывает Intermasq, Proxmox VE и Caddy: по MAC-адресу нового контейнера
плагин автоматически создаёт DNS-запись в dnsmasq и маршрут reverse-proxy с
TLS-сертификатом в Caddy. Плагин запускается панелью как sidecar-процесс по
контракту `/etc/intermasq/plugins/` и взаимодействует с панелью через
Unix-сокет.

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
- [Документация](#документация)
- [Лицензия](#лицензия)

> Архитектурные детали — конвенция тегов PVE, формула IP-адресации,
> обоснование принудительного перезапуска Caddy и контракт манифеста —
> приведены в [`docs/INTERNALS.md`](docs/INTERNALS.md).

Проект разработан в соответствии с заранее определённой архитектурой; при
подготовке исходного кода использовался ИИ-ассистент.[^1]

---

## Возможности

- **Обнаружение** новых контейнеров: сравнение ARP- и dhcp-аренд панели с
  зарегистрированными хостами; формирование списка ожидающих MAC-адресов.
- **Автоматическое назначение** по MAC: поиск контейнера в Proxmox VE
  (LXC/QEMU), расчёт IP по подсети узла, DNS-запись в dnsmasq, маршрут
  reverse-proxy и политика TLS в Caddy.
- **Режим «только DNS» (`dnsOnly`)**: добавление только dnsmasq-записи без
  обращения к Caddy.
- **Снятие назначения (Deprovisioning)**: удаление маршрута и политики TLS из
  Caddy и хост-записи из dnsmasq для остановленного контейнера.
- **Восстановление (Replay)**: перестроение конфигурации Caddy из локального
  `routes.json` после сброса или перезапуска Caddy (upsert всех записей и один
  перезапуск на узел).
- **Step-CA ACME**: сертификаты выпускаются внутренним Step-CA; HTTP-01-запрос
  отключён.
- **Контракт Intermasq**: `manifest.json`, Unix-сокет (`PLUGIN_SOCKET`),
  обратные вызовы в API панели через `INTERMASQ_KEY`, корректное завершение по
  SIGTERM.

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
| dnsmasq | любой | управляется панелью |

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

Каталог плагина должен содержать `manifest.json`, исполняемый файл `povez` и
`config.json` (путь можно задать через `CONFIG_PATH`). После перезапуска панели
плагин появляется в меню и доступен по адресу `/plugins/povez/`.

> Автоматическая перезагрузка плагинов не поддерживается: после изменения
> манифеста или исполняемого файла требуется перезапуск панели.

---

## Конфигурация

Конфигурация считывается из `config.json` (путь переопределяется через
`CONFIG_PATH`). Секция `nodes` обязательна; остальные секции имеют значения по
умолчанию и могут быть опущены. См. `config.example.json`.

### Основные поля

| Поле | Тип | Описание |
|---|---|---|
| `intermasq_url` | string | URL API панели (например `http://172.20.0.1:8080/api`) |
| `intermasq_key` | string | API-ключ панели. **Имеет более низкий приоритет, чем переменная окружения `INTERMASQ_KEY`** |
| `base_domain` | string | суффикс домена (например `.internal`) |
| `nodes` | map | конфигурация узлов PVE (см. ниже) |

### Конфиг ноды

| Поле | Описание |
|---|---|
| `subnet` | подсеть контейнеров вида `172.20.5` (3 октета) |
| `caddy_url` | URL admin API Caddy на ноде (`http://host:2019`) |
| `pve_url` | URL API Proxmox (`https://host:8006/api2/json`) |
| `pve_token_id` | ID токена PVE (`user@pam!token`) |
| `pve_secret` | секрет токена PVE |

### Опциональные секции (со значениями по умолчанию)

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
| `plugin.tcp_debug_port` | `:5000` | TCP-порт локальной отладки (без панели) |
| `plugin.cert_settle_seconds` | `2` | пауза перед перезапуском Caddy (выпуск сертификата) |

### Переменные окружения

| Переменная | Назначение |
|---|---|
| `PLUGIN_SOCKET` | путь Unix-сокета (передаётся панелью; без него — режим TCP-отладки) |
| `INTERMASQ_KEY` | API-ключ панели (приоритет над `config.intermasq_key`) |
| `STATE_FILE` | путь к `routes.json` (default `/etc/intermasq/plugins/povez/routes.json`) |
| `CONFIG_PATH` | путь к config.json (default `config.json` в CWD) |

---

## API плагина

Все эндпоинты монтируются панелью под `/plugins/povez/*` (аутентификация
выполняется панелью до проксирования).

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

Подробности — в [`docs/INTERNALS.md`](docs/INTERNALS.md).

---

## Как это работает

1. **Обнаружение.** `GET /api/pending` сравнивает `/leases` панели с
   `/hosts`; устройства, отсутствующие в dnsmasq, включаются в список ожидающих.
2. **Назначение (Provision).** По MAC выполняется поиск контейнера в PVE
   (`/cluster/resources` и сетевая конфигурация); из тегов определяются порт и
   протокол; IP рассчитывается как `<subnet>.<VMID - vmid_base>`. В dnsmasq
   помещается хост-запись, в Caddy — маршрут reverse_proxy и политика TLS
   (Step-CA ACME), после чего Caddy принудительно перезапускается
   (`POST /stop` → `Restart=always`) для применения нового сертификата.
3. **Снятие назначения (Deprovision).** Проверяется, что контейнер остановлен,
   после чего из Caddy удаляются маршрут и политика TLS, а из dnsmasq —
   хост-запись.
4. **Восстановление (Replay).** Все записи из `routes.json` помещаются в Caddy
   методом upsert (PUT/POST по `@id`), затем выполняется один запрос `/stop` на
   каждый затронутый узел.

> Принудительный перезапуск вместо мягкой перезагрузки применён намеренно: Caddy
> кэширует сертификат в `autosave.json`, и мягкая перезагрузка не применяет
> сертификат, только что выпущенный Step-CA. Принудительный перезапуск
> заставляет Caddy считать сертификат с диска. Подробности — в
> `docs/INTERNALS.md`.

---

## Структура проекта

```
povez/
├── main.go              # точка входа: конфигурация, клиенты, сервер, listener
├── manifest.json        # {id, name, bin} — контракт Intermasq
├── config.example.json  # шаблон конфигурации (→ config.json)
├── index.html           # интерфейс (Vue 3 SPA)
├── go.mod
├── api/
│   └── routes.go        # HTTP-обработчики, проверка методов, DTO
├── core/
│   ├── caddy.go         # Caddy admin API (upsert route/TLS, перезапуск)
│   ├── engine.go        # оркестратор: Provision / Deprovision / Replay
│   ├── intermasq.go     # HTTP-клиент к API панели
│   ├── proxmox.go       # клиент PVE (поиск по MAC, разбор тегов)
│   └── state.go         # JSON state store (атомарная запись)
├── docs/
│   ├── FEATURES.md      # возможности
│   ├── SETUP.md         # установка и настройка
│   ├── INTERNALS.md     # внутренняя архитектура
│   └── CHANGELOG.md     # история изменений
├── .forgejo/workflows/  # CI: build/test/publish + mirror
├── LICENSE              # AGPL-3.0
└── README.md / README.en.md
```

---

## Технологический стек

- **Go 1.25** — только стандартная библиотека, без внешних зависимостей
- **Vue 3 + Bootstrap 5** — интерфейс (CDN, без этапа сборки)
- **log/slog** — структурное логирование
- **httptest** — тесты (stdlib `testing`, без testify)
- **Forgejo Actions** — CI на `fedora:44`

---

## Документация

- [`docs/FEATURES.md`](docs/FEATURES.md) — функциональные возможности
- [`docs/SETUP.md`](docs/SETUP.md) — пошаговая установка и настройка
- [`docs/INTERNALS.md`](docs/INTERNALS.md) — внутренняя архитектура
- [`docs/CHANGELOG.md`](docs/CHANGELOG.md) — история изменений

---

## Лицензия

Copyright (C) 2026 AlexRus1234. Распространяется по лицензии
**GNU Affero General Public License v3.0**. Полный текст — в [`LICENSE`](LICENSE).

---

[^1]: ИИ-ассистент использовался для подготовки исходного кода по заранее
      заданной архитектуре; все проектные решения, поведение и совместимость
      с контрактом Intermasq проверялись автором.
