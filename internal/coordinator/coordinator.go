package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mini-dynamo/internal/ring"
	"mini-dynamo/internal/store"
	"mini-dynamo/internal/transport"
	"mini-dynamo/internal/types"
)

type Config struct {
	N       int
	R       int
	W       int
	Timeout time.Duration
}

type Coordinator struct {
	Self   types.NodeInfo
	Ring   ring.Ring
	Store  *store.MemStore
	Client *transport.Client
	Cfg    Config
}

func New(self types.NodeInfo, rg ring.Ring, st *store.MemStore, cl *transport.Client, cfg Config) *Coordinator {
	return &Coordinator{
		Self:   self,
		Ring:   rg,
		Store:  st,
		Client: cl,
		Cfg:    cfg,
	}
}

func baseURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

func sameVersion(a, b store.Record) bool {
	return a.Ts == b.Ts && a.WriterID == b.WriterID
}

func (c *Coordinator) replicaPut(ctx context.Context, n types.NodeInfo, rec store.Record) error {
	if n.ID == c.Self.ID {
		c.Store.PutLWW(rec)
		return nil
	}
	var resp transport.PutResponse
	return c.Client.PostJSON(ctx, baseURL(n.Addr)+"/internal/put", transport.PutRequest{Record: rec}, &resp)
}

func (c *Coordinator) replicaGet(ctx context.Context, n types.NodeInfo, key string) (store.Record, bool, error) {
	if n.ID == c.Self.ID {
		rec, ok := c.Store.Get(key)
		return rec, ok, nil
	}
	var resp transport.GetResponse
	err := c.Client.PostJSON(ctx, baseURL(n.Addr)+"/internal/get", transport.GetRequest{Key: key}, &resp)
	if err != nil {
		return store.Record{}, false, err
	}
	return resp.Record, resp.Found, nil
}

func (c *Coordinator) Put(ctx context.Context, key string, value []byte) error {
	replicas := c.Ring.GetReplicas(key, c.Cfg.N)
	if len(replicas) < c.Cfg.W {
		return fmt.Errorf("write quorum impossible: replicas=%d W=%d", len(replicas), c.Cfg.W)
	}

	rec := store.Record{
		Key:      key,
		Value:    value,
		Ts:       time.Now().UnixNano(),
		WriterID: c.Self.ID,
	}

	ctx, cancel := context.WithTimeout(ctx, c.Cfg.Timeout)
	defer cancel()

	type result struct{ err error }
	ch := make(chan result, len(replicas))

	for _, n := range replicas {
		n := n
		go func() {
			ch <- result{err: c.replicaPut(ctx, n, rec)}
		}()
	}

	acks := 0
	for i := 0; i < len(replicas); i++ {
		if (<-ch).err == nil {
			acks++
			if acks >= c.Cfg.W {
				cancel() // stop waiting on stragglers
				return nil
			}
		}
	}

	return fmt.Errorf("write quorum not reached: acks=%d need=%d", acks, c.Cfg.W)
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

	// Collect R successful responses (success = request succeeded, found or not).
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

	// Read repair (best-effort): update replicas that are missing or stale.
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
				_ = c.replicaPut(ctx2, n, winner)
			}()
		}
	}

	return winner, true, nil
}
