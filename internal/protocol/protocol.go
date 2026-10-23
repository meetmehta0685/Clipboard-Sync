// Package protocol defines the clipsync wire format: envelopes, chunks and
// the control messages exchanged with the relay and LAN peers.
//
// Messages are JSON so the browser client can speak the same protocol without
// a code generator. Byte slices are base64 (Go's default JSON encoding).
package protocol

import (
	"encoding/json"
	"fmt"
)

// Version is bumped on any incompatible wire change.
const Version = 1

// ChunkSize is the plaintext size of each chunk. 256 KiB keeps individual
// frames small so text clips are never stuck behind a large image.
const ChunkSize = 256 << 10

// Kind identifies a message.
type Kind string

const (
	KindHello       Kind = "hello"        // client → relay: join a room
	KindReady       Kind = "ready"        // relay → client: replay finished
	KindClip        Kind = "clip"         // encrypted clip header (+ first chunk)
	KindChunk       Kind = "chunk"        // additional encrypted chunk
	KindNeed        Kind = "need"         // request missing chunks of a clip
	KindControl     Kind = "control"      // encrypted group update (device list)
	KindRekey       Kind = "rekey"        // new group key sealed to one device
	KindError       Kind = "error"        // relay → client
	KindPairHello   Kind = "pair_hello"   // joiner → inviter during pairing
	KindPairWelcome Kind = "pair_welcome" // inviter → joiner during pairing
)

// Envelope is the header of a clip. Everything the relay can see is here;
// the content itself is only ever present as ciphertext.
type Envelope struct {
	V      int    `json:"v"`
	ID     string `json:"id"`     // random UUID of this clip
	Origin string `json:"origin"` // device ID that copied it
	Clock  uint64 `json:"clock"`  // Lamport timestamp
	Epoch  uint32 `json:"epoch"`  // group key epoch used to encrypt
	Type   string `json:"type"`   // "text" | "image" | "file"
	// Hash is a *keyed* MAC of the plaintext (HMAC-SHA256 under a key derived
	// from the group key). A plain SHA-256 would let the relay confirm
	// guesses of short clips like "yes" or a 6-digit code.
	Hash   []byte `json:"hash"`
	Size   int64  `json:"size"`   // plaintext size in bytes
	Chunks int    `json:"chunks"` // total chunks, >= 1
	Sent   int64  `json:"sent"`   // sender wall clock, unix ms (metrics only)
	Nonce  []byte `json:"nonce"`
	// Payload is chunk 0 encrypted with the group key.
	Payload []byte `json:"payload"`
}

// AAD is the associated data bound into every ciphertext of this clip, so a
// relay cannot splice a payload onto a different header.
func (e *Envelope) AAD() []byte {
	return fmt.Appendf(nil, "clipsync/v%d|%s|%s|%d|%d|%s|%d|%d|%d|%x",
		e.V, e.ID, e.Origin, e.Clock, e.Epoch, e.Type, e.Size, e.Chunks, e.Sent, e.Hash)
}

// ChunkAAD is the associated data for chunk i (i >= 1) of this clip.
func (e *Envelope) ChunkAAD(i int) []byte {
	return fmt.Appendf(e.AAD(), "|chunk|%d", i)
}

// Chunk carries chunk Index (>= 1) of clip ID.
type Chunk struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Nonce   []byte `json:"nonce"`
	Payload []byte `json:"payload"`
}

// Need asks whoever holds clip ID to resend the listed chunks.
type Need struct {
	ID      string `json:"id"`
	Indexes []int  `json:"indexes"`
	From    string `json:"from"` // requesting device
}

// Hello joins a relay room. The relay replays stored messages with a
// sequence number greater than Since, provided Gen matches the room's
// generation (it changes when the relay restarts and loses its storage).
//
// Catch-up deliberately uses relay sequence numbers rather than Lamport
// clocks: a device that copied things while offline has advanced its own
// clock past clips it never received, so "everything newer than my clock"
// would silently skip them.
type Hello struct {
	V      int    `json:"v"`
	Room   string `json:"room"`
	Token  []byte `json:"token"` // proves membership; derived from the group key
	Device string `json:"device"`
	Gen    string `json:"gen,omitempty"`
	Since  uint64 `json:"since"`
	// Pair marks an ephemeral pairing room: no storage, at most two members.
	Pair bool `json:"pair,omitempty"`
}

// Control is a group-key-encrypted administrative message.
type Control struct {
	Origin  string `json:"origin"`
	Clock   uint64 `json:"clock"`
	Epoch   uint32 `json:"epoch"`
	Nonce   []byte `json:"nonce"`
	Payload []byte `json:"payload"`
}

// AAD binds the control header into its ciphertext.
func (c *Control) AAD() []byte {
	return fmt.Appendf(nil, "clipsync/control|%s|%d|%d", c.Origin, c.Clock, c.Epoch)
}

// Sealed is a message encrypted to one device's X25519 key.
type Sealed struct {
	Ephemeral []byte `json:"eph"`
	Sender    []byte `json:"sender"` // sender's static public key
	Nonce     []byte `json:"nonce"`
	Payload   []byte `json:"payload"`
}

// Rekey delivers a new group key to one device after a device is removed.
type Rekey struct {
	To     string  `json:"to"`
	From   string  `json:"from"`
	Epoch  uint32  `json:"epoch"` // new epoch
	Clock  uint64  `json:"clock"`
	Sealed *Sealed `json:"sealed"`
}

// PairHello is sent by a new device into the pairing room.
type PairHello struct {
	Device string `json:"device"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // "desktop" | "phone"
	Pub    []byte `json:"pub"`
	MAC    []byte `json:"mac"` // HMAC(code, ...) proves it saw the QR code
}

// PairWelcome carries the group state sealed to the new device.
type PairWelcome struct {
	Sealed *Sealed `json:"sealed"`
	MAC    []byte  `json:"mac"`
}

// Message is the single frame type on every transport.
type Message struct {
	Kind        Kind         `json:"kind"`
	Clip        *Envelope    `json:"clip,omitempty"`
	Chunk       *Chunk       `json:"chunk,omitempty"`
	Need        *Need        `json:"need,omitempty"`
	Hello       *Hello       `json:"hello,omitempty"`
	Control     *Control     `json:"control,omitempty"`
	Rekey       *Rekey       `json:"rekey,omitempty"`
	PairHello   *PairHello   `json:"pair_hello,omitempty"`
	PairWelcome *PairWelcome `json:"pair_welcome,omitempty"`
	Error       string       `json:"error,omitempty"`
	// Seq and Gen are stamped by the relay on stored messages.
	Seq uint64 `json:"seq,omitempty"`
	Gen string `json:"gen,omitempty"`
}

// Clock returns the Lamport time carried by the message, if any.
func (m *Message) Clock() uint64 {
	switch {
	case m.Clip != nil:
		return m.Clip.Clock
	case m.Control != nil:
		return m.Control.Clock
	case m.Rekey != nil:
		return m.Rekey.Clock
	}
	return 0
}

// Priority reports whether the message should jump ahead of bulk chunk data.
func (m *Message) Priority() bool { return m.Kind != KindChunk }

// Encode marshals a message.
func Encode(m *Message) ([]byte, error) { return json.Marshal(m) }

// Decode unmarshals a message.
func Decode(b []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Kind == "" {
		return nil, fmt.Errorf("protocol: message without kind")
	}
	return &m, nil
}

// Split cuts data into ChunkSize pieces. Empty input yields one empty chunk.
func Split(data []byte, size int) [][]byte {
	if size <= 0 {
		size = ChunkSize
	}
	if len(data) == 0 {
		return [][]byte{{}}
	}
	n := (len(data) + size - 1) / size
	out := make([][]byte, 0, n)
	for i := 0; i < len(data); i += size {
		out = append(out, data[i:min(i+size, len(data))])
	}
	return out
}

// ChunkCount returns how many chunks a payload of size bytes needs.
func ChunkCount(size int64, chunk int) int {
	if size <= 0 {
		return 1
	}
	return int((size + int64(chunk) - 1) / int64(chunk))
}
