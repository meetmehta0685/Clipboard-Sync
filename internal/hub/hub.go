// Package hub is the relay server: a WebSocket hub where devices join a room
// and exchange encrypted envelopes. It never sees plaintext or keys.
package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/meetmehta/clipsync/internal/protocol"
)

// Config tunes the relay's limits.
type Config struct {
	MaxFrame     int64         // max bytes per WebSocket message
	RoomHistory  int           // clips (and control messages) kept per room
	RoomBytes    int64         // max stored ciphertext per room
	MsgsPerSec   float64       // per-connection message rate
	BytesPerSec  int           // per-connection byte rate
	MaxRooms     int           // rooms in memory
	RoomIdle     time.Duration // forget empty rooms after this long
	PairTTL      time.Duration // pairing rooms expire after this long
	Origins      []string      // allowed browser origins (besides same-host)
	PingInterval time.Duration
}

// DefaultConfig returns production-ish limits.
func DefaultConfig() Config {
	return Config{
		MaxFrame:     1 << 20,
		RoomHistory:  50,
		RoomBytes:    64 << 20,
		MsgsPerSec:   200,
		BytesPerSec:  16 << 20,
		MaxRooms:     10000,
		RoomIdle:     7 * 24 * time.Hour,
		PairTTL:      10 * time.Minute,
		PingInterval: 30 * time.Second,
	}
}

// Stats are exposed at /stats.
type Stats struct {
	Rooms       int64 `json:"rooms"`
	Connections int64 `json:"connections"`
	Messages    int64 `json:"messages"`
	Bytes       int64 `json:"bytes"`
	Rejected    int64 `json:"rejected"`
}

// Hub holds all rooms.
type Hub struct {
	cfg Config
	log *slog.Logger

	mu    sync.Mutex
	rooms map[string]*room

	conns, msgs, bytes, rejected atomic.Int64
}

// New creates a hub.
func New(cfg Config, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{cfg: cfg, log: log, rooms: make(map[string]*room)}
}

// stored is one replayable message with its encoded form.
type stored struct {
	seq  uint64
	id   string // clip ID, empty for control/rekey
	raw  []byte
	size int64
}

type room struct {
	id        string
	gen       string // changes whenever the room is (re)created in memory
	pair      bool
	created   time.Time
	tokenHash [32]byte
	hasToken  bool

	mu         sync.Mutex
	members    map[*client]struct{}
	log        []*stored
	seq        uint64
	chunks     map[string]map[int]*stored
	bytes      int64
	lastActive time.Time
}

type client struct {
	device string
	send   chan []byte
	room   *room
	closed chan struct{}
	once   sync.Once
}

func (c *client) close() { c.once.Do(func() { close(c.closed) }) }

// deliver queues a frame without blocking; a client that cannot keep up is
// disconnected rather than stalling the room.
func (c *client) deliver(b []byte) {
	select {
	case c.send <- b:
	case <-c.closed:
	default:
		c.close()
	}
}

// Stats snapshots counters.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	n := len(h.rooms)
	h.mu.Unlock()
	return Stats{Rooms: int64(n), Connections: h.conns.Load(), Messages: h.msgs.Load(), Bytes: h.bytes.Load(), Rejected: h.rejected.Load()}
}

// ServeHTTP upgrades to WebSocket and runs one client.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.cfg.Origins})
	if err != nil {
		return
	}
	ws.SetReadLimit(h.cfg.MaxFrame)
	h.conns.Add(1)
	defer h.conns.Add(-1)
	if err := h.serve(r.Context(), ws); err != nil && !isClose(err) {
		h.log.Debug("client closed", "err", err)
	}
}

func isClose(err error) bool {
	return errors.Is(err, context.Canceled) || websocket.CloseStatus(err) != -1
}

func (h *Hub) fail(ctx context.Context, ws *websocket.Conn, msg string) error {
	h.rejected.Add(1)
	b, _ := protocol.Encode(&protocol.Message{Kind: protocol.KindError, Error: msg})
	_ = ws.Write(ctx, websocket.MessageText, b)
	ws.Close(websocket.StatusPolicyViolation, msg)
	return errors.New(msg)
}

func (h *Hub) serve(ctx context.Context, ws *websocket.Conn) error {
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, b, err := ws.Read(hctx)
	cancel()
	if err != nil {
		return err
	}
	m, err := protocol.Decode(b)
	if err != nil || m.Kind != protocol.KindHello || m.Hello == nil {
		return h.fail(ctx, ws, "expected hello")
	}
	hello := m.Hello
	if !validRoom(hello.Room, hello.Pair) {
		return h.fail(ctx, ws, "invalid room")
	}

	rm, err := h.join(hello)
	if err != nil {
		return h.fail(ctx, ws, err.Error())
	}
	c := &client{device: hello.Device, send: make(chan []byte, 512), room: rm, closed: make(chan struct{})}
	replay := rm.add(c, hello.Gen, hello.Since)
	defer h.leave(rm, c)

	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	go func() {
		select {
		case <-c.closed:
		case <-ctx.Done():
		}
		cancelAll()
	}()

	// Writer: replay, ready marker, then live traffic and pings.
	go func() {
		defer c.close()
		for _, f := range replay {
			if ws.Write(ctx, websocket.MessageText, f) != nil {
				return
			}
		}
		ready, _ := protocol.Encode(&protocol.Message{Kind: protocol.KindReady, Gen: rm.gen})
		if ws.Write(ctx, websocket.MessageText, ready) != nil {
			return
		}
		ping := time.NewTicker(h.cfg.PingInterval)
		defer ping.Stop()
		for {
			select {
			case f := <-c.send:
				if ws.Write(ctx, websocket.MessageText, f) != nil {
					return
				}
			case <-ping.C:
				pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				err := ws.Ping(pctx)
				cancel()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	msgLimit := rate.NewLimiter(rate.Limit(h.cfg.MsgsPerSec), int(h.cfg.MsgsPerSec)*2)
	byteLimit := rate.NewLimiter(rate.Limit(h.cfg.BytesPerSec), max(h.cfg.BytesPerSec*2, int(h.cfg.MaxFrame)))
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		// Waiting (rather than dropping) slows a noisy client down and pushes
		// backpressure onto its TCP connection.
		if msgLimit.Wait(ctx) != nil || byteLimit.WaitN(ctx, len(b)) != nil {
			return ctx.Err()
		}
		h.msgs.Add(1)
		h.bytes.Add(int64(len(b)))
		m, err := protocol.Decode(b)
		if err != nil {
			return h.fail(ctx, ws, "bad message")
		}
		h.handle(rm, c, m, b)
	}
}

func validRoom(id string, pair bool) bool {
	if pair {
		id = strings.TrimPrefix(id, "pair-")
	}
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (h *Hub) join(hello *protocol.Hello) (*room, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gcLocked()
	rm := h.rooms[hello.Room]
	if rm == nil {
		if len(h.rooms) >= h.cfg.MaxRooms {
			return nil, errors.New("relay full")
		}
		rm = &room{id: hello.Room, gen: hex.EncodeToString(randomBytes(8)), pair: hello.Pair, created: time.Now(), members: map[*client]struct{}{},
			chunks: map[string]map[int]*stored{}, lastActive: time.Now()}
		if !hello.Pair {
			// Trust on first use: the first member sets the room token and
			// everyone after must present the same one.
			rm.tokenHash = sha256.Sum256(hello.Token)
			rm.hasToken = true
		}
		h.rooms[hello.Room] = rm
	}
	if rm.pair != hello.Pair {
		return nil, errors.New("room type mismatch")
	}
	if rm.hasToken {
		th := sha256.Sum256(hello.Token)
		if len(hello.Token) < 16 || subtle.ConstantTimeCompare(th[:], rm.tokenHash[:]) != 1 {
			return nil, errors.New("bad room token")
		}
	}
	if rm.pair {
		rm.mu.Lock()
		n := len(rm.members)
		rm.mu.Unlock()
		if n >= 2 {
			return nil, errors.New("pairing room full")
		}
	}
	return rm, nil
}

func (h *Hub) gcLocked() {
	now := time.Now()
	for id, rm := range h.rooms {
		rm.mu.Lock()
		empty := len(rm.members) == 0
		idle := now.Sub(rm.lastActive)
		rm.mu.Unlock()
		if (rm.pair && now.Sub(rm.created) > h.cfg.PairTTL) || (empty && idle > h.cfg.RoomIdle) || (rm.pair && empty && idle > time.Minute) {
			delete(h.rooms, id)
		}
	}
}

func (h *Hub) leave(rm *room, c *client) {
	c.close()
	rm.mu.Lock()
	delete(rm.members, c)
	rm.lastActive = time.Now()
	rm.mu.Unlock()
}

// add registers c and returns the frames it missed after sequence number
// since. A cursor from another generation (the relay restarted) replays
// everything that is stored.
func (rm *room) add(c *client, gen string, since uint64) [][]byte {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.members[c] = struct{}{}
	rm.lastActive = time.Now()
	if gen != rm.gen || rm.pair {
		since = 0
	}
	var out [][]byte
	for _, s := range rm.log {
		if s.seq <= since {
			continue
		}
		out = append(out, s.raw)
		if s.id != "" {
			for i := 1; ; i++ {
				ch, ok := rm.chunks[s.id][i]
				if !ok {
					break
				}
				out = append(out, ch.raw)
			}
		}
	}
	return out
}

func (h *Hub) handle(rm *room, from *client, m *protocol.Message, raw []byte) {
	m.Seq, m.Gen = 0, "" // only the relay assigns these
	switch m.Kind {
	case protocol.KindClip, protocol.KindControl, protocol.KindRekey,
		protocol.KindPairHello, protocol.KindPairWelcome:
		id := ""
		if m.Clip != nil {
			id = m.Clip.ID
		}
		rm.broadcast(from, rm.store(h.cfg, m, id))
	case protocol.KindChunk:
		if m.Chunk != nil {
			rm.storeChunk(h.cfg, m.Chunk, raw)
		}
		rm.broadcast(from, raw)
	case protocol.KindNeed:
		if m.Need == nil {
			return
		}
		// Answer from storage; forward to the room only if something is
		// still missing, so the origin device can fill the gap.
		if missing := rm.answer(from, m.Need); len(missing) > 0 {
			fwd := *m.Need
			fwd.Indexes = missing
			b, _ := protocol.Encode(&protocol.Message{Kind: protocol.KindNeed, Need: &fwd})
			rm.broadcast(from, b)
		}
	}
}

func (rm *room) broadcast(from *client, raw []byte) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.lastActive = time.Now()
	for c := range rm.members {
		if c != from {
			c.deliver(raw)
		}
	}
}

// store appends m to the replay log, stamping it with the next sequence
// number, and returns the encoded frame to broadcast.
func (rm *room) store(cfg Config, m *protocol.Message, id string) []byte {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.seq++
	m.Seq, m.Gen = rm.seq, rm.gen
	raw, _ := protocol.Encode(m)
	s := &stored{seq: rm.seq, id: id, raw: raw, size: int64(len(raw))}
	limit := cfg.RoomHistory
	if rm.pair {
		limit = 4
	}
	rm.log = append(rm.log, s)
	rm.bytes += s.size
	for len(rm.log) > limit || (rm.bytes > cfg.RoomBytes && len(rm.log) > 1) {
		rm.evictLocked()
	}
	return raw
}

func (rm *room) evictLocked() {
	old := rm.log[0]
	rm.log = rm.log[1:]
	rm.bytes -= old.size
	for _, ch := range rm.chunks[old.id] {
		rm.bytes -= ch.size
	}
	delete(rm.chunks, old.id)
}

func (rm *room) storeChunk(cfg Config, c *protocol.Chunk, raw []byte) {
	if rm.pair {
		return
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	known := false
	for _, s := range rm.log {
		if s.id == c.ID {
			known = true
			break
		}
	}
	if !known {
		return
	}
	if rm.chunks[c.ID] == nil {
		rm.chunks[c.ID] = map[int]*stored{}
	}
	if _, dup := rm.chunks[c.ID][c.Index]; dup {
		return
	}
	s := &stored{raw: raw, size: int64(len(raw))}
	rm.chunks[c.ID][c.Index] = s
	rm.bytes += s.size
	for rm.bytes > cfg.RoomBytes && len(rm.log) > 1 {
		rm.evictLocked()
	}
}

func (rm *room) answer(to *client, n *protocol.Need) (missing []int) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	for _, i := range n.Indexes {
		if s, ok := rm.chunks[n.ID][i]; ok {
			to.deliver(s.raw)
		} else {
			missing = append(missing, i)
		}
	}
	return missing
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
