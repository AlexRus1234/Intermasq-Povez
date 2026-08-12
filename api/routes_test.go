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

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"povez/core"
)

// newMockEngine собирает Engine с живым Intermasq-моком (httptest), чтобы
// handlers могли дойти до реального кода engine без реальной инфраструктуры.
func newMockEngine(t *testing.T, hostsBody, leasesBody string) *core.Engine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hosts":
			w.Write([]byte(hostsBody))
		case "/leases":
			w.Write([]byte(leasesBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	imq := core.NewIntermasqClient(srv.URL, "key", 5*time.Second)
	return core.NewEngine(nil, imq, nil, nil, ".test", nil, core.EngineSettings{})
}

// newMockPveEngine строит Engine, чей PveClient смотрит на локальный мок PVE.
// resourcesBody/configBody позволяют управлять тем, что вернёт Proxmox.
func newMockPveEngine(t *testing.T, resourcesBody, configBody string) *core.Engine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cluster/resources":
			fmt.Fprint(w, resourcesBody)
		case r.URL.Path == "/nodes/YADR01/lxc/100/config":
			fmt.Fprint(w, configBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	pve := core.NewPveClient(
		map[string]core.NodeConfig{"yadr01": {PveURL: srv.URL}},
		core.ProxmoxSettings{PortPrefix: "port-", ProtoPrefix: "proto-", NamePrefix: "name-"},
	)
	return core.NewEngine(pve, nil, nil, nil, ".test", nil, core.EngineSettings{})
}

func TestHandleGetPending_OK(t *testing.T) {
	s := NewApiServer(newMockEngine(t, `[]`, `[
		{"mac":"AA:BB:CC:DD:EE:01","ip":"10.0.0.1","hostname":"h1"}]`))

	req := httptest.NewRequest(http.MethodGet, "/api/pending", nil)
	rec := httptest.NewRecorder()
	s.HandleGetPending(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []core.PendingDevice
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got) != 1 || got[0].MAC != "AA:BB:CC:DD:EE:01" {
		t.Errorf("pending = %#v", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestHandleGetPending_MethodNotAllowed(t *testing.T) {
	s := NewApiServer(newMockEngine(t, `[]`, `[]`))
	req := httptest.NewRequest(http.MethodPost, "/api/pending", nil)
	rec := httptest.NewRecorder()
	s.HandleGetPending(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to contain GET", allow)
	}
}

func TestHandleProvision_InvalidJSON(t *testing.T) {
	s := NewApiServer(newMockEngine(t, `[]`, `[]`))
	req := httptest.NewRequest(http.MethodPost, "/api/provision", strings.NewReader("{broken"))
	rec := httptest.NewRecorder()
	s.HandleProvision(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleProvision_MethodNotAllowed(t *testing.T) {
	s := NewApiServer(newMockEngine(t, `[]`, `[]`))
	req := httptest.NewRequest(http.MethodGet, "/api/provision", nil)
	rec := httptest.NewRecorder()
	s.HandleProvision(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleProvision_MissingMAC_400(t *testing.T) {
	s := NewApiServer(newMockEngine(t, `[]`, `[]`))
	req := httptest.NewRequest(http.MethodPost, "/api/provision", strings.NewReader(`{"mac":""}`))
	rec := httptest.NewRecorder()
	s.HandleProvision(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing MAC)", rec.Code)
	}
}

func TestHandleProvision_NotFound_MapsTo404(t *testing.T) {
	// PVE возвращает пустой список ресурсов → FindByMAC фейлится →
	// ErrContainerNotFound → 404.
	e := newMockPveEngine(t, `{"data":[]}`, `{"data":{}}`)
	s := NewApiServer(e)

	req := httptest.NewRequest(http.MethodPost, "/api/provision", strings.NewReader(`{"mac":"aa:bb:cc:dd:ee:01"}`))
	rec := httptest.NewRecorder()
	s.HandleProvision(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleDeprovision_MissingMAC(t *testing.T) {
	s := NewApiServer(newMockEngine(t, `[]`, `[]`))
	req := httptest.NewRequest(http.MethodDelete, "/api/deprovision", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.HandleDeprovision(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing MAC)", rec.Code)
	}
}

func TestHandleDeprovision_PostAlsoAccepted(t *testing.T) {
	// POST tolerated for back-compat (old UI clients).
	s := NewApiServer(newMockEngine(t, `[]`, `[]`))
	req := httptest.NewRequest(http.MethodPost, "/api/deprovision", strings.NewReader(`{"mac":""}`))
	rec := httptest.NewRecorder()
	s.HandleDeprovision(rec, req)
	// Empty MAC → 400 (reaches the body check, proving POST was accepted).
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (POST should reach MAC check)", rec.Code)
	}
}

func TestHandleDeprovision_MethodNotAllowed(t *testing.T) {
	s := NewApiServer(newMockEngine(t, `[]`, `[]`))
	req := httptest.NewRequest(http.MethodPut, "/api/deprovision", nil)
	rec := httptest.NewRecorder()
	s.HandleDeprovision(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleDeprovision_RunningContainer_MapsTo409(t *testing.T) {
	// Контейнер найден, но status=running → ErrContainerRunning → 409.
	e := newMockPveEngine(t,
		`{"data":[{"node":"YADR01","type":"lxc","vmid":100,"name":"web01","status":"running"}]}`,
		`{"data":{"net0":"virtio=AA:BB:CC:DD:EE:01","tags":"port-8080"}}`)
	s := NewApiServer(e)

	req := httptest.NewRequest(http.MethodDelete, "/api/deprovision", strings.NewReader(`{"mac":"aa:bb:cc:dd:ee:01"}`))
	rec := httptest.NewRecorder()
	s.HandleDeprovision(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleGetState_EmptyWhenNoState(t *testing.T) {
	// Engine with nil State → GetState returns empty slice, not error.
	e := core.NewEngine(nil, nil, nil, nil, "", nil, core.EngineSettings{})
	s := NewApiServer(e)

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := httptest.NewRecorder()
	s.HandleGetState(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []core.RouteRecord
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty state, got %d records", len(got))
	}
}

func TestHandleReplay_NoStateReturns500(t *testing.T) {
	// Engine with nil State → ReplayCaddy errors → handler 500.
	e := core.NewEngine(nil, nil, nil, nil, "", nil, core.EngineSettings{})
	s := NewApiServer(e)

	req := httptest.NewRequest(http.MethodPost, "/api/replay", nil)
	rec := httptest.NewRecorder()
	s.HandleReplay(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
