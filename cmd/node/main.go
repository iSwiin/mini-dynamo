package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mini-dynamo/internal/coordinator"
	"mini-dynamo/internal/hints"
	"mini-dynamo/internal/ring"
	"mini-dynamo/internal/store"
	"mini-dynamo/internal/transport"
	"mini-dynamo/internal/types"
)

type ClusterConfig struct {
	Nodes  []types.NodeInfo `json:"nodes"`
	VNodes int              `json:"vnodes"`
	N      int              `json:"n"`
	R      int              `json:"r"`
	W      int              `json:"w"`
}

func loadConfig(path string) (ClusterConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ClusterConfig{}, err
	}
	var cfg ClusterConfig
	return cfg, json.Unmarshal(b, &cfg)
}

func baseURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

type aeStats struct {
	mu           sync.Mutex
	enabled      bool
	interval     time.Duration
	maxPerTick   int
	lastPeer     string
	lastRun      time.Time
	lastDur      time.Duration
	lastCompared int
	lastPulled   int
	totalPulled  int
	totalErrors  int
	lastErr      string
}

func (s *aeStats) setRun(peer string, dur time.Duration, compared, pulled int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastPeer = peer
	s.lastRun = time.Now()
	s.lastDur = dur
	s.lastCompared = compared
	s.lastPulled = pulled
	s.totalPulled += pulled
	if err != nil {
		s.totalErrors++
		s.lastErr = err.Error()
	} else {
		s.lastErr = ""
	}
}

func (s *aeStats) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	lastUnix := int64(0)
	if !s.lastRun.IsZero() {
		lastUnix = s.lastRun.Unix()
	}

	return map[string]any{
		"enabled":       s.enabled,
		"interval_ms":   int64(s.interval / time.Millisecond),
		"max_per_tick":  s.maxPerTick,
		"last_peer":     s.lastPeer,
		"last_run_unix": lastUnix,
		"last_dur_ms":   int64(s.lastDur / time.Millisecond),
		"last_compared": s.lastCompared,
		"last_pulled":   s.lastPulled,
		"total_pulled":  s.totalPulled,
		"total_errors":  s.totalErrors,
		"last_error":    s.lastErr,
	}
}

func main() {
	var (
		id      = flag.String("id", "n1", "node id (n1/n2/n3)")
		cfgp    = flag.String("config", "nodes.json", "path to cluster config")
		hintwal = flag.String("hintwal", "", "path to hint WAL (default data/hints_<id>.wal)")

		kvwal  = flag.String("kvwal", "", "path to kv WAL (default data/kv_<id>.wal)")
		kvsnap = flag.String("kvsnap", "", "path to kv snapshot (default data/kv_<id>.snap.json)")
		snapI  = flag.Duration("snap_interval", 0, "snapshot interval (0 disables). snapshot blocks writes briefly")

		aeEnable   = flag.Bool("ae", true, "enable anti-entropy background sync")
		aeInterval = flag.Duration("ae_interval", 1500*time.Millisecond, "anti-entropy interval")
		aeMax      = flag.Int("ae_max", 200, "max keys repaired per anti-entropy tick")
	)
	flag.Parse()

	cfg, err := loadConfig(*cfgp)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if len(cfg.Nodes) == 0 {
		log.Fatalf("config has 0 nodes")
	}
	if cfg.VNodes <= 0 {
		log.Fatalf("vnodes must be > 0")
	}
	if cfg.N <= 0 || cfg.N > len(cfg.Nodes) {
		log.Fatalf("bad N=%d (nodes=%d)", cfg.N, len(cfg.Nodes))
	}
	if cfg.R <= 0 || cfg.R > cfg.N || cfg.W <= 0 || cfg.W > cfg.N {
		log.Fatalf("bad quorum R=%d W=%d for N=%d", cfg.R, cfg.W, cfg.N)
	}

	var self types.NodeInfo
	found := false
	for _, n := range cfg.Nodes {
		if n.ID == *id {
			self = n
			found = true
			break
		}
	}
	if !found {
		log.Fatalf("node id %q not found in config", *id)
	}

	nodesByID := make(map[string]types.NodeInfo, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		nodesByID[n.ID] = n
	}

	_ = os.MkdirAll("data", 0o755)

	// Ring + transport.
	rg := ring.New(cfg.Nodes, cfg.VNodes)
	tc := transport.NewClient(800 * time.Millisecond)

	// === Step 5: KV durability (snapshot + WAL replay) ===
	st := store.NewMem()

	kvWalPath := *kvwal
	if kvWalPath == "" {
		kvWalPath = filepath.Join("data", fmt.Sprintf("kv_%s.wal", self.ID))
	}
	kvSnapPath := *kvsnap
	if kvSnapPath == "" {
		kvSnapPath = filepath.Join("data", fmt.Sprintf("kv_%s.snap.json", self.ID))
	}

	if snap, err := store.LoadSnapshot(kvSnapPath); err != nil {
		log.Fatalf("load snapshot: %v", err)
	} else if snap != nil {
		st.LoadAll(snap)
	}

	kvWAL, err := store.OpenWAL(kvWalPath)
	if err != nil {
		log.Fatalf("open kv wal: %v", err)
	}
	defer func() { _ = kvWAL.Close() }()

	// Replay WAL into store (no WAL writes during replay).
	if err := kvWAL.Replay(func(rec store.Record) { st.ApplyLWW(rec) }); err != nil {
		log.Fatalf("replay kv wal: %v", err)
	}

	// Attach WAL so future writes are durable.
	st.AttachWAL(kvWAL)

	// Optional periodic snapshot + WAL truncate.
	if *snapI > 0 {
		go func() {
			t := time.NewTicker(*snapI)
			defer t.Stop()
			for range t.C {
				if err := st.SnapshotAndResetWAL(kvSnapPath); err != nil {
					log.Printf("snapshot: %v", err)
				}
			}
		}()
	}

	// === Step 3: durable hints + handoff loop ===
	hwal := *hintwal
	if hwal == "" {
		hwal = filepath.Join("data", fmt.Sprintf("hints_%s.wal", self.ID))
	}
	hm, err := hints.NewPersistent(hwal)
	if err != nil {
		log.Fatalf("hint wal: %v", err)
	}
	defer func() { _ = hm.Close() }()

	go func() {
		t := time.NewTicker(400 * time.Millisecond)
		defer t.Stop()

		for range t.C {
			targets := hm.Targets()
			for _, tid := range targets {
				target, ok := nodesByID[tid]
				if !ok {
					continue
				}
				recs := hm.RecordsFor(tid)
				for _, rec := range recs {
					ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
					var resp transport.PutResponse
					err := tc.PostJSON(ctx, baseURL(target.Addr)+"/internal/put", transport.PutRequest{Record: rec}, &resp)
					cancel()
					if err == nil {
						hm.DeleteIfSame(tid, rec.Key, rec)
					}
				}
			}
			if err := hm.MaybeCompact(); err != nil {
				log.Printf("hint wal compact: %v", err)
			}
		}
	}()

	// Coordinator.
	coord := coordinator.New(self, rg, st, tc, hm, coordinator.Config{
		N:        cfg.N,
		R:        cfg.R,
		W:        cfg.W,
		NumNodes: len(cfg.Nodes),
		Timeout:  800 * time.Millisecond,
	})

	// === Step 4: anti-entropy ===
	ae := &aeStats{
		enabled:    *aeEnable,
		interval:   *aeInterval,
		maxPerTick: *aeMax,
	}

	if ae.enabled {
		peers := make([]types.NodeInfo, 0, len(cfg.Nodes)-1)
		for _, n := range cfg.Nodes {
			if n.ID != self.ID {
				peers = append(peers, n)
			}
		}

		if len(peers) > 0 {
			go func() {
				t := time.NewTicker(ae.interval)
				defer t.Stop()

				next := 0
				for range t.C {
					peer := peers[next%len(peers)]
					next++

					start := time.Now()
					compared, pulled, runErr := runAntiEntropyOnce(tc, st, peer, ae.maxPerTick)
					ae.setRun(peer.ID, time.Since(start), compared, pulled, runErr)
				}
			}()
		}
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	// Distributed KV
	mux.HandleFunc("/kv/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/kv/")
		if key == "" {
			http.Error(w, "missing key", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodPut:
			val, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body failed", http.StatusBadRequest)
				return
			}
			if err := coord.Put(r.Context(), key, val); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case http.MethodDelete:
			if err := coord.Delete(r.Context(), key); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case http.MethodGet:
			rec, ok, err := coord.Get(r.Context(), key)
			_ = rec
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(rec.Value)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Internal replica APIs
	mux.HandleFunc("/internal/put", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req transport.PutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Record.Key == "" {
			http.Error(w, "missing record.key", http.StatusBadRequest)
			return
		}

		st.PutLWW(req.Record)

		if req.HintFor != "" {
			hm.Add(req.HintFor, req.Record)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transport.PutResponse{OK: true})
	})

	mux.HandleFunc("/internal/get", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req transport.GetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
			http.Error(w, "bad json or missing key", http.StatusBadRequest)
			return
		}

		rec, ok := st.Get(req.Key)

		w.Header().Set("Content-Type", "application/json")
		if !ok {
			_ = json.NewEncoder(w).Encode(transport.GetResponse{Found: false})
			return
		}
		_ = json.NewEncoder(w).Encode(transport.GetResponse{Found: true, Record: rec})
	})

	// Anti-entropy metadata endpoint
	mux.HandleFunc("/internal/keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		meta := st.KeysMeta()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transport.KeysResponse{Keys: meta})
	})

	// Debug endpoints
	mux.HandleFunc("/debug/hints", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count":   hm.Count(),
			"targets": hm.Targets(),
			"wal":     hwal,
		})
	})

	mux.HandleFunc("/debug/ae", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ae.snapshot())
	})

	mux.HandleFunc("/debug/persist", func(w http.ResponseWriter, r *http.Request) {
		ops, bytes := kvWAL.Stats()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kv_wal":        kvWalPath,
			"kv_snapshot":   kvSnapPath,
			"wal_ops":       ops,
			"wal_bytes":     bytes,
			"snapshot_tick": int64(*snapI / time.Millisecond),
		})
	})

	log.Printf("node %s listening on %s", self.ID, self.Addr)
	log.Fatal(http.ListenAndServe(self.Addr, mux))
}

func runAntiEntropyOnce(tc *transport.Client, st *store.MemStore, peer types.NodeInfo, maxPull int) (compared int, pulled int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	var kres transport.KeysResponse
	if e := tc.PostJSON(ctx, baseURL(peer.Addr)+"/internal/keys", transport.KeysRequest{}, &kres); e != nil {
		return 0, 0, e
	}

	if maxPull <= 0 {
		maxPull = 200
	}

	local := st.KeysMeta()

	for key, pm := range kres.Keys {
		compared++

		lm, ok := local[key]
		needPull := false

		if !ok {
			needPull = true
		} else {
			lr := store.Record{Ts: lm.Ts, WriterID: lm.WriterID}
			pr := store.Record{Ts: pm.Ts, WriterID: pm.WriterID}
			w := store.Newer(lr, pr)

			if !(lr.Ts == pr.Ts && lr.WriterID == pr.WriterID) &&
				w.Ts == pr.Ts && w.WriterID == pr.WriterID {
				needPull = true
			}
		}

		if !needPull {
			continue
		}

		ctx2, cancel2 := context.WithTimeout(context.Background(), 1200*time.Millisecond)
		var gres transport.GetResponse
		e := tc.PostJSON(ctx2, baseURL(peer.Addr)+"/internal/get", transport.GetRequest{Key: key}, &gres)
		cancel2()
		if e != nil {
			err = e
			continue
		}
		if !gres.Found {
			continue
		}

		st.PutLWW(gres.Record)
		pulled++

		if pulled >= maxPull {
			break
		}
	}

	return compared, pulled, err
}
