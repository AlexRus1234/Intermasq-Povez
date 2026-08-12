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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCaddyClient_GenerateRouteJSON_HTTP(t *testing.T) {
	c := NewCaddyClient(nil, CaddySettings{UpstreamInsecure: true})
	r := c.generateRouteJSON("a.test", "10.0.0.1", "8080", "http", "proxy-1")

	if r["@id"] != "proxy-1" {
		t.Errorf("@id = %v, want proxy-1", r["@id"])
	}
	match, ok := r["match"].([]interface{})
	if !ok || len(match) != 1 {
		t.Fatalf("match shape unexpected: %#v", r["match"])
	}
	m := match[0].(map[string]interface{})
	hosts, _ := m["host"].([]string)
	if len(hosts) != 1 || hosts[0] != "a.test" {
		t.Errorf("host = %v, want [a.test]", hosts)
	}

	handle := r["handle"].([]interface{})[0].(map[string]interface{})
	if handle["handler"] != "reverse_proxy" {
		t.Errorf("handler = %v, want reverse_proxy", handle["handler"])
	}
	upstreams := handle["upstreams"].([]interface{})
	up := upstreams[0].(map[string]interface{})
	if up["dial"] != "10.0.0.1:8080" {
		t.Errorf("dial = %v, want 10.0.0.1:8080", up["dial"])
	}
	// http → no transport.tls
	tr := handle["transport"].(map[string]interface{})
	if _, hasTLS := tr["tls"]; hasTLS {
		t.Errorf("http route must not set transport.tls, got %#v", tr["tls"])
	}
}

func TestCaddyClient_GenerateRouteJSON_HTTPSInsecureFromSettings(t *testing.T) {
	// UpstreamInsecure=true → tls.insecure_skip_verify=true
	c := NewCaddyClient(nil, CaddySettings{UpstreamInsecure: true})
	r := c.generateRouteJSON("a.test", "10.0.0.1", "8443", "https", "proxy-1")
	handle := r["handle"].([]interface{})[0].(map[string]interface{})
	tr := handle["transport"].(map[string]interface{})
	tls, ok := tr["tls"].(map[string]interface{})
	if !ok {
		t.Fatalf("https route must set transport.tls, got %#v", tr["tls"])
	}
	if tls["insecure_skip_verify"] != true {
		t.Errorf("insecure_skip_verify = %v, want true", tls["insecure_skip_verify"])
	}

	// UpstreamInsecure=false → tls.insecure_skip_verify=false (config-driven)
	c2 := NewCaddyClient(nil, CaddySettings{UpstreamInsecure: false})
	r2 := c2.generateRouteJSON("a.test", "10.0.0.1", "8443", "https", "proxy-1")
	handle2 := r2["handle"].([]interface{})[0].(map[string]interface{})
	tr2 := handle2["transport"].(map[string]interface{})
	tls2 := tr2["tls"].(map[string]interface{})
	if tls2["insecure_skip_verify"] != false {
		t.Errorf("insecure_skip_verify = %v, want false (from settings)", tls2["insecure_skip_verify"])
	}
}

func TestCaddyClient_GenerateTLSPolicy(t *testing.T) {
	c := NewCaddyClient(nil, CaddySettings{
		ACMEURL: "https://acme.test/dir",
		CARoots: "/path/ca.pem",
		Listen:  ":443",
	})
	p := c.generateTLSPolicy("a.test", "tls-1")

	if p["@id"] != "tls-1" {
		t.Errorf("@id = %v, want tls-1", p["@id"])
	}
	subjects, _ := p["subjects"].([]string)
	if len(subjects) != 1 || subjects[0] != "a.test" {
		t.Errorf("subjects = %v, want [a.test]", subjects)
	}
	issuers, _ := p["issuers"].([]map[string]interface{})
	if len(issuers) != 1 {
		t.Fatalf("expected 1 issuer, got %d", len(issuers))
	}
	if issuers[0]["ca"] != "https://acme.test/dir" {
		t.Errorf("ca = %v, want acme.test/dir", issuers[0]["ca"])
	}
	roots, _ := issuers[0]["trusted_roots_pem_files"].([]string)
	if len(roots) != 1 || roots[0] != "/path/ca.pem" {
		t.Errorf("trusted_roots = %v, want [/path/ca.pem]", roots)
	}
	// HTTP-01 challenge must be disabled (Step-CA serves DNS/TLS-ALPN only).
	ch := issuers[0]["challenges"].(map[string]interface{})
	httpCh := ch["http"].(map[string]interface{})
	if httpCh["disabled"] != true {
		t.Errorf("http challenge not disabled: %#v", httpCh)
	}
}

// mockCaddy builds an httptest server emulating the subset of the Caddy
// admin API the client uses. Returns the server and a map of recorded calls.
func mockCaddy(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return srv
}

func TestCaddyClient_UpsertByID_CreatesViaPOST(t *testing.T) {
	// GET /id/X → 404, then POST createPath → 200.
	var lastMethod, lastPath string
	srv := mockCaddy(t, func(w http.ResponseWriter, r *http.Request) {
		lastMethod, lastPath = r.Method, r.URL.Path
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/id/proxy-1":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/config/apps/http/servers/srv0/routes":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	c := NewCaddyClient(map[string]string{"node": srv.URL}, CaddySettings{Timeout: 5 * time.Second})

	if err := c.upsertByID(srv.URL, "proxy-1", "/config/apps/http/servers/srv0/routes",
		map[string]interface{}{"@id": "proxy-1"}, nil, nil); err != nil {
		t.Fatalf("upsertByID: %v", err)
	}
	if lastMethod != http.MethodPost {
		t.Errorf("expected final POST, got %s %s", lastMethod, lastPath)
	}
}

func TestCaddyClient_UpsertByID_UpdatesViaPUT(t *testing.T) {
	// GET /id/X → 200, then PUT /id/X → 200.
	var lastMethod string
	srv := mockCaddy(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/id/proxy-1":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/id/proxy-1":
			lastMethod = http.MethodPut
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	c := NewCaddyClient(map[string]string{"node": srv.URL}, CaddySettings{Timeout: 5 * time.Second})

	if err := c.upsertByID(srv.URL, "proxy-1", "/config/apps/http/servers/srv0/routes",
		map[string]interface{}{"@id": "proxy-1"}, nil, nil); err != nil {
		t.Fatalf("upsertByID: %v", err)
	}
	if lastMethod != http.MethodPut {
		t.Errorf("expected PUT for existing id, got %s", lastMethod)
	}
}

// TestCaddyClient_UpsertByID_GETNon404Propagates: GET /id/X → 500 не должен
// проваливаться в POST (иначе создаст дубликат). Ожидаем ошибку и ноль POST.
func TestCaddyClient_UpsertByID_GETNon404Propagates(t *testing.T) {
	var postCount int
	srv := mockCaddy(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/id/proxy-1":
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/config/apps/http/servers/srv0/routes":
			postCount++
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	c := NewCaddyClient(map[string]string{"node": srv.URL}, CaddySettings{Timeout: 5 * time.Second})

	err := c.upsertByID(srv.URL, "proxy-1", "/config/apps/http/servers/srv0/routes",
		map[string]interface{}{"@id": "proxy-1"}, nil, nil)
	if err == nil {
		t.Fatalf("expected error when GET returns 500, got nil")
	}
	if postCount != 0 {
		t.Errorf("POST must not be attempted when GET returns non-404, got %d POST calls", postCount)
	}
}

// TestCaddyClient_UpsertByID_GETNetworkErrorPropagates: сетевая ошибка на GET
// (закрытый слушатель) не должна проваливаться в POST.
func TestCaddyClient_UpsertByID_GETNetworkErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close() // порт больше не слушается → connection refused

	c := NewCaddyClient(nil, CaddySettings{Timeout: 2 * time.Second})
	err := c.upsertByID(addr, "proxy-1", "/config/apps/http/servers/srv0/routes",
		map[string]interface{}{"@id": "proxy-1"}, nil, nil)
	if err == nil {
		t.Fatalf("expected network error from unreachable server, got nil")
	}
}

func TestCaddyClient_DeleteRouteAndTLS(t *testing.T) {
	deleted := map[string]int{}
	srv := mockCaddy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deleted[r.URL.Path]++
		// routeID → 200, tlsID → 404 (already gone, must be tolerated)
		switch r.URL.Path {
		case "/id/proxy-1":
			w.WriteHeader(http.StatusOK)
		case "/id/tls-1":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	c := NewCaddyClient(map[string]string{"node": srv.URL}, CaddySettings{Timeout: 5 * time.Second})

	if err := c.DeleteRouteAndTLS("node", "proxy-1", "tls-1"); err != nil {
		t.Fatalf("DeleteRouteAndTLS: %v", err)
	}
	if deleted["/id/proxy-1"] != 1 || deleted["/id/tls-1"] != 1 {
		t.Errorf("delete counts = %v, want both called once", deleted)
	}
}

func TestCaddyClient_DeleteRouteAndTLS_NodeMissing(t *testing.T) {
	c := NewCaddyClient(map[string]string{"node": "http://127.0.0.1:1"}, CaddySettings{Timeout: 1 * time.Second})
	// Unknown node → error, not silent nil.
	if err := c.DeleteRouteAndTLS("ghost", "proxy-1", "tls-1"); err == nil {
		t.Errorf("expected error for unknown node, got nil")
	}
}

// TestCaddyClient_NewCaddyClient_NormalizesKeys guards the one-shot lowercase
// normalisation so lookups with mixed-case node names resolve.
func TestCaddyClient_NewCaddyClient_NormalizesKeys(t *testing.T) {
	c := NewCaddyClient(map[string]string{"YADR01": "http://c:2019"}, CaddySettings{})
	if _, err := c.baseURL("yadr01"); err != nil {
		t.Errorf("lowercased lookup failed: %v", err)
	}
	if _, err := c.baseURL("YADR01"); err != nil {
		t.Errorf("original-case lookup failed after normalise: %v", err)
	}
	// also trims trailing slash
	c2 := NewCaddyClient(map[string]string{"n": "http://c:2019/"}, CaddySettings{})
	u, _ := c2.baseURL("n")
	if u != "http://c:2019" {
		t.Errorf("trailing slash not trimmed: %q", u)
	}
}

func TestCaddyClient_DisableLocalCerts_ReturnsErrorOn500(t *testing.T) {
	srv := mockCaddy(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewCaddyClient(map[string]string{"node": srv.URL}, CaddySettings{Timeout: 5 * time.Second})
	if err := c.disableLocalCerts(srv.URL); err == nil {
		t.Fatalf("expected error on 500, got nil")
	}
}

func TestCaddyClient_InitTLSParent_ReturnsErrorOn500(t *testing.T) {
	srv := mockCaddy(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewCaddyClient(map[string]string{"node": srv.URL}, CaddySettings{Timeout: 5 * time.Second})
	if err := c.initTLSParent(srv.URL, map[string]interface{}{"@id": "tls-1"}); err == nil {
		t.Fatalf("expected error on 500, got nil")
	}
}

func TestCaddyClient_InitSrv0_ReturnsErrorOn500(t *testing.T) {
	srv := mockCaddy(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewCaddyClient(map[string]string{"node": srv.URL}, CaddySettings{Timeout: 5 * time.Second})
	if err := c.initSrv0(srv.URL, map[string]interface{}{"@id": "proxy-1"}); err == nil {
		t.Fatalf("expected error on 500, got nil")
	}
}

// TestCaddyClient_InitSrv0_DoesNotClobberExisting: POST route вернул 500, но
// родитель srv0 уже существует (GET /config/apps/http/servers/srv0 → 200).
// initIfMissing не должен вызываться — иначе PUT затрёт существующие routes.
func TestCaddyClient_InitSrv0_DoesNotClobberExisting(t *testing.T) {
	var initCalled bool
	var postCount int
	srv := mockCaddy(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/id/proxy-1":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/config/apps/http/servers/srv0/routes":
			postCount++
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodGet && r.URL.Path == "/config/apps/http/servers/srv0":
			// родитель уже существует с маршрутами
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	c := NewCaddyClient(map[string]string{"node": srv.URL}, CaddySettings{Timeout: 5 * time.Second})

	parentExists := func() (bool, error) {
		return c.pathExists(srv.URL, "/config/apps/http/servers/srv0")
	}
	initIfMissing := func() error { initCalled = true; return nil }

	err := c.upsertByID(srv.URL, "proxy-1", "/config/apps/http/servers/srv0/routes",
		map[string]interface{}{"@id": "proxy-1"}, parentExists, initIfMissing)
	if err == nil {
		t.Fatalf("expected error when parent exists and POST returns 500, got nil")
	}
	if initCalled {
		t.Errorf("initIfMissing must NOT be called when parent already exists")
	}
	if postCount != 1 {
		t.Errorf("expected exactly one POST (no init retry), got %d", postCount)
	}
}
