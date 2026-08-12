package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func NewIntermasqClient(url, apiKey string) *IntermasqClient {
	return &IntermasqClient{
		BaseURL: strings.TrimRight(url, "/"),
		ApiKey:  apiKey,
		client:  &http.Client{Timeout: 10 * time.Second},
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
		return nil, fmt.Errorf("Intermasq connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Intermasq API error (%d): %s", resp.StatusCode, string(respBody))
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
	endpoint := fmt.Sprintf("/hosts/%s?file=%s", mac, file)
	_, err := c.doRequest("DELETE", endpoint, nil)
	return err
}

func (c *IntermasqClient) Reload() error {
	_, err := c.doRequest("POST", "/reload", nil)
	return err
}

// FindFileByMAC ищет dnsmasq-файл, в котором зарегистрирован MAC, перебирая
// текущий список хостов матери.
func (c *IntermasqClient) FindFileByMAC(mac string) (string, error) {
	hosts, err := c.GetHosts()
	if err != nil {
		return "", err
	}

	mac = strings.ToLower(mac)
	for _, h := range hosts {
		if strings.ToLower(h.Mac) == mac {
			return strings.Split(h.File, "|")[0], nil
		}
	}
	return "", fmt.Errorf("MAC %s не найден", mac)
}
