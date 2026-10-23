// Package daemon wires the clipboard, engine, transports and history together
// and exposes a local HTTP API over a Unix socket for the CLI and tray app.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/meetmehta0685/Clipboard-Sync/internal/clipboard"
	"github.com/meetmehta0685/Clipboard-Sync/internal/config"
	"github.com/meetmehta0685/Clipboard-Sync/internal/crypto"
	"github.com/meetmehta0685/Clipboard-Sync/internal/engine"
	"github.com/meetmehta0685/Clipboard-Sync/internal/protocol"
	"github.com/meetmehta0685/Clipboard-Sync/internal/store"
	"github.com/meetmehta0685/Clipboard-Sync/internal/transport"
	"github.com/meetmehta0685/Clipboard-Sync/internal/transport/lan"
	"github.com/meetmehta0685/Clipboard-Sync/internal/transport/relay"
	"github.com/meetmehta0685/Clipboard-Sync/internal/watcher"
)

// SocketPath is where the local API listens.
func SocketPath(dir string) string { return filepath.Join(dir, "clipsyncd.sock") }

// Daemon is a running clipsync instance.
type Daemon struct {
	St      *config.State
	Engine  *engine.Engine
	History *store.Store
	Watcher *watcher.Watcher
	Log     *slog.Logger

	ctx context.Context
}

// FilterFrom builds the watcher filter from settings.
func FilterFrom(s config.Settings) (watcher.Filter, error) {
	f := watcher.Filter{MaxBytes: s.MaxClipBytes, Images: s.SyncImages, Secrets: s.FilterSecrets}
	for _, expr := range s.ExtraFilters {
		re, err := regexp.Compile(expr)
		if err != nil {
			return f, fmt.Errorf("filter %q: %w", expr, err)
		}
		f.Extra = append(f.Extra, re)
	}
	return f, nil
}

// New assembles a daemon around a clipboard implementation.
func New(st *config.State, cb clipboard.Clipboard, log *slog.Logger) (*Daemon, error) {
	s := st.Settings()
	f, err := FilterFrom(s)
	if err != nil {
		return nil, err
	}
	secret, err := st.HistorySecret()
	if err != nil {
		return nil, fmt.Errorf("history key: %w", err)
	}
	h, err := store.Open(filepath.Join(st.Dir, "history.db"), secret)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	d := &Daemon{St: st, History: h, Log: log}
	d.Watcher = watcher.New(cb, s.PollInterval.D(), f, log)
	d.Engine = engine.New(engine.Options{State: st, Watcher: d.Watcher, History: h, Transports: d.transports, Log: log})
	return d, nil
}

func (d *Daemon) hello(g *crypto.GroupKey) func() *protocol.Hello {
	return func() *protocol.Hello {
		gen, seq := d.St.RelayCursor()
		return &protocol.Hello{V: protocol.Version, Room: g.RoomID(), Token: g.RoomToken(), Device: d.St.DeviceID(), Gen: gen, Since: seq}
	}
}

func (d *Daemon) transports(g *crypto.GroupKey) []transport.Transport {
	s := d.St.Settings()
	var ts []transport.Transport
	if s.LAN {
		id := d.St.Identity()
		ts = append(ts, lan.New(lan.Options{
			DeviceID: d.St.DeviceID(),
			Keys:     lan.Keys{Private: id.Bytes(), Public: id.Public()},
			Room:     fmt.Sprintf("%s|%d", g.RoomID(), g.Epoch),
			Port:     s.LANPort,
			Discover: true,
			Log:      d.Log,
			Peers: func() []lan.Peer {
				var ps []lan.Peer
				for _, dev := range d.St.Devices() {
					if dev.Kind != "phone" {
						ps = append(ps, lan.Peer{ID: dev.ID, Pub: dev.Pub})
					}
				}
				return ps
			},
		}))
	}
	if s.RelayURL != "" {
		ts = append(ts, relay.New(s.RelayURL, d.hello(g), d.Log))
	}
	return ts
}

// Run serves the local API and runs sync until ctx ends.
func (d *Daemon) Run(ctx context.Context) error {
	d.ctx = ctx
	sock := SocketPath(d.St.Dir)
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		c.Close()
		return errors.New("clipsyncd is already running for this profile")
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("local API: %w", err)
	}
	_ = os.Chmod(sock, 0o600)
	srv := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.Log.Error("local API stopped", "err", err)
		}
	}()
	defer os.Remove(sock)
	defer d.History.Close()
	d.Log.Info("clipsyncd running", "device", d.St.DeviceID()[:8], "name", d.St.Name(),
		"relay", d.St.Settings().RelayURL, "devices", len(d.St.Devices()), "api", sock)
	return d.Engine.Run(ctx)
}

// RemoveDevice rotates the group key so the removed device cannot read
// anything sent afterwards, and distributes the new key to everyone else.
func (d *Daemon) RemoveDevice(ctx context.Context, id string) error {
	old := d.St.Group()
	ng, keep, msgs, err := d.Engine.Rekeys(id)
	if err != nil {
		return err
	}
	// Deliver on the old room over a dedicated connection so the messages are
	// written before the engine switches rooms. The relay stores them, so
	// offline devices pick them up when they reconnect.
	if url := d.St.Settings().RelayURL; url != "" {
		gen, seq := d.St.RelayCursor()
		conn, err := relay.Dial(ctx, url, &protocol.Hello{V: protocol.Version, Room: old.RoomID(), Token: old.RoomToken(), Device: d.St.DeviceID(), Gen: gen, Since: seq})
		if err != nil {
			return fmt.Errorf("rekey: relay unreachable, device not removed: %w", err)
		}
		for _, m := range msgs {
			if err := conn.Send(ctx, m); err != nil {
				conn.Close()
				return fmt.Errorf("rekey: %w", err)
			}
		}
		conn.Close()
	}
	for _, m := range msgs {
		d.Engine.SendNow(ctx, m) // LAN peers
	}
	time.Sleep(300 * time.Millisecond)
	return d.Engine.CommitRekey(ng, keep)
}
