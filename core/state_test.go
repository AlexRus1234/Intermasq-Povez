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
	"os"
	"path/filepath"
	"testing"
)

func TestStateStore_LoadMissingFile(t *testing.T) {
	s := NewStateStore(filepath.Join(t.TempDir(), "nope.json"))
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load missing file: unexpected error %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice for missing file, got %d records", len(got))
	}
}

func TestStateStore_LoadInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	s := NewStateStore(path)
	if _, err := s.Load(); err == nil {
		t.Errorf("expected error on invalid JSON, got nil")
	}
}

func TestStateStore_SaveCreatesSubdirAndRoundTrips(t *testing.T) {
	// subdir does not exist yet — Save must MkdirAll it.
	s := NewStateStore(filepath.Join(t.TempDir(), "deep", "sub", "routes.json"))
	in := []RouteRecord{
		{Domain: "a.test", RouteID: "proxy-1-node", TLSID: "tls-1-node", Node: "node"},
		{Domain: "b.test", RouteID: "proxy-2-node", TLSID: "tls-2-node", Node: "node"},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records after round-trip, got %d", len(got))
	}
}

func TestStateStore_UpsertInsertAndUpdate(t *testing.T) {
	s := NewStateStore(filepath.Join(t.TempDir(), "routes.json"))

	if err := s.Upsert(RouteRecord{RouteID: "proxy-1-node", Domain: "a.test"}); err != nil {
		t.Fatalf("Upsert insert #1: %v", err)
	}
	if err := s.Upsert(RouteRecord{RouteID: "proxy-2-node", Domain: "b.test"}); err != nil {
		t.Fatalf("Upsert insert #2: %v", err)
	}
	// Update existing routeID — must overwrite, not append.
	if err := s.Upsert(RouteRecord{RouteID: "proxy-1-node", Domain: "a-updated.test"}); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records (update, not insert), got %d", len(got))
	}
	for _, r := range got {
		if r.RouteID == "proxy-1-node" {
			if r.Domain != "a-updated.test" {
				t.Errorf("update not applied: domain=%q", r.Domain)
			}
			if r.UpdatedAt == "" {
				t.Errorf("UpdatedAt not stamped on update")
			}
		}
	}
}

func TestStateStore_Remove(t *testing.T) {
	s := NewStateStore(filepath.Join(t.TempDir(), "routes.json"))
	_ = s.Upsert(RouteRecord{RouteID: "proxy-1-node"})
	_ = s.Upsert(RouteRecord{RouteID: "proxy-2-node"})

	if err := s.Remove("proxy-1-node"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ := s.Load()
	if len(got) != 1 {
		t.Fatalf("expected 1 record after Remove, got %d", len(got))
	}
	if got[0].RouteID != "proxy-2-node" {
		t.Errorf("wrong record left behind: %q", got[0].RouteID)
	}

	// Removing a non-existent id is a no-op, not an error.
	if err := s.Remove("does-not-exist"); err != nil {
		t.Errorf("Remove non-existent should be no-op, got %v", err)
	}
}
