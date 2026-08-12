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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIntermasqClient_DeleteHost_EscapesSpecialChars(t *testing.T) {
	var gotPath, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewIntermasqClient(srv.URL, "", time.Second)
	if err := c.DeleteHost("aa:bb:cc:dd:ee:01", "weird/path?x=1&y=2"); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}

	if want := "/hosts/aa:bb:cc:dd:ee:01"; gotPath != want {
		t.Errorf("path: got %q, want %q", gotPath, want)
	}
	if want := "file=weird%2Fpath%3Fx%3D1%26y%3D2"; gotRawQuery != want {
		t.Errorf("raw query: got %q, want %q", gotRawQuery, want)
	}
}

func TestIntermasqClient_FindFileByMAC_ParsesPipeFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts := []HostEntry{
			{Mac: "aa:bb:cc:dd:ee:01", File: "/etc/dnsmasq.d/x.conf|extra"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(hosts)
	}))
	defer srv.Close()

	c := NewIntermasqClient(srv.URL, "", time.Second)
	got, err := c.FindFileByMAC("aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatalf("FindFileByMAC: %v", err)
	}
	if want := "/etc/dnsmasq.d/x.conf"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
