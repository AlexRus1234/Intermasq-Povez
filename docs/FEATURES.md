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

# Функциональные возможности Povez

## Обзор

Povez — плагин Intermasq, выполняющий автоматическое назначение доменных имён
и выпуск сертификатов для контейнеров Proxmox VE (LXC/QEMU). По MAC-адресу
нового устройства плагин определяет контейнер в Proxmox, рассчитывает IP-адрес
по подсети узла, создаёт DNS-запись в dnsmasq и помещает маршрут reverse-proxy
с политикой TLS в Caddy. Координация Intermasq, Proxmox VE и Caddy
осуществляется из единого процесса sidecar.

---

## Обнаружение контейнеров

- **Сравнение аренд и записей.** `GET /api/pending` сопоставляет аренды
  (`/leases`) и статические записи (`/hosts`) панели Intermasq; устройства,
  отсутствующие в dnsmasq, включаются в список ожидающих назначения.
- **Идентификация по MAC.** MAC-адрес является ключом связи между арендой,
  выданной dnsmasq, и контейнером Proxmox VE.
- **Ориентированный на события запуск.** Назначение инициируется
  администратором для конкретного MAC; фоновый опрос Proxmox не выполняется.

## Автоматическое назначение (Provision)

- **Поиск в Proxmox VE.** По MAC выполняется запрос к
  `/cluster/resources` и сетевой конфигурации; определяется VMID, узел и имя
  контейнера.
- **Расчёт IP-адреса.** Последний октет вычисляется как
  `VMID - vmid_base` (по умолчанию 98) в пределах подсети узла; формула и
  примеры приведены в [INTERNALS.md](INTERNALS.md).
- **DNS-запись в dnsmasq.** Хост-запись помещается в файл
  `<RealNode>x<subnet_octet>.conf` внутри `-conf-dir`; после записи вызывается
  перезагрузка dnsmasq через API панели.
- **Маршрут reverse-proxy в Caddy.** Создаются маршрут
  `proxy-<VMID>-<node>` и политика TLS `tls-<VMID>-<node>` с издателем
  Step-CA ACME; HTTP-01-запрос отключён.
- **Режим «только DNS» (`dnsOnly`).** Создаётся только dnsmasq-запись; маршрут
  и сертификат в Caddy не размещаются.
- **Хосты Caddy.** Если имя контейнера содержит подстроку `caddy`, для него
  создаётся только dnsmasq-запись (без маршрута в Caddy), а IP помещается в
  отдельный файл хоста Caddy.

## Снятие назначения (Deprovision)

- **Проверка состояния.** Удаление допускается только для остановленных
  контейнеров; при попытке снять назначение работающего контейнера возвращается
  ошибка 409.
- **Очистка ресурсов.** Удаляются маршрут и политика TLS из Caddy, запись из
  `routes.json` и хост-запись из dnsmasq; ошибки агрегируются.
- **Перезагрузка dnsmasq.** После удаления записи вызывается перезагрузка через
  API панели (по принципу наилучшего усилия).

## Восстановление конфигурации Caddy (Replay)

- **Перестроение из состояния.** `POST /api/replay` помещает все записи из
  `routes.json` в Caddy методом upsert и выполняет один принудительный
  перезапуск на каждый затронутый узел.
- **Применение после сброса.** Операция используется после сброса Caddy или при
  миграции для восстановления актуальной конфигурации.

## Сертификаты и безопасность

- **Step-CA ACME.** Сертификаты выпускаются внутренним центром сертификации
  Step-CA; внутренний издатель Caddy отключается, чтобы исключить
  самоподписанные сертификаты.
- **Принудительный перезапуск Caddy.** После выпуска сертификата выполняется
  запрос `POST /stop`; системный юнит с `Restart=always` перезапускает процесс,
  вследствие чего Caddy применяет сертификат, минуя устаревший кэш
  `autosave.json`. Обоснование — в [INTERNALS.md](INTERNALS.md).
- **Непривилегированное выполнение.** Плагин работает как дочерний процесс
  панели от имени пользователя `intermasq`; связь осуществляется через
  Unix-сокет с правами `0770`.
- **Аутентификация обратных вызовов.** Запросы к API Intermasq выполняются с
  заголовком `X-API-Key`, значение которого берётся из переменной окружения
  `INTERMASQ_KEY`.

## Интеграция с инфраструктурой

- **Контракт Intermasq.** `manifest.json`, Unix-сокет (`PLUGIN_SOCKET`),
  обратные вызовы в API панели через `INTERMASQ_KEY`, корректное завершение по
  `SIGTERM`.
- **Сосуществование с Pomen.** Идентификаторы маршрутов используют префикс
  `proxy-<VMID>-<node>`, что исключает конфликты с префиксом `pod-` плагина
  Pomen при общем экземпляре Caddy.
- **Минимальные внешние зависимости.** Бэкенд на Go (только стандартная
  библиотека); интерфейс — Vue 3 и Bootstrap 5 через CDN, без этапа сборки.
