// Package pairing adds a device to a group.
//
//	Existing device (inviter)                     New device (joiner)
//	shows QR: {relay, device id, public key, one-time code}
//	                                   ◀── scans QR / pastes link
//	both join relay room "pair-" + HKDF(code)
//	                 ◀── PairHello{id, name, pub, HMAC(code, ...)}
//	verify HMAC: the joiner really saw the QR
//	PairWelcome{SealTo(joiner pub, group key + devices), HMAC(code, ...)} ──▶
//	                                            verify HMAC and inviter key
//
// The code never touches the relay (only a hash of it names the room), so a
// malicious relay can neither join as the new device nor forge the welcome.
package pairing

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/meetmehta0685/Clipboard-Sync/internal/config"
	"github.com/meetmehta0685/Clipboard-Sync/internal/crypto"
	"github.com/meetmehta0685/Clipboard-Sync/internal/engine"
	"github.com/meetmehta0685/Clipboard-Sync/internal/protocol"
	"github.com/meetmehta0685/Clipboard-Sync/internal/transport/relay"
)

// Invite is what the QR code encodes.
type Invite struct {
	V      int    `json:"v"`
	Relay  string `json:"r"` // relay WebSocket URL
	Device string `json:"d"`
	Name   string `json:"n"`
	Pub    []byte `json:"k"`
	Code   []byte `json:"c"`
}

// Welcome is the plaintext sealed to the new device.
type Welcome struct {
	Key     []byte          `json:"key"`
	Epoch   uint32          `json:"epoch"`
	Devices []config.Device `json:"devices"`
	Relay   string          `json:"relay"`
}

// WebBase turns a relay WebSocket URL into the HTTP base URL that serves the
// PWA: wss://relay.example.com/ws → https://relay.example.com.
func WebBase(relayURL string) string {
	u, err := url.Parse(relayURL)
	if err != nil {
		return relayURL
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	}
	u.Path = strings.TrimSuffix(u.Path, "/ws")
	return strings.TrimSuffix(u.String(), "/")
}

// Link encodes the invite as a URL that opens the PWA's pairing screen, so a
// phone camera can scan it directly. The secret is in the fragment, which
// browsers never send to the server.
func (inv *Invite) Link() string {
	b, _ := json.Marshal(inv)
	return WebBase(inv.Relay) + "/pair#" + base64.RawURLEncoding.EncodeToString(b)
}

// ParseLink decodes a link produced by Link (or just its fragment).
func ParseLink(s string) (*Invite, error) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '#'); i >= 0 {
		s = s[i+1:]
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("pairing: not a clipsync link: %w", err)
	}
	var inv Invite
	if err := json.Unmarshal(b, &inv); err != nil {
		return nil, fmt.Errorf("pairing: bad invite: %w", err)
	}
	if inv.V != 1 || len(inv.Code) < 16 || len(inv.Pub) != 32 || inv.Relay == "" {
		return nil, errors.New("pairing: incomplete invite")
	}
	return &inv, nil
}

func helloMAC(code []byte, h *protocol.PairHello) []byte {
	return crypto.PairMAC(code, "clipsync pair hello", []byte(h.Device), []byte(h.Name), []byte(h.Kind), h.Pub)
}

func welcomeMAC(code []byte, s *protocol.Sealed) []byte {
	return crypto.PairMAC(code, "clipsync pair welcome", s.Ephemeral, s.Sender, s.Nonce, s.Payload)
}

// Host is an open invitation on the inviter's side.
type Host struct {
	Invite *Invite
	st     *config.State
	conn   *relay.Conn
}

// NewHost creates an invite and waits in the pairing room.
func NewHost(ctx context.Context, st *config.State) (*Host, error) {
	inv := &Invite{
		V: 1, Relay: st.Settings().RelayURL, Device: st.DeviceID(), Name: st.Name(),
		Pub: st.Identity().Public(), Code: crypto.RandomBytes(16),
	}
	conn, err := relay.Dial(ctx, inv.Relay, &protocol.Hello{V: protocol.Version, Room: crypto.PairRoom(inv.Code), Pair: true, Device: inv.Device})
	if err != nil {
		return nil, fmt.Errorf("pairing: connect to relay: %w", err)
	}
	return &Host{Invite: inv, st: st, conn: conn}, nil
}

// Wait blocks until a device joins (or ctx ends), adds it to the group and
// sends it the group key.
func (h *Host) Wait(ctx context.Context) (config.Device, error) {
	defer h.conn.Close()
	for {
		m, err := h.conn.Recv(ctx)
		if err != nil {
			return config.Device{}, err
		}
		if m.Kind != protocol.KindPairHello || m.PairHello == nil {
			continue
		}
		ph := m.PairHello
		if !hmac.Equal(ph.MAC, helloMAC(h.Invite.Code, ph)) || len(ph.Pub) != 32 || ph.Device == "" {
			continue // someone without the code; keep waiting
		}
		kind := ph.Kind
		if kind != "phone" {
			kind = "desktop"
		}
		dev := config.Device{ID: ph.Device, Name: ph.Name, Pub: ph.Pub, Kind: kind, Added: time.Now().UTC()}
		if _, err := h.st.MergeDevices([]config.Device{dev}); err != nil {
			return config.Device{}, err
		}
		g := h.st.Group()
		w, _ := json.Marshal(Welcome{Key: g.Key[:], Epoch: g.Epoch, Devices: h.st.Devices(), Relay: h.Invite.Relay})
		sealed, err := crypto.SealTo(h.st.Identity(), ph.Pub, w, engine.WelcomeInfo)
		if err != nil {
			return config.Device{}, err
		}
		reply := &protocol.Message{Kind: protocol.KindPairWelcome, PairWelcome: &protocol.PairWelcome{Sealed: sealed, MAC: welcomeMAC(h.Invite.Code, sealed)}}
		if err := h.conn.Send(ctx, reply); err != nil {
			return config.Device{}, err
		}
		return dev, nil
	}
}

// Close abandons the invitation.
func (h *Host) Close() error { return h.conn.Close() }

// Join pairs this device using an invite link and replaces its group.
func Join(ctx context.Context, st *config.State, link string, kind string) (*Invite, error) {
	inv, err := ParseLink(link)
	if err != nil {
		return nil, err
	}
	if inv.Device == st.DeviceID() {
		return nil, errors.New("pairing: that invite is from this device")
	}
	conn, err := relay.Dial(ctx, inv.Relay, &protocol.Hello{V: protocol.Version, Room: crypto.PairRoom(inv.Code), Pair: true, Device: st.DeviceID()})
	if err != nil {
		return nil, fmt.Errorf("pairing: connect to relay: %w", err)
	}
	defer conn.Close()

	ph := &protocol.PairHello{Device: st.DeviceID(), Name: st.Name(), Kind: kind, Pub: st.Identity().Public()}
	ph.MAC = helloMAC(inv.Code, ph)
	if err := conn.Send(ctx, &protocol.Message{Kind: protocol.KindPairHello, PairHello: ph}); err != nil {
		return nil, err
	}
	for {
		m, err := conn.Recv(ctx)
		if err != nil {
			return nil, fmt.Errorf("pairing: waiting for inviter: %w", err)
		}
		if m.Kind != protocol.KindPairWelcome || m.PairWelcome == nil || m.PairWelcome.Sealed == nil {
			continue
		}
		pw := m.PairWelcome
		if !hmac.Equal(pw.MAC, welcomeMAC(inv.Code, pw.Sealed)) || !hmac.Equal(pw.Sealed.Sender, inv.Pub) {
			continue
		}
		pt, err := crypto.OpenFrom(st.Identity(), pw.Sealed, engine.WelcomeInfo)
		if err != nil {
			return nil, err
		}
		var w Welcome
		if err := json.Unmarshal(pt, &w); err != nil || len(w.Key) != crypto.KeySize {
			return nil, errors.New("pairing: malformed welcome")
		}
		g := &crypto.GroupKey{Epoch: w.Epoch}
		copy(g.Key[:], w.Key)
		if err := st.JoinGroup(g, w.Devices); err != nil {
			return nil, err
		}
		if w.Relay != "" {
			_ = st.Update(func(f *config.File) { f.Settings.RelayURL = w.Relay })
		}
		return inv, nil
	}
}
