// Package relay is the WebSocket client for the clipsync relay server.
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/meetmehta/clipsync/internal/protocol"
	"github.com/meetmehta/clipsync/internal/transport"
)

// MaxFrame bounds a single WebSocket message. A 256 KiB chunk is ~350 KiB
// once base64'd into JSON.
const MaxFrame = 1 << 20

// Client keeps a connection to one relay room open.
type Client struct {
	URL string
	// Hello is called on every (re)connect so Since reflects the latest clock.
	Hello func() *protocol.Hello
	Log   *slog.Logger

	q         *transport.Queue
	connected atomic.Bool
}

// New creates a relay transport.
func New(url string, hello func() *protocol.Hello, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{URL: url, Hello: hello, Log: log, q: transport.NewQueue()}
}

func (c *Client) Name() string    { return "relay" }
func (c *Client) Connected() bool { return c.connected.Load() }

func (c *Client) Send(ctx context.Context, m *protocol.Message) { c.q.Push(ctx, m) }

// Run reconnects with jittered exponential backoff until ctx ends.
func (c *Client) Run(ctx context.Context, inbox chan<- transport.Incoming) error {
	backoff := 500 * time.Millisecond
	for {
		start := time.Now()
		err := c.session(ctx, inbox)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > 30*time.Second {
			backoff = 500 * time.Millisecond
		}
		c.Log.Warn("relay disconnected", "url", c.URL, "err", err, "retry_in", backoff)
		sleep := backoff/2 + rand.N(backoff/2+1)
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (c *Client) session(ctx context.Context, inbox chan<- transport.Incoming) error {
	conn, err := Dial(ctx, c.URL, c.Hello())
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c.q.SetOpen(true)
	c.connected.Store(true)
	defer func() {
		c.connected.Store(false)
		c.q.SetOpen(false)
	}()
	c.Log.Info("relay connected", "url", c.URL)

	errc := make(chan error, 2)
	go func() {
		for {
			m, err := c.q.Next(ctx)
			if err != nil {
				errc <- err
				return
			}
			if err := conn.Send(ctx, m); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		reply := func(m *protocol.Message) { c.q.Push(ctx, m) }
		for {
			m, err := conn.Recv(ctx)
			if err != nil {
				errc <- err
				return
			}
			if m.Kind == protocol.KindError {
				errc <- fmt.Errorf("relay: %s", m.Error)
				return
			}
			select {
			case inbox <- transport.Incoming{Msg: m, Via: "relay", Reply: reply}:
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			}
		}
	}()
	return <-errc
}

// Conn is a raw relay connection, used directly by pairing.
type Conn struct {
	ws *websocket.Conn
}

// Dial connects to the relay and joins the room described by hello.
func Dial(ctx context.Context, url string, hello *protocol.Hello) (*Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, url, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxFrame)
	c := &Conn{ws: ws}
	if err := c.Send(dctx, &protocol.Message{Kind: protocol.KindHello, Hello: hello}); err != nil {
		ws.CloseNow()
		return nil, err
	}
	return c, nil
}

// Send writes one message.
func (c *Conn) Send(ctx context.Context, m *protocol.Message) error {
	b, err := protocol.Encode(m)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.ws.Write(wctx, websocket.MessageText, b)
}

// Recv reads one message.
func (c *Conn) Recv(ctx context.Context) (*protocol.Message, error) {
	_, b, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	return protocol.Decode(b)
}

// Close closes the connection.
func (c *Conn) Close() error {
	err := c.ws.Close(websocket.StatusNormalClosure, "")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
