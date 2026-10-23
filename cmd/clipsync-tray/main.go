// Command clipsync-tray is a menu-bar / system-tray front end for a running
// clipsyncd: sync status, pause, recent clips, devices and pairing.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"rsc.io/qr"

	"github.com/meetmehta0685/Clipboard-Sync/internal/client"
	"github.com/meetmehta0685/Clipboard-Sync/internal/config"
	"github.com/meetmehta0685/Clipboard-Sync/internal/daemon"
	"github.com/meetmehta0685/Clipboard-Sync/internal/engine"
)

const recentSlots = 8

func main() {
	home := flag.String("home", config.DefaultDir(), "profile directory of the daemon")
	flag.Parse()
	t := &tray{api: client.New(daemon.SocketPath(*home))}
	systray.Run(t.ready, func() {})
}

type tray struct {
	api *client.Client

	status, pause, pair, devices, quit *systray.MenuItem
	recent                             []*systray.MenuItem

	mu        sync.Mutex
	recentIDs []string
	paused    bool
}

func (t *tray) ready() {
	systray.SetTemplateIcon(icon(true), icon(false))
	systray.SetTooltip("ClipSync")

	t.status = systray.AddMenuItem("Connecting to clipsyncd…", "")
	t.status.Disable()
	systray.AddSeparator()
	t.pause = systray.AddMenuItemCheckbox("Pause sync", "Stop sending what you copy", false)

	recentMenu := systray.AddMenuItem("Recent clips", "Click to copy again")
	for i := range recentSlots {
		item := recentMenu.AddSubMenuItem("", "")
		item.Hide()
		t.recent = append(t.recent, item)
		go func() {
			for range item.ClickedCh {
				t.copyRecent(i)
			}
		}()
	}
	t.devices = systray.AddMenuItem("Devices", "")
	t.pair = systray.AddMenuItem("Pair a device…", "Show a QR code for your phone or another computer")
	systray.AddSeparator()
	t.quit = systray.AddMenuItem("Quit tray", "clipsyncd keeps running")

	go t.loop()
	go t.events()
	go func() {
		for {
			select {
			case <-t.pause.ClickedCh:
				t.togglePause()
			case <-t.pair.ClickedCh:
				go t.startPairing()
			case <-t.quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func (t *tray) loop() {
	for {
		t.refresh()
		time.Sleep(3 * time.Second)
	}
}

func (t *tray) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var r struct {
		Status engine.Status `json:"status"`
	}
	if err := t.api.Call(ctx, "GET", "/status", nil, &r); err != nil {
		t.status.SetTitle("clipsyncd is not running")
		systray.SetTooltip("ClipSync: daemon not running")
		return
	}
	s := r.Status
	var up []string
	for _, tr := range s.Transports {
		if tr.Connected {
			up = append(up, tr.Name)
		}
	}
	state := "Offline"
	switch {
	case s.Paused:
		state = "Paused"
	case len(up) > 0:
		state = "Syncing via " + strings.Join(up, " + ")
	}
	t.status.SetTitle(fmt.Sprintf("%s · %d devices", state, s.Devices))
	systray.SetTooltip("ClipSync: " + state)
	t.mu.Lock()
	t.paused = s.Paused
	t.mu.Unlock()
	if s.Paused {
		t.pause.Check()
	} else {
		t.pause.Uncheck()
	}
	t.devices.SetTitle(fmt.Sprintf("Devices (%d)", s.Devices))
	t.loadRecent(ctx)
}

func (t *tray) loadRecent(ctx context.Context) {
	var items []daemon.HistoryItem
	if err := t.api.Call(ctx, "GET", client.Path("/history", "limit", fmt.Sprint(recentSlots)), nil, &items); err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recentIDs = t.recentIDs[:0]
	for i, slot := range t.recent {
		if i >= len(items) {
			slot.Hide()
			continue
		}
		title := items[i].Preview
		if len([]rune(title)) > 48 {
			title = string([]rune(title)[:47]) + "…"
		}
		slot.SetTitle(title)
		slot.Show()
		t.recentIDs = append(t.recentIDs, items[i].ID)
	}
}

// events refreshes immediately when a clip is sent or received.
func (t *tray) events() {
	for {
		_ = t.api.Events(context.Background(), func(json.RawMessage) { t.refresh() })
		time.Sleep(3 * time.Second)
	}
}

func (t *tray) copyRecent(i int) {
	t.mu.Lock()
	if i >= len(t.recentIDs) {
		t.mu.Unlock()
		return
	}
	id := t.recentIDs[i]
	t.mu.Unlock()
	_ = t.api.Call(context.Background(), "POST", client.Path("/copy", "id", id), nil, nil)
}

func (t *tray) togglePause() {
	t.mu.Lock()
	path := "/pause"
	if t.paused {
		path = "/resume"
	}
	t.mu.Unlock()
	_ = t.api.Call(context.Background(), "POST", path, nil, nil)
	t.refresh()
}

var pairPage = template.Must(template.New("pair").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Pair a device · ClipSync</title>
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f2f4f5;color:#1e3687;font:16px/1.5 ui-sans-serif,-apple-system,"Segoe UI",sans-serif}
main{max-width:420px;padding:32px;text-align:center}
img{width:280px;height:280px;image-rendering:pixelated;background:#fff;padding:12px;border-radius:12px}
code{display:block;margin-top:16px;padding:12px;background:#e4e9ee;border-radius:8px;font-size:12px;word-break:break-all;text-align:left}
p{color:#46578f}
</style>
<main>
<h1>Pair a device</h1>
<img src="data:image/png;base64,{{.QR}}" alt="QR code for pairing">
<p>Scan with your phone’s camera. On another computer, run:</p>
<code>clipsyncd join '{{.Link}}'</code>
<p>The code works once and expires in 10 minutes. You can close this page after pairing.</p>
</main></html>`))

func (t *tray) startPairing() {
	t.pair.Disable()
	t.pair.SetTitle("Waiting for a device…")
	defer func() {
		t.pair.SetTitle("Pair a device…")
		t.pair.Enable()
	}()
	err := t.api.Stream(context.Background(), "/pair/invite", func(m map[string]json.RawMessage) error {
		if raw, ok := m["link"]; ok {
			var link string
			_ = json.Unmarshal(raw, &link)
			code, err := qr.Encode(link, qr.L)
			if err != nil {
				return err
			}
			code.Scale = 8
			f, err := os.CreateTemp("", "clipsync-pair-*.html")
			if err != nil {
				return err
			}
			defer f.Close()
			if err := pairPage.Execute(f, map[string]string{"QR": base64.StdEncoding.EncodeToString(code.PNG()), "Link": link}); err != nil {
				return err
			}
			openBrowser(f.Name())
			// The page holds the one-time code; remove it once pairing ends.
			go func(p string) { time.Sleep(10 * time.Minute); os.Remove(p) }(f.Name())
		}
		if _, ok := m["device"]; ok {
			t.refresh()
		}
		return nil
	})
	if err != nil {
		t.status.SetTitle("Pairing failed: " + err.Error())
	}
}

func openBrowser(path string) {
	url := "file://" + filepath.ToSlash(path)
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}
