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

# Установка и настройка Povez

## 1. Требования

| Компонент | Версия | Назначение |
|---|---|---|
| Go | 1.25+ | сборка плагина |
| Intermasq | последняя | хост-панель (контракт плагинов) |
| Proxmox VE | 7+ | источник данных о контейнерах |
| Caddy | 2.7+ | reverse-proxy и TLS (admin API на `:2019`) |
| Step-CA | любой | ACME-центр для выпуска сертификатов |
| dnsmasq | любой | управляется панелью |

## 2. Сборка

```bash
git clone <repo-url> povez
cd povez
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o povez .
```

## 3. Установка плагина в Intermasq

```bash
sudo mkdir -p /etc/intermasq/plugins/povez
sudo cp povez                 /etc/intermasq/plugins/povez/povez
sudo cp manifest.json         /etc/intermasq/plugins/povez/manifest.json
sudo cp config.example.json   /etc/intermasq/plugins/povez/config.json
sudo nano /etc/intermasq/plugins/povez/config.json   # заполнить ключи и узлы
sudo chown -R intermasq:intermasq /etc/intermasq/plugins/povez
sudo systemctl restart intermasq
```

Каталог плагина должен содержать `manifest.json`, исполняемый файл `povez` и
`config.json` (путь можно задать через `CONFIG_PATH`). После перезапуска панели
плагин появляется в меню и доступен по адресу `/plugins/povez/`.

> Автоматическая перезагрузка плагинов не поддерживается: после изменения
> манифеста или исполняемого файла требуется перезапуск панели.

## 4. Конфигурация

Конфигурация считывается из `config.json` (путь переопределяется через
`CONFIG_PATH`). Секция `nodes` обязательна; остальные секции имеют значения по
умолчанию и могут быть опущены. Шаблон — `config.example.json`.

### 4.1 Основные поля

| Поле | Тип | Описание |
|---|---|---|
| `intermasq_url` | string | URL API панели (например `http://172.20.0.1:8080/api`) |
| `intermasq_key` | string | API-ключ панели. **Имеет более низкий приоритет, чем переменная окружения `INTERMASQ_KEY`** |
| `base_domain` | string | суффикс домена (например `.internal`) |
| `nodes` | map | конфигурация узлов PVE (см. ниже) |

### 4.2 Конфигурация узла

| Поле | Описание |
|---|---|
| `subnet` | подсеть контейнеров вида `172.20.5` (три октета) |
| `caddy_url` | URL admin API Caddy на узле (`http://host:2019`) |
| `pve_url` | URL API Proxmox (`https://host:8006/api2/json`) |
| `pve_token_id` | идентификатор токена PVE (`user@pam!token`) |
| `pve_secret` | секрет токена PVE |

### 4.3 Опциональные секции (со значениями по умолчанию)

| Поле секции | По умолчанию | Описание |
|---|---|---|
| `caddy.acme_url` | `https://172.20.0.1:9000/acme/acme/directory` | Step-CA ACME directory |
| `caddy.ca_roots` | `/etc/caddy/root_ca.crt` | путь к root CA PEM |
| `caddy.listen` | `:443` | порт прослушивания Caddy |
| `caddy.upstream_insecure` | `true` | `insecure_skip_verify` для восходящего HTTPS-потока |
| `dnsmasq.conf_dir` | `/etc/dnsmasq.d` | каталог include-файлов dnsmasq |
| `dnsmasq.caddy_file` | `/etc/dnsmasq.d/caddy.conf` | файл хоста Caddy |
| `http.timeout_seconds` | `10` | общий таймаут HTTP-клиентов |
| `proxmox.port_prefix` | `port-` | префикс тега порта |
| `proxmox.proto_prefix` | `proto-` | префикс тега протокола |
| `proxmox.name_prefix` | `name-` | префикс тега имени |
| `proxmox.vmid_base` | `98` | базовый VMID для расчёта суффикса IP |
| `proxmox.insecure_skip_verify` | `true` | пропуск проверки TLS PVE |
| `plugin.tcp_debug_port` | `:5000` | TCP-порт локальной отладки (без панели) |
| `plugin.cert_settle_seconds` | `2` | пауза перед перезапуском Caddy (выпуск сертификата) |

### 4.4 Переменные окружения

| Переменная | Назначение |
|---|---|
| `PLUGIN_SOCKET` | путь Unix-сокета (передаётся панелью; без него включается режим TCP-отладки) |
| `INTERMASQ_KEY` | API-ключ панели (приоритет над `config.intermasq_key`) |
| `STATE_FILE` | путь к `routes.json` (по умолчанию `/etc/intermasq/plugins/povez/routes.json`) |
| `CONFIG_PATH` | путь к config.json (по умолчанию `config.json` в текущем каталоге) |

## 5. Конвенция тегов PVE

Povez считывает теги контейнера или виртуальной машины из Proxmox (поле `tags`;
разделители `,`, `;` и пробел):

| Тег | Назначение |
|---|---|
| `port-XX` | порт службы восходящего потока (обязателен, кроме хостов Caddy) |
| `proto-http` / `proto-https` | протокол восходящего потока (по умолчанию `http`) |
| `name-foo` | переопределение имени (используется в домене) |

Если имя контейнера содержит подстроку `caddy`, он рассматривается как хост
Caddy: для него создаётся только dnsmasq-запись (без маршрута в Caddy).
Подробности — в [INTERNALS.md](INTERNALS.md).

## 6. Подготовка Caddy (на каждом узле)

- Admin API на порту `:2019`.
- Глобальная настройка Step-CA в качестве ACME CA.
- `Restart=always` в системном юните (для принудительного перезапуска — запрос
  `/stop` с автоматическим восстановлением процесса).
- Корневой сертификат Step-CA в `/etc/caddy/root_ca.crt`.

## 7. Первый запуск

1. Откройте интерфейс: `http://<IP_INTERMASQ>:8080/plugins/povez/`.
2. `GET /api/pending` — отобразится список MAC-адресов, ожидающих назначения.
3. Выберите MAC и выполните провижнинг (`POST /api/provision`).
4. `GET /api/state` — убедитесь, что домен появился в таблице маршрутов.
5. Откройте в браузере `https://<name>.<node>.internal` — служба должна быть
   доступна через Caddy с действительным сертификатом Step-CA.

## 8. Обновление плагина

```bash
sudo systemctl stop intermasq
sudo cp povez /etc/intermasq/plugins/povez/povez
sudo chown intermasq:intermasq /etc/intermasq/plugins/povez/povez
sudo systemctl start intermasq
```

Состояние (`routes.json`) сохраняется; данные не теряются.
