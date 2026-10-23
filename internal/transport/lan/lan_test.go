package lan

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/meetmehta0685/Clipboard-Sync/internal/crypto"
	"github.com/meetmehta0685/Clipboard-Sync/internal/protocol"
	"github.com/meetmehta0685/Clipboard-Sync/internal/transport"
)

func keys(t *testing.T) Keys {
	id, err := crypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return Keys{Private: id.Bytes(), Public: id.Public()}
}

type node struct {
	t     *Transport
	inbox chan transport.Incoming
}

func start(t *testing.T, ctx context.Context, id string, k Keys, room string, peers func() []Peer) *node {
	tr := New(Options{DeviceID: id, Keys: k, Room: room, Peers: peers, Port: 0,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	n := &node{t: tr, inbox: make(chan transport.Incoming, 64)}
	go tr.Run(ctx, n.inbox)
	tr.Addr()
	return n
}

func wait(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestPeersConnectAndExchange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ka, kb := keys(t), keys(t)
	peers := func() []Peer { return []Peer{{"a", ka.Public}, {"b", kb.Public}} }
	a := start(t, ctx, "a", ka, "room1", peers)
	b := start(t, ctx, "b", kb, "room1", peers)
	a.t.AddPeerAddr("b", b.t.Addr()) // "a" < "b", so a dials
	wait(t, func() bool { return a.t.Connected() && b.t.Connected() })

	// A frame bigger than one Noise record (64 KiB) exercises splitting.
	big := bytes.Repeat([]byte("z"), 300<<10)
	a.t.Send(ctx, &protocol.Message{Kind: protocol.KindClip, Clip: &protocol.Envelope{ID: "1", Payload: big}})
	select {
	case in := <-b.inbox:
		if in.Via != "lan" || !bytes.Equal(in.Msg.Clip.Payload, big) {
			t.Fatal("payload corrupted")
		}
		in.Reply(&protocol.Message{Kind: protocol.KindNeed, Need: &protocol.Need{ID: "1"}})
	case <-time.After(3 * time.Second):
		t.Fatal("b did not receive")
	}
	select {
	case in := <-a.inbox:
		if in.Msg.Kind != protocol.KindNeed {
			t.Fatal("wrong reply")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reply not received")
	}
}

func TestOutsiderRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ka, kb, kEvil := keys(t), keys(t), keys(t)
	members := func() []Peer { return []Peer{{"a", ka.Public}, {"b", kb.Public}} }
	b := start(t, ctx, "b", kb, "room1", members)

	// "a" is in b's list, but the dialer holds a different key.
	evil := start(t, ctx, "a", kEvil, "room1", func() []Peer { return []Peer{{"a", kEvil.Public}, {"b", kb.Public}} })
	evil.t.AddPeerAddr("b", b.t.Addr())
	time.Sleep(1500 * time.Millisecond)
	if b.t.Connected() {
		t.Fatal("device with an unknown key was accepted")
	}
}

func TestOldEpochRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ka, kb := keys(t), keys(t)
	peers := func() []Peer { return []Peer{{"a", ka.Public}, {"b", kb.Public}} }
	b := start(t, ctx, "b", kb, "room-epoch-2", peers)
	a := start(t, ctx, "a", ka, "room-epoch-1", peers) // prologue mismatch
	a.t.AddPeerAddr("b", b.t.Addr())
	time.Sleep(1500 * time.Millisecond)
	if a.t.Connected() || b.t.Connected() {
		t.Fatal("handshake across key epochs succeeded")
	}
}

// While a dial is in flight the peer must not count as connected, and
// sending must not touch the half-open slot (this used to panic).
func TestSendDuringSlowDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ka, kb := keys(t), keys(t)
	peers := func() []Peer { return []Peer{{"a", ka.Public}, {"b", kb.Public}} }
	a := start(t, ctx, "a", ka, "room1", peers)
	a.t.AddPeerAddr("b", "10.255.255.1:9") // unroutable: the dial hangs until timeout
	time.Sleep(1300 * time.Millisecond)    // let the dial loop start dialing
	if a.t.Connected() {
		t.Fatal("reported connected while only dialing")
	}
	a.t.Send(ctx, &protocol.Message{Kind: protocol.KindNeed, Need: &protocol.Need{ID: "x"}})
}
