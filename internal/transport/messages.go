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
	Record store.Record `json:"record"`
}

type PutResponse struct {
	OK bool `json:"ok"`
}
