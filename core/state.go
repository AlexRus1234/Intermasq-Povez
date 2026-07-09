package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type RouteRecord struct {
	Domain     string `json:"domain"`
	TargetIP   string `json:"target_ip"`
	TargetPort string `json:"target_port"`
	Protocol   string `json:"protocol"`
	RouteID    string `json:"route_id"`
	TLSID      string `json:"tls_id"`
	Node       string `json:"node"`
	UpdatedAt  string `json:"updated_at"`
}

type StateStore struct {
	path string
}

func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

func (s *StateStore) Load() ([]RouteRecord, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []RouteRecord{}, nil
		}
		return nil, err
	}
	var records []RouteRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		records = []RouteRecord{}
	}
	return records, nil
}

func (s *StateStore) Save(records []RouteRecord) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0660); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *StateStore) Upsert(rec RouteRecord) error {
	records, err := s.Load()
	if err != nil {
		return err
	}
	if rec.UpdatedAt == "" {
		rec.UpdatedAt = time.Now().Format(time.RFC3339)
	}
	found := false
	for i, r := range records {
		if r.RouteID == rec.RouteID {
			rec.UpdatedAt = time.Now().Format(time.RFC3339)
			records[i] = rec
			found = true
			break
		}
	}
	if !found {
		records = append(records, rec)
	}
	return s.Save(records)
}

func (s *StateStore) Remove(routeID string) error {
	records, err := s.Load()
	if err != nil {
		return err
	}
	filtered := records[:0]
	for _, r := range records {
		if r.RouteID != routeID {
			filtered = append(filtered, r)
		}
	}
	return s.Save(filtered)
}
