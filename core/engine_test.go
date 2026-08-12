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
