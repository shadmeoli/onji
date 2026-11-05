package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"onji/internal/daemon"
	"onji/internal/process"
)

type Client struct {
	http      *http.Client
	transport *http.Transport
}

func NewClient(socket string) *Client {
	t := &http.Transport{MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, IdleConnTimeout: 15 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		}}
	return &Client{http: &http.Client{Transport: t, Timeout: 125 * time.Second}, transport: t}
}
func (c *Client) Close() { c.transport.CloseIdleConnections() }
func (c *Client) call(ctx context.Context, method, path string, body, result any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, "http://onji"+path, &buf)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return fmt.Errorf("cannot reach onjid (start the daemon and check --socket): %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 16<<20 {
		return errors.New("daemon response exceeded 16 MiB")
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("daemon returned %s", resp.Status)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(b, result)
}
func (c *Client) State(ctx context.Context) (daemon.State, error) {
	var s daemon.State
	err := c.call(ctx, "GET", "/v1/state", nil, &s)
	return s, err
}
func (c *Client) Processes(ctx context.Context, host string) ([]process.Info, error) {
	var p []process.Info
	err := c.call(ctx, "GET", "/v1/processes?host="+url.QueryEscape(host), nil, &p)
	return p, err
}
func (c *Client) Forward(ctx context.Context, q ForwardRequest) error {
	return c.call(ctx, "POST", "/v1/forward", q, nil)
}
func (c *Client) Signal(ctx context.Context, q SignalRequest) error {
	return c.call(ctx, "POST", "/v1/signal", q, nil)
}
