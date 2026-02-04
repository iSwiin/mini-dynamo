package transport

import "mini-dynamo/internal/store"

// GET
type GetRequest struct {
	Key string `json:"key"`
}

type GetResponse struct {
	Found  bool         `json:"found"`
	Record store.Record `json:"record,omitempty"`
}

// PUT
type PutRequest struct {
	Record  store.Record `json:"record"`
	HintFor string       `json:"hint_for,omitempty"`
}

type PutResponse struct {
	OK bool `json:"ok"`
}

// KEYS (anti-entropy)
type KeysRequest struct{}

type KeysResponse struct {
	Keys map[string]store.Meta `json:"keys"`
}
