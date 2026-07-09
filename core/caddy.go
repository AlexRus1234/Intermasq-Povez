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

type CaddyClient struct {
	BaseURLs map[string]string
	client   *http.Client
}

func NewCaddyClient(urls map[string]string) *CaddyClient {
	for k, v := range urls { urls[k] = strings.TrimRight(v, "/") }
	return &CaddyClient{BaseURLs: urls, client: &http.Client{Timeout: 10 * time.Second}}
}

func GenerateRouteJSON(domain, targetIP, targetPort, protocol, routeID string) map[string]interface{} {
	upstream := map[string]interface{}{"dial": fmt.Sprintf("%s:%s", targetIP, targetPort)}
	transport := map[string]interface{}{"protocol": "http"}
	if protocol == "https" {
		transport["tls"] = map[string]interface{}{"insecure_skip_verify": true}
	}
	handler := map[string]interface{}{
		"handler":   "reverse_proxy",
		"upstreams": []interface{}{upstream},
		"transport": transport,
	}
	return map[string]interface{}{
		"@id": routeID,
		"match": []interface{}{map[string]interface{}{"host": []string{domain}}},
		"handle": []interface{}{handler},
	}
}

// Формируем четкую политику TLS (Отключаем HTTP-01 челлендж, требуем Step-CA)
func GenerateTLSPolicyJSON(domain, tlsID string) map[string]interface{} {
	return map[string]interface{}{
		"@id":      tlsID,
		"subjects": []string{domain},
		"issuers": []map[string]interface{}{
			{
				"module":                  "acme",
				"ca":                      "https://172.20.0.1:9000/acme/acme/directory",
				"trusted_roots_pem_files": []string{"/etc/caddy/root_ca.crt"},
				"challenges": map[string]interface{}{
					"http": map[string]interface{}{
						"disabled": true,
					},
				},
			},
		},
	}
}

func (c *CaddyClient) AddRoute(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID string) error {
	baseURL, ok := c.BaseURLs[strings.ToLower(nodeName)]
	if !ok { return fmt.Errorf("URL Caddy не найден") }

	fmt.Printf("[CADDY] Настройка %s...\n", domain)

	// ХАК: ГЛОБАЛЬНО ОТКЛЮЧАЕМ LOCAL_CERTS
	http.Post(baseURL+"/config/apps/tls/local_certs_disabled", "application/json", bytes.NewBuffer([]byte(`true`)))

	// 1. ДОБАВЛЯЕМ ПОЛИТИКУ TLS
	tlsPolicy := GenerateTLSPolicyJSON(domain, tlsID)
	tlsPayload, _ := json.Marshal(tlsPolicy)

	req1, _ := http.NewRequest("POST", baseURL+"/config/apps/tls/automation/policies", bytes.NewBuffer(tlsPayload))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err1 := c.client.Do(req1)
	if err1 == nil {
		if resp1.StatusCode == 500 {
			resp1.Body.Close()
			initTlsPayload, _ := json.Marshal(map[string]interface{}{
				"automation": map[string]interface{}{
					"policies": []interface{}{tlsPolicy},
				},
			})
			reqInit, _ := http.NewRequest("PUT", baseURL+"/config/apps/tls", bytes.NewBuffer(initTlsPayload))
			reqInit.Header.Set("Content-Type", "application/json")
			rInit, _ := c.client.Do(reqInit)
			if rInit != nil { rInit.Body.Close() }
		} else {
			resp1.Body.Close()
		}
	}

	// 2. ЗАСТАВЛЯЕМ CADDY ПОЛУЧИТЬ СЕРТИФИКАТ НЕМЕДЛЕННО
	fmt.Printf("[CADDY] Ожидание выпуска сертификата для %s...\n", domain)
	automatePayload, _ := json.Marshal([]string{domain})
	reqAuth, _ := http.NewRequest("POST", baseURL+"/config/apps/tls/certificates/automate", bytes.NewBuffer(automatePayload))
	reqAuth.Header.Set("Content-Type", "application/json")
	respAuth, errAuth := c.client.Do(reqAuth)
	if errAuth == nil && respAuth != nil {
		respAuth.Body.Close()
	}

	// 3. ДОБАВЛЯЕМ МАРШРУТ (HTTP Route)
	routeConfig := GenerateRouteJSON(domain, targetIP, targetPort, protocol, routeID)
	routePayload, _ := json.Marshal(routeConfig)

	req2, _ := http.NewRequest("POST", baseURL+"/config/apps/http/servers/srv0/routes", bytes.NewBuffer(routePayload))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err2 := c.client.Do(req2)

	if err2 != nil { return err2 }
	
	if resp2.StatusCode == 500 {
		resp2.Body.Close()
		fmt.Println("[CADDY] Создаем сервер srv0...")
		initPayload, _ := json.Marshal(map[string]interface{}{
			"listen": []string{":443"},
			"routes": []interface{}{routeConfig},
		})
		reqInitSrv, _ := http.NewRequest("PUT", baseURL+"/config/apps/http/servers/srv0", bytes.NewBuffer(initPayload))
		reqInitSrv.Header.Set("Content-Type", "application/json")
		rInitSrv, _ := c.client.Do(reqInitSrv)
		if rInitSrv != nil { rInitSrv.Body.Close() }
	} else if resp2.StatusCode >= 400 {
		body, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		return fmt.Errorf("Ошибка добавления маршрута (%d): %s", resp2.StatusCode, string(body))
	} else {
		resp2.Body.Close()
	}

	// =================================================================
	// 4. THE NUKE: ЖЕСТКАЯ ПЕРЕЗАГРУЗКА CADDY
	// =================================================================
	// Ждем 2 секунды, чтобы Caddy успел записать свежий сертификат Step-CA на диск
	fmt.Println("[CADDY] Маршрут добавлен. Ждем 2 секунды перед рестартом кэша...")
	time.Sleep(2 * time.Second)

	// Шлем POST /stop. Caddy умрет.
	// Так как в службе Caddy прописано Restart=always, Systemd поднимет его через 1 секунду.
	// При старте Caddy прочитает autosave.json и загрузит сертификат с диска, забыв плохой кэш.
	fmt.Println("[CADDY] Отправляем команду на рестарт (POST /stop)...")
	http.Post(baseURL+"/stop", "application/json", nil)

	fmt.Println("[CADDY] ✅ Успех! Настройка завершена.")
	return nil
}

func (c *CaddyClient) DeleteRouteAndTLS(nodeName, routeID, tlsID string) error {
	baseURL, ok := c.BaseURLs[strings.ToLower(nodeName)]
	if !ok { return nil }

	req1, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/id/%s", baseURL, routeID), nil)
	c.client.Do(req1)

	req2, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/id/%s", baseURL, tlsID), nil)
	c.client.Do(req2)

	return nil
}

// RestartCaddy отправляет POST /stop. Systemd поднимет Caddy обратно.
func (c *CaddyClient) RestartCaddy(nodeName string) error {
	baseURL, ok := c.BaseURLs[strings.ToLower(nodeName)]
	if !ok { return fmt.Errorf("URL Caddy не найден для ноды %s", nodeName) }
	fmt.Printf("[CADDY] Рестарт ноды %s (POST /stop)...\n", nodeName)
	_, err := http.Post(baseURL+"/stop", "application/json", nil)
	return err
}

// upsertByID: если запись с @id существует (GET 200) — PUT, иначе POST.
// createPath и postInitFn нужны потому, что POST route/tls может вернуть 500
// при отсутствии родительского контейнера (srv0 / automation.policies),
// тогда надо его инициализировать и повторить попытку.
func (c *CaddyClient) upsertByID(baseURL, id, createPath string, payload map[string]interface{}, initIfMissing func() error) error {
	data, _ := json.Marshal(payload)

	// 1. Пробуем GET /id/<id>
	getResp, err := c.client.Get(fmt.Sprintf("%s/id/%s", baseURL, id))
	if err == nil {
		getResp.Body.Close()
		if getResp.StatusCode == 200 {
			// Существует → PUT
			req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/id/%s", baseURL, id), bytes.NewBuffer(data))
			req.Header.Set("Content-Type", "application/json")
			resp, err := c.client.Do(req)
			if err != nil { return err }
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("PUT %s (%d): %s", id, resp.StatusCode, string(body))
			}
			return nil
		}
	}

	// 2. Не существует → POST
	req, _ := http.NewRequest("POST", baseURL+createPath, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil { return err }
	defer resp.Body.Close()

	if resp.StatusCode == 500 {
		// Родительский контейнер отсутствует — инициализируем и повторяем POST
		if initIfMissing != nil {
			if err := initIfMissing(); err != nil { return err }
			req2, _ := http.NewRequest("POST", baseURL+createPath, bytes.NewBuffer(data))
			req2.Header.Set("Content-Type", "application/json")
			resp2, err := c.client.Do(req2)
			if err != nil { return err }
			defer resp2.Body.Close()
			if resp2.StatusCode >= 400 {
				body, _ := io.ReadAll(resp2.Body)
				return fmt.Errorf("POST %s после init (%d): %s", createPath, resp2.StatusCode, string(body))
			}
			return nil
		}
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s (500): %s", createPath, string(body))
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s (%d): %s", createPath, resp.StatusCode, string(body))
	}
	return nil
}

// ReplayRoute обновляет/создаёт route и TLS-политику по их @id без финального /stop.
func (c *CaddyClient) ReplayRoute(nodeName, domain, targetIP, targetPort, protocol, routeID, tlsID string) error {
	baseURL, ok := c.BaseURLs[strings.ToLower(nodeName)]
	if !ok { return fmt.Errorf("URL Caddy не найден для ноды %s", nodeName) }

	fmt.Printf("[CADDY] Replay %s (%s)...\n", domain, routeID)

	// TLS-политика
	tlsPolicy := GenerateTLSPolicyJSON(domain, tlsID)
	initTLS := func() error {
		initTlsPayload, _ := json.Marshal(map[string]interface{}{
			"automation": map[string]interface{}{
				"policies": []interface{}{tlsPolicy},
			},
		})
		req, _ := http.NewRequest("PUT", baseURL+"/config/apps/tls", bytes.NewBuffer(initTlsPayload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.client.Do(req)
		if err != nil { return err }
		resp.Body.Close()
		return nil
	}
	if err := c.upsertByID(baseURL, tlsID, "/config/apps/tls/automation/policies", tlsPolicy, initTLS); err != nil {
		return fmt.Errorf("TLS policy: %w", err)
	}

	// Заставляем Caddy выпустить сертификат
	automatePayload, _ := json.Marshal([]string{domain})
	reqAuth, _ := http.NewRequest("POST", baseURL+"/config/apps/tls/certificates/automate", bytes.NewBuffer(automatePayload))
	reqAuth.Header.Set("Content-Type", "application/json")
	if respAuth, err := c.client.Do(reqAuth); err == nil && respAuth != nil {
		respAuth.Body.Close()
	}

	// Route
	routeConfig := GenerateRouteJSON(domain, targetIP, targetPort, protocol, routeID)
	initRoute := func() error {
		initPayload, _ := json.Marshal(map[string]interface{}{
			"listen":  []string{":443"},
			"routes":  []interface{}{routeConfig},
		})
		req, _ := http.NewRequest("PUT", baseURL+"/config/apps/http/servers/srv0", bytes.NewBuffer(initPayload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.client.Do(req)
		if err != nil { return err }
		resp.Body.Close()
		return nil
	}
	if err := c.upsertByID(baseURL, routeID, "/config/apps/http/servers/srv0/routes", routeConfig, initRoute); err != nil {
		return fmt.Errorf("route: %w", err)
	}

	return nil
}
