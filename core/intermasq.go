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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type IntermasqClient struct {
	BaseURL string
	ApiKey  string
	client  *http.Client
}

type HostEntry struct {
	Mac      string `json:"mac"`
	Ip       string `json:"ip"`
	Hostname string `json:"hostname"`
	File     string `json:"file"`
}

type LeaseEntry struct {
	Ip       string `json:"ip"`
	Mac      string `json:"mac"`
	Hostname string `json:"hostname"`
}

func NewIntermasqClient(url, apiKey string, timeout time.Duration) *IntermasqClient {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &IntermasqClient{
		BaseURL: strings.TrimRight(url, "/"),
		ApiKey:  apiKey,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *IntermasqClient) doRequest(method, endpoint string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, c.BaseURL+endpoint, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-API-Key", c.ApiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("intermasq connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("intermasq API error (%d): %s", resp.StatusCode, string(respBody))
	}
	return io.ReadAll(resp.Body)
}

func (c *IntermasqClient) GetLeases() ([]LeaseEntry, error) {
	body, err := c.doRequest("GET", "/leases", nil)
	if err != nil {
		return nil, err
	}
	var leases []LeaseEntry
	err = json.Unmarshal(body, &leases)
	return leases, err
}

func (c *IntermasqClient) GetHosts() ([]HostEntry, error) {
	body, err := c.doRequest("GET", "/hosts", nil)
	if err != nil {
		return nil, err
	}
	var hosts []HostEntry
	err = json.Unmarshal(body, &hosts)
	return hosts, err
}

func (c *IntermasqClient) AddHost(mac, ip, hostname, file string) error {
	payload := map[string]string{
		"mac": mac, "ip": ip, "hostname": hostname, "file": file,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal host payload: %w", err)
	}
	_, err = c.doRequest("POST", "/hosts", bytes.NewBuffer(data))
	return err
}

func (c *IntermasqClient) DeleteHost(mac, file string) error {
	endpoint := fmt.Sprintf("/hosts/%s?file=%s", url.PathEscape(mac), url.QueryEscape(file))
	_, err := c.doRequest("DELETE", endpoint, nil)
	return err
}

func (c *IntermasqClient) Reload() error {
	_, err := c.doRequest("POST", "/reload", nil)
	return err
}

// TODO(server): Intermasq /hosts endpoint should accept a `?mac=XX` filter
// so we can stop fetching the entire host list on every Deprovision.
func (c *IntermasqClient) FindFileByMAC(mac string) (string, error) {
	hosts, err := c.GetHosts()
	if err != nil {
		return "", err
	}

	mac = strings.ToLower(mac)
	for _, h := range hosts {
		if strings.ToLower(h.Mac) != mac {
			continue
		}
		// Пустой file пропускаем — иначе DeleteHost(mac, "") сделал бы
		// мусорный запрос; ищем дальше среди остальных хостов.
		if h.File == "" {
			continue
		}
		// TODO(server): mother should return file as a structured object, not
		// pipe-delimited string. We take only the path before any `|`.
		return strings.Split(h.File, "|")[0], nil
	}
	return "", fmt.Errorf("host not found by mac %s", mac)
}
