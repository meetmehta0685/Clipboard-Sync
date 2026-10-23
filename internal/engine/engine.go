// Package engine is the sync core: it turns local clipboard changes into
// encrypted envelopes sent on every transport, and applies remote envelopes
// with Lamport ordering, dedupe and echo-loop prevention.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/meetmehta/clipsync/internal/clipboard"
	"github.com/meetmehta/clipsync/internal/config"
	"github.com/meetmehta/clipsync/internal/crypto"
	"github.com/meetmehta/clipsync/internal/protocol"
	"github.com/meetmehta/clipsync/internal/store"
	"github.com/meetmehta/clipsync/internal/transport"
	"github.com/meetmehta/clipsync/internal/watcher"
)

// Info strings for sealed messages; mirrored in web/lib/crypto.ts.
const (
	RekeyInfo   = "clipsync rekey v1"
	WelcomeInfo = "clipsync pair welcome v1"
)

// Factory builds the transports for a group. It is called again whenever the
// group key changes (pairing, rekey), because the relay room changes too.
type Factory func(g *crypto.GroupKey) []transport.Transport

// Options configures an Engine.
type Options struct {
	State      *config.State
	Watcher    *watcher.Watcher
	History    *store.Store // optional
	Transports Factory
	Log        *slog.Logger
	ChunkSize  int           // default protocol.ChunkSize
	StallAfter time.Duration // re-request missing chunks after this; default 2s
}

// Event is published to API subscribers (tray, TUI, `clipsyncd watch`).
type Event struct {
	Type string      `json:"type"` // "clip" | "devices" | "status"
	Clip *store.Clip `json:"clip,omitempty"`
	// Preview is a short description of the clip (Clip.Data never leaves
	// the daemon's API).
	Preview string `json:"preview,omitempty"`
	Remote  bool   `json:"remote,omitempty"`
	Note    string `json:"note,omitempty"`
}

type outgoing struct {
	env    *protocol.Envelope
	chunks []*protocol.Chunk
}

// Engine runs sync for one device.
type Engine struct {
	opt   Options
	st    *config.State
	w     *watcher.Watcher
	log   *slog.Logger
	clock *protocol.Lamport
	seen  *protocol.Seen
	asm   *protocol.Assembler

	mu      sync.Mutex
	latest  protocol.Stamp
	outbox  map[string]*outgoing
	outIDs  []string
	ts      []transport.Transport
	subs    map[chan Event]struct{}
	lat     map[string]*latency
	regroup chan struct{}
}

// New creates an engine. Call Run to start it.
func New(opt Options) *Engine {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.ChunkSize <= 0 {
		opt.ChunkSize = protocol.ChunkSize
	}
	if opt.StallAfter <= 0 {
		opt.StallAfter = 2 * time.Second
	}
	start := opt.State.LastClock()
	if opt.History != nil {
		if c, err := opt.History.MaxClock(); err == nil && c > start {
			start = c
		}
	}
	return &Engine{
		opt:     opt,
		st:      opt.State,
		w:       opt.Watcher,
		log:     opt.Log,
		clock:   protocol.NewLamport(start),
		seen:    protocol.NewSeen(4096),
		asm:     protocol.NewAssembler(opt.State.Settings().MaxClipBytes + 1024),
		outbox:  map[string]*outgoing{},
		subs:    map[chan Event]struct{}{},
		lat:     map[string]*latency{},
		regroup: make(chan struct{}, 1),
	}
}

// Run blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	local := make(chan clipboard.Content, 8)
	inbox := make(chan transport.Incoming, 256)
	go func() {
		if err := e.w.Run(ctx, local); err != nil && !errors.Is(err, context.Canceled) {
			e.log.Error("watcher stopped", "err", err)
		}
	}()
	go e.supervise(ctx, inbox)

	sweep := time.NewTicker(e.opt.StallAfter / 2)
	defer sweep.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	e.prune()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c := <-local:
			if _, err := e.Publish(ctx, c); err != nil {
				e.log.Error("publish failed", "err", err)
				continue
			}
			if err := e.w.Restore(c); err != nil {
				e.log.Warn("restore local clip failed", "err", err)
			}
		case in := <-inbox:
			e.handle(ctx, in)
		case <-sweep.C:
			e.requestMissing(ctx)
		case <-prune.C:
			e.prune()
		}
	}
}

// supervise runs the transports for the current group and restarts them when
// the group changes.
func (e *Engine) supervise(ctx context.Context, inbox chan<- transport.Incoming) {
	for {
		g := e.st.Group()
		ts := e.opt.Transports(g)
		tctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for _, t := range ts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := t.Run(tctx, inbox); err != nil && !errors.Is(err, context.Canceled) {
					e.log.Error("transport stopped", "transport", t.Name(), "err", err)
				}
			}()
		}
		e.mu.Lock()
		e.ts = ts
		e.mu.Unlock()
		e.log.Info("sync started", "room", g.RoomID()[:8], "epoch", g.Epoch, "transports", len(ts))

		select {
		case <-ctx.Done():
			cancel()
			wg.Wait()
			return
		case <-e.regroup:
			cancel()
			wg.Wait()
		}
	}
}

// Regroup restarts transports after the group key changed.
func (e *Engine) Regroup() {
	select {
	case e.regroup <- struct{}{}:
	default:
	}
}

func (e *Engine) broadcast(ctx context.Context, m *protocol.Message) {
	e.mu.Lock()
	ts := slices.Clone(e.ts)
	e.mu.Unlock()
	// Multipath: send everywhere and let the receiver dedupe. Simpler than
	// picking a path, and it makes LAN/relay failover instant.
	for _, t := range ts {
		t.Send(ctx, m)
	}
}

// Publish encrypts and sends a local clip, returning its ID.
func (e *Engine) Publish(ctx context.Context, c clipboard.Content) (string, error) {
	plaintext := c.Data
	if c.Type == clipboard.File {
		plaintext = append(append([]byte(c.Name), 0), c.Data...)
	}
	if max := e.st.Settings().MaxClipBytes; max > 0 && int64(len(plaintext)) > max {
		return "", fmt.Errorf("clip too large: %d bytes (max %d)", len(plaintext), max)
	}
	g := e.st.Group()
	env := &protocol.Envelope{
		ID:     uuid.NewString(),
		Origin: e.st.DeviceID(),
		Clock:  e.clock.Tick(),
		Type:   string(c.Type),
		Sent:   time.Now().UnixMilli(),
	}
	chunks, err := g.SealClip(env, plaintext, e.opt.ChunkSize)
	if err != nil {
		return "", err
	}
	e.seen.Add(env.ID)

	e.mu.Lock()
	if s := (protocol.Stamp{Clock: env.Clock, Origin: env.Origin}); s.After(e.latest) {
		e.latest = s
	}
	e.outbox[env.ID] = &outgoing{env: env, chunks: chunks}
	e.outIDs = append(e.outIDs, env.ID)
	for len(e.outIDs) > 16 {
		delete(e.outbox, e.outIDs[0])
		e.outIDs = e.outIDs[1:]
	}
	e.mu.Unlock()

	clip := store.Clip{ID: env.ID, Origin: env.Origin, Clock: env.Clock, Type: env.Type, Size: env.Size, Created: time.Now(), Local: true, Data: plaintext}
	e.record(clip, false)
	_ = e.st.SetLastClock(env.Clock)

	e.broadcast(ctx, &protocol.Message{Kind: protocol.KindClip, Clip: env})
	if len(chunks) > 0 {
		go func() {
			for _, ch := range chunks {
				if ctx.Err() != nil {
					return
				}
				e.broadcast(ctx, &protocol.Message{Kind: protocol.KindChunk, Chunk: ch})
			}
		}()
	}
	e.log.Info("sent clip", "id", env.ID[:8], "type", env.Type, "size", env.Size, "clock", env.Clock, "chunks", env.Chunks)
	return env.ID, nil
}

func (e *Engine) handle(ctx context.Context, in transport.Incoming) {
	m := in.Msg
	if m.Seq > 0 && in.Via == "relay" && m.Kind != protocol.KindChunk {
		_ = e.st.SetRelayCursor(m.Gen, m.Seq)
	}
	switch m.Kind {
	case protocol.KindClip:
		if m.Clip != nil {
			e.handleClip(m.Clip, in.Via)
		}
	case protocol.KindChunk:
		if m.Chunk != nil {
			e.handleChunk(m.Chunk, in.Via)
		}
	case protocol.KindNeed:
		if m.Need != nil {
			e.handleNeed(ctx, m.Need, in)
		}
	case protocol.KindControl:
		if m.Control != nil {
			e.handleControl(m.Control)
		}
	case protocol.KindRekey:
		if m.Rekey != nil {
			e.handleRekey(m.Rekey)
		}
	case protocol.KindReady:
		if m.Gen != "" {
			gen, _ := e.st.RelayCursor()
			if gen != m.Gen {
				_ = e.st.SetRelayCursor(m.Gen, 0)
			}
		}
		e.log.Debug("relay caught up")
	}
}

func (e *Engine) handleClip(env *protocol.Envelope, via string) {
	if env.Origin == e.st.DeviceID() || e.seen.Has(env.ID) {
		return
	}
	e.clock.Observe(env.Clock)
	g := e.st.Group()
	if env.Epoch != g.Epoch {
		e.log.Debug("clip from another key epoch", "epoch", env.Epoch, "have", g.Epoch)
		return
	}
	if max := e.st.Settings().MaxClipBytes; max > 0 && env.Size > max {
		e.log.Info("ignoring oversized clip", "size", env.Size)
		return
	}
	// Bound the chunk table we allocate: no sender uses chunks under 1 KiB.
	if env.Chunks < 1 || env.Chunks > protocol.ChunkCount(env.Size, 1024) {
		return
	}
	first, err := g.OpenClip(env)
	if err != nil {
		// Don't mark as seen: a genuine copy may still arrive on another path.
		e.log.Warn("dropping clip that failed to decrypt", "id", env.ID, "via", via)
		return
	}
	if !e.seen.Add(env.ID) {
		return
	}
	if data, ok := e.asm.Start(env, first); ok {
		e.deliver(env, data, via)
	}
}

func (e *Engine) handleChunk(c *protocol.Chunk, via string) {
	env := e.asm.Header(c.ID)
	if env == nil {
		return
	}
	data, err := e.st.Group().OpenChunk(env, c)
	if err != nil {
		return
	}
	if env, full, ok := e.asm.Add(c.ID, c.Index, data); ok {
		e.deliver(env, full, via)
	}
}

func (e *Engine) deliver(env *protocol.Envelope, data []byte, via string) {
	if !e.st.Group().VerifyHash(env, data) {
		e.log.Warn("clip hash mismatch", "id", env.ID)
		return
	}
	e.observeLatency(via, env.Sent)
	clip := store.Clip{ID: env.ID, Origin: env.Origin, Clock: env.Clock, Type: env.Type, Size: env.Size, Created: time.Now(), Data: data}
	_ = e.st.SetLastClock(e.clock.Now())

	stamp := protocol.Stamp{Clock: env.Clock, Origin: env.Origin}
	e.mu.Lock()
	newest := stamp.After(e.latest)
	if newest {
		e.latest = stamp
	}
	e.mu.Unlock()

	e.record(clip, true)
	if !newest {
		// An older clip (replay, or lost the race to a concurrent copy): keep
		// it in history but don't overwrite a newer clipboard.
		e.log.Debug("stored older clip", "id", env.ID[:8], "clock", env.Clock)
		return
	}
	if err := e.apply(clip); err != nil {
		e.log.Error("apply clip failed", "err", err)
		return
	}
	e.log.Info("received clip", "id", env.ID[:8], "type", env.Type, "size", env.Size, "via", via, "clock", env.Clock)
}

func (e *Engine) apply(c store.Clip) error {
	switch clipboard.Type(c.Type) {
	case clipboard.Text:
		return e.w.WriteRemote(clipboard.Content{Type: clipboard.Text, Data: c.Data})
	case clipboard.Image:
		if !e.st.Settings().SyncImages {
			return nil
		}
		return e.w.WriteRemote(clipboard.Content{Type: clipboard.Image, Data: c.Data})
	case clipboard.File:
		path, err := e.saveFile(c.Data)
		if err != nil {
			return err
		}
		e.emit(Event{Type: "status", Note: "saved file to " + path})
		return nil
	}
	return fmt.Errorf("unknown clip type %q", c.Type)
}

// saveFile writes a received file into the download directory without ever
// trusting the sender's path.
func (e *Engine) saveFile(data []byte) (string, error) {
	rawName, body, ok := bytes.Cut(data, []byte{0})
	if !ok {
		return "", errors.New("malformed file clip")
	}
	name := filepath.Base(strings.ReplaceAll(string(rawName), "\\", "/"))
	if name == "." || name == "/" || name == ".." || name == "" {
		name = "clip"
	}
	dir := e.st.Settings().DownloadDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; ; i++ {
		p := filepath.Join(dir, name)
		if i > 0 {
			p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(body)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return p, werr
	}
}

func (e *Engine) handleNeed(ctx context.Context, n *protocol.Need, in transport.Incoming) {
	if n.From == e.st.DeviceID() {
		return
	}
	e.mu.Lock()
	out := e.outbox[n.ID]
	e.mu.Unlock()
	if out == nil || in.Reply == nil {
		return
	}
	var msgs []*protocol.Message
	for _, i := range n.Indexes {
		if i >= 1 && i-1 < len(out.chunks) {
			msgs = append(msgs, &protocol.Message{Kind: protocol.KindChunk, Chunk: out.chunks[i-1]})
		}
	}
	go func() {
		for _, m := range msgs {
			if ctx.Err() != nil {
				return
			}
			in.Reply(m)
		}
	}()
}

func (e *Engine) requestMissing(ctx context.Context) {
	for _, s := range e.asm.Sweep(e.opt.StallAfter, 5) {
		e.log.Debug("requesting missing chunks", "id", s.Env.ID[:8], "missing", len(s.Missing))
		e.broadcast(ctx, &protocol.Message{Kind: protocol.KindNeed, Need: &protocol.Need{ID: s.Env.ID, Indexes: s.Missing, From: e.st.DeviceID()}})
	}
}

// controlPayload is the plaintext of a Control message.
type controlPayload struct {
	Type    string          `json:"type"` // "devices"
	Devices []config.Device `json:"devices"`
}

func (e *Engine) handleControl(c *protocol.Control) {
	if c.Origin == e.st.DeviceID() {
		return
	}
	e.clock.Observe(c.Clock)
	pt, err := e.st.Group().OpenControl(c)
	if err != nil {
		return
	}
	var p controlPayload
	if json.Unmarshal(pt, &p) != nil || p.Type != "devices" {
		return
	}
	if n, err := e.st.MergeDevices(p.Devices); err == nil && n > 0 {
		e.log.Info("learned new devices", "count", n)
		e.emit(Event{Type: "devices"})
	}
}

// AnnounceDevices tells the group about the current device list (after
// pairing a new device).
func (e *Engine) AnnounceDevices(ctx context.Context) error {
	b, err := json.Marshal(controlPayload{Type: "devices", Devices: e.st.Devices()})
	if err != nil {
		return err
	}
	c := &protocol.Control{Origin: e.st.DeviceID(), Clock: e.clock.Tick()}
	if err := e.st.Group().SealControl(c, b); err != nil {
		return err
	}
	e.broadcast(ctx, &protocol.Message{Kind: protocol.KindControl, Control: c})
	e.emit(Event{Type: "devices"})
	return nil
}

// RekeyPayload is the plaintext sealed to each remaining device.
type RekeyPayload struct {
	Key     []byte          `json:"key"`
	Epoch   uint32          `json:"epoch"`
	Devices []config.Device `json:"devices"`
}

func (e *Engine) handleRekey(r *protocol.Rekey) {
	if r.To != e.st.DeviceID() || r.Sealed == nil {
		return
	}
	g := e.st.Group()
	if r.Epoch <= g.Epoch {
		return
	}
	sender, ok := e.st.Device(r.From)
	if !ok || !bytes.Equal(sender.Pub, r.Sealed.Sender) {
		e.log.Warn("rekey from unknown device", "from", r.From)
		return
	}
	pt, err := crypto.OpenFrom(e.st.Identity(), r.Sealed, RekeyInfo)
	if err != nil {
		return
	}
	var p RekeyPayload
	if json.Unmarshal(pt, &p) != nil || len(p.Key) != crypto.KeySize || p.Epoch != r.Epoch {
		return
	}
	ng := &crypto.GroupKey{Epoch: p.Epoch}
	copy(ng.Key[:], p.Key)
	if err := e.st.JoinGroup(ng, p.Devices); err != nil {
		e.log.Error("rekey: save group", "err", err)
		return
	}
	e.log.Info("group key rotated", "epoch", p.Epoch, "by", sender.Name)
	e.emit(Event{Type: "devices", Note: "group key rotated"})
	e.Regroup()
}

// Rekeys builds the messages for rotating the group key without device
// `remove`. The caller sends them on the old room and then applies the new
// key with CommitRekey.
func (e *Engine) Rekeys(remove string) (*crypto.GroupKey, []config.Device, []*protocol.Message, error) {
	if remove == e.st.DeviceID() {
		return nil, nil, nil, errors.New("cannot remove this device; use `leave` instead")
	}
	if _, ok := e.st.Device(remove); !ok {
		return nil, nil, nil, fmt.Errorf("unknown device %q", remove)
	}
	old := e.st.Group()
	ng, err := crypto.NewGroupKey(old.Epoch + 1)
	if err != nil {
		return nil, nil, nil, err
	}
	var keep []config.Device
	for _, d := range e.st.Devices() {
		if d.ID != remove {
			keep = append(keep, d)
		}
	}
	payload, _ := json.Marshal(RekeyPayload{Key: ng.Key[:], Epoch: ng.Epoch, Devices: keep})
	clock := e.clock.Tick()
	var msgs []*protocol.Message
	for _, d := range keep {
		if d.ID == e.st.DeviceID() {
			continue
		}
		sealed, err := crypto.SealTo(e.st.Identity(), d.Pub, payload, RekeyInfo)
		if err != nil {
			return nil, nil, nil, err
		}
		msgs = append(msgs, &protocol.Message{Kind: protocol.KindRekey, Rekey: &protocol.Rekey{
			To: d.ID, From: e.st.DeviceID(), Epoch: ng.Epoch, Clock: clock, Sealed: sealed}})
	}
	return ng, keep, msgs, nil
}

// SendNow broadcasts on the current transports.
func (e *Engine) SendNow(ctx context.Context, m *protocol.Message) { e.broadcast(ctx, m) }

// CommitRekey switches this device to the new key.
func (e *Engine) CommitRekey(ng *crypto.GroupKey, keep []config.Device) error {
	if err := e.st.JoinGroup(ng, keep); err != nil {
		return err
	}
	e.emit(Event{Type: "devices", Note: "group key rotated"})
	e.Regroup()
	return nil
}

func (e *Engine) record(c store.Clip, remote bool) {
	if e.opt.History != nil {
		if err := e.opt.History.Add(c); err != nil {
			e.log.Warn("history write failed", "err", err)
		}
	}
	cp := c
	cp.Data = nil
	e.emit(Event{Type: "clip", Clip: &cp, Remote: remote, Preview: c.Preview(200)})
}

func (e *Engine) prune() {
	if e.opt.History == nil {
		return
	}
	s := e.st.Settings()
	if n, err := e.opt.History.Prune(s.HistoryLimit, s.HistoryTTL.D()); err == nil && n > 0 {
		e.log.Info("pruned history", "deleted", n)
	}
}

// Copy puts a history clip back on the local clipboard.
func (e *Engine) Copy(id string) error {
	if e.opt.History == nil {
		return errors.New("history disabled")
	}
	c, err := e.opt.History.Get(id)
	if err != nil {
		return err
	}
	return e.apply(c)
}

// Pause stops sending local clips.
func (e *Engine) Pause(p bool) { e.w.Pause(p); e.emit(Event{Type: "status"}) }

// Subscribe returns a channel of events and a cancel function.
func (e *Engine) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.subs, ch)
		e.mu.Unlock()
	}
}

func (e *Engine) emit(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Latest returns the stamp of the clip currently on the clipboard.
func (e *Engine) Latest() protocol.Stamp { e.mu.Lock(); defer e.mu.Unlock(); return e.latest }

// TransportStatus describes one transport.
type TransportStatus struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

// Status is a snapshot for the local API.
type Status struct {
	Device     string                  `json:"device"`
	Name       string                  `json:"name"`
	Paused     bool                    `json:"paused"`
	Epoch      uint32                  `json:"epoch"`
	Clock      uint64                  `json:"clock"`
	Devices    int                     `json:"devices"`
	Transports []TransportStatus       `json:"transports"`
	Skipped    int64                   `json:"skipped"`
	Pending    int                     `json:"pending_chunks"`
	Latency    map[string]LatencyStats `json:"latency"`
}

// Status returns the engine state.
func (e *Engine) Status() Status {
	e.mu.Lock()
	ts := slices.Clone(e.ts)
	lat := map[string]LatencyStats{}
	for k, v := range e.lat {
		lat[k] = v.stats()
	}
	e.mu.Unlock()
	s := Status{
		Device: e.st.DeviceID(), Name: e.st.Name(), Paused: e.w.Paused(), Epoch: e.st.Group().Epoch,
		Clock: e.clock.Now(), Devices: len(e.st.Devices()), Skipped: e.w.Skipped(), Pending: e.asm.Pending(), Latency: lat,
	}
	for _, t := range ts {
		s.Transports = append(s.Transports, TransportStatus{Name: t.Name(), Connected: t.Connected()})
	}
	return s
}

// latency keeps a window of end-to-end sync times per transport. Values
// depend on device clocks agreeing, which NTP usually gets within a few ms.
type latency struct{ samples []time.Duration }

// LatencyStats summarises the window.
type LatencyStats struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
}

func (e *Engine) observeLatency(via string, sent int64) {
	if sent <= 0 {
		return
	}
	d := time.Since(time.UnixMilli(sent))
	if d < 0 {
		d = 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.lat[via]
	if l == nil {
		l = &latency{}
		e.lat[via] = l
	}
	l.samples = append(l.samples, d)
	if len(l.samples) > 200 {
		l.samples = l.samples[1:]
	}
}

func (l *latency) stats() LatencyStats {
	s := slices.Clone(l.samples)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if len(s) == 0 {
		return LatencyStats{}
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	return LatencyStats{Count: len(s), P50: ms(s[len(s)/2]), P95: ms(s[(len(s)*95)/100])}
}
