package store

import (
	"bytes"
	"encoding/json"
	"sync"
)

type Record struct {
	Key      string `json:"key"`
	Value    []byte `json:"value,omitempty"`
	Ts       int64  `json:"ts"`
	WriterID string `json:"writer_id"`
	Deleted  bool   `json:"deleted,omitempty"` // tombstone
}

type Meta struct {
	Ts       int64  `json:"ts"`
	WriterID string `json:"writer_id"`
	Deleted  bool   `json:"deleted,omitempty"`
}

type MemStore struct {
	mu  sync.RWMutex
	m   map[string]Record
	wal *WAL
}

func NewMem() *MemStore {
	return &MemStore{m: make(map[string]Record)}
}

func (s *MemStore) AttachWAL(w *WAL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wal = w
}

func (s *MemStore) Get(key string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.m[key]
	return rec, ok
}

// KeysMeta returns a snapshot of key->metadata for anti-entropy comparisons.
func (s *MemStore) KeysMeta() map[string]Meta {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]Meta, len(s.m))
	for k, r := range s.m {
		out[k] = Meta{Ts: r.Ts, WriterID: r.WriterID, Deleted: r.Deleted}
	}
	return out
}

func (s *MemStore) DumpAll() map[string]Record {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]Record, len(s.m))
	for k, r := range s.m {
		out[k] = r
	}
	return out
}

func (s *MemStore) LoadAll(m map[string]Record) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.m = make(map[string]Record, len(m))
	for k, r := range m {
		s.m[k] = r
	}
}

// ApplyLWW applies a record with LWW merge WITHOUT writing to the WAL.
// Use this during snapshot/WAL replay on startup.
func (s *MemStore) ApplyLWW(rec Record) Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.m[rec.Key]
	if !ok {
		s.m[rec.Key] = rec
		return rec
	}
	winner := Newer(cur, rec)
	s.m[rec.Key] = winner
	return winner
}

func (s *MemStore) Put(rec Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[rec.Key] = rec
}

// PutLWW merges under Last-Write-Wins and persists the winner to WAL (if attached).
func (s *MemStore) PutLWW(rec Record) Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.m[rec.Key]
	if !ok {
		// persist
		if s.wal != nil {
			_ = s.wal.Append(rec)
		}
		s.m[rec.Key] = rec
		return rec
	}

	winner := Newer(cur, rec)

	// If nothing changes, do nothing (don’t bloat WAL).
	if winner.Ts == cur.Ts &&
		winner.WriterID == cur.WriterID &&
		winner.Deleted == cur.Deleted &&
		bytes.Equal(winner.Value, cur.Value) {
		return cur
	}

	if s.wal != nil {
		_ = s.wal.Append(winner)
	}
	s.m[rec.Key] = winner
	return winner
}

// SnapshotAndResetWAL blocks writers, writes a full snapshot, then truncates the WAL.
// This is safe but pauses writes while snapshotting.
func (s *MemStore) SnapshotAndResetWAL(snapPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := json.Marshal(s.m)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(snapPath, b); err != nil {
		return err
	}
	if s.wal != nil {
		return s.wal.Truncate()
	}
	return nil
}
