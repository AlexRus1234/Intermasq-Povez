package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
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
	mu   sync.Mutex
}

func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

func (s *StateStore) Load() ([]RouteRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *StateStore) Save(records []RouteRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(records)
}

// load и save предполагают, что мьютекс уже захвачен вызывающей стороной,
// чтобы Upsert/Remove могли выполнять Load→mutate→Save атомарно.
func (s *StateStore) load() ([]RouteRecord, error) {
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

func (s *StateStore) save(records []RouteRecord) error {
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
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.load()
	if err != nil {
		return err
	}
	rec.UpdatedAt = time.Now().Format(time.RFC3339)
	for i, r := range records {
		if r.RouteID == rec.RouteID {
			records[i] = rec
			return s.save(records)
		}
	}
	records = append(records, rec)
	return s.save(records)
}

func (s *StateStore) Remove(routeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.load()
	if err != nil {
		return err
	}
	filtered := make([]RouteRecord, 0, len(records))
	for _, r := range records {
		if r.RouteID != routeID {
			filtered = append(filtered, r)
		}
	}
	return s.save(filtered)
}
