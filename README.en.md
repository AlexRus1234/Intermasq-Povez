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

[Русский](README.md) | **English** |

<div align="center">

<h1>Povez</h1>

**Auto-provisioning plugin for [Intermasq](https://git.alexrus1234.ru/AlexRus1234/Intermasq)**

Povez ties Intermasq, Proxmox VE and Caddy together: given the MAC address of a
new container, it automatically creates a dnsmasq DNS record and a Caddy
reverse-proxy route with a TLS certificate. It runs as a sidecar process
launched by the panel under the `/etc/intermasq/plugins/` contract and
communicates with the panel over a Unix socket.

[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg?style=flat-square)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8.svg?style=flat-square)](https://go.dev/)
[![Platform](https://img.shields.io/badge/Linux-any-1793D1.svg?style=flat-square)](#quick-start)
[![Intermasq](https://img.shields.io/badge/Intermasq-plugin-6f42c1.svg?style=flat-square)](https://git.alexrus1234.ru/AlexRus1234/Intermasq)

</div>

---

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Plugin API](#plugin-api)
- [PVE tag convention](#pve-tag-convention)
- [How it works](#how-it-works)
- [Project structure](#project-structure)
- [Tech stack](#tech-stack)
- [Documentation](#documentation)
- [License](#license)

> Architectural details — the PVE tag convention, the IP-allocation formula,
> the rationale behind the forced restart of Caddy and the manifest contract —
> are provided in [`docs/INTERNALS.md`](docs/INTERNALS.md).

The project is built against a predefined architecture; an AI assistant was
used during source preparation.[^1]

---

## Features

- **Discovery** of new containers: the panel's ARP/dhcp leases are diffed
  against the registered hosts, producing a list of MAC addresses pending
  assignment.
- **Auto-provisioning** by MAC: the container is located in Proxmox VE
  (LXC/QEMU), the IP is computed from the node subnet, and a dnsmasq host
  record together with a Caddy reverse-proxy route and TLS policy are created.
- **DNS-only mode** (`dnsOnly`): only the dnsmasq record is added; Caddy is not
  contacted.
- **Deprovisioning**: removes the Caddy route and TLS policy and the dnsmasq
  host record for a stopped container.
- **Replay**: rebuilds the Caddy configuration from the on-disk `routes.json`
  after a Caddy reset or restart (upsert of every record and one restart per
  node).
- **Step-CA ACME**: certificates are issued by an internal Step-CA; the HTTP-01
  challenge is disabled.
- **Intermasq contract**: `manifest.json`, Unix socket (`PLUGIN_SOCKET`),
  callbacks into the panel API via `INTERMASQ_KEY`, graceful SIGTERM handling.

---

## Quick start

### Requirements

| Component | Version | Purpose |
|---|---|---|
| Go | 1.25+ | build the plugin |
| Intermasq | latest | host panel (plugin contract) |
| Proxmox VE | 7+ | source of container data |
| Caddy | 2.7+ | reverse proxy + TLS (admin API on `:2019`) |
| Step-CA | any | ACME CA for certificates |
| dnsmasq | any | managed by the panel |

### Build

```bash
git clone <repo-url> povez
cd povez
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o povez .
```

### Install into Intermasq

```bash
sudo mkdir -p /etc/intermasq/plugins/povez
sudo cp povez        /etc/intermasq/plugins/povez/povez
sudo cp manifest.json /etc/intermasq/plugins/povez/manifest.json
sudo cp config.example.json /etc/intermasq/plugins/povez/config.json
sudo nano /etc/intermasq/plugins/povez/config.json   # fill keys/nodes
sudo systemctl restart intermasq
```

The plugin directory must contain `manifest.json`, the `povez` binary and
`config.json` (or set its path via `CONFIG_PATH`). After a panel restart the
plugin appears in the menu and is served at `/plugins/povez/`.

> Hot-reload of plugins is not supported — restart the panel after changing
> the manifest or binary.

---

## Configuration

Config is read from `config.json` (path overridable via `CONFIG_PATH`).
The `nodes` section is required; all other sections have sensible defaults
and may be omitted. See `config.example.json`.

### Top-level fields

| Field | Type | Description |
|---|---|---|
| `intermasq_url` | string | panel API URL (e.g. `http://172.20.0.1:8080/api`) |
| `intermasq_key` | string | panel API key. **Lower priority than the `INTERMASQ_KEY` env** |
| `base_domain` | string | domain suffix (e.g. `.internal`) |
| `nodes` | map | per-node PVE config (see below) |

### Node config

| Field | Description |
|---|---|
| `subnet` | container subnet of the form `172.20.5` (3 octets) |
| `caddy_url` | Caddy admin API URL on the node (`http://host:2019`) |
| `pve_url` | Proxmox API URL (`https://host:8006/api2/json`) |
| `pve_token_id` | PVE API token ID (`user@pam!token`) |
| `pve_secret` | PVE API token secret |

### Optional sections (with defaults)

| Section.field | Default | Description |
|---|---|---|
| `caddy.acme_url` | `https://172.20.0.1:9000/acme/acme/directory` | Step-CA ACME directory |
| `caddy.ca_roots` | `/etc/caddy/root_ca.crt` | root CA PEM path |
| `caddy.listen` | `:443` | Caddy listen port |
| `caddy.upstream_insecure` | `true` | `insecure_skip_verify` for https upstream |
| `dnsmasq.conf_dir` | `/etc/dnsmasq.d` | dnsmasq include directory |
| `dnsmasq.caddy_file` | `/etc/dnsmasq.d/caddy.conf` | Caddy host file |
| `http.timeout_seconds` | `10` | shared HTTP client timeout |
| `proxmox.port_prefix` | `port-` | port-tag prefix |
| `proxmox.proto_prefix` | `proto-` | protocol-tag prefix |
| `proxmox.name_prefix` | `name-` | name-tag prefix |
| `proxmox.vmid_base` | `98` | base VMID for the IP-offset formula |
| `proxmox.insecure_skip_verify` | `true` | skip PVE TLS verification |
| `plugin.tcp_debug_port` | `:5000` | TCP port for local debug (without panel) |
| `plugin.cert_settle_seconds` | `2` | delay before Caddy restart (cert issuance) |

### Environment variables

| Variable | Purpose |
|---|---|
| `PLUGIN_SOCKET` | Unix socket path (injected by the panel; without it → TCP debug mode) |
| `INTERMASQ_KEY` | panel API key (overrides `config.intermasq_key`) |
| `STATE_FILE` | path to `routes.json` (default `/etc/intermasq/plugins/povez/routes.json`) |
| `CONFIG_PATH` | path to config.json (default `config.json` in CWD) |

---

## Plugin API

All endpoints are mounted by the panel under `/plugins/povez/*` (auth is
enforced by the panel before proxying).

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | UI (`index.html`, Vue 3 SPA) |
| `GET` | `/health` | healthcheck (`{status, plugin, version}`) |
| `GET` | `/api/pending` | unknown MACs (leases without a host record) |
| `POST` | `/api/provision` | provision: `{mac, dnsOnly}` |
| `DELETE`/`POST` | `/api/deprovision` | remove configs: `{mac}` |
| `GET` | `/api/state` | contents of `routes.json` |
| `POST` | `/api/replay` | rebuild Caddy from `routes.json` |

---

## PVE tag convention

Povez reads container/VM tags from Proxmox (the `tags` field; `,`/`;`/space
separated):

| Tag | Purpose |
|---|---|
| `port-XX` | upstream service port (required, except for Caddy hosts) |
| `proto-http` / `proto-https` | upstream protocol (default `http`) |
| `name-foo` | name override (used in the domain) |

If the container name contains the substring `caddy`, it is treated as a
Caddy host: only a dnsmasq record is created (no Caddy route).

Details in [`docs/INTERNALS.md`](docs/INTERNALS.md).

---

## How it works

1. **Discovery.** `GET /api/pending` diffs the panel's `/leases` against
   `/hosts`; devices not yet present in dnsmasq are reported as pending.
2. **Provision.** The container is looked up in PVE (`/cluster/resources` and
   the network configuration); port and protocol are obtained from the tags; the
   IP is computed as `<subnet>.<VMID - vmid_base>`. A host record is written to
   dnsmasq, a reverse_proxy route and a TLS policy (Step-CA ACME) are pushed to
   Caddy, and Caddy is forcibly restarted (`POST /stop` → `Restart=always`) so
   that the newly issued certificate is applied.
3. **Deprovision.** The container is verified to be `stopped`; the Caddy route
   and TLS policy and the dnsmasq host record are then removed.
4. **Replay.** Every record from `routes.json` is upserted into Caddy
   (PUT/POST by `@id`), followed by a single `/stop` per touched node.

> The forced restart instead of a soft reload is intentional: Caddy caches the
> certificate in `autosave.json`, and a soft reload does not apply a certificate
> freshly issued by Step-CA. A forced restart forces Caddy to read the
> certificate from disk. See `docs/INTERNALS.md` for details.

---

## Project structure

```
povez/
├── main.go              # entry point: config, clients, server, listener
├── manifest.json        # {id, name, bin} — Intermasq contract
├── config.example.json  # config template (→ config.json)
├── index.html           # UI (Vue 3 SPA)
├── go.mod
├── api/
│   └── routes.go        # HTTP handlers, method guards, DTOs
├── core/
│   ├── caddy.go         # Caddy admin API (upsert route/TLS, restart)
│   ├── engine.go        # orchestrator: Provision / Deprovision / Replay
│   ├── intermasq.go     # HTTP client for the panel API
│   ├── proxmox.go       # PVE client (lookup by MAC, tag parsing)
│   └── state.go         # JSON state store (atomic write)
├── docs/
│   ├── FEATURES.md      # features
│   ├── SETUP.md         # installation and setup
│   ├── INTERNALS.md     # internal architecture
│   └── CHANGELOG.md     # change log
├── .forgejo/workflows/  # CI: build/test/publish + mirror
├── LICENSE              # AGPL-3.0
└── README.md / README.en.md
```

---

## Tech stack

- **Go 1.25** — standard library only, no external dependencies
- **Vue 3 + Bootstrap 5** — UI (CDN, no bundler)
- **log/slog** — structured logging
- **httptest** — tests (stdlib `testing`, no testify)
- **Forgejo Actions** — CI on `fedora:44`

---

## Documentation

- [`docs/FEATURES.md`](docs/FEATURES.md) — features
- [`docs/SETUP.md`](docs/SETUP.md) — installation and setup
- [`docs/INTERNALS.md`](docs/INTERNALS.md) — internal architecture
- [`docs/CHANGELOG.md`](docs/CHANGELOG.md) — change log

---

## License

Copyright (C) 2026 AlexRus1234. Licensed under the
**GNU Affero General Public License v3.0**. Full text in [`LICENSE`](LICENSE).

---

[^1]: An AI assistant was used to prepare source code against a predefined
      architecture; all design decisions, behaviour and Intermasq-contract
      compatibility were verified by the author.
