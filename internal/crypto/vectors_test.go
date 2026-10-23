package crypto

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"testing"

	"github.com/meetmehta/clipsync/internal/protocol"
)

// Cross-language test vectors. Go writes testdata/vectors.json (with
// -update); web/test/vectors.test.ts decrypts them and writes
// web/test/js_vectors.json, which TestJSVectors decrypts here. Together they
// prove Go ↔ browser compatibility in both directions.
//
//	go test ./internal/crypto -run Vectors -update
//	cd web && pnpm test
//	go test ./internal/crypto -run JSVectors

var update = flag.Bool("update", false, "regenerate testdata/vectors.json")

const (
	vectorsPath   = "../../testdata/vectors.json"
	jsVectorsPath = "../../web/test/js_vectors.json"
)

type clipVector struct {
	ChunkSize int                `json:"chunk_size"`
	Plaintext []byte             `json:"plaintext"`
	Envelope  *protocol.Envelope `json:"envelope"`
	Chunks    []*protocol.Chunk  `json:"chunks"`
}

type vectors struct {
	GroupKey    []byte `json:"group_key"`
	Epoch       uint32 `json:"epoch"`
	RoomID      string `json:"room_id"`
	RoomToken   []byte `json:"room_token"`
	ContentHash struct {
		Type string `json:"type"`
		Data []byte `json:"data"`
		Hash []byte `json:"hash"`
	} `json:"content_hash"`
	Clips   []clipVector `json:"clips"`
	Control struct {
		Control   *protocol.Control `json:"control"`
		Plaintext []byte            `json:"plaintext"`
	} `json:"control"`
	Sealed struct {
		RecipientPriv []byte           `json:"recipient_priv"`
		Info          string           `json:"info"`
		Sealed        *protocol.Sealed `json:"sealed"`
		Plaintext     []byte           `json:"plaintext"`
	} `json:"sealed"`
	PairMAC struct {
		Code   []byte   `json:"code"`
		Label  string   `json:"label"`
		Fields [][]byte `json:"fields"`
		MAC    []byte   `json:"mac"`
	} `json:"pair_mac"`
	PairRoom struct {
		Code []byte `json:"code"`
		Room string `json:"room"`
	} `json:"pair_room"`
}

func generate(t *testing.T) *vectors {
	var v vectors
	g := &GroupKey{Epoch: 3}
	for i := range g.Key {
		g.Key[i] = byte(i)
	}
	v.GroupKey, v.Epoch, v.RoomID, v.RoomToken = g.Key[:], g.Epoch, g.RoomID(), g.RoomToken()
	v.ContentHash.Type, v.ContentHash.Data = "text", []byte("yes")
	v.ContentHash.Hash = g.ContentHash("text", v.ContentHash.Data)

	for _, c := range []struct {
		typ  string
		data []byte
		cs   int
	}{
		{"text", []byte("héllo wörld 👋"), protocol.ChunkSize},
		{"text", nil, protocol.ChunkSize},
		{"image", bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 700), 1000}, // 3 chunks
		{"file", append([]byte("notes.txt\x00"), []byte("file body")...), protocol.ChunkSize},
	} {
		env := &protocol.Envelope{ID: "0f8e2a3c-0000-4000-8000-00000000000" + string(rune('a'+len(v.Clips))), Origin: "go-device", Clock: uint64(10 + len(v.Clips)), Type: c.typ, Sent: 1761300000000}
		chunks, err := g.SealClip(env, c.data, c.cs)
		if err != nil {
			t.Fatal(err)
		}
		v.Clips = append(v.Clips, clipVector{ChunkSize: c.cs, Plaintext: c.data, Envelope: env, Chunks: chunks})
	}

	v.Control.Plaintext = []byte(`{"type":"devices","devices":[]}`)
	v.Control.Control = &protocol.Control{Origin: "go-device", Clock: 42}
	if err := g.SealControl(v.Control.Control, v.Control.Plaintext); err != nil {
		t.Fatal(err)
	}

	sender, _ := IdentityFromBytes(bytes.Repeat([]byte{7}, 32))
	recipient, _ := IdentityFromBytes(bytes.Repeat([]byte{9}, 32))
	v.Sealed.RecipientPriv, v.Sealed.Info = recipient.Bytes(), "clipsync pair welcome v1"
	v.Sealed.Plaintext = []byte(`{"key":"secret"}`)
	s, err := SealTo(sender, recipient.Public(), v.Sealed.Plaintext, v.Sealed.Info)
	if err != nil {
		t.Fatal(err)
	}
	v.Sealed.Sealed = s

	v.PairMAC.Code = bytes.Repeat([]byte{0xab}, 16)
	v.PairMAC.Label = "clipsync pair hello"
	v.PairMAC.Fields = [][]byte{[]byte("device-id"), []byte("Phone"), []byte("phone"), recipient.Public()}
	v.PairMAC.MAC = PairMAC(v.PairMAC.Code, v.PairMAC.Label, v.PairMAC.Fields...)
	v.PairRoom.Code = v.PairMAC.Code
	v.PairRoom.Room = PairRoom(v.PairRoom.Code)
	return &v
}

func TestVectors(t *testing.T) {
	if *update {
		b, _ := json.MarshalIndent(generate(t), "", "  ")
		if err := os.MkdirAll("../../testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vectorsPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	// Derivations are deterministic, so check them against a fresh run.
	fresh := generate(t)
	if v.RoomID != fresh.RoomID || !bytes.Equal(v.RoomToken, fresh.RoomToken) ||
		!bytes.Equal(v.ContentHash.Hash, fresh.ContentHash.Hash) || !bytes.Equal(v.PairMAC.MAC, fresh.PairMAC.MAC) ||
		v.PairRoom.Room != fresh.PairRoom.Room {
		t.Fatal("derived values changed; this breaks compatibility with existing clients")
	}
	checkVectors(t, &v, &v)
}

// checkVectors decrypts the encrypted parts of enc using keys from base.
func checkVectors(t *testing.T, base, enc *vectors) {
	t.Helper()
	g := &GroupKey{Epoch: base.Epoch}
	copy(g.Key[:], base.GroupKey)
	for i, c := range enc.Clips {
		out, err := g.OpenClip(c.Envelope)
		if err != nil {
			t.Fatalf("clip %d: %v", i, err)
		}
		for _, ch := range c.Chunks {
			p, err := g.OpenChunk(c.Envelope, ch)
			if err != nil {
				t.Fatalf("clip %d chunk %d: %v", i, ch.Index, err)
			}
			out = append(out, p...)
		}
		if !bytes.Equal(out, c.Plaintext) || !g.VerifyHash(c.Envelope, out) {
			t.Fatalf("clip %d: plaintext mismatch", i)
		}
	}
	if enc.Control.Control != nil {
		pt, err := g.OpenControl(enc.Control.Control)
		if err != nil || !bytes.Equal(pt, enc.Control.Plaintext) {
			t.Fatalf("control: %v", err)
		}
	}
	recipient, _ := IdentityFromBytes(base.Sealed.RecipientPriv)
	pt, err := OpenFrom(recipient, enc.Sealed.Sealed, enc.Sealed.Info)
	if err != nil || !bytes.Equal(pt, enc.Sealed.Plaintext) {
		t.Fatalf("sealed: %v", err)
	}
}

func TestJSVectors(t *testing.T) {
	jb, err := os.ReadFile(jsVectorsPath)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("no JS vectors yet; run `pnpm test` in web/")
	}
	if err != nil {
		t.Fatal(err)
	}
	var base, js vectors
	b, _ := os.ReadFile(vectorsPath)
	if err := json.Unmarshal(b, &base); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jb, &js); err != nil {
		t.Fatal(err)
	}
	if js.RoomID != base.RoomID || !bytes.Equal(js.PairMAC.MAC, base.PairMAC.MAC) || js.PairRoom.Room != base.PairRoom.Room {
		t.Fatal("JS derivations disagree with Go")
	}
	checkVectors(t, &base, &js)
}
