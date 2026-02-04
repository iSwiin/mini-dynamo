package hints

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mini-dynamo/internal/store"
)

type walEntry struct {
	Op     string        `json:"op"` // "add" | "del"
	Target string        `json:"target,omitempty"`
	Key    string        `json:"key,omitempty"`
	Ts     int64         `json:"ts,omitempty"`
	Writer string        `json:"writer_id,omitempty"`
	Record *store.Record `json:"record,omitempty"`
}

type Manager struct {
	mu sync.Mutex
	// targetID -> key -> record (keep only latest per key/target)
	m map[string]map[string]store.Record

	// WAL (optional)
	walPath     string
	walFile     *os.File
	walOps      int
	walBytes    int64
	lastCompact time.Time
}

func New() *Manager {
	return &Manager{m: make(map[string]map[string]store.Record)}
}

// NewPersistent loads any existing WAL at walPath and appends future updates to it.
func NewPersistent(walPath string) (*Manager, error) {
	if walPath == "" {
		return New(), nil
	}

	dir := filepath.Dir(walPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	h := &Manager{
		m:       make(map[string]map[string]store.Record),
		walPath: walPath,
	}

	// Replay existing WAL if present.
	if err := h.replay(); err != nil {
		return nil, err
	}

	// Open for append.
	f, err := os.OpenFile(walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	h.walFile = f

	// Initialize size counter.
	if st, err := f.Stat(); err == nil {
		h.walBytes = st.Size()
	}

	return h, nil
}

func (h *Manager) replay() error {
	f, err := os.Open(h.walPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Records can be larger than 64K; allow up to 16MB lines.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e walEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		switch e.Op {
		case "add":
			if e.Record != nil {
				h.addNoWAL(e.Target, *e.Record)
			}
		case "del":
			h.delNoWAL(e.Target, e.Key, e.Ts, e.Writer)
		default:
			// ignore unknown ops for forward-compat
		}
	}
	return sc.Err()
}

func (h *Manager) addNoWAL(targetID string, rec store.Record) {
	if targetID == "" || rec.Key == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.addLocked(targetID, rec)
}

func (h *Manager) delNoWAL(targetID, key string, ts int64, writerID string) {
	if targetID == "" || key == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	byKey := h.m[targetID]
	if byKey == nil {
		return
	}
	cur, ok := byKey[key]
	if !ok {
		return
	}
	if cur.Ts == ts && cur.WriterID == writerID {
		delete(byKey, key)
		if len(byKey) == 0 {
			delete(h.m, targetID)
		}
	}
}

func (h *Manager) addLocked(targetID string, rec store.Record) (changed bool) {
	byKey, ok := h.m[targetID]
	if !ok {
		byKey = make(map[string]store.Record)
		h.m[targetID] = byKey
	}

	if cur, ok := byKey[rec.Key]; ok {
		w := store.Newer(cur, rec)
		if w.Ts != cur.Ts || w.WriterID != cur.WriterID {
			byKey[rec.Key] = w
			return true
		}
		return false
	}

	byKey[rec.Key] = rec
	return true
}

func (h *Manager) appendWAL(e walEntry) error {
	if h.walFile == nil {
		return nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	// Best-effort durability: write + fsync.
	if _, err := h.walFile.Write(b); err != nil {
		return err
	}
	if err := h.walFile.Sync(); err != nil {
		return err
	}

	h.walOps++
	h.walBytes += int64(len(b))
	return nil
}

func (h *Manager) Add(targetID string, rec store.Record) {
	if targetID == "" || rec.Key == "" {
		return
	}

	h.mu.Lock()
	changed := h.addLocked(targetID, rec)
	h.mu.Unlock()

	// Persist only if it changed the "latest" record for that (target,key).
	if changed {
		rc := rec // copy for pointer stability
		_ = h.appendWAL(walEntry{Op: "add", Target: targetID, Record: &rc})
	}
}

func (h *Manager) Targets() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]string, 0, len(h.m))
	for t := range h.m {
		out = append(out, t)
	}
	return out
}

func (h *Manager) RecordsFor(targetID string) []store.Record {
	h.mu.Lock()
	defer h.mu.Unlock()

	byKey := h.m[targetID]
	if len(byKey) == 0 {
		return nil
	}
	out := make([]store.Record, 0, len(byKey))
	for _, rec := range byKey {
		out = append(out, rec)
	}
	return out
}

// DeleteIfSame removes the hint only if it has not been overwritten by a newer version.
// If removed, it appends a WAL "del" marker so replays won't resurrect delivered hints.
func (h *Manager) DeleteIfSame(targetID string, key string, rec store.Record) {
	if targetID == "" || key == "" {
		return
	}

	deleted := false

	h.mu.Lock()
	byKey := h.m[targetID]
	if byKey != nil {
		cur, ok := byKey[key]
		if ok && cur.Ts == rec.Ts && cur.WriterID == rec.WriterID {
			delete(byKey, key)
			if len(byKey) == 0 {
				delete(h.m, targetID)
			}
			deleted = true
		}
	}
	h.mu.Unlock()

	if deleted {
		_ = h.appendWAL(walEntry{
			Op:     "del",
			Target: targetID,
			Key:    key,
			Ts:     rec.Ts,
			Writer: rec.WriterID,
		})
	}
}

func (h *Manager) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	total := 0
	for _, byKey := range h.m {
		total += len(byKey)
	}
	return total
}

// MaybeCompact rewrites the WAL to contain only current outstanding hints.
// Call this periodically from a background loop.
func (h *Manager) MaybeCompact() error {
	if h.walPath == "" || h.walFile == nil {
		return nil
	}
	// Compact if WAL got "too big" / too many ops, but not too frequently.
	if h.walOps < 2000 && h.walBytes < 1<<20 { // 1 MiB
		return nil
	}
	if !h.lastCompact.IsZero() && time.Since(h.lastCompact) < 2*time.Second {
		return nil
	}
	return h.compactLocked()
}

func (h *Manager) compactLocked() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.walFile == nil {
		return nil
	}

	// Close current WAL before rewrite.
	_ = h.walFile.Close()
	h.walFile = nil

	tmp := h.walPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		// Try reopening WAL for appends so we don't get stuck.
		h.walFile, _ = os.OpenFile(h.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		return err
	}

	w := bufio.NewWriter(f)
	for target, byKey := range h.m {
		for _, rec := range byKey {
			rc := rec
			b, err := json.Marshal(walEntry{Op: "add", Target: target, Record: &rc})
			if err != nil {
				_ = w.Flush()
				_ = f.Close()
				_ = os.Remove(tmp)
				h.walFile, _ = os.OpenFile(h.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
				return err
			}
			b = append(b, '\n')
			if _, err := w.Write(b); err != nil {
				_ = w.Flush()
				_ = f.Close()
				_ = os.Remove(tmp)
				h.walFile, _ = os.OpenFile(h.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
				return err
			}
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		h.walFile, _ = os.OpenFile(h.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		h.walFile, _ = os.OpenFile(h.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		h.walFile, _ = os.OpenFile(h.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		return err
	}

	// Replace WAL across platforms.
	_ = os.Remove(h.walPath)
	if err := os.Rename(tmp, h.walPath); err != nil {
		_ = os.Remove(tmp)
		h.walFile, _ = os.OpenFile(h.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		return err
	}

	// Reopen WAL for append.
	wal, err := os.OpenFile(h.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	h.walFile = wal

	// Reset counters based on the compacted file.
	if st, err := wal.Stat(); err == nil {
		h.walBytes = st.Size()
	} else {
		h.walBytes = 0
	}
	h.walOps = 0
	h.lastCompact = time.Now()
	return nil
}

// Close flushes and closes the WAL (optional).
func (h *Manager) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.walFile == nil {
		return nil
	}
	err := h.walFile.Close()
	h.walFile = nil
	return err
}
