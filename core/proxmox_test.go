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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockPve emulates the two PVE endpoints used by FindByMAC:
// /cluster/resources (list) and /nodes/{node}/{type}/{vmid}/config (per-guest).
func mockPve(t *testing.T, resourcesBody, configBody string) *httptest.Server {
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
	return srv
}

func defaultProxmoxSettings() ProxmoxSettings {
	return ProxmoxSettings{
		PortPrefix:  "port-",
		ProtoPrefix: "proto-",
		NamePrefix:  "name-",
		Timeout:     5 * time.Second,
	}
}

func TestPveClient_FindByMAC_ParsesTags(t *testing.T) {
	srv := mockPve(t,
		`{"data":[{"node":"YADR01","type":"lxc","vmid":100,"name":"web01","status":"running"}]}`,
		`{"data":{"net0":"virtio=AA:BB:CC:DD:EE:01,bridge=vmbr0","tags":"port-8080 proto-http"}}`)
	p := NewPveClient(map[string]NodeConfig{"yadr01": {PveURL: srv.URL}}, defaultProxmoxSettings())

	info, err := p.FindByMAC("aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatalf("FindByMAC: %v", err)
	}
	if info.VMID != 100 {
		t.Errorf("VMID = %d, want 100", info.VMID)
	}
	if info.Port != "8080" {
		t.Errorf("Port = %q, want 8080", info.Port)
	}
	if info.Protocol != "http" {
		t.Errorf("Protocol = %q, want http", info.Protocol)
	}
	if info.RealNode != "YADR01" {
		t.Errorf("RealNode = %q, want YADR01", info.RealNode)
	}
	if info.NodeKey != "yadr01" {
		t.Errorf("NodeKey = %q, want yadr01", info.NodeKey)
	}
	if info.IsCaddy {
		t.Errorf("IsCaddy = true, want false")
	}
}

func TestPveClient_FindByMAC_MissingPortTagErrors(t *testing.T) {
	// Found by MAC but no port-XX tag → descriptive error, not a nil-info success.
	srv := mockPve(t,
		`{"data":[{"node":"YADR01","type":"lxc","vmid":100,"name":"web01","status":"running"}]}`,
		`{"data":{"net0":"virtio=AA:BB:CC:DD:EE:01"}}`)
	p := NewPveClient(map[string]NodeConfig{"yadr01": {PveURL: srv.URL}}, defaultProxmoxSettings())

	if _, err := p.FindByMAC("aa:bb:cc:dd:ee:01"); err == nil {
		t.Errorf("expected error for missing port- tag, got nil")
	}
}

func TestPveClient_FindByMAC_NotFound(t *testing.T) {
	srv := mockPve(t,
		`{"data":[{"node":"YADR01","type":"lxc","vmid":100,"name":"web01","status":"running"}]}`,
		`{"data":{"net0":"virtio=11:22:33:44:55:66"}}`)
	p := NewPveClient(map[string]NodeConfig{"yadr01": {PveURL: srv.URL}}, defaultProxmoxSettings())

	if _, err := p.FindByMAC("aa:bb:cc:dd:ee:99"); err == nil {
		t.Errorf("expected error for unknown MAC, got nil")
	}
}

func TestPveClient_FindByMAC_DetectsCaddyByName(t *testing.T) {
	// "caddy" substring in the name flips IsCaddy even without a port tag.
	srv := mockPve(t,
		`{"data":[{"node":"YADR01","type":"lxc","vmid":100,"name":"caddy01","status":"running"}]}`,
		`{"data":{"net0":"virtio=AA:BB:CC:DD:EE:01"}}`)
	p := NewPveClient(map[string]NodeConfig{"yadr01": {PveURL: srv.URL}}, defaultProxmoxSettings())

	info, err := p.FindByMAC("aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatalf("FindByMAC: %v", err)
	}
	if !info.IsCaddy {
		t.Errorf("IsCaddy = false, want true for name containing 'caddy'")
	}
}

func TestPveClient_FindByMAC_NameTagOverridesPVEName(t *testing.T) {
	// tags contain name-foo → ContainerInfo.Name = "foo", not "web01".
	srv := mockPve(t,
		`{"data":[{"node":"YADR01","type":"lxc","vmid":100,"name":"web01","status":"running"}]}`,
		`{"data":{"net0":"virtio=AA:BB:CC:DD:EE:01","tags":"port-8080 name-foo"}}`)
	p := NewPveClient(map[string]NodeConfig{"yadr01": {PveURL: srv.URL}}, defaultProxmoxSettings())

	info, err := p.FindByMAC("aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatalf("FindByMAC: %v", err)
	}
	if info.Name != "foo" {
		t.Errorf("Name = %q, want foo (overridden by name- tag)", info.Name)
	}
}

func TestPveClient_NewPveClient_NormalizesKeys(t *testing.T) {
	p := NewPveClient(map[string]NodeConfig{"YADR01": {}}, defaultProxmoxSettings())
	if _, ok := p.Nodes["yadr01"]; !ok {
		t.Errorf("node key not lowercased")
	}
	if _, ok := p.Nodes["YADR01"]; ok {
		t.Errorf("original-case key still present after normalise")
	}
}
