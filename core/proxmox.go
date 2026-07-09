package core

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Структура конфига одной ноды (из config.json)
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
	NodeKey  string // Ключ из конфига (например "yadr01") - нужен для поиска Caddy
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
	// Приводим ключи конфига к нижнему регистру для надежности
	cleanNodes := make(map[string]NodeConfig)
	for k, v := range nodes {
		cleanNodes[strings.ToLower(k)] = v
	}
	return &PveClient{
		Nodes:  cleanNodes,
		client: &http.Client{Transport: tr, Timeout: 10 * time.Second},
	}
}

// Выполняет запрос к конкретной ноде
func (p *PveClient) request(conf NodeConfig, method, endpoint string) ([]byte, error) {
	url := strings.TrimRight(conf.PveURL, "/") + endpoint
	req, _ := http.NewRequest(method, url, nil)
	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", conf.PveTokenID, conf.PveSecret))
	
	resp, err := p.client.Do(req)
	if err != nil { return nil, err }
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ГЛАВНЫЙ ПОИСК
func (p *PveClient) FindByMAC(mac string) (*ContainerInfo, error) {
	mac = strings.ToLower(mac)
	fmt.Printf("[PROXMOX] Ищем %s по всем нодам...\n", mac)

	for nodeKey, conf := range p.Nodes {
		// 1. Ищем в LXC
		if info, err := p.scanType(nodeKey, conf, "lxc", mac); err == nil {
			return info, nil
		}
		// 2. Ищем в QEMU (ВМ)
		if info, err := p.scanType(nodeKey, conf, "qemu", mac); err == nil {
			return info, nil
		}
	}
	return nil, fmt.Errorf("MAC %s не найден нигде", mac)
}

func (p *PveClient) scanType(nodeKey string, conf NodeConfig, vmType, targetMac string) (*ContainerInfo, error) {
	// Шаг 1: Получаем список ресурсов, доступных этому токену
	// Используем /cluster/resources, так как это самый надежный способ узнать ID и реальное имя ноды
	resBody, err := p.request(conf, "GET", "/cluster/resources")
	if err != nil { return nil, err }

	var clusterRes struct { Data []struct { Node string `json:"node"`; Type string `json:"type"`; VMID float64 `json:"vmid"`; Name string `json:"name"`; Status string `json:"status"` } }
	json.Unmarshal(resBody, &clusterRes)

	for _, item := range clusterRes.Data {
		if item.Type != vmType { continue }
		vmid := int(item.VMID)

		// Шаг 2: Запрашиваем конфиг сети и тегов
		// Используем item.Node (реальное имя ноды), чтобы API не вернул 596/500
		confBody, err := p.request(conf, "GET", fmt.Sprintf("/nodes/%s/%s/%d/config", item.Node, vmType, vmid))
		if err != nil { continue }

		var vmConf struct { Data map[string]interface{} `json:"data"` }
		json.Unmarshal(confBody, &vmConf)

		// Шаг 3: Ищем MAC
		found := false
		for k, v := range vmConf.Data {
			if strings.HasPrefix(k, "net") && strings.Contains(strings.ToLower(fmt.Sprint(v)), targetMac) {
				found = true; break
			}
		}

		if found {
			fmt.Printf("[PROXMOX] Найдено: %s (VMID %d) на ноде %s (Key: %s)\n", item.Name, vmid, item.Node, nodeKey)
			
			info := &ContainerInfo{
				NodeKey:  nodeKey,   // Ключ из конфига (yadr01) -> нужен для Caddy URL
				RealNode: item.Node, // Реальное имя (YADR01) -> нужно для имен файлов
				VMID:     vmid,
				Name:     item.Name,
				Status:   item.Status,
				Protocol: "http", 
				Port:     "",     
				IsCaddy:  strings.Contains(strings.ToLower(item.Name), "caddy"),
			}

			if tags, ok := vmConf.Data["tags"].(string); ok {
				// Заменяем любые разделители на пробелы для парсинга
				tags = strings.ReplaceAll(tags, ",", " ")
				tags = strings.ReplaceAll(tags, ";", " ")
				
				for _, t := range strings.Fields(tags) {
					t = strings.ToLower(strings.TrimSpace(t))
					if strings.HasPrefix(t, "port-") { info.Port = strings.TrimPrefix(t, "port-") }
					if strings.HasPrefix(t, "proto-") { info.Protocol = strings.TrimPrefix(t, "proto-") }
					if strings.HasPrefix(t, "name-") { info.Name = strings.TrimPrefix(t, "name-") }
				}
			}
			
			if !info.IsCaddy && info.Port == "" {
				return nil, fmt.Errorf("У контейнера %s нет тега port-XX", item.Name)
			}
			return info, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (p *PveClient) GetStatus(nodeKey string, vmid int) (string, error) {
	conf, ok := p.Nodes[nodeKey]
	if !ok { return "", fmt.Errorf("Конфиг ноды %s не найден", nodeKey) }

	// Пробуем получить статус как LXC, если ошибка - как QEMU
	// Нам нужно знать реальное имя ноды. В данном случае мы можем перебрать resources
	resBody, err := p.request(conf, "GET", "/cluster/resources")
	if err != nil { return "", err }
	
	var clusterRes struct { Data []struct { VMID float64; Status string } }
	json.Unmarshal(resBody, &clusterRes)

	for _, item := range clusterRes.Data {
		if int(item.VMID) == vmid {
			return item.Status, nil
		}
	}
	return "", fmt.Errorf("VMID %d не найден", vmid)
}
