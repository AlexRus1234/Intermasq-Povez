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

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

type Engine struct {
	PVE    *PveClient
	IMQ    *IntermasqClient
	Caddy  *CaddyClient
	State  *StateStore
	Domain string
	Nodes  map[string]NodeConfig
}

func NewEngine(pve *PveClient, imq *IntermasqClient, caddy *CaddyClient, state *StateStore, domain string, nodes map[string]NodeConfig) *Engine {
	cleanNodes := make(map[string]NodeConfig, len(nodes))
	for k, v := range nodes {
		cleanNodes[strings.ToLower(k)] = v
	}
	return &Engine{
		PVE: pve, IMQ: imq, Caddy: caddy, State: state,
		Domain: domain, Nodes: cleanNodes,
	}
}

type PendingDevice struct {
	MAC string `json:"mac"`
	IP  string `json:"ip"`
}

func (e *Engine) GetPendingDevices() ([]PendingDevice, error) {
	hosts, err := e.IMQ.GetHosts()
	if err != nil {
		return nil, fmt.Errorf("get hosts: %w", err)
	}
	leases, err := e.IMQ.GetLeases()
	if err != nil {
		return nil, fmt.Errorf("get leases: %w", err)
	}

	knownMacs := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		knownMacs[strings.ToLower(h.Mac)] = struct{}{}
	}

	pending := []PendingDevice{}
	for _, l := range leases {
		if _, ok := knownMacs[strings.ToLower(l.Mac)]; !ok {
			pending = append(pending, PendingDevice{MAC: l.Mac, IP: l.Ip})
		}
	}
	return pending, nil
}

// vmidBase — базовый VMID, от которого считается IP-суффикс контейнера
// (VMID 99 → суффикс 1, 100 → 2, ...).
// TODO(eta3): вынести в конфиг ноды.
const vmidBase = 98

func (e *Engine) Provision(mac string, dnsOnly bool) (string, error) {
	info, err := e.PVE.FindByMAC(mac)
	if err != nil {
		return "", err
	}

	nodeConf, ok := e.Nodes[info.NodeKey]
	if !ok {
		return "", fmt.Errorf("config not found for node %q", info.NodeKey)
	}

	newIP := fmt.Sprintf("%s.%d", nodeConf.Subnet, info.VMID-vmidBase)

	octets := strings.Split(nodeConf.Subnet, ".")
	if len(octets) < 3 {
		return "", fmt.Errorf("invalid subnet %q", nodeConf.Subnet)
	}
	subnetOctet, err := strconv.Atoi(octets[2])
	if err != nil {
		return "", fmt.Errorf("invalid subnet octet %q: %w", octets[2], err)
	}
	dnsmasqFile := fmt.Sprintf("/etc/dnsmasq.d/%sx%02d.conf", strings.ToLower(info.RealNode), subnetOctet)

	domain := fmt.Sprintf("%s.%s%s", strings.ToLower(info.Name), strings.ToLower(info.RealNode), e.Domain)
	routeID := fmt.Sprintf("proxy-%d-%s", info.VMID, info.NodeKey)
	tlsID := fmt.Sprintf("tls-%d-%s", info.VMID, info.NodeKey)

	if info.IsCaddy {
		if err := e.IMQ.AddHost(mac, newIP, info.Name, "/etc/dnsmasq.d/caddy.conf"); err != nil {
			return "", fmt.Errorf("add caddy host: %w", err)
		}
		e.reloadDnsmasq()
		return "Caddy настроен", nil
	}

	if err := e.IMQ.AddHost(mac, newIP, info.Name, dnsmasqFile); err != nil {
		return "", fmt.Errorf("add DNS host: %w", err)
	}

	if !dnsOnly {
		if err := e.Caddy.AddRoute(info.NodeKey, domain, newIP, info.Port, info.Protocol, routeID, tlsID); err != nil {
			return "", fmt.Errorf("DNS ok, caddy error: %w", err)
		}
		if e.State != nil {
			if err := e.State.Upsert(RouteRecord{
				Domain:     domain,
				TargetIP:   newIP,
				TargetPort: info.Port,
				Protocol:   info.Protocol,
				RouteID:    routeID,
				TLSID:      tlsID,
				Node:       info.NodeKey,
			}); err != nil {
				slog.Warn("state upsert failed", "route_id", routeID, "err", err)
			}
		}
	}

	e.reloadDnsmasq()
	return fmt.Sprintf("Успех! %s -> %s", domain, newIP), nil
}

func (e *Engine) Deprovision(mac string) error {
	info, err := e.PVE.FindByMAC(mac)
	if err != nil {
		return err
	}

	status, err := e.PVE.GetStatus(info.NodeKey, info.VMID)
	if err != nil {
		return fmt.Errorf("get container status: %w", err)
	}
	if status != "stopped" {
		return fmt.Errorf("container is running (status=%q), stop it first", status)
	}

	routeID := fmt.Sprintf("proxy-%d-%s", info.VMID, info.NodeKey)
	tlsID := fmt.Sprintf("tls-%d-%s", info.VMID, info.NodeKey)

	if err := e.Caddy.DeleteRouteAndTLS(info.NodeKey, routeID, tlsID); err != nil {
		slog.Warn("caddy delete failed", "mac", mac, "err", err)
	}
	if e.State != nil {
		if err := e.State.Remove(routeID); err != nil {
			slog.Warn("state remove failed", "route_id", routeID, "err", err)
		}
	}

	if fileName, err := e.IMQ.FindFileByMAC(mac); err == nil {
		if err := e.IMQ.DeleteHost(mac, fileName); err != nil {
			slog.Warn("delete host failed", "mac", mac, "err", err)
		}
	} else {
		slog.Warn("host file not found for mac", "mac", mac, "err", err)
	}

	e.reloadDnsmasq()
	return nil
}

// reloadDnsmasq перезагружает конфиг матери; ошибка логируется, но не валит
// операцию (DNsmasq перечитает конфиг при следующем reload/рестарте).
func (e *Engine) reloadDnsmasq() {
	if err := e.IMQ.Reload(); err != nil {
		slog.Warn("dnsmasq reload failed", "err", err)
	}
}

// ReplayCaddy перезаписывает конфиг Caddy из файла-таблицы (восстановление
// после сброса). Возвращает количество восстановленных записей и список ошибок.
func (e *Engine) ReplayCaddy() (int, []string, error) {
	if e.State == nil {
		return 0, nil, fmt.Errorf("state store not initialized")
	}
	records, err := e.State.Load()
	if err != nil {
		return 0, nil, err
	}
	if len(records) == 0 {
		return 0, nil, nil
	}

	ok := 0
	var errs []string
	touchedNodes := make(map[string]struct{})

	for _, rec := range records {
		if err := e.Caddy.ReplayRoute(rec.Node, rec.Domain, rec.TargetIP, rec.TargetPort, rec.Protocol, rec.RouteID, rec.TLSID); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rec.Domain, err))
			continue
		}
		ok++
		touchedNodes[rec.Node] = struct{}{}
	}

	// Один финальный /stop на каждую затронутую ноду — сброс cert cache.
	time.Sleep(2 * time.Second)
	for node := range touchedNodes {
		if err := e.Caddy.RestartCaddy(node); err != nil {
			errs = append(errs, fmt.Sprintf("restart %s: %v", node, err))
		}
	}

	return ok, errs, nil
}

// GetState отдаёт содержимое файла-таблицы для UI.
func (e *Engine) GetState() ([]RouteRecord, error) {
	if e.State == nil {
		return []RouteRecord{}, nil
	}
	return e.State.Load()
}
