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
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// EngineSettings — параметры оркестратора (расположение dnsmasq, формула IP,
// пауза перед рестартом Caddy).
type EngineSettings struct {
	DnsmasqDir string        // каталог include-файлов dnsmasq
	CaddyFile  string        // файл хоста Caddy внутри матери
	VMIDBase   int           // базовый VMID для расчёта IP-суффикса
	CertSettle time.Duration // пауза перед финальным /stop в ReplayCaddy
}

// Engine координирует PVE, Intermasq, Caddy и StateStore для provisioning-а.
type Engine struct {
	pve      PVEFinder
	imq      HostManager
	caddy    RouteManager
	state    StateBackend
	domain   string
	nodes    map[string]NodeConfig
	settings EngineSettings
}

// NewEngine собирает Engine. state обязателен (nil → panic); pve/imq/caddy
// могут быть nil для тестов, упражняющих только один клиент.
func NewEngine(pve PVEFinder, imq HostManager, caddy RouteManager, state StateBackend, domain string, nodes map[string]NodeConfig, s EngineSettings) *Engine {
	if state == nil {
		panic("state must not be nil; pass core.NewStateStore(...) or a NoOp implementation")
	}
	cleanNodes := make(map[string]NodeConfig, len(nodes))
	for k, v := range nodes {
		cleanNodes[strings.ToLower(k)] = v
	}
	return &Engine{
		pve: pve, imq: imq, caddy: caddy, state: state,
		domain: domain, nodes: cleanNodes, settings: s,
	}
}

type PendingDevice struct {
	MAC string `json:"mac"`
	IP  string `json:"ip"`
}

func (e *Engine) GetPendingDevices() ([]PendingDevice, error) {
	hosts, err := e.imq.GetHosts()
	if err != nil {
		return nil, fmt.Errorf("get hosts: %w", err)
	}
	leases, err := e.imq.GetLeases()
	if err != nil {
		return nil, fmt.Errorf("get leases: %w", err)
	}

	knownMacs := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		knownMacs[strings.ToLower(h.Mac)] = struct{}{}
	}

	pending := make([]PendingDevice, 0, len(leases))
	for _, l := range leases {
		if _, ok := knownMacs[strings.ToLower(l.Mac)]; !ok {
			pending = append(pending, PendingDevice{MAC: l.Mac, IP: l.Ip})
		}
	}
	return pending, nil
}

// makeIDs строит route/TLS-идентификаторы по конвенции proxy-<vmid>-<node> / tls-<vmid>-<node>.
func makeIDs(info *ContainerInfo) (routeID, tlsID string) {
	routeID = fmt.Sprintf("proxy-%d-%s", info.VMID, info.NodeKey)
	tlsID = fmt.Sprintf("tls-%d-%s", info.VMID, info.NodeKey)
	return routeID, tlsID
}

// computeIP формирует IP контейнера: последний октет = VMID - VMIDBase в [0,255].
func (e *Engine) computeIP(info *ContainerInfo, nodeConf NodeConfig) (string, error) {
	lastOctet := info.VMID - e.settings.VMIDBase
	if lastOctet < 0 || lastOctet > 255 {
		return "", fmt.Errorf("%w: последний октет %d вне диапазона [0,255] (vmid=%d, base=%d)",
			ErrInvalidIP, lastOctet, info.VMID, e.settings.VMIDBase)
	}
	return fmt.Sprintf("%s.%d", nodeConf.Subnet, lastOctet), nil
}

// Provision находит контейнер, прописывает DNS и (опционально) Caddy-маршрут.
func (e *Engine) Provision(mac string, dnsOnly bool) (string, error) {
	info, err := e.pve.FindByMAC(mac)
	if err != nil {
		return "", fmt.Errorf("find by mac: %w", errors.Join(ErrContainerNotFound, err))
	}

	nodeConf, ok := e.nodes[info.NodeKey]
	if !ok {
		return "", fmt.Errorf("config not found for node %q", info.NodeKey)
	}

	newIP, err := e.computeIP(info, nodeConf)
	if err != nil {
		return "", err
	}

	octets := strings.Split(nodeConf.Subnet, ".")
	if len(octets) < 3 {
		return "", fmt.Errorf("invalid subnet %q", nodeConf.Subnet)
	}
	subnetOctet, err := strconv.Atoi(octets[2])
	if err != nil {
		return "", fmt.Errorf("invalid subnet octet %q: %w", octets[2], err)
	}
	dnsmasqFile := fmt.Sprintf("%s/%sx%02d.conf", e.settings.DnsmasqDir, strings.ToLower(info.RealNode), subnetOctet)

	domain := fmt.Sprintf("%s.%s%s", strings.ToLower(info.Name), strings.ToLower(info.RealNode), e.domain)
	routeID, tlsID := makeIDs(info)

	if info.IsCaddy {
		if err := e.imq.AddHost(mac, newIP, info.Name, e.settings.CaddyFile); err != nil {
			return "", fmt.Errorf("add caddy host: %w", err)
		}
		e.reloadDnsmasq()
		return "Caddy настроен", nil
	}

	if err := e.imq.AddHost(mac, newIP, info.Name, dnsmasqFile); err != nil {
		return "", fmt.Errorf("add DNS host: %w", err)
	}

	if !dnsOnly {
		if err := e.caddy.AddRoute(info.NodeKey, domain, newIP, info.Port, info.Protocol, routeID, tlsID); err != nil {
			return "", fmt.Errorf("DNS ok, caddy error: %w", err)
		}
		if err := e.state.Upsert(RouteRecord{
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

	e.reloadDnsmasq()
	return fmt.Sprintf("Успех! %s -> %s", domain, newIP), nil
}

// Deprovision удаляет Caddy-маршрут, запись состояния и DNS; ошибки агрегируются.
func (e *Engine) Deprovision(mac string) error {
	info, err := e.pve.FindByMAC(mac)
	if err != nil {
		return fmt.Errorf("find by mac: %w", errors.Join(ErrContainerNotFound, err))
	}

	if info.Status != "stopped" {
		return fmt.Errorf("%w: status=%q, stop it first", ErrContainerRunning, info.Status)
	}

	routeID, tlsID := makeIDs(info)

	var errs []error
	if err := e.caddy.DeleteRouteAndTLS(info.NodeKey, routeID, tlsID); err != nil {
		errs = append(errs, fmt.Errorf("caddy delete: %w", err))
	}
	if err := e.state.Remove(routeID); err != nil {
		errs = append(errs, fmt.Errorf("state remove: %w", err))
	}

	if fileName, err := e.imq.FindFileByMAC(mac); err == nil {
		if err := e.imq.DeleteHost(mac, fileName); err != nil {
			errs = append(errs, fmt.Errorf("dns delete: %w", err))
		}
	} else {
		slog.Warn("host file not found for mac", "mac", mac, "err", err)
	}

	e.reloadDnsmasq()

	if len(errs) > 0 {
		return fmt.Errorf("deprovision completed with errors: %w", errors.Join(errs...))
	}
	return nil
}

// reloadDnsmasq перезагружает конфиг матери; ошибка логируется (best-effort).
func (e *Engine) reloadDnsmasq() {
	if err := e.imq.Reload(); err != nil {
		slog.Warn("dnsmasq reload failed", "err", err)
	}
}

// ReplayCaddy восстанавливает конфиг Caddy из State и перезапускает ноды.
func (e *Engine) ReplayCaddy() (int, []string, error) {
	records, err := e.state.Load()
	if err != nil {
		return 0, nil, err
	}
	if len(records) == 0 {
		return 0, nil, nil
	}

	ok := 0
	var errs []string
	touchedNodes := make(map[string]struct{})
	domainByNode := make(map[string]string)

	for _, rec := range records {
		if err := e.caddy.ReplayRoute(rec.Node, rec.Domain, rec.TargetIP, rec.TargetPort, rec.Protocol, rec.RouteID, rec.TLSID); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rec.Domain, err))
			continue
		}
		ok++
		touchedNodes[rec.Node] = struct{}{}
		domainByNode[rec.Node] = rec.Domain
	}

	// Финальный рестарт на каждую затронутую ноду — сброс cert cache.
	// Сначала ждём выпуска сертификата, потом перезапускаем.
	maxWait := e.settings.CertSettle * 15
	if maxWait < 5*time.Second {
		maxWait = 30 * time.Second
	}
	for node := range touchedNodes {
		if err := e.caddy.WaitForCert(node, domainByNode[node], maxWait); err != nil {
			slog.Warn("cert poll failed, restarting anyway", "node", node, "err", err)
		}
		if err := e.caddy.RestartCaddy(node); err != nil {
			errs = append(errs, fmt.Sprintf("restart %s: %v", node, err))
		}
	}

	return ok, errs, nil
}

// GetState отдаёт содержимое файла-таблицы для UI.
func (e *Engine) GetState() ([]RouteRecord, error) {
	return e.state.Load()
}
