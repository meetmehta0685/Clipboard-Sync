// Package lan is the direct device-to-device transport: peers advertise and
// discover each other over mDNS (_clipsync._tcp) and talk over TCP secured
// with the Noise IK handshake, using the same X25519 identity keys as
// pairing. It works without internet access and skips the relay round trip.
package lan

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/meetmehta/clipsync/internal/protocol"
	"github.com/meetmehta/clipsync/internal/transport"
)

// Service is the mDNS service type.
const Service = "_clipsync._tcp"

// Peer is a group member that may be reachable on the LAN.
type Peer struct {
	ID  string
	Pub []byte
}

// Options configures the LAN transport.
type Options struct {
	DeviceID string
	Keys     Keys
	// Room binds handshakes to the current group (and key epoch) via the
	// Noise prologue, so devices from an old epoch can't connect.
	Room string
	// Peers returns the current device list.
	Peers func() []Peer
	Port  int // 0 picks a free port
	// Discover enables mDNS. Tests disable it and use AddPeerAddr.
	Discover bool
	Log      *slog.Logger
}

type conn struct {
	sc *secureConn
	q  *transport.Queue
}

// Transport is the LAN transport.
type Transport struct {
	opt      Options
	prologue []byte

	mu    sync.Mutex
	peers map[string]*conn  // connected, by device ID
	addrs map[string]string // discovered host:port, by device ID
	ln    net.Listener
	ready chan struct{}
}

// New creates a LAN transport.
func New(opt Options) *Transport {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	return &Transport{
		opt:      opt,
		prologue: []byte("clipsync-lan-v1|" + opt.Room),
		peers:    map[string]*conn{},
		addrs:    map[string]string{},
		ready:    make(chan struct{}),
	}
}

func (t *Transport) Name() string { return "lan" }

func (t *Transport) Connected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.peers) > 0
}

// Addr returns the listening address once Run has started.
func (t *Transport) Addr() string {
	<-t.ready
	return t.ln.Addr().String()
}

// AddPeerAddr registers a peer address manually (tests, or networks where
// multicast is blocked).
func (t *Transport) AddPeerAddr(id, addr string) {
	t.mu.Lock()
	t.addrs[id] = addr
	t.mu.Unlock()
}

func (t *Transport) Send(ctx context.Context, m *protocol.Message) {
	t.mu.Lock()
	conns := make([]*conn, 0, len(t.peers))
	for _, c := range t.peers {
		conns = append(conns, c)
	}
	t.mu.Unlock()
	for _, c := range conns {
		c.q.Push(ctx, m)
	}
}

func (t *Transport) peer(id string) (Peer, bool) {
	for _, p := range t.opt.Peers() {
		if p.ID == id {
			return p, true
		}
	}
	return Peer{}, false
}

func (t *Transport) authorize(pub []byte, id string) bool {
	p, ok := t.peer(id)
	return ok && id != t.opt.DeviceID && string(p.Pub) == string(pub)
}

// Run listens, advertises, discovers and maintains peer connections.
func (t *Transport) Run(ctx context.Context, inbox chan<- transport.Incoming) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", t.opt.Port))
	if err != nil {
		return err
	}
	t.ln = ln
	close(t.ready)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	if t.opt.Discover {
		port := ln.Addr().(*net.TCPAddr).Port
		srv, err := zeroconf.Register(t.opt.DeviceID[:min(8, len(t.opt.DeviceID))]+"-clipsync", Service, "local.", port,
			[]string{"id=" + t.opt.DeviceID, "v=1"}, nil)
		if err != nil {
			t.opt.Log.Warn("mDNS advertise failed; LAN discovery off", "err", err)
		} else {
			defer srv.Shutdown()
			go t.browse(ctx)
		}
	}
	go t.dialLoop(ctx, inbox)

	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				t.closeAll()
				return ctx.Err()
			}
			return err
		}
		go func() {
			sc, err := serverHandshake(raw, t.opt.Keys, t.opt.DeviceID, t.prologue, t.authorize)
			if err != nil {
				t.opt.Log.Debug("lan handshake rejected", "remote", raw.RemoteAddr(), "err", err)
				raw.Close()
				return
			}
			t.serve(ctx, sc, inbox)
		}()
	}
}

func (t *Transport) closeAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.peers {
		c.sc.Close()
	}
}

func (t *Transport) browse(ctx context.Context) {
	for ctx.Err() == nil {
		r, err := zeroconf.NewResolver(nil)
		if err != nil {
			t.opt.Log.Warn("mDNS resolver failed", "err", err)
			return
		}
		entries := make(chan *zeroconf.ServiceEntry, 16)
		bctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		go func() {
			for e := range entries {
				t.discovered(e)
			}
		}()
		if err := r.Browse(bctx, Service, "local.", entries); err != nil {
			t.opt.Log.Warn("mDNS browse failed", "err", err)
		}
		<-bctx.Done()
		cancel()
	}
}

func (t *Transport) discovered(e *zeroconf.ServiceEntry) {
	var id string
	for _, kv := range e.Text {
		if v, ok := strings.CutPrefix(kv, "id="); ok {
			id = v
		}
	}
	if id == "" || id == t.opt.DeviceID {
		return
	}
	if _, ok := t.peer(id); !ok {
		return // a clipsync device, but not in our group
	}
	var ip net.IP
	switch {
	case len(e.AddrIPv4) > 0:
		ip = e.AddrIPv4[0]
	case len(e.AddrIPv6) > 0:
		ip = e.AddrIPv6[0]
	default:
		return
	}
	addr := net.JoinHostPort(ip.String(), fmt.Sprint(e.Port))
	t.mu.Lock()
	old := t.addrs[id]
	t.addrs[id] = addr
	t.mu.Unlock()
	if old != addr {
		t.opt.Log.Info("discovered LAN peer", "device", id[:min(8, len(id))], "addr", addr)
	}
}

// dialLoop connects to discovered peers. Only the device with the lower ID
// dials, so two peers never open duplicate connections to each other.
func (t *Transport) dialLoop(ctx context.Context, inbox chan<- transport.Incoming) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		t.mu.Lock()
		var todo []string
		for id := range t.addrs {
			if _, connected := t.peers[id]; !connected && t.opt.DeviceID < id {
				todo = append(todo, id)
			}
		}
		t.mu.Unlock()
		for _, id := range todo {
			go t.dial(ctx, id, inbox)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (t *Transport) dial(ctx context.Context, id string, inbox chan<- transport.Incoming) {
	p, ok := t.peer(id)
	if !ok {
		return
	}
	t.mu.Lock()
	addr := t.addrs[id]
	if _, connected := t.peers[id]; connected {
		t.mu.Unlock()
		return
	}
	// Reserve the slot so the next tick doesn't dial again.
	t.peers[id] = nil
	t.mu.Unlock()

	d := net.Dialer{Timeout: 3 * time.Second, KeepAlive: 15 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err == nil {
		var sc *secureConn
		if sc, err = clientHandshake(raw, t.opt.Keys, t.opt.DeviceID, p.Pub, t.prologue); err == nil {
			if sc.peerID != id {
				sc.Close()
				err = fmt.Errorf("peer at %s claims to be %s", addr, sc.peerID)
			} else {
				t.mu.Lock()
				delete(t.peers, id)
				t.mu.Unlock()
				t.serve(ctx, sc, inbox)
				return
			}
		} else {
			raw.Close()
		}
	}
	t.opt.Log.Debug("lan dial failed", "addr", addr, "err", err)
	t.mu.Lock()
	if t.peers[id] == nil {
		delete(t.peers, id)
	}
	t.mu.Unlock()
}

// serve runs one authenticated connection until it fails.
func (t *Transport) serve(ctx context.Context, sc *secureConn, inbox chan<- transport.Incoming) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &conn{sc: sc, q: transport.NewQueue()}
	c.q.SetOpen(true)
	t.mu.Lock()
	if old := t.peers[sc.peerID]; old != nil {
		old.sc.Close()
	}
	t.peers[sc.peerID] = c
	t.mu.Unlock()
	t.opt.Log.Info("LAN peer connected", "device", sc.peerID[:min(8, len(sc.peerID))], "addr", sc.raw.RemoteAddr())
	defer func() {
		c.q.SetOpen(false)
		sc.Close()
		t.mu.Lock()
		if t.peers[sc.peerID] == c {
			delete(t.peers, sc.peerID)
		}
		t.mu.Unlock()
		t.opt.Log.Info("LAN peer disconnected", "device", sc.peerID[:min(8, len(sc.peerID))])
	}()

	go func() {
		defer sc.Close()
		for {
			m, err := c.q.Next(ctx)
			if err != nil {
				return
			}
			b, err := protocol.Encode(m)
			if err != nil {
				continue
			}
			if err := sc.WriteFrame(b); err != nil {
				return
			}
		}
	}()
	reply := func(m *protocol.Message) { c.q.Push(ctx, m) }
	for {
		b, err := sc.ReadFrame()
		if err != nil {
			return
		}
		m, err := protocol.Decode(b)
		if err != nil {
			return
		}
		select {
		case inbox <- transport.Incoming{Msg: m, Via: "lan", Reply: reply}:
		case <-ctx.Done():
			return
		}
	}
}
