// Povez - Intermasq provisioning plugin
// Copyright (C) 2026 AlexRus1234
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package core

import "time"

// PVEFinder — поиск контейнера по MAC в Proxmox.
type PVEFinder interface {
	FindByMAC(mac string) (*ContainerInfo, error)
}

// HostManager — управление dnsmasq-конфигом матери через Intermasq.
type HostManager interface {
	GetHosts() ([]HostEntry, error)
	GetLeases() ([]LeaseEntry, error)
	AddHost(mac, ip, hostname, file string) error
	DeleteHost(mac, file string) error
	Reload() error
	FindFileByMAC(mac string) (string, error)
}

// RouteManager — установка/удаление маршрутов и TLS в Caddy, рестарт ноды.
type RouteManager interface {
	AddRoute(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID string) error
	DeleteRouteAndTLS(nodeName, routeID, tlsID string) error
	ReplayRoute(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID string) error
	RestartCaddy(nodeName string) error
	WaitForCert(nodeName, domain string, maxWait time.Duration) error
}

// StateBackend — персистентное хранилище таблицы маршрутов.
type StateBackend interface {
	Load() ([]RouteRecord, error)
	Upsert(rec RouteRecord) error
	Remove(routeID string) error
}
