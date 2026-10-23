package pairing

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meetmehta/clipsync/internal/clipboard"
	"github.com/meetmehta/clipsync/internal/config"
	"github.com/meetmehta/clipsync/internal/crypto"
	"github.com/meetmehta/clipsync/internal/engine"
	"github.com/meetmehta/clipsync/internal/hub"
	"github.com/meetmehta/clipsync/internal/keystore"
	"github.com/meetmehta/clipsync/internal/protocol"
	"github.com/meetmehta/clipsync/internal/transport"
	"github.com/meetmehta/clipsync/internal/transport/relay"
	"github.com/meetmehta/clipsync/internal/watcher"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func relayURL(t *testing.T) string {
	srv := httptest.NewServer(hub.New(hub.DefaultConfig(), quiet()))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func newState(t *testing.T, name, relay string) *config.State {
	st, err := config.Load(t.TempDir(), &keystore.Memory{})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Update(func(f *config.File) { f.Name = name; f.Settings.RelayURL = relay })
	return st
}

func TestLinkRoundTrip(t *testing.T) {
	inv := &Invite{V: 1, Relay: "wss://relay.example.com/ws", Device: "d", Name: "Mac", Pub: make([]byte, 32), Code: make([]byte, 16)}
	link := inv.Link()
	if !strings.HasPrefix(link, "https://relay.example.com/pair#") {
		t.Fatalf("link %s", link)
	}
	back, err := ParseLink(link)
	if err != nil || back.Name != "Mac" {
		t.Fatalf("parse: %v", err)
	}
	if _, err := ParseLink("https://example.com/#garbage"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func pair(t *testing.T, ctx context.Context, inviter, joiner *config.State) config.Device {
	t.Helper()
	host, err := NewHost(ctx, inviter)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan config.Device, 1)
	errc := make(chan error, 1)
	go func() {
		d, err := host.Wait(ctx)
		if err != nil {
			errc <- err
			return
		}
		got <- d
	}()
	if _, err := Join(ctx, joiner, host.Invite.Link(), "desktop"); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		return d
	case err := <-errc:
		t.Fatal(err)
	}
	return config.Device{}
}

func TestPairing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := relayURL(t)
	laptop, desktop := newState(t, "laptop", url), newState(t, "desktop", url)
	if bytes.Equal(laptop.Group().Key[:], desktop.Group().Key[:]) {
		t.Fatal("fresh devices should start in different groups")
	}

	d := pair(t, ctx, laptop, desktop)
	if d.ID != desktop.DeviceID() || !bytes.Equal(d.Pub, desktop.Identity().Public()) {
		t.Fatal("inviter recorded the wrong device")
	}
	if !bytes.Equal(laptop.Group().Key[:], desktop.Group().Key[:]) {
		t.Fatal("devices do not share the group key")
	}
	if len(laptop.Devices()) != 2 || len(desktop.Devices()) != 2 {
		t.Fatalf("device lists: %d / %d", len(laptop.Devices()), len(desktop.Devices()))
	}
}

func TestWrongCodeIgnored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := relayURL(t)
	laptop, attacker := newState(t, "laptop", url), newState(t, "attacker", url)
	host, err := NewHost(ctx, laptop)
	if err != nil {
		t.Fatal(err)
	}
	// The attacker can find the room (say the relay leaked it) but does not
	// know the code, so its MAC is wrong.
	conn, err := relay.Dial(ctx, url, &protocol.Hello{Room: crypto.PairRoom(host.Invite.Code), Pair: true})
	if err != nil {
		t.Fatal(err)
	}
	ph := &protocol.PairHello{Device: attacker.DeviceID(), Name: "evil", Pub: attacker.Identity().Public(), MAC: make([]byte, 32)}
	_ = conn.Send(ctx, &protocol.Message{Kind: protocol.KindPairHello, PairHello: ph})

	wctx, wcancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer wcancel()
	if _, err := host.Wait(wctx); err == nil {
		t.Fatal("host accepted a hello without the code")
	}
	if len(laptop.Devices()) != 1 {
		t.Fatal("attacker was added to the group")
	}
}

// TestEndToEndOverRelay pairs two devices and syncs a clip through a real
// relay server with real WebSocket clients.
func TestEndToEndOverRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := relayURL(t)
	a, b := newState(t, "a", url), newState(t, "b", url)
	pair(t, ctx, a, b)

	run := func(st *config.State) *clipboard.Memory {
		cb := clipboard.NewMemory()
		w := watcher.New(cb, 10*time.Millisecond, watcher.Filter{}, quiet())
		e := engine.New(engine.Options{State: st, Watcher: w, Log: quiet(), Transports: func(g *crypto.GroupKey) []transport.Transport {
			return []transport.Transport{relay.New(st.Settings().RelayURL, func() *protocol.Hello {
				gen, seq := st.RelayCursor()
				return &protocol.Hello{V: protocol.Version, Room: g.RoomID(), Token: g.RoomToken(), Device: st.DeviceID(), Gen: gen, Since: seq}
			}, quiet())}
		}})
		go e.Run(ctx)
		return cb
	}
	cbA, cbB := run(a), run(b)
	time.Sleep(300 * time.Millisecond)

	cbA.SetText("over the relay")
	for cbB.Text() != "over the relay" {
		select {
		case <-ctx.Done():
			t.Fatal("clip never arrived over the relay")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
