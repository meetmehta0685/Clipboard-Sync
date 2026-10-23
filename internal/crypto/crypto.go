// Package crypto holds clipsync's key material and all encryption.
//
//   - Each device has a long-term X25519 identity keypair.
//   - All devices in a group share a 32-byte symmetric group key with an
//     epoch number; removing a device rotates it.
//   - Clips are encrypted with XChaCha20-Poly1305 under the group key, with
//     the clip header as associated data.
//   - Pairing and rekeying seal secrets to one device's public key.
//
// Everything here is mirrored in web/lib/crypto.ts and checked by the shared
// test vectors in testdata/vectors.json.
package crypto

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/meetmehta/clipsync/internal/protocol"
)

// KeySize is the size of symmetric keys.
const KeySize = 32

var ErrDecrypt = errors.New("crypto: message authentication failed")

// Identity is a device's long-term keypair.
type Identity struct {
	Private *ecdh.PrivateKey
}

// NewIdentity generates a fresh X25519 keypair.
func NewIdentity() (*Identity, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{Private: k}, nil
}

// IdentityFromBytes restores a keypair from its 32-byte private key.
func IdentityFromBytes(b []byte) (*Identity, error) {
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return nil, err
	}
	return &Identity{Private: k}, nil
}

// Public returns the 32-byte public key.
func (id *Identity) Public() []byte { return id.Private.PublicKey().Bytes() }

// Bytes returns the private key for storage in the keychain.
func (id *Identity) Bytes() []byte { return id.Private.Bytes() }

// GroupKey is the symmetric key shared by every device in a group.
type GroupKey struct {
	Key   [KeySize]byte
	Epoch uint32
}

// NewGroupKey generates a random group key at the given epoch.
func NewGroupKey(epoch uint32) (*GroupKey, error) {
	g := &GroupKey{Epoch: epoch}
	if _, err := rand.Read(g.Key[:]); err != nil {
		return nil, err
	}
	return g, nil
}

func derive(secret []byte, info string, n int) []byte {
	out, err := hkdf.Key(sha256.New, secret, nil, info, n)
	if err != nil {
		panic(err) // only fails for absurd lengths
	}
	return out
}

// RoomID is the relay room for this group. It is derived from the key so the
// relay never learns anything that identifies the group's members.
func (g *GroupKey) RoomID() string {
	return hex.EncodeToString(derive(g.Key[:], "clipsync room id", 16))
}

// RoomToken proves membership to the relay.
func (g *GroupKey) RoomToken() []byte { return derive(g.Key[:], "clipsync room token", 32) }

// contentKey is the AEAD key; hashKey keys the dedupe MAC. Keeping them
// separate from the raw group key follows the one-key-one-purpose rule.
func (g *GroupKey) contentKey() []byte { return derive(g.Key[:], "clipsync content key", KeySize) }
func (g *GroupKey) hashKey() []byte    { return derive(g.Key[:], "clipsync hash key", KeySize) }

// ContentHash is the keyed fingerprint of a clip carried in Envelope.Hash.
func (g *GroupKey) ContentHash(typ string, data []byte) []byte {
	m := hmac.New(sha256.New, g.hashKey())
	m.Write([]byte(typ))
	m.Write([]byte{0})
	m.Write(data)
	return m.Sum(nil)
}

func seal(key, plaintext, aad []byte) (nonce, ct []byte, err error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plaintext, aad), nil
}

func open(key, nonce, ct, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// SealClip fills in env.Epoch, Hash, Size, Chunks, Nonce and Payload for
// content, and returns the encrypted follow-up chunks (index 1..n-1).
// env.ID, Origin, Clock and Type must already be set.
func (g *GroupKey) SealClip(env *protocol.Envelope, content []byte, chunkSize int) ([]*protocol.Chunk, error) {
	parts := protocol.Split(content, chunkSize)
	env.V = protocol.Version
	env.Epoch = g.Epoch
	env.Hash = g.ContentHash(env.Type, content)
	env.Size = int64(len(content))
	env.Chunks = len(parts)
	key := g.contentKey()
	var err error
	env.Nonce, env.Payload, err = seal(key, parts[0], env.AAD())
	if err != nil {
		return nil, err
	}
	chunks := make([]*protocol.Chunk, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		nonce, ct, err := seal(key, parts[i], env.ChunkAAD(i))
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, &protocol.Chunk{ID: env.ID, Index: i, Nonce: nonce, Payload: ct})
	}
	return chunks, nil
}

// OpenClip decrypts chunk 0 of a clip.
func (g *GroupKey) OpenClip(env *protocol.Envelope) ([]byte, error) {
	if env.Epoch != g.Epoch {
		return nil, fmt.Errorf("crypto: clip epoch %d, have %d", env.Epoch, g.Epoch)
	}
	return open(g.contentKey(), env.Nonce, env.Payload, env.AAD())
}

// OpenChunk decrypts a follow-up chunk of env.
func (g *GroupKey) OpenChunk(env *protocol.Envelope, c *protocol.Chunk) ([]byte, error) {
	if c.ID != env.ID || c.Index <= 0 || c.Index >= env.Chunks {
		return nil, ErrDecrypt
	}
	return open(g.contentKey(), c.Nonce, c.Payload, env.ChunkAAD(c.Index))
}

// VerifyHash checks the reassembled plaintext against the header.
func (g *GroupKey) VerifyHash(env *protocol.Envelope, content []byte) bool {
	return int64(len(content)) == env.Size && hmac.Equal(env.Hash, g.ContentHash(env.Type, content))
}

// SealControl encrypts an administrative payload for the whole group.
func (g *GroupKey) SealControl(c *protocol.Control, plaintext []byte) error {
	c.Epoch = g.Epoch
	var err error
	c.Nonce, c.Payload, err = seal(g.contentKey(), plaintext, c.AAD())
	return err
}

// OpenControl decrypts a control message.
func (g *GroupKey) OpenControl(c *protocol.Control) ([]byte, error) {
	if c.Epoch != g.Epoch {
		return nil, ErrDecrypt
	}
	return open(g.contentKey(), c.Nonce, c.Payload, c.AAD())
}

// SealTo encrypts plaintext so only recipient can read it and it can verify
// the sender. The key mixes an ephemeral-static DH (forward secrecy for the
// sender) with a static-static DH (sender authentication):
//
//	key = HKDF-SHA256(DH(eph, R) || DH(S, R), info, salt = eph || S || R)
func SealTo(sender *Identity, recipient []byte, plaintext []byte, info string) (*protocol.Sealed, error) {
	rpub, err := ecdh.X25519().NewPublicKey(recipient)
	if err != nil {
		return nil, err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	dh1, err := eph.ECDH(rpub)
	if err != nil {
		return nil, err
	}
	dh2, err := sender.Private.ECDH(rpub)
	if err != nil {
		return nil, err
	}
	key := sealedKDF(dh1, dh2, eph.PublicKey().Bytes(), sender.Public(), recipient, info)
	nonce, ct, err := seal(key, plaintext, []byte(info))
	if err != nil {
		return nil, err
	}
	return &protocol.Sealed{Ephemeral: eph.PublicKey().Bytes(), Sender: sender.Public(), Nonce: nonce, Payload: ct}, nil
}

// OpenFrom decrypts a sealed message addressed to me. The caller must check
// s.Sender against the public key it expects.
func OpenFrom(me *Identity, s *protocol.Sealed, info string) ([]byte, error) {
	ephPub, err := ecdh.X25519().NewPublicKey(s.Ephemeral)
	if err != nil {
		return nil, ErrDecrypt
	}
	senderPub, err := ecdh.X25519().NewPublicKey(s.Sender)
	if err != nil {
		return nil, ErrDecrypt
	}
	dh1, err := me.Private.ECDH(ephPub)
	if err != nil {
		return nil, ErrDecrypt
	}
	dh2, err := me.Private.ECDH(senderPub)
	if err != nil {
		return nil, ErrDecrypt
	}
	key := sealedKDF(dh1, dh2, s.Ephemeral, s.Sender, me.Public(), info)
	return open(key, s.Nonce, s.Payload, []byte(info))
}

func sealedKDF(dh1, dh2, ephPub, senderPub, recipientPub []byte, info string) []byte {
	secret := append(append([]byte{}, dh1...), dh2...)
	salt := append(append(append([]byte{}, ephPub...), senderPub...), recipientPub...)
	prk, err := hkdf.Extract(sha256.New, secret, salt)
	if err != nil {
		panic(err)
	}
	key, err := hkdf.Expand(sha256.New, prk, info, KeySize)
	if err != nil {
		panic(err)
	}
	return key
}

// PairMAC authenticates a pairing message with the one-time code from the QR.
func PairMAC(code []byte, label string, fields ...[]byte) []byte {
	m := hmac.New(sha256.New, code)
	m.Write([]byte(label))
	for _, f := range fields {
		var l [4]byte
		l[0], l[1], l[2], l[3] = byte(len(f)>>24), byte(len(f)>>16), byte(len(f)>>8), byte(len(f))
		m.Write(l[:])
		m.Write(f)
	}
	return m.Sum(nil)
}

// PairRoom is the relay room used while pairing with code.
func PairRoom(code []byte) string {
	return "pair-" + hex.EncodeToString(derive(code, "clipsync pair room", 16))
}

// RandomBytes returns n random bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// LocalKey derives the at-rest key for local history from a random secret
// stored in the keychain.
func LocalKey(secret []byte) []byte { return derive(secret, "clipsync history key", KeySize) }

// SealLocal and OpenLocal encrypt data at rest with a local key.
func SealLocal(key, plaintext, aad []byte) (nonce, ct []byte, err error) {
	return seal(key, plaintext, aad)
}

func OpenLocal(key, nonce, ct, aad []byte) ([]byte, error) { return open(key, nonce, ct, aad) }
