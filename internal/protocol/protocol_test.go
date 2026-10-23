package protocol

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestStampOrdering(t *testing.T) {
	cases := []struct {
		a, b Stamp
		want bool
	}{
		{Stamp{2, "a"}, Stamp{1, "z"}, true},  // higher clock wins
		{Stamp{1, "z"}, Stamp{2, "a"}, false}, //
		{Stamp{5, "b"}, Stamp{5, "a"}, true},  // tie broken by device ID
		{Stamp{5, "a"}, Stamp{5, "a"}, false}, // equal is not after
		{Stamp{1, "a"}, Stamp{}, true},        // anything beats zero
	}
	for _, c := range cases {
		if got := c.a.After(c.b); got != c.want {
			t.Errorf("%v.After(%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestLamport(t *testing.T) {
	l := NewLamport(0)
	if l.Tick() != 1 || l.Tick() != 2 {
		t.Fatal("tick should increment")
	}
	l.Observe(10)
	if got := l.Tick(); got != 11 {
		t.Fatalf("after observing 10, tick = %d, want 11", got)
	}
	l.Observe(3) // older remote time never moves the clock back
	if got := l.Now(); got != 11 {
		t.Fatalf("now = %d, want 11", got)
	}
}

func TestSeenEvictsOldest(t *testing.T) {
	s := NewSeen(3)
	for _, id := range []string{"a", "b", "c"} {
		if !s.Add(id) {
			t.Fatalf("%s should be new", id)
		}
	}
	if s.Add("a") {
		t.Fatal("a is a duplicate")
	}
	s.Add("d") // evicts b (a was refreshed)
	if s.Has("b") || !s.Has("a") || !s.Has("d") {
		t.Fatal("LRU eviction order wrong")
	}
}

func TestSplitAndAssemble(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 1000) // 10,000 bytes
	parts := Split(data, 3000)
	if len(parts) != 4 || ChunkCount(int64(len(data)), 3000) != 4 {
		t.Fatalf("got %d parts", len(parts))
	}
	a := NewAssembler(1 << 20)
	env := &Envelope{ID: "x", Chunks: len(parts)}
	if _, ok := a.Start(env, parts[0]); ok {
		t.Fatal("multi-chunk clip completed early")
	}
	// Out-of-order and duplicate chunks are fine.
	for _, i := range []int{3, 1, 1, 2} {
		_, out, ok := a.Add("x", i, parts[i])
		if ok {
			if !bytes.Equal(out, data) {
				t.Fatal("reassembled data differs")
			}
			if a.Pending() != 0 {
				t.Fatal("completed clip still pending")
			}
			return
		}
	}
	t.Fatal("never completed")
}

func TestAssemblerSweep(t *testing.T) {
	a := NewAssembler(1 << 20)
	a.Start(&Envelope{ID: "x", Chunks: 3}, []byte("a"))
	a.Add("x", 2, []byte("c"))
	time.Sleep(5 * time.Millisecond)
	st := a.Sweep(time.Millisecond, 2)
	if len(st) != 1 || fmt.Sprint(st[0].Missing) != "[1]" {
		t.Fatalf("sweep = %+v", st)
	}
	time.Sleep(5 * time.Millisecond)
	a.Sweep(time.Millisecond, 2)
	time.Sleep(5 * time.Millisecond)
	a.Sweep(time.Millisecond, 2) // third time: gives up
	if a.Pending() != 0 {
		t.Fatal("stalled clip should be dropped after max asks")
	}
}

func TestMessageRoundTrip(t *testing.T) {
	m := &Message{Kind: KindClip, Clip: &Envelope{V: 1, ID: "id", Hash: []byte{1, 2}, Payload: []byte("ct")}}
	b, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b)
	if err != nil || got.Clip.ID != "id" || !bytes.Equal(got.Clip.Payload, []byte("ct")) {
		t.Fatalf("round trip failed: %v %+v", err, got)
	}
	if _, err := Decode([]byte(`{}`)); err == nil {
		t.Fatal("message without kind should fail")
	}
}
