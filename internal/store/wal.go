package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type WAL struct {
	mu    sync.Mutex
	path  string
	f     *os.File
	ops   int
	bytes int64
}

func OpenWAL(path string) (*WAL, error) {
	if path == "" {
		return nil, errors.New("wal path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	w := &WAL{path: path, f: f}
	if st, err := f.Stat(); err == nil {
		w.bytes = st.Size()
	}
	return w, nil
}

func (w *WAL) Append(rec Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	if _, err := w.f.Write(b); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}

	w.ops++
	w.bytes += int64(len(b))
	return nil
}

func (w *WAL) Replay(apply func(Record)) error {
	f, err := os.Open(w.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// allow bigger lines (values are base64 in JSON)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if rec.Key == "" {
			continue
		}
		apply(rec)
	}
	return sc.Err()
}

func (w *WAL) Truncate() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	w.ops = 0
	w.bytes = 0
	return w.f.Sync()
}

func (w *WAL) Stats() (ops int, bytes int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ops, w.bytes
}

func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func (w *WAL) Path() string { return w.path }
