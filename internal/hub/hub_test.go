package hub

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meetmehta/clipsync/internal/protocol"
	"github.com/meetmehta/clipsync/internal/transport/relay"
)

const room = "0123456789abcdef0123456789abcdef"

var token = []byte("0123456789abcdef-token")

func server(t *testing.T) (string, *Hub) {
	t.Helper()
	h := New(DefaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), h
}

func dial(t *testing.T, url string, hello *protocol.Hello) *relay.Conn {
	t.Helper()
	c, err := relay.Dial(context.Background(), url, hello)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func recv(t *testing.T, c *relay.Conn) *protocol.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m, err := c.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func clip(id string, clock uint64) *protocol.Message {
	return &protocol.Message{Kind: protocol.KindClip, Clip: &protocol.Envelope{ID: id, Clock: clock, Chunks: 2, Payload: []byte("ct")}}
}

func TestBroadcastAndCatchUp(t *testing.T) {
	url, _ := server(t)
	ctx := context.Background()

	a := dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "a"})
	if m := recv(t, a); m.Kind != protocol.KindReady || m.Gen == "" {
		t.Fatalf("want ready with generation, got %+v", m)
	}
	b := dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "b"})
	gen := recv(t, b).Gen

	_ = a.Send(ctx, clip("c1", 1))
	_ = a.Send(ctx, &protocol.Message{Kind: protocol.KindChunk, Chunk: &protocol.Chunk{ID: "c1", Index: 1, Payload: []byte("x")}})
	m := recv(t, b)
	if m.Kind != protocol.KindClip || m.Clip.ID != "c1" || m.Seq != 1 || m.Gen != gen {
		t.Fatalf("live delivery: %+v", m)
	}
	recv(t, b) // chunk
	b.Close()

	// While b is offline, a device whose Lamport clock is *behind* sends a
	// clip. A clock-based cursor would skip it; the sequence cursor doesn't.
	_ = a.Send(ctx, clip("c2", 50))
	_ = a.Send(ctx, clip("c3", 2))
	time.Sleep(50 * time.Millisecond)

	b2 := dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "b", Gen: gen, Since: 1})
	got := []string{}
	for {
		m := recv(t, b2)
		if m.Kind == protocol.KindReady {
			break
		}
		got = append(got, m.Clip.ID)
	}
	if strings.Join(got, ",") != "c2,c3" {
		t.Fatalf("replay = %v, want c2,c3", got)
	}

	// A cursor from an older relay generation replays everything stored.
	b3 := dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "b", Gen: "stale", Since: 99})
	m = recv(t, b3)
	if m.Kind != protocol.KindClip || m.Clip.ID != "c1" {
		t.Fatalf("stale generation should replay from the start, got %+v", m)
	}
	if m := recv(t, b3); m.Kind != protocol.KindChunk {
		t.Fatalf("stored chunk should follow its clip, got %v", m.Kind)
	}
}

func TestWrongTokenRejected(t *testing.T) {
	url, _ := server(t)
	dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "a"})
	time.Sleep(20 * time.Millisecond)
	bad := dial(t, url, &protocol.Hello{Room: room, Token: []byte("someone-elses-token"), Device: "x"})
	m := recv(t, bad)
	if m.Kind != protocol.KindError || !strings.Contains(m.Error, "token") {
		t.Fatalf("want token error, got %+v", m)
	}
}

func TestNeedAnsweredFromStorage(t *testing.T) {
	url, _ := server(t)
	ctx := context.Background()
	a := dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "a"})
	recv(t, a)
	_ = a.Send(ctx, clip("big", 1))
	_ = a.Send(ctx, &protocol.Message{Kind: protocol.KindChunk, Chunk: &protocol.Chunk{ID: "big", Index: 1, Payload: []byte("one")}})
	time.Sleep(50 * time.Millisecond)

	b := dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "b", Gen: "x", Since: 0})
	for recv(t, b).Kind != protocol.KindReady {
	}
	_ = b.Send(ctx, &protocol.Message{Kind: protocol.KindNeed, Need: &protocol.Need{ID: "big", Indexes: []int{1}, From: "b"}})
	m := recv(t, b)
	if m.Kind != protocol.KindChunk || string(m.Chunk.Payload) != "one" {
		t.Fatalf("relay should answer need from storage, got %+v", m)
	}
}

func TestPairRoomLimits(t *testing.T) {
	url, _ := server(t)
	pr := "pair-" + room
	a := dial(t, url, &protocol.Hello{Room: pr, Pair: true})
	recv(t, a)
	dial(t, url, &protocol.Hello{Room: pr, Pair: true})
	time.Sleep(20 * time.Millisecond)
	c := dial(t, url, &protocol.Hello{Room: pr, Pair: true})
	if m := recv(t, c); m.Kind != protocol.KindError {
		t.Fatalf("third member should be rejected, got %+v", m)
	}
}

func TestHistoryBounded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RoomHistory = 3
	h := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(h)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	a := dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "a"})
	recv(t, a)
	for i := range 10 {
		_ = a.Send(context.Background(), clip(string(rune('a'+i)), uint64(i)))
	}
	time.Sleep(50 * time.Millisecond)
	b := dial(t, url, &protocol.Hello{Room: room, Token: token, Device: "b"})
	n := 0
	for recv(t, b).Kind != protocol.KindReady {
		n++
	}
	if n != 3 {
		t.Fatalf("replayed %d, want 3", n)
	}
}
