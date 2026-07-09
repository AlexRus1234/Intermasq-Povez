package core

import (
	"fmt"
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
	cleanNodes := make(map[string]NodeConfig)
	for k, v := range nodes {
		cleanNodes[strings.ToLower(k)] = v
	}
	return &Engine{PVE: pve, IMQ: imq, Caddy: caddy, State: state, Domain: domain, Nodes: cleanNodes}
}

type PendingDevice struct {
	MAC string `json:"mac"`
	IP  string `json:"ip"`
}

func (e *Engine) GetPendingDevices() ([]PendingDevice, error) {
	hosts, err := e.IMQ.GetHosts()
	if err != nil { return nil, err }
	leases, err := e.IMQ.GetLeases()
	if err != nil { return nil, err }

	knownMacs := make(map[string]bool)
	for _, h := range hosts { knownMacs[strings.ToLower(h.Mac)] = true }

	var pending []PendingDevice
	for _, l := range leases {
		if !knownMacs[strings.ToLower(l.Mac)] {
			pending = append(pending, PendingDevice{MAC: l.Mac, IP: l.Ip})
		}
	}
	return pending, nil
}

func (e *Engine) Provision(mac string, dnsOnly bool) (string, error) {
	info, err := e.PVE.FindByMAC(mac)
	if err != nil { return "", err }

	nodeConf, ok := e.Nodes[info.NodeKey]
	if !ok { return "", fmt.Errorf("Нет конфига для ноды %s", info.NodeKey) }
	
	suffix := info.VMID - 98
	newIP := fmt.Sprintf("%s.%d", nodeConf.Subnet, suffix)

	octets := strings.Split(nodeConf.Subnet, ".")
	if len(octets) < 3 { return "", fmt.Errorf("Ошибка подсети") }
	qq, _ := strconv.Atoi(octets[2])
	fileName := fmt.Sprintf("/etc/dnsmasq.d/%sx%02d.conf", strings.ToLower(info.RealNode), qq)

	domain := fmt.Sprintf("%s.%s%s", strings.ToLower(info.Name), strings.ToLower(info.RealNode), e.Domain)
	routeID := fmt.Sprintf("proxy-%d-%s", info.VMID, info.NodeKey)
	tlsID := fmt.Sprintf("tls-%d-%s", info.VMID, info.NodeKey)

	if info.IsCaddy {
		e.IMQ.AddHost(mac, newIP, info.Name, "/etc/dnsmasq.d/caddy.conf")
		e.IMQ.Reload()
		return "Caddy настроен", nil
	}

	err = e.IMQ.AddHost(mac, newIP, info.Name, fileName)
	if err != nil { return "", fmt.Errorf("Ошибка DNS: %v", err) }

	// Если dnsOnly - пропускаем настройку Caddy
	if !dnsOnly {
		// ИСПРАВЛЕНО: Передаем 7 аргументов (включая tlsID)
		err = e.Caddy.AddRoute(info.NodeKey, domain, newIP, info.Port, info.Protocol, routeID, tlsID)
		if err != nil { return "", fmt.Errorf("DNS ок, Caddy ошибка: %v", err) }
		if e.State != nil {
			e.State.Upsert(RouteRecord{
				Domain:     domain,
				TargetIP:   newIP,
				TargetPort: info.Port,
				Protocol:   info.Protocol,
				RouteID:    routeID,
				TLSID:      tlsID,
				Node:       info.NodeKey,
			})
		}
	}
	
	e.IMQ.Reload()
	
	return fmt.Sprintf("Успех! %s -> %s", domain, newIP), nil
}

func (e *Engine) Deprovision(mac string) error {
info, err := e.PVE.FindByMAC(mac)
	if err != nil { return err }

	status, _ := e.PVE.GetStatus(info.NodeKey, info.VMID)
	if status != "stopped" { return fmt.Errorf("Контейнер работает!") }

	routeID := fmt.Sprintf("proxy-%d-%s", info.VMID, info.NodeKey)
	tlsID := fmt.Sprintf("tls-%d-%s", info.VMID, info.NodeKey)
	
	// ИСПРАВЛЕНО: Вызов DeleteRouteAndTLS
	e.Caddy.DeleteRouteAndTLS(info.NodeKey, routeID, tlsID)
	if e.State != nil { e.State.Remove(routeID) }

	fileName, err := e.IMQ.FindFileByMAC(mac)
	if err == nil { e.IMQ.DeleteHost(mac, fileName) }

	e.IMQ.Reload()
	return nil
}

// ReplayCaddy перезаписывает конфиг Caddy из файла-таблицы (восстановление после сброса).
// Возвращает количество успешно восстановленных записей и список ошибок.
func (e *Engine) ReplayCaddy() (int, []string, error) {
	if e.State == nil {
		return 0, nil, fmt.Errorf("state store не инициализирован")
	}
	records, err := e.State.Load()
	if err != nil {
		return 0, nil, err
	}
	if len(records) == 0 {
		return 0, nil, nil
	}

	ok := 0
	var errors []string
	touchedNodes := make(map[string]bool)

	for _, rec := range records {
		if err := e.Caddy.ReplayRoute(rec.Node, rec.Domain, rec.TargetIP, rec.TargetPort, rec.Protocol, rec.RouteID, rec.TLSID); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", rec.Domain, err))
			continue
		}
		ok++
		touchedNodes[rec.Node] = true
	}

	// Один финальный /stop на каждую затронутую ноду
	time.Sleep(2 * time.Second)
	for node := range touchedNodes {
		if err := e.Caddy.RestartCaddy(node); err != nil {
			errors = append(errors, fmt.Sprintf("рестарт %s: %v", node, err))
		}
	}

	return ok, errors, nil
}

// GetState отдаёт содержимое файла-таблицы для UI.
func (e *Engine) GetState() ([]RouteRecord, error) {
	if e.State == nil {
		return []RouteRecord{}, nil
	}
	return e.State.Load()
}
