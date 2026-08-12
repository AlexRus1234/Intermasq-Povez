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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
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

// caddyRoute / caddyMatch / caddyHandler / caddyUpstream / caddyTransport /
// caddyTLS — типизированное представление reverse_proxy-маршрута Caddy. Поля
// отсортированы по JSON-тегу, чтобы маршалинг был побайтово идентичен старому
// выводу через map[string]interface{} (keys сортируются алфавитом).
type caddyRoute struct {
	ID     string         `json:"@id"`
	Handle []caddyHandler `json:"handle"`
	Match  []caddyMatch   `json:"match"`
}

type caddyMatch struct {
	Host []string `json:"host"`
}

type caddyHandler struct {
	Handler   string          `json:"handler"`
	Transport caddyTransport  `json:"transport"`
	Upstreams []caddyUpstream `json:"upstreams"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
}

type caddyTransport struct {
	Protocol string    `json:"protocol"`
	TLS      *caddyTLS `json:"tls,omitempty"`
}

type caddyTLS struct {
	InsecureSkipVerify bool `json:"insecure_skip_verify"`
}

// caddyTLSPolicy / caddyIssuer / caddyChallenges / caddyHTTPChallenge —
// типизированное представление automation policy для Step-CA ACME.
type caddyTLSPolicy struct {
	ID       string        `json:"@id"`
	Issuers  []caddyIssuer `json:"issuers"`
	Subjects []string      `json:"subjects"`
}

type caddyIssuer struct {
	CA                   string          `json:"ca"`
	Challenges           caddyChallenges `json:"challenges"`
	Module               string          `json:"module"`
	TrustedRootsPEMFiles []string        `json:"trusted_roots_pem_files"`
}

type caddyChallenges struct {
	HTTP caddyHTTPChallenge `json:"http"`
}

type caddyHTTPChallenge struct {
	Disabled bool `json:"disabled"`
}

// generateRouteJSON строит конфиг reverse_proxy-маршрута. Для https upstream
// добавляет transport.tls.insecure_skip_verify из настроек клиента (внутренние
// сервисы обычно с self-signed сертификатом).
func (c *CaddyClient) generateRouteJSON(domain, targetIP, targetPort, protocol, routeID string) caddyRoute {
	handler := caddyHandler{
		Handler:   "reverse_proxy",
		Upstreams: []caddyUpstream{{Dial: fmt.Sprintf("%s:%s", targetIP, targetPort)}},
		Transport: caddyTransport{Protocol: "http"},
	}
	if protocol == "https" {
		handler.Transport.TLS = &caddyTLS{InsecureSkipVerify: c.settings.UpstreamInsecure}
	}
	return caddyRoute{
		ID:     routeID,
		Handle: []caddyHandler{handler},
		Match:  []caddyMatch{{Host: []string{domain}}},
	}
}

// generateTLSPolicy формирует политику TLS: HTTP-01 челлендж отключён,
// сертификат выпускается через Step-CA ACME по настройкам клиента.
func (c *CaddyClient) generateTLSPolicy(domain, tlsID string) caddyTLSPolicy {
	return caddyTLSPolicy{
		ID:       tlsID,
		Subjects: []string{domain},
		Issuers: []caddyIssuer{{
			Module:               "acme",
			CA:                   c.settings.ACMEURL,
			TrustedRootsPEMFiles: []string{c.settings.CARoots},
			Challenges: caddyChallenges{
				HTTP: caddyHTTPChallenge{Disabled: true},
			},
		}},
	}
}

// upsertByID: если запись с @id существует (GET 200) — PUT, иначе POST.
// createPath и initIfMissing нужны потому, что POST route/tls может вернуть
// 500 при отсутствии родительского контейнера (srv0 / automation.policies),
// тогда надо его инициализировать и повторить попытку.
//
// parentExists защищает от перетирания данных: POST может вернуть 500 не
// только из-за отсутствия родителя (бывает конфликт @id, ошибки валидации и
// т.п.). Если в этом случае безусловно вызвать initIfMissing, то PUT
// /config/apps/http/servers/srv0 с единичным маршрутом затрёт уже
// существующие routes. Поэтому перед initIfMissing вызывается parentExists —
// если родитель уже есть, init пропускается и возвращается исходная ошибка.
func (c *CaddyClient) upsertByID(
	baseURL, id, createPath string,
	payload interface{},
	parentExists func() (bool, error),
	initIfMissing func() error,
) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	getResp, err := c.client.Get(fmt.Sprintf("%s/id/%s", baseURL, id))
	if err != nil {
		return fmt.Errorf("GET %s: %w", id, err)
	}
	defer getResp.Body.Close()

	// Существующий @id → обновляем через PUT.
	if getResp.StatusCode == http.StatusOK {
		req, err := http.NewRequest("PUT", fmt.Sprintf("%s/id/%s", baseURL, id), bytes.NewBuffer(data))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
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

	// Любой статус, кроме 404, на GET — это ошибка (5xx на стороне Caddy,
	// конфликт и т.п.), а не "отсутствует". НЕ проваливаемся в POST, иначе
	// есть риск создать дубликат по @id.
	if getResp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(getResp.Body)
		return fmt.Errorf("GET %s (%d): %s", id, getResp.StatusCode, string(body))
	}

	// 404 — запись действительно отсутствует, создаём через POST.
	req, err := http.NewRequest("POST", baseURL+createPath, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusInternalServerError && initIfMissing != nil {
		// Прежде чем пересоздавать родителя, убедимся что его действительно
		// нет — иначе затрём уже существующие routes/policies одним routes.
		if parentExists != nil {
			exists, perr := parentExists()
			if perr != nil {
				return fmt.Errorf("parent check before init: %w", perr)
			}
			if exists {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("POST %s (%d): parent already exists, init skipped to avoid clobber: %s",
					createPath, resp.StatusCode, string(body))
			}
		}
		if err := initIfMissing(); err != nil {
			return err
		}
		req2, err := http.NewRequest("POST", baseURL+createPath, bytes.NewBuffer(data))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
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

// pathExists проверяет наличие узла конфигурации по пути (не по @id). true =
// узел есть (200), false = 404 (узла нет), ошибка — любой другой статус или
// сетевая проблема. Используется upsertByID как parentExists перед init.
func (c *CaddyClient) pathExists(baseURL, cfgPath string) (bool, error) {
	resp, err := c.client.Get(baseURL + cfgPath)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("GET %s (%d): %s", cfgPath, resp.StatusCode, string(body))
	}
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
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("disable local_certs (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// initTLSParent создаёт родительский контейнер /config/apps/tls.
func (c *CaddyClient) initTLSParent(baseURL string, tlsPolicy interface{}) error {
	resp, err := c.doJSON("PUT", baseURL+"/config/apps/tls", map[string]interface{}{
		"automation": map[string]interface{}{
			"policies": []interface{}{tlsPolicy},
		},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("init tls parent (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// initSrv0 создаёт HTTP-сервер srv0, если его нет.
func (c *CaddyClient) initSrv0(baseURL string, routeConfig interface{}) error {
	resp, err := c.doJSON("PUT", baseURL+"/config/apps/http/servers/srv0", map[string]interface{}{
		"listen": []string{c.settings.Listen},
		"routes": []interface{}{routeConfig},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("init srv0 (%d): %s", resp.StatusCode, string(body))
	}
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
	tlsParentExists := func() (bool, error) { return c.pathExists(baseURL, "/config/apps/tls") }
	if err := c.upsertByID(baseURL, tlsID, "/config/apps/tls/automation/policies", tlsPolicy, tlsParentExists, initTLS); err != nil {
		return fmt.Errorf("tls policy: %w", err)
	}

	if err := c.automateCert(baseURL, domain); err != nil {
		return fmt.Errorf("automate cert: %w", err)
	}

	routeConfig := c.generateRouteJSON(domain, targetIP, targetPort, protocol, routeID)
	initRoute := func() error { return c.initSrv0(baseURL, routeConfig) }
	srv0Exists := func() (bool, error) { return c.pathExists(baseURL, "/config/apps/http/servers/srv0") }
	if err := c.upsertByID(baseURL, routeID, "/config/apps/http/servers/srv0/routes", routeConfig, srv0Exists, initRoute); err != nil {
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

	// Ждём, пока Caddy запишет свежий сертификат Step-CA и начнёт его
	// обслуживать на TLS-порту, затем убиваем процесс — systemd поднимет
	// его, и при старте cert загрузится с диска, минуя плохой кэш.
	slog.Info("caddy route installed, waiting for cert before restart", "domain", domain)
	maxWait := c.settings.CertSettleTime * 15
	if maxWait < 5*time.Second {
		maxWait = 30 * time.Second
	}
	if err := c.WaitForCert(nodeName, domain, maxWait); err != nil {
		slog.Warn("cert poll failed, restarting anyway", "domain", domain, "err", err)
	}
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

// RestartCaddy шлёт POST /stop. Systemd (Restart=always) поднимает Caddy обратно.
// Процесс умирает прямо во время ответа, поэтому любые транспортные ошибки
// (обрыв/reset/EOF) и ошибки чтения тела считаются нормой и игнорируются.
func (c *CaddyClient) RestartCaddy(nodeName string) error {
	baseURL, err := c.baseURL(nodeName)
	if err != nil {
		return err
	}
	slog.Info("caddy restart (POST /stop)", "node", nodeName)
	resp, err := c.doJSON("POST", baseURL+"/stop", nil)
	if err != nil {
		slog.Info("caddy /stop transport error (expected, process is dying)", "node", nodeName, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("restart /stop (%d): %s", resp.StatusCode, string(body))
	}
	if _, rerr := io.ReadAll(resp.Body); rerr != nil {
		slog.Info("caddy /stop body read error (expected, process is dying)", "node", nodeName, "err", rerr)
	}
	return nil
}

// WaitForCert опрашивает TLS-порт узла, пока Caddy не начнёт отдавать сертификат
// для domain (или пока не истечёт maxWait). Заменяет слепой time.Sleep в AddRoute.
func (c *CaddyClient) WaitForCert(nodeName, domain string, maxWait time.Duration) error {
	baseURL, err := c.baseURL(nodeName)
	if err != nil {
		return err
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("parse base url: %w", err)
	}
	host := u.Hostname()

	port := strings.TrimPrefix(c.settings.Listen, ":")
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	// Подгружаем CA-корни; если файл недоступен или не парсится — переключаемся
	// на небезопасный режим с ручной проверкой leaf-сертификата по subject.
	insecure := false
	var roots *x509.CertPool
	if c.settings.CARoots == "" {
		insecure = true
	} else if pem, rerr := os.ReadFile(c.settings.CARoots); rerr != nil {
		slog.Warn("cert roots unreadable, falling back to insecure check", "path", c.settings.CARoots, "err", rerr)
		insecure = true
	} else {
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			slog.Warn("cert roots parse failed, falling back to insecure check", "path", c.settings.CARoots)
			insecure = true
		}
	}

	deadline := time.Now().Add(maxWait)
	var lastErr error
	for time.Now().Before(deadline) {
		tlsConf := &tls.Config{ServerName: domain}
		if insecure {
			tlsConf.InsecureSkipVerify = true
		} else {
			tlsConf.RootCAs = roots
		}
		conn, derr := tls.Dial("tcp", addr, tlsConf)
		if derr != nil {
			lastErr = derr
		} else {
			matched := false
			if leaf := leafCert(conn); leaf != nil {
				matched = certMatches(leaf, domain)
			}
			conn.Close()
			if matched {
				return nil
			}
			lastErr = fmt.Errorf("connected but cert does not match %s", domain)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("cert for %s not ready after %s: %w", domain, maxWait, lastErr)
}

// leafCert возвращает leaf-сертификат из установленного TLS-соединения или nil.
func leafCert(conn *tls.Conn) *x509.Certificate {
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil
	}
	return certs[0]
}

// certMatches проверяет, что domain совпадает с CommonName или одним из DNSNames.
func certMatches(cert *x509.Certificate, domain string) bool {
	if cert.Subject.CommonName == domain {
		return true
	}
	for _, n := range cert.DNSNames {
		if n == domain {
			return true
		}
	}
	return false
}
