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
	"strings"
	"time"

	"mini-dynamo/internal/coordinator"
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

func main() {
	var (
		id   = flag.String("id", "n1", "node id (n1/n2/n3)")
		cfgp = flag.String("config", "nodes.json", "path to cluster config")
	)
	flag.Parse()

	cfg, err := loadConfig(*cfgp)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Validate quorum config.
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

	// Ring + store + transport.
	rg := ring.New(cfg.Nodes, cfg.VNodes)
	st := store.NewMem()
	tc := transport.NewClient(800 * time.Millisecond)

	// Coordinator (quorum reads/writes + read repair).
	coord := coordinator.New(self, rg, st, tc, coordinator.Config{
		N:       cfg.N,
		R:       cfg.R,
		W:       cfg.W,
		Timeout: 800 * time.Millisecond,
	})

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	// Distributed KV endpoints (Dynamo-style quorum + read-repair).
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

		case http.MethodGet:
			rec, ok, err := coord.Get(r.Context(), key)
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

	// /internal/put writes a record into this node's local store (LWW merge).
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

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transport.PutResponse{OK: true})
	})

	// /internal/get returns the record from this node's local store.
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
		_ = json.NewEncoder(w).Encode(transport.GetResponse{
			Found:  true,
			Record: rec,
		})
	})

	// Debug helper: ask this node to send an internal PUT to another node.
	// Example:
	//   /debug/replica_put?target=127.0.0.1:9002&key=cat&value=meow
	mux.HandleFunc("/debug/replica_put", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("target")
		key := r.URL.Query().Get("key")
		value := r.URL.Query().Get("value")
		if target == "" || key == "" {
			http.Error(w, "missing target or key", http.StatusBadRequest)
			return
		}

		rec := store.Record{
			Key:      key,
			Value:    []byte(value),
			Ts:       time.Now().UnixNano(),
			WriterID: self.ID,
		}

		// Self shortcut.
		if strings.TrimPrefix(baseURL(target), "http://") == self.Addr || strings.Contains(baseURL(target), self.Addr) {
			st.PutLWW(rec)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		defer cancel()

		var resp transport.PutResponse
		err := tc.PostJSON(ctx, baseURL(target)+"/internal/put", transport.PutRequest{Record: rec}, &resp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Debug: ring + sample replica mapping
	mux.HandleFunc("/debug/ring", func(w http.ResponseWriter, r *http.Request) {
		sampleKeys := []string{"a", "b", "cat", "dog", "pizza"}

		replicaMap := make(map[string][]types.NodeInfo, len(sampleKeys))
		for _, k := range sampleKeys {
			replicaMap[k] = rg.GetReplicas(k, cfg.N)
		}

		preview := 12
		if len(rg.VNodes) < preview {
			preview = len(rg.VNodes)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"self": self,
			"config": map[string]any{
				"vnodes_per_node": cfg.VNodes,
				"N":               cfg.N,
				"R":               cfg.R,
				"W":               cfg.W,
				"num_nodes":       len(cfg.Nodes),
				"num_vnodes":      len(rg.VNodes),
			},
			"token_preview":            rg.VNodes[:preview],
			"replicas_for_sample_keys": replicaMap,
		})
	})

	log.Printf("node %s listening on %s", self.ID, self.Addr)
	log.Fatal(http.ListenAndServe(self.Addr, mux))
}
