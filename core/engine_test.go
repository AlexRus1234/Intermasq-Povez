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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockIntermasq serves /hosts and /leases JSON lists. A device is "pending"
// iff its MAC appears in /leases but not in /hosts.
func mockIntermasq(t *testing.T, hostsBody, leasesBody string) *httptest.Server {
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
	return srv
}

func TestEngine_GetPendingDevices_FiltersKnownMacs(t *testing.T) {
	srv := mockIntermasq(t,
		`[{"mac":"AA:BB:CC:DD:EE:01","ip":"10.0.0.1","hostname":"h1","file":"f"}]`,
		`[
			{"mac":"AA:BB:CC:DD:EE:01","ip":"10.0.0.1","hostname":"h1"},
			{"mac":"AA:BB:CC:DD:EE:02","ip":"10.0.0.2","hostname":"h2"}
		]`)
	imq := NewIntermasqClient(srv.URL, "key", 5*time.Second)
	e := &Engine{IMQ: imq}

	pending, err := e.GetPendingDevices()
	if err != nil {
		t.Fatalf("GetPendingDevices: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending device, got %d", len(pending))
	}
	if pending[0].MAC != "AA:BB:CC:DD:EE:02" {
		t.Errorf("pending MAC = %q, want AA:BB:CC:DD:EE:02", pending[0].MAC)
	}
	if pending[0].IP != "10.0.0.2" {
		t.Errorf("pending IP = %q, want 10.0.0.2", pending[0].IP)
	}
}

func TestEngine_GetPendingDevices_EmptyLeases(t *testing.T) {
	srv := mockIntermasq(t, `[]`, `[]`)
	imq := NewIntermasqClient(srv.URL, "key", 5*time.Second)
	e := &Engine{IMQ: imq}

	pending, err := e.GetPendingDevices()
	if err != nil {
		t.Fatalf("GetPendingDevices: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected empty pending, got %d", len(pending))
	}
}

// MAC comparison is case-insensitive — a lease in uppercase must still match
// a lowercase host entry.
func TestEngine_GetPendingDevices_CaseInsensitiveMAC(t *testing.T) {
	srv := mockIntermasq(t,
		`[{"mac":"aa:bb:cc:dd:ee:01","ip":"10.0.0.1","hostname":"h1","file":"f"}]`,
		`[{"mac":"AA:BB:CC:DD:EE:01","ip":"10.0.0.1","hostname":"h1"}]`)
	imq := NewIntermasqClient(srv.URL, "key", 5*time.Second)
	e := &Engine{IMQ: imq}

	pending, err := e.GetPendingDevices()
	if err != nil {
		t.Fatalf("GetPendingDevices: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("uppercase lease must match lowercase host, got %d pending", len(pending))
	}
}

func TestEngine_GetPendingDevices_HostsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	imq := NewIntermasqClient(srv.URL, "key", 5*time.Second)
	e := &Engine{IMQ: imq}

	if _, err := e.GetPendingDevices(); err == nil {
		t.Errorf("expected error when /hosts returns 500, got nil")
	}
}

func TestMakeIDs(t *testing.T) {
	info := &ContainerInfo{VMID: 150, NodeKey: "yadr01"}
	routeID, tlsID := makeIDs(info)
	if routeID != "proxy-150-yadr01" {
		t.Errorf("routeID = %q, want proxy-150-yadr01", routeID)
	}
	if tlsID != "tls-150-yadr01" {
		t.Errorf("tlsID = %q, want tls-150-yadr01", tlsID)
	}
}

// TestEngine_Provision_IPValidation проверяет расчёт последнего октета IP
// через computeIP (используется Provision). Октет = VMID - VMIDBase и обязан
// лежать в [0,255], иначе ErrInvalidIP.
func TestEngine_Provision_IPValidation(t *testing.T) {
	e := &Engine{settings: EngineSettings{VMIDBase: 100}}
	nodeConf := NodeConfig{Subnet: "172.20.5"}

	tests := []struct {
		name    string
		vmid    int
		wantIP  string
		wantErr error
	}{
		{"valid low", 100, "172.20.5.0", nil},
		{"valid mid", 150, "172.20.5.50", nil},
		{"valid high", 355, "172.20.5.255", nil},
		{"overflow", 500, "", ErrInvalidIP}, // 400 > 255
		{"negative", 50, "", ErrInvalidIP},  // -50 < 0
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &ContainerInfo{VMID: tc.vmid}
			ip, err := e.computeIP(info, nodeConf)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("computeIP err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if ip != tc.wantIP {
				t.Errorf("computeIP = %q, want %q", ip, tc.wantIP)
			}
		})
	}
}

// mockPveRunning эмулирует PVE: контейнер найден по MAC, но status=running.
// Депровиж должен вернуть ErrContainerRunning, не доходя до Caddy/IMQ.
func mockPveRunning(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cluster/resources":
			fmt.Fprint(w, `{"data":[{"node":"YADR01","type":"lxc","vmid":100,"name":"web01","status":"running"}]}`)
		case r.URL.Path == "/nodes/YADR01/lxc/100/config":
			fmt.Fprint(w, `{"data":{"net0":"virtio=AA:BB:CC:DD:EE:01","tags":"port-8080"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEngine_Deprovision_ContainerRunning(t *testing.T) {
	srv := mockPveRunning(t)
	pve := NewPveClient(map[string]NodeConfig{"yadr01": {PveURL: srv.URL}}, defaultProxmoxSettings())
	e := &Engine{PVE: pve}

	err := e.Deprovision("aa:bb:cc:dd:ee:01")
	if !errors.Is(err, ErrContainerRunning) {
		t.Errorf("expected ErrContainerRunning, got %v", err)
	}
}
