package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/meetmehta/clipsync/internal/clipboard"
	"github.com/meetmehta/clipsync/internal/config"
	"github.com/meetmehta/clipsync/internal/crypto"
	"github.com/meetmehta/clipsync/internal/keystore"
	"github.com/meetmehta/clipsync/internal/store"
	"github.com/meetmehta/clipsync/internal/transport"
	"github.com/meetmehta/clipsync/internal/transport/memnet"
	"github.com/meetmehta/clipsync/internal/watcher"
)

type device struct {
	name string
	cb   *clipboard.Memory
	st   *config.State
	e    *Engine
	h    *store.Store
	eps  []*memnet.Endpoint
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newGroup creates n devices sharing one group key, each joined to every
// given network (e.g. a "lan" and a "relay" network for multipath tests).
func newGroup(t *testing.T, ctx context.Context, n int, nets ...*memnet.Network) []*device {
	t.Helper()
	g, _ := crypto.NewGroupKey(1)
	var devs []*device
	var members []config.Device
	for i := range n {
		name := string(rune('a' + i))
		st, err := config.Load(t.TempDir(), &keystore.Memory{})
		if err != nil {
			t.Fatal(err)
		}
		_ = st.Update(func(f *config.File) {
			f.DeviceID, f.Name = "dev-"+name, name
			f.Settings.MaxClipBytes = 1 << 20
			f.Settings.DownloadDir = t.TempDir()
		})
		members = append(members, config.Device{ID: "dev-" + name, Name: name, Pub: st.Identity().Public()})
		devs = append(devs, &device{name: name, cb: clipboard.NewMemory(), st: st})
	}
	for _, d := range devs {
		if err := d.st.JoinGroup(g, members); err != nil {
			t.Fatal(err)
		}
		h, err := store.Open(t.TempDir()+"/h.db", []byte("k"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h.Close() })
		d.h = h
		for _, n := range nets {
			d.eps = append(d.eps, n.Join(d.name, fmt.Sprintf("net%d", len(d.eps))))
		}
		w := watcher.New(d.cb, 5*time.Millisecond, watcher.Filter{Images: true}, quiet())
		d.e = New(Options{
			State: d.st, Watcher: w, History: h, Log: quiet(), ChunkSize: 1024,
			StallAfter: 100 * time.Millisecond,
			Transports: func(*crypto.GroupKey) []transport.Transport {
				ts := make([]transport.Transport, len(d.eps))
				for i, ep := range d.eps {
					ts[i] = ep
				}
				return ts
			},
		})
		go d.e.Run(ctx)
	}
	// Let watchers take their start-up snapshot and transports come up.
	time.Sleep(50 * time.Millisecond)
	return devs
}

func eventually(t *testing.T, timeout time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func allHave(devs []*device, text string) func() bool {
	return func() bool {
		for _, d := range devs {
			if d.cb.Text() != text {
				return false
			}
		}
		return true
	}
}

func historyLen(d *device) int {
	l, _ := d.h.List(0, "")
	return len(l)
}

func TestThreeDevicesSyncWithoutEchoLoops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	devs := newGroup(t, ctx, 3, memnet.New(memnet.Chaos{}, 1))
	a, b := devs[0], devs[1]

	a.cb.SetText("hello from a")
	eventually(t, 2*time.Second, "all devices to get a's clip", allHave(devs, "hello from a"))

	b.cb.SetText("reply from b")
	eventually(t, 2*time.Second, "all devices to get b's clip", allHave(devs, "reply from b"))

	// With an echo loop, devices would keep re-sending and history would
	// grow without bound. Give it time to misbehave, then check.
	time.Sleep(200 * time.Millisecond)
	for _, d := range devs {
		if n := historyLen(d); n != 2 {
			t.Errorf("device %s has %d history entries, want 2", d.name, n)
		}
	}
}

func TestMultipathDedupe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lan, relay := memnet.New(memnet.Chaos{}, 1), memnet.New(memnet.Chaos{MaxDelay: 20 * time.Millisecond}, 2)
	devs := newGroup(t, ctx, 2, lan, relay)
	a, b := devs[0], devs[1]
	before := b.cb.ChangeCount()

	a.cb.SetText("sent twice")
	eventually(t, 2*time.Second, "b to receive", allHave(devs, "sent twice"))
	time.Sleep(100 * time.Millisecond) // let the second copy arrive

	if n := b.cb.ChangeCount() - before; n != 1 {
		t.Fatalf("b's clipboard was written %d times, want 1", n)
	}
	if n := historyLen(b); n != 1 {
		t.Fatalf("b has %d history entries, want 1", n)
	}
}

func TestConcurrentCopiesConverge(t *testing.T) {
	for seed := range uint64(5) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			net := memnet.New(memnet.Chaos{MaxDelay: 40 * time.Millisecond}, seed)
			devs := newGroup(t, ctx, 3, net)
			// Everyone copies at once; messages cross and arrive reordered.
			for _, d := range devs {
				d.cb.SetText("copy from " + d.name)
			}
			defer func() {
				if t.Failed() {
					for _, d := range devs {
						l, _ := d.h.List(0, "")
						var hs []string
						for _, c := range l {
							hs = append(hs, fmt.Sprintf("%s@%d/%s", c.Data, c.Clock, c.Origin))
						}
						t.Logf("%s: clipboard=%q latest=%v history=%v", d.name, d.cb.Text(), d.e.Latest(), hs)
					}
				}
			}()
			eventually(t, 3*time.Second, "devices to converge", func() bool {
				first := devs[0].cb.Text()
				for _, d := range devs {
					if d.cb.Text() != first || historyLen(d) != 3 {
						return false
					}
				}
				return true
			})
			// The winner is the highest (clock, device ID) stamp, identical
			// everywhere.
			want := devs[0].e.Latest()
			for _, d := range devs {
				if d.e.Latest() != want {
					t.Fatalf("device %s latest %v, want %v", d.name, d.e.Latest(), want)
				}
			}
		})
	}
}

func TestLargeImageSurvivesChunkLoss(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 30% of chunks are lost; the receiver must notice and re-request them.
	net := memnet.New(memnet.Chaos{DropRate: 0.3, DropChunks: true, MaxDelay: 5 * time.Millisecond}, 7)
	devs := newGroup(t, ctx, 2, net)
	a, b := devs[0], devs[1]

	img := make([]byte, 60<<10) // 60 chunks of 1 KiB
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img {
		img[i] = byte(r.IntN(256))
	}
	_ = a.cb.Write(clipboard.Content{Type: clipboard.Image, Data: img})

	eventually(t, 5*time.Second, "b to reassemble the image", func() bool {
		c, _ := b.cb.Read()
		return c.Type == clipboard.Image && bytes.Equal(c.Data, img)
	})
	if _, lost := net.Stats(); lost == 0 {
		t.Fatal("chaos did not drop anything; test is not exercising recovery")
	}
}

func TestTextNotBlockedByImage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	devs := newGroup(t, ctx, 2, memnet.New(memnet.Chaos{}, 3))
	a, b := devs[0], devs[1]
	_, err := a.e.Publish(ctx, clipboard.Content{Type: clipboard.Image, Data: make([]byte, 500<<10)})
	if err != nil {
		t.Fatal(err)
	}
	a.cb.SetText("urgent")
	eventually(t, 2*time.Second, "text clip", func() bool { return b.cb.Text() == "urgent" })
}

func TestRemovedDeviceCannotDecrypt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	devs := newGroup(t, ctx, 3, memnet.New(memnet.Chaos{}, 4))
	a, b, c := devs[0], devs[1], devs[2]

	ng, keep, msgs, err := a.e.Rekeys(c.st.DeviceID())
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Rekey.To != b.st.DeviceID() {
		t.Fatalf("expected one rekey for b, got %d", len(msgs))
	}
	for _, m := range msgs {
		a.e.SendNow(ctx, m)
	}
	eventually(t, 2*time.Second, "b to rotate", func() bool { return b.st.Group().Epoch == 2 })
	if err := a.e.CommitRekey(ng, keep); err != nil {
		t.Fatal(err)
	}
	if c.st.Group().Epoch != 1 {
		t.Fatal("removed device received the new key")
	}
	if len(b.st.Devices()) != 2 {
		t.Fatalf("b should know 2 devices, has %d", len(b.st.Devices()))
	}
	time.Sleep(50 * time.Millisecond)

	a.cb.SetText("after removal")
	eventually(t, 2*time.Second, "b to receive", func() bool { return b.cb.Text() == "after removal" })
	time.Sleep(100 * time.Millisecond)
	if c.cb.Text() == "after removal" {
		t.Fatal("removed device decrypted a clip sent after rotation")
	}
}

func TestFileClipSavedSafely(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	devs := newGroup(t, ctx, 2, memnet.New(memnet.Chaos{}, 5))
	a, b := devs[0], devs[1]
	ev, cancelSub := b.e.Subscribe()
	defer cancelSub()
	if _, err := a.e.Publish(ctx, clipboard.Content{Type: clipboard.File, Name: "../../etc/evil.txt", Data: []byte("payload")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-ev:
			if e.Note != "" {
				want := b.st.Settings().DownloadDir + "/evil.txt"
				if e.Note != "saved file to "+want {
					t.Fatalf("got %q, want file saved inside download dir as %s", e.Note, want)
				}
				return
			}
		case <-deadline:
			t.Fatal("file not received")
		}
	}
}
