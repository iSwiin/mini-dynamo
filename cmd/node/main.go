package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"mini-dynamo/internal/ring"
	"mini-dynamo/internal/store"
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

	// Build the consistent-hash ring (vnodes + sorted tokens).
	rg := ring.New(cfg.Nodes, cfg.VNodes)

	// Local in-memory store (single-node KV for now).
	st := store.NewMem()

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	// Local-only KV endpoints (Milestone 2: storage + API).
	// Later, this route becomes the "coordinator" that talks to replicas.
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
			rec := store.Record{
				Key:      key,
				Value:    val,
				Ts:       time.Now().UnixNano(),
				WriterID: self.ID,
			}
			st.Put(rec)
			w.WriteHeader(http.StatusNoContent)

		case http.MethodGet:
			rec, ok := st.Get(key)
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
