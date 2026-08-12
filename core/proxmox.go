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
	"strings"
	"time"
)

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

func NewPveClient(nodes map[string]NodeConfig) *PveClient {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	cleanNodes := make(map[string]NodeConfig, len(nodes))
	for k, v := range nodes {
		cleanNodes[strings.ToLower(k)] = v
	}
	return &PveClient{
		Nodes:  cleanNodes,
		client: &http.Client{Transport: tr, Timeout: 10 * time.Second},
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
		if info, err := p.scanType(nodeKey, conf, "lxc", mac); err == nil {
			return info, nil
		}
		if info, err := p.scanType(nodeKey, conf, "qemu", mac); err == nil {
			return info, nil
		}
	}
	return nil, fmt.Errorf("MAC %s не найден нигде", mac)
}

func (p *PveClient) scanType(nodeKey string, conf NodeConfig, vmType, targetMac string) (*ContainerInfo, error) {
	// /cluster/resources — самый надёжный способ узнать VMID и реальное имя ноды.
	resBody, err := p.request(conf, "GET", "/cluster/resources")
	if err != nil {
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
		return nil, fmt.Errorf("parse cluster resources: %w", err)
	}

	for _, item := range clusterRes.Data {
		if item.Type != vmType {
			continue
		}
		vmid := int(item.VMID)

		// item.Node — реальное имя ноды, иначе API вернёт 596/500.
		confBody, err := p.request(conf, "GET", fmt.Sprintf("/nodes/%s/%s/%d/config", item.Node, vmType, vmid))
		if err != nil {
			continue
		}

		var vmConf struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(confBody, &vmConf); err != nil {
			slog.Warn("parse vm config failed", "vmid", vmid, "node", item.Node, "err", err)
			continue
		}

		// Ищем MAC в net*-интерфейсах.
		found := false
		for k, v := range vmConf.Data {
			if strings.HasPrefix(k, "net") && strings.Contains(strings.ToLower(fmt.Sprint(v)), targetMac) {
				found = true
				break
			}
		}

		if found {
			slog.Info("proxmox container found", "name", item.Name, "vmid", vmid, "node", item.Node, "key", nodeKey)
			info := &ContainerInfo{
				NodeKey:  nodeKey,
				RealNode: item.Node,
				VMID:     vmid,
				Name:     item.Name,
				Status:   item.Status,
				Protocol: "http",
				IsCaddy:  strings.Contains(strings.ToLower(item.Name), "caddy"),
			}
			if tags, ok := vmConf.Data["tags"].(string); ok {
				tags = strings.ReplaceAll(tags, ",", " ")
				tags = strings.ReplaceAll(tags, ";", " ")
				for _, t := range strings.Fields(tags) {
					t = strings.ToLower(strings.TrimSpace(t))
					switch {
					case strings.HasPrefix(t, "port-"):
						info.Port = strings.TrimPrefix(t, "port-")
					case strings.HasPrefix(t, "proto-"):
						info.Protocol = strings.TrimPrefix(t, "proto-")
					case strings.HasPrefix(t, "name-"):
						info.Name = strings.TrimPrefix(t, "name-")
					}
				}
			}

			if !info.IsCaddy && info.Port == "" {
				return nil, fmt.Errorf("у контейнера %s нет тега port-XX", item.Name)
			}
			return info, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

// GetStatus возвращает текущий статус (running/stopped/...) контейнера/ВМ.
func (p *PveClient) GetStatus(nodeKey string, vmid int) (string, error) {
	conf, ok := p.Nodes[nodeKey]
	if !ok {
		return "", fmt.Errorf("config not found for node %q", nodeKey)
	}

	resBody, err := p.request(conf, "GET", "/cluster/resources")
	if err != nil {
		return "", err
	}

	var clusterRes struct {
		Data []struct {
			VMID   float64
			Status string
		}
	}
	if err := json.Unmarshal(resBody, &clusterRes); err != nil {
		return "", fmt.Errorf("parse cluster resources: %w", err)
	}

	for _, item := range clusterRes.Data {
		if int(item.VMID) == vmid {
			return item.Status, nil
		}
	}
	return "", fmt.Errorf("VMID %d не найден", vmid)
}
