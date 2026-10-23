// Package transport defines how the sync engine talks to other devices. Each
// transport (relay, LAN) delivers protocol messages; the engine sends every
// message on all of them and dedupes on receipt.
package transport

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/meetmehta/clipsync/internal/protocol"
)

// Incoming is a message received from a peer.
type Incoming struct {
	Msg *protocol.Message
	Via string // transport name, for metrics and logs
	// Reply sends a message back along the same path (one LAN peer, or the
	// relay). Used to answer chunk requests.
	Reply func(*protocol.Message)
}

// Transport is one delivery path.
type Transport interface {
	Name() string
	// Run connects and keeps the transport alive until ctx is cancelled,
	// delivering received messages to inbox.
	Run(ctx context.Context, inbox chan<- Incoming) error
	// Send queues a message for every peer reachable on this path. It must
	// not block for long; when disconnected it drops the message.
	Send(ctx context.Context, m *protocol.Message)
	// Connected reports whether at least one peer is reachable.
	Connected() bool
}

// Queue is a two-lane outbound queue: control and clip headers go ahead of
// bulk chunk data, so a text clip is never stuck behind a 10 MB screenshot.
type Queue struct {
	high chan *protocol.Message
	low  chan *protocol.Message
	open atomic.Bool
}

// NewQueue creates a queue. Chunk pushes block when the low lane is full,
// which gives the sender backpressure.
func NewQueue() *Queue {
	return &Queue{high: make(chan *protocol.Message, 256), low: make(chan *protocol.Message, 16)}
}

// SetOpen marks whether the connection behind the queue is up. Messages
// pushed while closed are dropped.
func (q *Queue) SetOpen(v bool) {
	q.open.Store(v)
	if !v {
		q.drain()
	}
}

func (q *Queue) drain() {
	for {
		select {
		case <-q.high:
		case <-q.low:
		default:
			return
		}
	}
}

// Push enqueues m. High-priority messages are dropped if the lane is full
// (the relay replay recovers them); chunks block until there is room.
func (q *Queue) Push(ctx context.Context, m *protocol.Message) {
	if !q.open.Load() {
		return
	}
	if m.Priority() {
		select {
		case q.high <- m:
		default:
		}
		return
	}
	select {
	case q.low <- m:
	case <-ctx.Done():
	}
}

// ErrClosed is returned by Next when ctx ends.
var ErrClosed = errors.New("transport: queue closed")

// Next returns the next message, always preferring the high lane.
func (q *Queue) Next(ctx context.Context) (*protocol.Message, error) {
	select {
	case m := <-q.high:
		return m, nil
	default:
	}
	select {
	case m := <-q.high:
		return m, nil
	case m := <-q.low:
		return m, nil
	case <-ctx.Done():
		return nil, ErrClosed
	}
}
