package lan

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
)

// Noise_IK_25519_ChaChaPoly_BLAKE2s: the initiator already knows the
// responder's static key (from the group device list), so the handshake is a
// single round trip and both sides end up mutually authenticated. It is the
// same construction WireGuard uses.
var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

const (
	maxRecord   = 65535 - 16 // Noise message limit minus the AEAD tag
	maxAppFrame = 4 << 20
)

// secureConn is an authenticated, encrypted stream of length-prefixed frames.
type secureConn struct {
	raw      net.Conn
	r        *bufio.Reader
	peerKey  []byte
	peerID   string
	sendMu   sync.Mutex
	send     *noise.CipherState
	recv     *noise.CipherState
	plainBuf []byte
}

func writeRecord(w io.Writer, b []byte) error {
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readRecord(r io.Reader) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	b := make([]byte, binary.BigEndian.Uint16(hdr[:]))
	_, err := io.ReadFull(r, b)
	return b, err
}

// Keys is a device's static X25519 keypair.
type Keys struct{ Private, Public []byte }

func (k Keys) dh() noise.DHKey { return noise.DHKey{Private: k.Private, Public: k.Public} }

// clientHandshake dials as initiator. myID travels encrypted in the first
// message; the responder checks it against my static key.
func clientHandshake(conn net.Conn, me Keys, myID string, peerPub, prologue []byte) (*secureConn, error) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: suite, Pattern: noise.HandshakeIK, Initiator: true,
		Prologue: prologue, StaticKeypair: me.dh(), PeerStatic: peerPub,
	})
	if err != nil {
		return nil, err
	}
	msg, _, _, err := hs.WriteMessage(nil, []byte(myID))
	if err != nil {
		return nil, err
	}
	if err := writeRecord(conn, msg); err != nil {
		return nil, err
	}
	r := bufio.NewReader(conn)
	reply, err := readRecord(r)
	if err != nil {
		return nil, err
	}
	payload, c1, c2, err := hs.ReadMessage(nil, reply)
	if err != nil {
		return nil, fmt.Errorf("noise: %w", err)
	}
	if c1 == nil {
		return nil, errors.New("noise: handshake incomplete")
	}
	return &secureConn{raw: conn, r: r, peerKey: peerPub, peerID: string(payload), send: c1, recv: c2}, nil
}

// serverHandshake accepts as responder. authorize maps the initiator's
// static key and claimed ID to an allow/deny decision.
func serverHandshake(conn net.Conn, me Keys, myID string, prologue []byte, authorize func(pub []byte, id string) bool) (*secureConn, error) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: suite, Pattern: noise.HandshakeIK, Initiator: false,
		Prologue: prologue, StaticKeypair: me.dh(),
	})
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(conn)
	first, err := readRecord(r)
	if err != nil {
		return nil, err
	}
	payload, _, _, err := hs.ReadMessage(nil, first)
	if err != nil {
		return nil, fmt.Errorf("noise: %w", err)
	}
	peerPub := hs.PeerStatic()
	if !authorize(peerPub, string(payload)) {
		return nil, errors.New("noise: peer not in group")
	}
	msg, c1, c2, err := hs.WriteMessage(nil, []byte(myID))
	if err != nil {
		return nil, err
	}
	if err := writeRecord(conn, msg); err != nil {
		return nil, err
	}
	return &secureConn{raw: conn, r: r, peerKey: peerPub, peerID: string(payload), send: c2, recv: c1}, nil
}

// WriteFrame sends one application frame, split into Noise records.
func (s *secureConn) WriteFrame(b []byte) error {
	if len(b) > maxAppFrame {
		return errors.New("lan: frame too large")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	framed := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(b)), uint32(len(b)))
	framed = append(framed, b...)
	w := bufio.NewWriterSize(s.raw, 64<<10)
	for len(framed) > 0 {
		n := min(len(framed), maxRecord)
		ct, err := s.send.Encrypt(nil, nil, framed[:n])
		if err != nil {
			return err
		}
		if err := writeRecord(w, ct); err != nil {
			return err
		}
		framed = framed[n:]
	}
	_ = s.raw.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return w.Flush()
}

// Read implements io.Reader over the decrypted record stream.
func (s *secureConn) Read(p []byte) (int, error) {
	for len(s.plainBuf) == 0 {
		rec, err := readRecord(s.r)
		if err != nil {
			return 0, err
		}
		pt, err := s.recv.Decrypt(nil, nil, rec)
		if err != nil {
			return 0, fmt.Errorf("noise: %w", err)
		}
		s.plainBuf = pt
	}
	n := copy(p, s.plainBuf)
	s.plainBuf = s.plainBuf[n:]
	return n, nil
}

// ReadFrame reads one application frame.
func (s *secureConn) ReadFrame() ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(s, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxAppFrame {
		return nil, errors.New("lan: frame too large")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(s, b)
	return b, err
}

func (s *secureConn) Close() error { return s.raw.Close() }
