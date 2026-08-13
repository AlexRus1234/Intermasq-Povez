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

# Changelog

Формат: значимые изменения выделены в отдельные разделы. Ссылки на коммиты
приведены в журнале git.

## [Unreleased]

### Документация

- Приведена в соответствие со структурой документации Pomen: добавлены
  [`FEATURES.md`](FEATURES.md), [`SETUP.md`](SETUP.md) и настоящий
  `CHANGELOG.md`.
- `docs/architecture.md` переименован в [`INTERNALS.md`](INTERNALS.md) и
  переработан в академическом стиле; обновлены перекрёстные ссылки в README.
- `README.md` и `README.en.md` переработаны: устранена неформальная лексика,
  ссылки указывают на `INTERNALS.md`.

### Текущие возможности

- Обнаружение контейнеров: сравнение аренд и статических записей панели
  Intermasq; список MAC-адресов, ожидающих назначения (`GET /api/pending`).
- Автоматическое назначение по MAC: поиск контейнера в Proxmox VE, расчёт
  IP-адреса по подсети узла, DNS-запись в dnsmasq, маршрут reverse-proxy и
  политика TLS в Caddy (`POST /api/provision`, режим `dnsOnly`).
- Снятие назначения: удаление маршрута и политики TLS из Caddy, записи из
  `routes.json` и хост-записи из dnsmasq для остановленного контейнера
  (`DELETE`/`POST /api/deprovision`).
- Восстановление конфигурации Caddy из `routes.json` (`POST /api/replay`).
- Выпуск сертификатов через внутренний Step-CA (ACME); HTTP-01-запрос отключён.
- Контракт Intermasq: `manifest.json`, Unix-сокет (`PLUGIN_SOCKET`), обратные
  вызовы через `INTERMASQ_KEY`, корректное завершение по `SIGTERM`.

### Архитектура и тесты

- Послойная организация: `main` (конфигурация, клиенты, сервер, listener),
  `api` (HTTP-обработчики, проверка методов, типизированные статусы ошибок),
  `core` (Engine и клиенты PVE/Intermasq/Caddy, взаимодействующие через
  интерфейсы).
- Типизированные ошибки `ErrContainerNotFound` / `ErrContainerRunning` /
  `ErrInvalidIP` преобразуются в HTTP 404 / 409 / 400 соответственно.
- Модульные тесты (`*_test.go`) на основе stdlib `testing` и `httptest` без
  testify; внешние сервисы замещаются заглушками `httptest.Server`.
- Принудительный перезапуск Caddy вместо мягкой перезагрузки для применения
  вновь выпущенного сертификата (обоснование — в [INTERNALS.md](INTERNALS.md)).
