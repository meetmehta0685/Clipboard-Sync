package crypto

import (
	"bytes"
	"testing"

	"github.com/meetmehta0685/Clipboard-Sync/internal/protocol"
)

func newEnv() *protocol.Envelope {
	return &protocol.Envelope{ID: "11111111-2222-3333-4444-555555555555", Origin: "dev-a", Clock: 7, Type: "text", Sent: 1}
}

func TestClipRoundTrip(t *testing.T) {
	g, _ := NewGroupKey(1)
	for _, size := range []int{0, 5, 1000, 1024*10 + 3} {
		data := bytes.Repeat([]byte{'x'}, size)
		env := newEnv()
		chunks, err := g.SealClip(env, data, 1024)
		if err != nil {
			t.Fatal(err)
		}
		out, err := g.OpenClip(env)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		for _, c := range chunks {
			p, err := g.OpenChunk(env, c)
			if err != nil {
				t.Fatalf("chunk %d: %v", c.Index, err)
			}
			out = append(out, p...)
		}
		if !bytes.Equal(out, data) || !g.VerifyHash(env, out) {
			t.Fatalf("size %d: round trip mismatch", size)
		}
	}
}

func TestTamperingDetected(t *testing.T) {
	g, _ := NewGroupKey(1)
	env := newEnv()
	chunks, _ := g.SealClip(env, bytes.Repeat([]byte("secret "), 400), 1024)

	// A relay swapping header fields breaks the AAD.
	for name, mutate := range map[string]func(e *protocol.Envelope){
		"clock":   func(e *protocol.Envelope) { e.Clock++ },
		"origin":  func(e *protocol.Envelope) { e.Origin = "evil" },
		"type":    func(e *protocol.Envelope) { e.Type = "file" },
		"payload": func(e *protocol.Envelope) { e.Payload[0] ^= 1 },
	} {
		e := *env
		e.Payload = bytes.Clone(env.Payload)
		mutate(&e)
		if _, err := g.OpenClip(&e); err == nil {
			t.Errorf("tampered %s accepted", name)
		}
	}
	// A chunk cannot be replayed at another index.
	c := *chunks[0]
	c.Index = 2
	if _, err := g.OpenChunk(env, &c); err == nil {
		t.Error("chunk accepted at wrong index")
	}
	// A different group cannot read it.
	other, _ := NewGroupKey(1)
	if _, err := other.OpenClip(env); err == nil {
		t.Error("foreign key decrypted clip")
	}
}

func TestContentHashIsKeyed(t *testing.T) {
	a, _ := NewGroupKey(1)
	b, _ := NewGroupKey(1)
	if bytes.Equal(a.ContentHash("text", []byte("yes")), b.ContentHash("text", []byte("yes"))) {
		t.Fatal("hash must depend on the group key")
	}
}

func TestSealTo(t *testing.T) {
	alice, _ := NewIdentity()
	bob, _ := NewIdentity()
	mallory, _ := NewIdentity()
	s, err := SealTo(alice, bob.Public(), []byte("group key"), "info")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := OpenFrom(bob, s, "info")
	if err != nil || string(pt) != "group key" {
		t.Fatalf("open: %v %q", err, pt)
	}
	if !bytes.Equal(s.Sender, alice.Public()) {
		t.Fatal("sender key missing")
	}
	if _, err := OpenFrom(mallory, s, "info"); err == nil {
		t.Fatal("wrong recipient opened sealed box")
	}
	if _, err := OpenFrom(bob, s, "other info"); err == nil {
		t.Fatal("info not bound")
	}
	// Claiming a different sender changes the static-static DH, so it fails.
	forged := *s
	forged.Sender = mallory.Public()
	if _, err := OpenFrom(bob, &forged, "info"); err == nil {
		t.Fatal("sender substitution accepted")
	}
}

func TestRoomDerivation(t *testing.T) {
	g, _ := NewGroupKey(1)
	if len(g.RoomID()) != 32 || g.RoomID() == PairRoom(g.Key[:])[5:] {
		t.Fatal("room IDs should be 128-bit hex and domain separated")
	}
	if bytes.Equal(g.RoomToken(), g.Key[:]) {
		t.Fatal("token must not be the raw key")
	}
}

func TestIdentityPersistence(t *testing.T) {
	id, _ := NewIdentity()
	back, err := IdentityFromBytes(id.Bytes())
	if err != nil || !bytes.Equal(back.Public(), id.Public()) {
		t.Fatal("identity did not round-trip")
	}
}
