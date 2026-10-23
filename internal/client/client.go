// Package client talks to a running clipsyncd over its Unix socket. It is
// shared by the CLI and the tray app.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotRunning means no daemon is listening on the socket.
var ErrNotRunning = errors.New("clipsyncd is not running (start it with `clipsyncd run`)")

// Client is a local API client.
type Client struct {
	sock string
	http *http.Client
}

// New creates a client for the socket at path.
func New(sock string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	return &Client{sock: sock, http: &http.Client{Transport: tr}}
}

// Running reports whether a daemon answers on the socket.
func (c *Client) Running() bool {
	conn, err := net.DialTimeout("unix", c.sock, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://clipsyncd"+path, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) {
			return nil, ErrNotRunning
		}
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var e struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, errors.New(e.Error)
	}
	return resp, nil
}

// Call performs a request and decodes the JSON response into out (if non-nil).
func (c *Client) Call(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	case io.Reader:
		r = b
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return err
		}
		r = bytes.NewReader(j)
	}
	resp, err := c.do(ctx, method, path, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Path builds a query string path.
func Path(p string, kv ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			q.Set(kv[i], kv[i+1])
		}
	}
	if len(q) == 0 {
		return p
	}
	return p + "?" + q.Encode()
}

// Stream reads newline-delimited JSON objects from a POST endpoint.
func (c *Client) Stream(ctx context.Context, path string, each func(map[string]json.RawMessage) error) error {
	resp, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if e, ok := m["error"]; ok {
			var s string
			_ = json.Unmarshal(e, &s)
			return errors.New(s)
		}
		if err := each(m); err != nil {
			return err
		}
	}
}

// Events subscribes to the server-sent event stream.
func (c *Client) Events(ctx context.Context, each func(json.RawMessage)) error {
	resp, err := c.do(ctx, http.MethodGet, "/events", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			each(json.RawMessage(data))
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("event stream: %w", err)
	}
	return nil
}
