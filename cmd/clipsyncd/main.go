// Command clipsyncd is the clipsync desktop daemon and its CLI.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/meetmehta/clipsync/internal/client"
	"github.com/meetmehta/clipsync/internal/clipboard"
	"github.com/meetmehta/clipsync/internal/config"
	"github.com/meetmehta/clipsync/internal/daemon"
	"github.com/meetmehta/clipsync/internal/engine"
	"github.com/meetmehta/clipsync/internal/keystore"
	"github.com/meetmehta/clipsync/internal/pairing"
)

var version = "dev"

const usage = `clipsyncd: end-to-end encrypted clipboard sync

Usage:
  clipsyncd [global flags] <command> [args]

Daemon:
  run                 Start syncing (the default command)

Pairing and devices:
  pair                Show a QR code / link to add a device to this group
  join <link>         Join another device's group using its link
  devices             List devices in the group
  remove <device>     Remove a device and rotate the group key
  leave               Leave the group and start a new one

Clipboard:
  status              Show sync status, transports and latency
  history [-n N] [-q TEXT]
                      Show recent clips
  copy <id>           Put a clip from history back on the clipboard
  send [file]         Send a file, or stdin as text, to all devices
  pause | resume      Stop or restart sending local copies
  watch               Stream sync events
  clear               Delete local history

Settings:
  config              Show settings
  config <key> <value>
                      Change a setting (keys: %s)

Global flags:
  -home DIR           Profile directory (default %s, or $CLIPSYNC_HOME)
  -file-keystore      Store secrets in a file instead of the OS keychain
  -clipboard-file F   Use a text file as the clipboard (run several daemons on one machine)
  -v                  Verbose logging
`

func main() {
	home := flag.String("home", config.DefaultDir(), "profile directory")
	fileKeys := flag.Bool("file-keystore", os.Getenv("CLIPSYNC_FILE_KEYSTORE") != "", "store secrets in a file instead of the OS keychain")
	verbose := flag.Bool("v", false, "verbose logging")
	clipFile := flag.String("clipboard-file", "", "use a text file instead of the system clipboard (for running several daemons on one machine)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, usage, strings.Join(config.SettableKeys, ", "), config.DefaultDir())
	}
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	args := flag.Args()
	cmd := "run"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := &app{home: *home, fileKeys: *fileKeys, clipFile: *clipFile, log: log, api: client.New(daemon.SocketPath(*home))}
	if err := app.dispatch(ctx, cmd, args); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type app struct {
	home     string
	fileKeys bool
	clipFile string
	log      *slog.Logger
	api      *client.Client
}

// keys returns the secret store. Each profile directory gets its own
// keychain service so several daemons can run on one machine.
func (a *app) keys() keystore.Store {
	fallback := filepath.Join(a.home, "secrets.json")
	if a.fileKeys {
		return &keystore.File{Path: fallback}
	}
	service := "clipsync"
	abs, _ := filepath.Abs(a.home)
	if ucd, err := os.UserConfigDir(); err != nil || abs != filepath.Join(ucd, "clipsync") {
		sum := sha256.Sum256([]byte(abs))
		service += "-" + hex.EncodeToString(sum[:4])
	}
	return keystore.Open(service, fallback)
}

func (a *app) state() (*config.State, error) { return config.Load(a.home, a.keys()) }

func (a *app) dispatch(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "run":
		return a.run(ctx)
	case "status":
		return a.status(ctx)
	case "history", "h":
		return a.history(ctx, args)
	case "copy":
		if len(args) != 1 {
			return errors.New("usage: clipsyncd copy <id>")
		}
		id, err := a.resolveClip(ctx, args[0])
		if err != nil {
			return err
		}
		return a.api.Call(ctx, "POST", client.Path("/copy", "id", id), nil, nil)
	case "send":
		return a.send(ctx, args)
	case "pause", "resume":
		if err := a.api.Call(ctx, "POST", "/"+cmd, nil, nil); err != nil {
			return err
		}
		fmt.Println("sync", map[string]string{"pause": "paused", "resume": "resumed"}[cmd])
		return nil
	case "pair", "invite":
		return a.pair(ctx)
	case "join":
		if len(args) != 1 {
			return errors.New("usage: clipsyncd join <link>")
		}
		return a.join(ctx, args[0])
	case "devices":
		return a.devices(ctx)
	case "remove":
		if len(args) != 1 {
			return errors.New("usage: clipsyncd remove <device-id-or-name>")
		}
		return a.remove(ctx, args[0])
	case "leave":
		return a.api.Call(ctx, "POST", "/leave", nil, nil)
	case "watch":
		return a.watch(ctx)
	case "clear":
		return a.api.Call(ctx, "POST", "/clear", nil, nil)
	case "config":
		return a.config(ctx, args)
	case "version":
		fmt.Println("clipsyncd", version)
		return nil
	case "help", "-h", "--help":
		flag.Usage()
		return nil
	}
	flag.Usage()
	return fmt.Errorf("unknown command %q", cmd)
}

func (a *app) run(ctx context.Context) error {
	st, err := a.state()
	if err != nil {
		return err
	}
	var cb clipboard.Clipboard = &clipboard.FileClipboard{Path: a.clipFile}
	if a.clipFile == "" {
		if cb, err = clipboard.System(); err != nil {
			return fmt.Errorf("clipboard: %w", err)
		}
	}
	d, err := daemon.New(st, cb, a.log)
	if err != nil {
		return err
	}
	return d.Run(ctx)
}

func (a *app) status(ctx context.Context) error {
	var r struct {
		Status   engine.Status `json:"status"`
		RelayURL string        `json:"relay_url"`
		LAN      bool          `json:"lan"`
	}
	if err := a.api.Call(ctx, "GET", "/status", nil, &r); err != nil {
		return err
	}
	s := r.Status
	state := "syncing"
	if s.Paused {
		state = "paused"
	}
	fmt.Printf("%s (%s) is %s\n", s.Name, s.Device[:8], state)
	fmt.Printf("  group:    %d devices, key epoch %d\n", s.Devices, s.Epoch)
	fmt.Printf("  relay:    %s\n", r.RelayURL)
	for _, t := range s.Transports {
		mark := "✗ disconnected"
		if t.Connected {
			mark = "✓ connected"
		}
		fmt.Printf("  %-8s  %s\n", t.Name+":", mark)
	}
	if s.Skipped > 0 {
		fmt.Printf("  filtered: %d clips not synced (concealed or secret-looking)\n", s.Skipped)
	}
	for via, l := range s.Latency {
		fmt.Printf("  latency via %s: p50 %.1f ms, p95 %.1f ms (%d clips)\n", via, l.P50, l.P95, l.Count)
	}
	return nil
}

func (a *app) history(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	n := fs.Int("n", 20, "number of clips")
	q := fs.String("q", "", "search text")
	_ = fs.Parse(args)
	var items []daemon.HistoryItem
	if err := a.api.Call(ctx, "GET", client.Path("/history", "limit", fmt.Sprint(*n), "q", *q), nil, &items); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, it := range items {
		from := it.Device
		if it.Local {
			from = "this device"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", it.ID[:8], it.Created.Format("Jan 02 15:04"), from, it.Preview)
	}
	return tw.Flush()
}

func (a *app) resolveClip(ctx context.Context, prefix string) (string, error) {
	var items []daemon.HistoryItem
	if err := a.api.Call(ctx, "GET", client.Path("/history", "limit", "500"), nil, &items); err != nil {
		return "", err
	}
	for _, it := range items {
		if strings.HasPrefix(it.ID, prefix) {
			return it.ID, nil
		}
	}
	return "", fmt.Errorf("no clip with id %s", prefix)
}

func (a *app) send(ctx context.Context, args []string) error {
	var body []byte
	var name string
	var err error
	if len(args) == 1 {
		body, err = os.ReadFile(args[0])
		name = filepath.Base(args[0])
	} else {
		body, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return err
	}
	var r struct{ ID string }
	if err := a.api.Call(ctx, "POST", client.Path("/send", "name", name), body, &r); err != nil {
		return err
	}
	fmt.Printf("sent %d bytes (clip %s)\n", len(body), r.ID[:8])
	return nil
}

func (a *app) pair(ctx context.Context) error {
	return a.api.Stream(ctx, "/pair/invite", func(m map[string]json.RawMessage) error {
		if raw, ok := m["link"]; ok {
			var link string
			_ = json.Unmarshal(raw, &link)
			fmt.Println("Scan with your phone, or run on another computer:")
			fmt.Println()
			qrterminal.GenerateWithConfig(link, qrterminal.Config{
				Level: qrterminal.L, Writer: os.Stdout, HalfBlocks: true,
				BlackChar: qrterminal.BLACK_BLACK, WhiteBlackChar: qrterminal.WHITE_BLACK,
				WhiteChar: qrterminal.WHITE_WHITE, BlackWhiteChar: qrterminal.BLACK_WHITE, QuietZone: 2,
			})
			fmt.Printf("\n  clipsyncd join '%s'\n\nWaiting for the other device (Ctrl-C to cancel)...\n", link)
		}
		if raw, ok := m["device"]; ok {
			var d config.Device
			_ = json.Unmarshal(raw, &d)
			fmt.Printf("Paired with %s (%s, %s).\n", d.Name, d.Kind, d.ID[:8])
		}
		return nil
	})
}

func (a *app) join(ctx context.Context, link string) error {
	if _, err := pairing.ParseLink(link); err != nil {
		return err
	}
	if a.api.Running() {
		var r struct {
			Inviter string
			Devices int
		}
		if err := a.api.Call(ctx, "POST", "/pair/join", map[string]string{"link": link}, &r); err != nil {
			return err
		}
		fmt.Printf("Joined %s's group (%d devices).\n", r.Inviter, r.Devices)
		return nil
	}
	st, err := a.state()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	inv, err := pairing.Join(ctx, st, link, "desktop")
	if err != nil {
		return err
	}
	fmt.Printf("Joined %s's group (%d devices). Start syncing with `clipsyncd run`.\n", inv.Name, len(st.Devices()))
	return nil
}

func (a *app) deviceList(ctx context.Context) ([]config.Device, string, error) {
	var devs []config.Device
	if err := a.api.Call(ctx, "GET", "/devices", nil, &devs); err == nil {
		var s struct {
			Status engine.Status `json:"status"`
		}
		_ = a.api.Call(ctx, "GET", "/status", nil, &s)
		return devs, s.Status.Device, nil
	} else if !errors.Is(err, client.ErrNotRunning) {
		return nil, "", err
	}
	st, err := a.state()
	if err != nil {
		return nil, "", err
	}
	return st.Devices(), st.DeviceID(), nil
}

func (a *app) devices(ctx context.Context) error {
	devs, me, err := a.deviceList(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tKIND\tADDED")
	for _, d := range devs {
		name := d.Name
		if d.ID == me {
			name += " (this device)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.ID[:8], name, d.Kind, d.Added.Format("2006-01-02"))
	}
	return tw.Flush()
}

func (a *app) remove(ctx context.Context, which string) error {
	devs, _, err := a.deviceList(ctx)
	if err != nil {
		return err
	}
	var match []config.Device
	for _, d := range devs {
		if strings.HasPrefix(d.ID, which) || strings.EqualFold(d.Name, which) {
			match = append(match, d)
		}
	}
	if len(match) != 1 {
		return fmt.Errorf("%q matches %d devices", which, len(match))
	}
	if err := a.api.Call(ctx, "POST", client.Path("/devices/remove", "id", match[0].ID), nil, nil); err != nil {
		return err
	}
	fmt.Printf("Removed %s and rotated the group key.\n", match[0].Name)
	return nil
}

func (a *app) watch(ctx context.Context) error {
	return a.api.Events(ctx, func(raw json.RawMessage) {
		var ev engine.Event
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		ts := time.Now().Format("15:04:05")
		switch ev.Type {
		case "clip":
			dir := "→ sent    "
			if ev.Remote {
				dir = "← received"
			}
			fmt.Printf("%s %s %s\n", ts, dir, ev.Preview)
		default:
			fmt.Printf("%s %s %s\n", ts, ev.Type, ev.Note)
		}
	})
}

func (a *app) config(ctx context.Context, args []string) error {
	running := a.api.Running()
	if len(args) == 0 {
		var f config.File
		if running {
			if err := a.api.Call(ctx, "GET", "/config", nil, &f); err != nil {
				return err
			}
		} else {
			st, err := a.state()
			if err != nil {
				return err
			}
			f = st.File()
		}
		out, _ := json.MarshalIndent(struct {
			Name     string          `json:"name"`
			Device   string          `json:"device_id"`
			Settings config.Settings `json:"settings"`
		}{f.Name, f.DeviceID, f.Settings}, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	if len(args) != 2 {
		return errors.New("usage: clipsyncd config <key> <value>")
	}
	if running {
		return a.api.Call(ctx, "POST", "/config", map[string]string{"Key": args[0], "Value": args[1]}, nil)
	}
	st, err := a.state()
	if err != nil {
		return err
	}
	var setErr error
	if err := st.Update(func(f *config.File) { setErr = config.Set(f, args[0], args[1]) }); err != nil {
		return err
	}
	return setErr
}
