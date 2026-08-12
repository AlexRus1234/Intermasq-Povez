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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCaddyClient_GenerateRouteJSON_HTTP(t *testing.T) {
	c := NewCaddyClient(nil, CaddySettings{UpstreamInsecure: true})
	r := c.generateRouteJSON("a.test", "10.0.0.1", "8080", "http", "proxy-1")

	if r.ID != "proxy-1" {
		t.Errorf("@id = %v, want proxy-1", r.ID)
	}
	if len(r.Match) != 1 || len(r.Match[0].Host) != 1 || r.Match[0].Host[0] != "a.test" {
		t.Errorf("host = %v, want [a.test]", r.Match)
	}
	if len(r.Handle) != 1 {
		t.Fatalf("handle shape unexpected: %#v", r.Handle)
	}
	h := r.Handle[0]
	if h.Handler != "reverse_proxy" {
		t.Errorf("handler = %v, want reverse_proxy", h.Handler)
	}
	if len(h.Upstreams) != 1 || h.Upstreams[0].Dial != "10.0.0.1:8080" {
		t.Errorf("dial = %v, want 10.0.0.1:8080", h.Upstreams)
	}
	// http → no transport.tls
	if h.Transport.TLS != nil {
		t.Errorf("http route must not set transport.tls, got %#v", h.Transport.TLS)
	}
}

func TestCaddyClient_GenerateRouteJSON_HTTPSInsecureFromSettings(t *testing.T) {
	// UpstreamInsecure=true → tls.insecure_skip_verify=true
	c := NewCaddyClient(nil, CaddySettings{UpstreamInsecure: true})
	r := c.generateRouteJSON("a.test", "10.0.0.1", "8443", "https", "proxy-1")
	tlsCfg := r.Handle[0].Transport.TLS
	if tlsCfg == nil {
		t.Fatalf("https route must set transport.tls, got nil")
	}
	if !tlsCfg.InsecureSkipVerify {
		t.Errorf("insecure_skip_verify = %v, want true", tlsCfg.InsecureSkipVerify)
	}

	// UpstreamInsecure=false → tls.insecure_skip_verify=false (config-driven)
	c2 := NewCaddyClient(nil, CaddySettings{UpstreamInsecure: false})
	r2 := c2.generateRouteJSON("a.test", "10.0.0.1", "8443", "https", "proxy-1")
	tlsCfg2 := r2.Handle[0].Transport.TLS
	if tlsCfg2 == nil {
		t.Fatalf("https route must set transport.tls, got nil")
	}
	if tlsCfg2.InsecureSkipVerify {
		t.Errorf("insecure_skip_verify = %v, want false (from settings)", tlsCfg2.InsecureSkipVerify)
	}
}

// TestCaddyClient_GenerateRouteJSON_ByteIdentical guards that the typed structs
// marshal to exactly the same bytes the old map[string]interface{} produced.
func TestCaddyClient_GenerateRouteJSON_ByteIdentical(t *testing.T) {
	c := NewCaddyClient(nil, CaddySettings{UpstreamInsecure: true})

	httpWant := `{"@id":"proxy-1","handle":[{"handler":"reverse_proxy","transport":{"protocol":"http"},"upstreams":[{"dial":"10.0.0.1:8080"}]}],"match":[{"host":["a.test"]}]}`
	if b, _ := json.Marshal(c.generateRouteJSON("a.test", "10.0.0.1", "8080", "http", "proxy-1")); string(b) != httpWant {
		t.Errorf("http route JSON mismatch:\n got: %s\nwant: %s", b, httpWant)
	}

	httpsWant := `{"@id":"proxy-1","handle":[{"handler":"reverse_proxy","transport":{"protocol":"http","tls":{"insecure_skip_verify":true}},"upstreams":[{"dial":"10.0.0.1:8443"}]}],"match":[{"host":["a.test"]}]}`
	if b, _ := json.Marshal(c.generateRouteJSON("a.test", "10.0.0.1", "8443", "https", "proxy-1")); string(b) != httpsWant {
		t.Errorf("https route JSON mismatch:\n got: %s\nwant: %s", b, httpsWant)
	}
}

func TestCaddyClient_GenerateTLSPolicy(t *testing.T) {
	c := NewCaddyClient(nil, CaddySettings{
		ACMEURL: "https://acme.test/dir",
		CARoots: "/path/ca.pem",
		Listen:  ":443",
	})
	p := c.generateTLSPolicy("a.test", "tls-1")

	if p.ID != "tls-1" {
		t.Errorf("@id = %v, want tls-1", p.ID)
	}
	if len(p.Subjects) != 1 || p.Subjects[0] != "a.test" {
		t.Errorf("subjects = %v, want [a.test]", p.Subjects)
	}
	if len(p.Issuers) != 1 {
		t.Fatalf("expected 1 issuer, got %d", len(p.Issuers))
	}
	iss := p.Issuers[0]
	if iss.CA != "https://acme.test/dir" {
		t.Errorf("ca = %v, want https://acme.test/dir", iss.CA)
	}
	if len(iss.TrustedRootsPEMFiles) != 1 || iss.TrustedRootsPEMFiles[0] != "/path/ca.pem" {
		t.Errorf("trusted_roots = %v, want [/path/ca.pem]", iss.TrustedRootsPEMFiles)
	}
	// HTTP-01 challenge must be disabled (Step-CA serves DNS/TLS-ALPN only).
	if !iss.Challenges.HTTP.Disabled {
		t.Errorf("http challenge not disabled: %#v", iss.Challenges.HTTP)
	}
}

// TestCaddyClient_GenerateTLSPolicy_ByteIdentical guards the typed struct's
// marshalled output against the old map-based byte sequence.
func TestCaddyClient_GenerateTLSPolicy_ByteIdentical(t *testing.T) {
	c := NewCaddyClient(nil, CaddySettings{
		ACMEURL: "https://acme.test/dir",
		CARoots: "/path/ca.pem",
	})
	want := `{"@id":"tls-1","issuers":[{"ca":"https://acme.test/dir","challenges":{"http":{"disabled":true}},"module":"acme","trusted_roots_pem_files":["/path/ca.pem"]}],"subjects":["a.test"]}`
	if b, _ := json.Marshal(c.generateTLSPolicy("a.test", "tls-1")); string(b) != want {
		t.Errorf("tls policy JSON mismatch:\n got: %s\nwant: %s", b, want)
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

// selfSignedCert builds an ECDSA self-signed certificate valid for domain,
// suitable for a *tls.Config passed to an httptest TLS server.
func selfSignedCert(t *testing.T, domain string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestCaddyClient_WaitForCert_Success(t *testing.T) {
	domain := "acme.test"
	cert := selfSignedCert(t, domain)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	tcpAddr := srv.Listener.Addr().(*net.TCPAddr)
	port := fmt.Sprintf("%d", tcpAddr.Port)

	// Non-existent CARoots forces the insecure-skip-verify + leaf-subject path.
	c := NewCaddyClient(map[string]string{"node": fmt.Sprintf("http://127.0.0.1:%s", port)}, CaddySettings{
		Listen:  ":" + port,
		CARoots: "/nonexistent/ca.pem",
	})
	if err := c.WaitForCert("node", domain, 3*time.Second); err != nil {
		t.Fatalf("WaitForCert: %v", err)
	}
}

func TestCaddyClient_WaitForCert_Timeout(t *testing.T) {
	// Listen then immediately close: every dial refuses → poll loop exhausts.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	tcpAddr := srv.Listener.Addr().(*net.TCPAddr)
	port := fmt.Sprintf("%d", tcpAddr.Port)
	srv.Close()

	c := NewCaddyClient(map[string]string{"node": fmt.Sprintf("http://127.0.0.1:%s", port)}, CaddySettings{
		Listen:  ":" + port,
		CARoots: "/nonexistent/ca.pem",
	})
	if err := c.WaitForCert("node", "nope.test", 1200*time.Millisecond); err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}

// TestCaddyClient_RestartCaddy_IgnoresTransportErrors: with nothing listening,
// POST /stop fails to connect; RestartCaddy must swallow that and return nil.
func TestCaddyClient_RestartCaddy_IgnoresTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	c := NewCaddyClient(map[string]string{"node": addr}, CaddySettings{Timeout: time.Second})
	if err := c.RestartCaddy("node"); err != nil {
		t.Errorf("RestartCaddy should ignore /stop transport errors, got: %v", err)
	}
}
