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
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CaddySettings — параметры Caddy-клиента (ACME, CA, порт, таймауты).
type CaddySettings struct {
	ACMEURL          string        // Step-CA ACME directory URL
	CARoots          string        // путь к root CA PEM
	Listen           string        // порт Caddy (":443")
	UpstreamInsecure bool          // insecure_skip_verify для https upstream
	Timeout          time.Duration // таймаут HTTP-клиента
	CertSettleTime   time.Duration // пауза перед /stop (выпуск cert)
}

type CaddyClient struct {
	BaseURLs map[string]string
	settings CaddySettings
	client   *http.Client
}

// NewCaddyClient нормализует ключи нод к нижнему регистру единожды.
func NewCaddyClient(urls map[string]string, s CaddySettings) *CaddyClient {
	clean := make(map[string]string, len(urls))
	for k, v := range urls {
		clean[strings.ToLower(k)] = strings.TrimRight(v, "/")
	}
	if s.Timeout == 0 {
		s.Timeout = 10 * time.Second
	}
	return &CaddyClient{
		BaseURLs: clean,
		settings: s,
		client:   &http.Client{Timeout: s.Timeout},
	}
}

func (c *CaddyClient) baseURL(nodeName string) (string, error) {
	if u, ok := c.BaseURLs[strings.ToLower(nodeName)]; ok {
		return u, nil
	}
	return "", fmt.Errorf("caddy URL not found for node %q", nodeName)
}

// doJSON выполняет запрос с JSON-телом и возвращает ответ. Caller обязан
// закрыть Body.
func (c *CaddyClient) doJSON(method, url string, body interface{}) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.client.Do(req)
}

// generateRouteJSON строит конфиг reverse_proxy-маршрута. Для https upstream
// добавляет transport.tls.insecure_skip_verify из настроек клиента (внутренние
// сервисы обычно с self-signed сертификатом).
func (c *CaddyClient) generateRouteJSON(domain, targetIP, targetPort, protocol, routeID string) map[string]interface{} {
	upstream := map[string]interface{}{"dial": fmt.Sprintf("%s:%s", targetIP, targetPort)}
	transport := map[string]interface{}{"protocol": "http"}
	if protocol == "https" {
		transport["tls"] = map[string]interface{}{"insecure_skip_verify": c.settings.UpstreamInsecure}
	}
	handler := map[string]interface{}{
		"handler":   "reverse_proxy",
		"upstreams": []interface{}{upstream},
		"transport": transport,
	}
	return map[string]interface{}{
		"@id":    routeID,
		"match":  []interface{}{map[string]interface{}{"host": []string{domain}}},
		"handle": []interface{}{handler},
	}
}

// generateTLSPolicy формирует политику TLS: HTTP-01 челлендж отключён,
// сертификат выпускается через Step-CA ACME. Метод — использует настройки
// клиента (ACME URL, CA roots) из CaddySettings.
func (c *CaddyClient) generateTLSPolicy(domain, tlsID string) map[string]interface{} {
	return map[string]interface{}{
		"@id":      tlsID,
		"subjects": []string{domain},
		"issuers": []map[string]interface{}{
			{
				"module":                  "acme",
				"ca":                      c.settings.ACMEURL,
				"trusted_roots_pem_files": []string{c.settings.CARoots},
				"challenges": map[string]interface{}{
					"http": map[string]interface{}{"disabled": true},
				},
			},
		},
	}
}

// upsertByID: если запись с @id существует (GET 200) — PUT, иначе POST.
// createPath и initIfMissing нужны потому, что POST route/tls может вернуть
// 500 при отсутствии родительского контейнера (srv0 / automation.policies),
// тогда надо его инициализировать и повторить попытку.
func (c *CaddyClient) upsertByID(baseURL, id, createPath string, payload map[string]interface{}, initIfMissing func() error) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	getResp, err := c.client.Get(fmt.Sprintf("%s/id/%s", baseURL, id))
	if err == nil {
		getResp.Body.Close()
		if getResp.StatusCode == 200 {
			req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/id/%s", baseURL, id), bytes.NewBuffer(data))
			req.Header.Set("Content-Type", "application/json")
			resp, err := c.client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("PUT %s (%d): %s", id, resp.StatusCode, string(body))
			}
			return nil
		}
	}

	req, _ := http.NewRequest("POST", baseURL+createPath, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 500 && initIfMissing != nil {
		if err := initIfMissing(); err != nil {
			return err
		}
		req2, _ := http.NewRequest("POST", baseURL+createPath, bytes.NewBuffer(data))
		req2.Header.Set("Content-Type", "application/json")
		resp2, err := c.client.Do(req2)
		if err != nil {
			return err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode >= 400 {
			body, _ := io.ReadAll(resp2.Body)
			return fmt.Errorf("POST %s after init (%d): %s", createPath, resp2.StatusCode, string(body))
		}
		return nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s (%d): %s", createPath, resp.StatusCode, string(body))
	}
	return nil
}

// automateCert заставляет Caddy немедленно выпустить сертификат для домена.
func (c *CaddyClient) automateCert(baseURL, domain string) error {
	resp, err := c.doJSON("POST", baseURL+"/config/apps/tls/certificates/automate", []string{domain})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("automate %s (%d): %s", domain, resp.StatusCode, string(body))
	}
	return nil
}

// disableLocalCerts глобально отключает Caddy internal CA (local_certs),
// чтобы сертификаты выпускались только через Step-CA.
func (c *CaddyClient) disableLocalCerts(baseURL string) error {
	resp, err := c.doJSON("POST", baseURL+"/config/apps/tls/local_certs_disabled", true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// initTLSParent создаёт родительский контейнер /config/apps/tls.
func (c *CaddyClient) initTLSParent(baseURL string, tlsPolicy map[string]interface{}) error {
	resp, err := c.doJSON("PUT", baseURL+"/config/apps/tls", map[string]interface{}{
		"automation": map[string]interface{}{
			"policies": []interface{}{tlsPolicy},
		},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// initSrv0 создаёт HTTP-сервер srv0, если его нет.
func (c *CaddyClient) initSrv0(baseURL string, routeConfig map[string]interface{}) error {
	resp, err := c.doJSON("PUT", baseURL+"/config/apps/http/servers/srv0", map[string]interface{}{
		"listen": []string{c.settings.Listen},
		"routes": []interface{}{routeConfig},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// installRouteAndCert — общий путь для AddRoute и ReplayRoute: upsert
// TLS-политики, запуск выпуска сертификата, upsert маршрута. Без финального
// рестарта — его делает AddRoute (сброс кэша сертификатов) или ReplayCaddy
// (один /stop на ноду после пачки записей).
func (c *CaddyClient) installRouteAndCert(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID string) error {
	baseURL, err := c.baseURL(nodeName)
	if err != nil {
		return err
	}

	tlsPolicy := c.generateTLSPolicy(domain, tlsID)
	initTLS := func() error { return c.initTLSParent(baseURL, tlsPolicy) }
	if err := c.upsertByID(baseURL, tlsID, "/config/apps/tls/automation/policies", tlsPolicy, initTLS); err != nil {
		return fmt.Errorf("tls policy: %w", err)
	}

	if err := c.automateCert(baseURL, domain); err != nil {
		return fmt.Errorf("automate cert: %w", err)
	}

	routeConfig := c.generateRouteJSON(domain, targetIP, targetPort, protocol, routeID)
	initRoute := func() error { return c.initSrv0(baseURL, routeConfig) }
	if err := c.upsertByID(baseURL, routeID, "/config/apps/http/servers/srv0/routes", routeConfig, initRoute); err != nil {
		return fmt.Errorf("route: %w", err)
	}
	return nil
}

// AddRoute полностью настраивает домен: отключает local_certs, ставит
// TLS+route и жёстко перезапускает Caddy (NUKE), чтобы сбросить кэш
// сертификатов и поднять свежий cert с диска. Требует Restart=always в
// systemd-юните Caddy.
func (c *CaddyClient) AddRoute(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID string) error {
	baseURL, err := c.baseURL(nodeName)
	if err != nil {
		return err
	}
	slog.Info("caddy add route", "domain", domain, "node", nodeName)

	if err := c.disableLocalCerts(baseURL); err != nil {
		return fmt.Errorf("disable local_certs: %w", err)
	}
	if err := c.installRouteAndCert(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID); err != nil {
		return err
	}

	// Ждём, пока Caddy запишет свежий сертификат Step-CA на диск, затем
	// убиваем процесс — systemd поднимет его, и при старте cert загрузится
	// с диска, минуя плохой кэш.
	slog.Info("caddy route installed, waiting before restart", "domain", domain, "settle", c.settings.CertSettleTime)
	time.Sleep(c.settings.CertSettleTime)
	if err := c.RestartCaddy(nodeName); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	slog.Info("caddy add route done", "domain", domain)
	return nil
}

// ReplayRoute восстанавливает route+TLS по @id без финального рестарта
// (рестарт делается отдельно в Engine.ReplayCaddy после пачки записей).
func (c *CaddyClient) ReplayRoute(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID string) error {
	slog.Info("caddy replay route", "domain", domain, "route_id", routeID)
	return c.installRouteAndCert(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID)
}

// DeleteRouteAndTLS удаляет route и TLS-политику по их @id. 404 трактуется
// как успех (запись уже удалена).
func (c *CaddyClient) DeleteRouteAndTLS(nodeName, routeID, tlsID string) error {
	baseURL, err := c.baseURL(nodeName)
	if err != nil {
		return err
	}
	var errs []string
	for _, id := range []string{routeID, tlsID} {
		req, rerr := http.NewRequest("DELETE", fmt.Sprintf("%s/id/%s", baseURL, id), nil)
		if rerr != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", id, rerr))
			continue
		}
		resp, derr := c.client.Do(req)
		if derr != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", id, derr))
			continue
		}
		status := resp.StatusCode
		resp.Body.Close()
		if status >= 400 && status != http.StatusNotFound {
			errs = append(errs, fmt.Sprintf("%s: HTTP %d", id, status))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("delete errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// RestartCaddy шлёт POST /stop. Systemd (Restart=always) поднимет Caddy обратно.
func (c *CaddyClient) RestartCaddy(nodeName string) error {
	baseURL, err := c.baseURL(nodeName)
	if err != nil {
		return err
	}
	slog.Info("caddy restart (POST /stop)", "node", nodeName)
	resp, err := c.doJSON("POST", baseURL+"/stop", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
