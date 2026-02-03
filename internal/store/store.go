package store

import (
	"sync"
)

type Record struct {
	Key      string `json:"key"`
	Value    []byte `json:"value"`
	Ts       int64  `json:"ts"`
	WriterID string `json:"writer_id"`
}

type MemStore struct {
	mu sync.RWMutex
	m  map[string]Record
}

func NewMem() *MemStore {
	return &MemStore{m: make(map[string]Record)}
}

func (s *MemStore) Get(key string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.m[key]
	return rec, ok
}

func (s *MemStore) Put(rec Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[rec.Key] = rec
}
