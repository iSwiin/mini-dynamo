package ring

import (
	"encoding/binary"
	"hash/fnv"
	"sort"
	"strconv"

	"mini-dynamo/internal/types"
)

type VNode struct {
	Token  uint64         `json:"token"`
	Node   types.NodeInfo `json:"node"`
	VIndex int            `json:"vindex"`
}

type Ring struct {
	VNodes []VNode
}

// New builds a ring with vnodesPerNode virtual nodes per physical node.
func New(nodes []types.NodeInfo, vnodesPerNode int) Ring {
	vnodes := make([]VNode, 0, len(nodes)*vnodesPerNode)
	for _, n := range nodes {
		for i := 0; i < vnodesPerNode; i++ {
			// Token for vnode i of node n
			token := hash64(n.ID + "#" + strconv.Itoa(i))
			vnodes = append(vnodes, VNode{
				Token:  token,
				Node:   n,
				VIndex: i,
			})
		}
	}

	sort.Slice(vnodes, func(i, j int) bool {
		return vnodes[i].Token < vnodes[j].Token
	})

	return Ring{VNodes: vnodes}
}

// GetReplicas returns N distinct physical nodes responsible for the key.
// It walks clockwise on the ring starting from the key's token.
func (r Ring) GetReplicas(key string, N int) []types.NodeInfo {
	if len(r.VNodes) == 0 || N <= 0 {
		return nil
	}

	start := r.search(hash64(key))

	seen := make(map[string]bool, N)
	out := make([]types.NodeInfo, 0, N)

	// Walk ring until we collect N distinct nodes or we looped all vnodes.
	for i := 0; i < len(r.VNodes) && len(out) < N; i++ {
		vn := r.VNodes[(start+i)%len(r.VNodes)]
		if !seen[vn.Node.ID] {
			seen[vn.Node.ID] = true
			out = append(out, vn.Node)
		}
	}
	return out
}

// search finds the first vnode index with Token >= target (clockwise start).
// If none, wraps to 0.
func (r Ring) search(target uint64) int {
	i := sort.Search(len(r.VNodes), func(i int) bool {
		return r.VNodes[i].Token >= target
	})
	if i == len(r.VNodes) {
		return 0
	}
	return i
}

func hash64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return binary.BigEndian.Uint64(h.Sum(nil))
}
