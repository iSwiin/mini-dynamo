package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	http *http.Client
}

func NewClient(timeout time.Duration) *Client {
	return &Client{
		http: &http.Client{Timeout: timeout},
	}
}

func (c *Client) PostJSON(ctx context.Context, url string, req any, resp any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	r, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer r.Body.Close()

	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return fmt.Errorf("POST %s: status %d", url, r.StatusCode)
	}

	if resp == nil {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(resp)
}
