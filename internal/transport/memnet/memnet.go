// Package memnet is an in-process network for tests. It can drop, delay and
// reorder messages to exercise the sync protocol under bad conditions.
package memnet

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meetmehta0685/Clipboard-Sync/internal/protocol"
	"github.com/meetmehta0685/Clipboard-Sync/internal/transport"
)

// Chaos configures misbehaviour. The zero value is a perfect network.
type Chaos struct {
	DropRate float64       // probability a message is lost
	MaxDelay time.Duration // random per-message delay (reorders messages)
	// DropChunks only applies DropRate to chunk messages.
	DropChunks bool
}

// Network connects endpoints by name.
type Network struct {
	mu    sync.Mutex
	nodes map[string]*Endpoint
	chaos Chaos
	rng   *rand.Rand
	sent  atomic.Int64
	lost  atomic.Int64
}

// New creates a network.
func New(chaos Chaos, seed uint64) *Network {
	return &Network{nodes: map[string]*Endpoint{}, chaos: chaos, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b9))}
}

// SetChaos changes misbehaviour at runtime.
func (n *Network) SetChaos(c Chaos) { n.mu.Lock(); n.chaos = c; n.mu.Unlock() }

// Endpoint is one device's view of the network; it implements Transport.
type Endpoint struct {
	net   *Network
	name  string
	via   string
	inbox chan<- transport.Incoming
	up    atomic.Bool
}

// Join adds a device. via names the transport (e.g. "lan" or "relay").
func (n *Network) Join(name, via string) *Endpoint {
	e := &Endpoint{net: n, name: name, via: via}
	n.mu.Lock()
	n.nodes[name] = e
	n.mu.Unlock()
	return e
}

// SetUp simulates the endpoint going offline (false) or back online.
func (e *Endpoint) SetUp(v bool) { e.up.Store(v) }

func (e *Endpoint) Name() string    { return e.via }
func (e *Endpoint) Connected() bool { return e.up.Load() }

func (e *Endpoint) Run(ctx context.Context, inbox chan<- transport.Incoming) error {
	e.net.mu.Lock()
	e.inbox = inbox
	e.net.mu.Unlock()
	e.up.Store(true)
	<-ctx.Done()
	e.up.Store(false)
	return ctx.Err()
}

func (e *Endpoint) Send(ctx context.Context, m *protocol.Message) {
	if !e.up.Load() {
		return
	}
	e.net.mu.Lock()
	var peers []*Endpoint
	for _, p := range e.net.nodes {
		if p != e {
			peers = append(peers, p)
		}
	}
	e.net.mu.Unlock()
	for _, p := range peers {
		e.net.deliver(ctx, e, p, m)
	}
}

func (n *Network) deliver(ctx context.Context, from, to *Endpoint, m *protocol.Message) {
	n.mu.Lock()
	c := n.chaos
	drop := c.DropRate > 0 && (!c.DropChunks || m.Kind == protocol.KindChunk) && n.rng.Float64() < c.DropRate
	var delay time.Duration
	if c.MaxDelay > 0 {
		delay = time.Duration(n.rng.Int64N(int64(c.MaxDelay)))
	}
	inbox := to.inbox
	n.mu.Unlock()
	n.sent.Add(1)
	if drop || inbox == nil || !to.up.Load() {
		n.lost.Add(1)
		return
	}
	// Round-trip through JSON so tests exercise the real encoding.
	b, err := protocol.Encode(m)
	if err != nil {
		return
	}
	cp, _ := protocol.Decode(b)
	in := transport.Incoming{Msg: cp, Via: to.via, Reply: func(r *protocol.Message) { n.deliver(ctx, to, from, r) }}
	send := func() {
		if !to.up.Load() {
			n.lost.Add(1)
			return
		}
		select {
		case inbox <- in:
		case <-ctx.Done():
		}
	}
	if delay > 0 {
		time.AfterFunc(delay, send)
		return
	}
	go send()
}

// Stats returns messages sent and lost.
func (n *Network) Stats() (sent, lost int64) { return n.sent.Load(), n.lost.Load() }
