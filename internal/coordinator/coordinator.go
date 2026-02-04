package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mini-dynamo/internal/hints"
	"mini-dynamo/internal/ring"
	"mini-dynamo/internal/store"
	"mini-dynamo/internal/transport"
	"mini-dynamo/internal/types"
)

type Config struct {
	N        int
	R        int
	W        int
	NumNodes int
	Timeout  time.Duration
}

type Coordinator struct {
	Self   types.NodeInfo
	Ring   ring.Ring
	Store  *store.MemStore
	Client *transport.Client
	Hints  *hints.Manager
	Cfg    Config
}

func New(self types.NodeInfo, rg ring.Ring, st *store.MemStore, cl *transport.Client, hm *hints.Manager, cfg Config) *Coordinator {
	return &Coordinator{
		Self:   self,
		Ring:   rg,
		Store:  st,
		Client: cl,
		Hints:  hm,
		Cfg:    cfg,
	}
}

func baseURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

func (c *Coordinator) replicaPut(ctx context.Context, n types.NodeInfo, rec store.Record, hintFor string) error {
	if n.ID == c.Self.ID {
		c.Store.PutLWW(rec)
		if hintFor != "" && c.Hints != nil {
			c.Hints.Add(hintFor, rec)
		}
		return nil
	}

	var resp transport.PutResponse
	return c.Client.PostJSON(ctx,
		baseURL(n.Addr)+"/internal/put",
		transport.PutRequest{Record: rec, HintFor: hintFor},
		&resp,
	)
}

func (c *Coordinator) replicaGet(ctx context.Context, n types.NodeInfo, key string) (store.Record, bool, error) {
	if n.ID == c.Self.ID {
		rec, ok := c.Store.Get(key)
		return rec, ok, nil
	}

	var resp transport.GetResponse
	err := c.Client.PostJSON(ctx,
		baseURL(n.Addr)+"/internal/get",
		transport.GetRequest{Key: key},
		&resp,
	)
	if err != nil {
		return store.Record{}, false, err
	}
	return resp.Record, resp.Found, nil
}

func sameVersion(a, b store.Record) bool {
	return a.Ts == b.Ts && a.WriterID == b.WriterID
}

// PutRecord is the shared write path (supports tombstones too).
func (c *Coordinator) PutRecord(ctx context.Context, key string, rec store.Record) error {
	if c.Cfg.NumNodes <= 0 {
		return fmt.Errorf("coordinator config missing NumNodes")
	}

	// Full unique node order around the ring (for sloppy quorum).
	order := c.Ring.GetReplicas(key, c.Cfg.NumNodes)
	if len(order) == 0 {
		return fmt.Errorf("no replicas available")
	}

	// Preferred replicas = first N.
	prefN := c.Cfg.N
	if prefN > len(order) {
		prefN = len(order)
	}
	preferred := order[:prefN]
	fallbacks := []types.NodeInfo{}
	if prefN < len(order) {
		fallbacks = order[prefN:]
	}

	// Phase 1: preferred replicas in parallel.
	ctx1, cancel1 := context.WithTimeout(ctx, c.Cfg.Timeout)
	defer cancel1()

	type res struct {
		node types.NodeInfo
		err  error
	}
	ch := make(chan res, len(preferred))

	for _, n := range preferred {
		n := n
		go func() {
			ch <- res{node: n, err: c.replicaPut(ctx1, n, rec, "")}
		}()
	}

	acks := 0
	failedPreferred := make([]types.NodeInfo, 0, len(preferred))

	for i := 0; i < len(preferred); i++ {
		r := <-ch
		if r.err == nil {
			acks++
			if acks >= c.Cfg.W {
				cancel1()
				return nil
			}
		} else {
			failedPreferred = append(failedPreferred, r.node)
		}
	}

	// Phase 2: sloppy quorum fallbacks + hinted handoff.
	need := c.Cfg.W - acks
	if need <= 0 {
		return nil
	}
	if len(fallbacks) == 0 {
		return fmt.Errorf("write quorum not reached: acks=%d need=%d (no fallbacks)", acks, c.Cfg.W)
	}

	failedIDs := make([]string, 0, len(failedPreferred))
	for _, n := range failedPreferred {
		failedIDs = append(failedIDs, n.ID)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), c.Cfg.Timeout)
	defer cancel2()

	for i := 0; i < len(fallbacks) && need > 0; i++ {
		fb := fallbacks[i]

		hintFor := ""
		if len(failedIDs) > 0 {
			hintFor = failedIDs[0]
			failedIDs = failedIDs[1:]
		}

		if err := c.replicaPut(ctx2, fb, rec, hintFor); err == nil {
			acks++
			need--
			if acks >= c.Cfg.W {
				return nil
			}
		}
	}

	return fmt.Errorf("write quorum not reached: acks=%d need=%d", acks, c.Cfg.W)
}

// Normal PUT (non-delete)
func (c *Coordinator) Put(ctx context.Context, key string, value []byte) error {
	rec := store.Record{
		Key:      key,
		Value:    value,
		Ts:       time.Now().UnixNano(),
		WriterID: c.Self.ID,
		Deleted:  false,
	}
	return c.PutRecord(ctx, key, rec)
}

// DELETE = tombstone write
func (c *Coordinator) Delete(ctx context.Context, key string) error {
	rec := store.Record{
		Key:      key,
		Value:    nil,
		Ts:       time.Now().UnixNano(),
		WriterID: c.Self.ID,
		Deleted:  true,
	}
	return c.PutRecord(ctx, key, rec)
}

func (c *Coordinator) Get(ctx context.Context, key string) (store.Record, bool, error) {
	replicas := c.Ring.GetReplicas(key, c.Cfg.N)
	if len(replicas) < c.Cfg.R {
		return store.Record{}, false, fmt.Errorf("read quorum impossible: replicas=%d R=%d", len(replicas), c.Cfg.R)
	}

	ctx, cancel := context.WithTimeout(ctx, c.Cfg.Timeout)
	defer cancel()

	type result struct {
		node  types.NodeInfo
		rec   store.Record
		found bool
		err   error
	}
	ch := make(chan result, len(replicas))

	for _, n := range replicas {
		n := n
		go func() {
			rec, found, err := c.replicaGet(ctx, n, key)
			ch <- result{node: n, rec: rec, found: found, err: err}
		}()
	}

	// Collect R successful responses.
	success := 0
	resps := make([]result, 0, c.Cfg.R)

	for i := 0; i < len(replicas) && success < c.Cfg.R; i++ {
		r := <-ch
		if r.err == nil {
			success++
			resps = append(resps, r)
		}
	}

	if success < c.Cfg.R {
		return store.Record{}, false, fmt.Errorf("read quorum not reached: success=%d need=%d", success, c.Cfg.R)
	}

	// Resolve winner via LWW among found responses.
	var winner store.Record
	haveWinner := false
	for _, r := range resps {
		if !r.found {
			continue
		}
		if !haveWinner {
			winner = r.rec
			haveWinner = true
		} else {
			winner = store.Newer(winner, r.rec)
		}
	}

	if !haveWinner {
		return store.Record{}, false, nil
	}

	// Read repair (best-effort), INCLUDING tombstones.
	for _, r := range resps {
		needsRepair := false

		if !r.found {
			needsRepair = true
		} else {
			best := store.Newer(r.rec, winner)
			if sameVersion(best, winner) && !sameVersion(r.rec, winner) {
				needsRepair = true
			}
		}

		if needsRepair {
			n := r.node
			go func() {
				ctx2, cancel2 := context.WithTimeout(context.Background(), c.Cfg.Timeout)
				defer cancel2()
				_ = c.replicaPut(ctx2, n, winner, "")
			}()
		}
	}

	// Tombstone means "logically not found"
	if winner.Deleted {
		return winner, false, nil
	}

	return winner, true, nil
}
