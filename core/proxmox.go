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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// macRE достаёт MAC из PVE net*-строки вида virtio=AA:..,bridge=vmbr0 или
// ...hwaddr=AA:.. — игнорирует MAC-подобные значения в bridge/comment.
var macRE = regexp.MustCompile(`(?i)(?:virtio|hwaddr|mac)=([0-9a-fA-F]{2}(?::[0-9a-fA-F]{2}){5})`)

// ProxmoxSettings — конвенция тегов PVE и параметры клиента.
type ProxmoxSettings struct {
	PortPrefix         string        // префикс тега порта ("port-")
	ProtoPrefix        string        // префикс тега протокола ("proto-")
	NamePrefix         string        // префикс тега имени ("name-")
	InsecureSkipVerify bool          // пропускать проверку TLS (PVE обычно self-signed)
	Timeout            time.Duration // таймаут HTTP-клиента
}

// NodeConfig — конфиг одной ноды из config.json.
type NodeConfig struct {
	Subnet     string `json:"subnet"`
	CaddyURL   string `json:"caddy_url"`
	PveURL     string `json:"pve_url"`
	PveTokenID string `json:"pve_token_id"`
	PveSecret  string `json:"pve_secret"`
}

type PveClient struct {
	Nodes  map[string]NodeConfig
	tags   ProxmoxSettings
	client *http.Client
}

type ContainerInfo struct {
	NodeKey  string // Ключ из конфига (например "yadr01") — нужен для поиска Caddy
	RealNode string // Реальное имя ноды в Proxmox (например "YADR01")
	VMID     int
	Name     string
	Status   string
	Port     string
	Protocol string
	IsCaddy  bool
}

func NewPveClient(nodes map[string]NodeConfig, s ProxmoxSettings) *PveClient {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: s.InsecureSkipVerify}}
	if s.Timeout == 0 {
		s.Timeout = 10 * time.Second
	}
	cleanNodes := make(map[string]NodeConfig, len(nodes))
	for k, v := range nodes {
		cleanNodes[strings.ToLower(k)] = v
	}
	return &PveClient{
		Nodes:  cleanNodes,
		tags:   s,
		client: &http.Client{Transport: tr, Timeout: s.Timeout},
	}
}

// request выполняет запрос к API конкретной ноды.
func (p *PveClient) request(conf NodeConfig, method, endpoint string) ([]byte, error) {
	url := strings.TrimRight(conf.PveURL, "/") + endpoint
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", conf.PveTokenID, conf.PveSecret))

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("pve %s %s: HTTP %d", method, endpoint, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// FindByMAC ищет контейнер/ВМ по MAC по всем нодам (сначала LXC, потом QEMU).
func (p *PveClient) FindByMAC(mac string) (*ContainerInfo, error) {
	mac = strings.ToLower(mac)
	slog.Info("proxmox search by mac", "mac", mac)

	for nodeKey, conf := range p.Nodes {
		// scanNode делает один запрос /cluster/resources на ноду и сам перебирает
		// lxc и qemu в нужном порядке. Ошибка одной ноды не валит весь поиск —
		// она логируется внутри scanNode, после чего переходим к следующей ноде.
		if info, err := p.scanNode(nodeKey, conf, mac); err == nil {
			return info, nil
		}
	}
	return nil, fmt.Errorf("MAC %s не найден нигде", mac)
}

// scanNode делает один запрос /cluster/resources для ноды и ищет MAC сначала
// среди LXC, потом среди QEMU — тот же приоритет, что был при раздельных вызовах.
func (p *PveClient) scanNode(nodeKey string, conf NodeConfig, targetMac string) (*ContainerInfo, error) {
	// /cluster/resources — самый надёжный способ узнать VMID и реальное имя ноды.
	resBody, err := p.request(conf, "GET", "/cluster/resources")
	if err != nil {
		slog.Warn("cluster/resources fetch failed", "node", nodeKey, "err", err)
		return nil, err
	}

	var clusterRes struct {
		Data []struct {
			Node   string  `json:"node"`
			Type   string  `json:"type"`
			VMID   float64 `json:"vmid"`
			Name   string  `json:"name"`
			Status string  `json:"status"`
		}
	}
	if err := json.Unmarshal(resBody, &clusterRes); err != nil {
		slog.Warn("parse cluster resources failed", "node", nodeKey, "err", err)
		return nil, fmt.Errorf("parse cluster resources: %w", err)
	}

	// Порядок lxc → qemu детерминирует выбор при прочих равных.
	for _, vmType := range []string{"lxc", "qemu"} {
		for _, item := range clusterRes.Data {
			if item.Type != vmType {
				continue
			}
			info, err := p.matchGuest(nodeKey, conf, item.Node, vmType, int(item.VMID), item.Name, item.Status, targetMac)
			if err == nil {
				return info, nil
			}
		}
	}
	return nil, fmt.Errorf("not found")
}

// matchGuest тащит config конкретного гостя, проверяет MAC и собирает ContainerInfo.
// Любая ошибка (сеть, 5xx, отсутствие тега порта) возвращается наверх — scanNode
// при этом просто перейдёт к следующему гостю.
func (p *PveClient) matchGuest(nodeKey string, conf NodeConfig, realNode, vmType string, vmid int, name, status, targetMac string) (*ContainerInfo, error) {
	// realNode — реальное имя ноды, иначе API вернёт 596/500.
	confBody, err := p.request(conf, "GET", fmt.Sprintf("/nodes/%s/%s/%d/config", realNode, vmType, vmid))
	if err != nil {
		slog.Warn("vm config fetch failed", "vmid", vmid, "node", realNode, "err", err)
		return nil, err
	}

	var vmConf struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(confBody, &vmConf); err != nil {
		slog.Warn("parse vm config failed", "vmid", vmid, "node", realNode, "err", err)
		return nil, err
	}

	// Ищем MAC в net*-интерфейсах, парся только virtio=/hwaddr=/mac= поля,
	// чтобы MAC-подобное значение в bridge= или комментарии не давало ложное совпадение.
	found := false
	for k, v := range vmConf.Data {
		if !strings.HasPrefix(k, "net") {
			continue
		}
		for _, m := range macRE.FindAllStringSubmatch(fmt.Sprint(v), -1) {
			if strings.ToLower(m[1]) == targetMac {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("not found")
	}

	slog.Info("proxmox container found", "name", name, "vmid", vmid, "node", realNode, "key", nodeKey)
	info := &ContainerInfo{
		NodeKey:  nodeKey,
		RealNode: realNode,
		VMID:     vmid,
		Name:     name,
		Status:   status,
		Protocol: "http",
	}
	if tags, ok := vmConf.Data["tags"].(string); ok {
		tags = strings.ReplaceAll(tags, ",", " ")
		tags = strings.ReplaceAll(tags, ";", " ")
		for _, t := range strings.Fields(tags) {
			t = strings.ToLower(strings.TrimSpace(t))
			switch {
			case t == "caddy":
				info.IsCaddy = true
			case strings.HasPrefix(t, p.tags.PortPrefix):
				info.Port = strings.TrimPrefix(t, p.tags.PortPrefix)
			case strings.HasPrefix(t, p.tags.ProtoPrefix):
				info.Protocol = strings.TrimPrefix(t, p.tags.ProtoPrefix)
			case strings.HasPrefix(t, p.tags.NamePrefix):
				info.Name = strings.TrimPrefix(t, p.tags.NamePrefix)
			}
		}
	}

	if !info.IsCaddy && info.Port == "" {
		return nil, fmt.Errorf("у контейнера %s нет тега %sXX", name, p.tags.PortPrefix)
	}
	return info, nil
}
