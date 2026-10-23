package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/meetmehta/clipsync/internal/clipboard"
	"github.com/meetmehta/clipsync/internal/config"
	"github.com/meetmehta/clipsync/internal/pairing"
)

// HistoryItem is a history entry as returned by the API.
type HistoryItem struct {
	ID      string    `json:"id"`
	Origin  string    `json:"origin"`
	Device  string    `json:"device"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
	Local   bool      `json:"local"`
	Preview string    `json:"preview"`
}

// StatusResponse is GET /status.
type StatusResponse struct {
	Status   any    `json:"status"`
	RelayURL string `json:"relay_url"`
	LAN      bool   `json:"lan"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// Handler returns the local API.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		s := d.St.Settings()
		writeJSON(w, StatusResponse{Status: d.Engine.Status(), RelayURL: s.RelayURL, LAN: s.LAN})
	})

	mux.HandleFunc("GET /history", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 20
		}
		clips, err := d.History.List(limit, r.URL.Query().Get("q"))
		if err != nil {
			httpError(w, 500, err)
			return
		}
		names := map[string]string{}
		for _, dev := range d.St.Devices() {
			names[dev.ID] = dev.Name
		}
		items := make([]HistoryItem, 0, len(clips))
		for _, c := range clips {
			items = append(items, HistoryItem{ID: c.ID, Origin: c.Origin, Device: names[c.Origin], Type: c.Type,
				Size: c.Size, Created: c.Created, Local: c.Local, Preview: c.Preview(120)})
		}
		writeJSON(w, items)
	})

	mux.HandleFunc("POST /copy", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Engine.Copy(r.URL.Query().Get("id")); err != nil {
			httpError(w, 404, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /clear", func(w http.ResponseWriter, r *http.Request) {
		if err := d.History.Clear(); err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /pause", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.Pause(true)
		writeJSON(w, map[string]bool{"paused": true})
	})
	mux.HandleFunc("POST /resume", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.Pause(false)
		writeJSON(w, map[string]bool{"paused": false})
	})

	mux.HandleFunc("GET /devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.St.Devices())
	})

	mux.HandleFunc("POST /devices/remove", func(w http.ResponseWriter, r *http.Request) {
		if err := d.RemoveDevice(r.Context(), r.URL.Query().Get("id")); err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, d.St.Settings().MaxClipBytes+1))
		if err != nil {
			httpError(w, 400, err)
			return
		}
		c := clipboard.Content{Type: clipboard.Text, Data: body}
		if name := r.URL.Query().Get("name"); name != "" {
			c = clipboard.Content{Type: clipboard.File, Name: name, Data: body}
		}
		// Use the daemon's context: chunks keep sending after this request ends.
		id, err := d.Engine.Publish(d.runCtx(), c)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, map[string]string{"id": id})
	})

	// POST /pair/invite streams two NDJSON lines: the invite link, then the
	// device that joined (or an error).
	mux.HandleFunc("POST /pair/invite", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		host, err := pairing.NewHost(ctx, d.St)
		if err != nil {
			httpError(w, 502, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		_ = enc.Encode(map[string]string{"link": host.Invite.Link()})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		dev, err := host.Wait(ctx)
		if err != nil {
			_ = enc.Encode(map[string]string{"error": err.Error()})
			return
		}
		if err := d.Engine.AnnounceDevices(d.runCtx()); err != nil {
			d.Log.Warn("announce devices failed", "err", err)
		}
		_ = enc.Encode(map[string]config.Device{"device": dev})
	})

	mux.HandleFunc("POST /pair/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Link string `json:"link"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, 400, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		inv, err := pairing.Join(ctx, d.St, req.Link, "desktop")
		if err != nil {
			httpError(w, 400, err)
			return
		}
		d.Engine.Regroup()
		writeJSON(w, map[string]any{"ok": true, "inviter": inv.Name, "devices": len(d.St.Devices())})
	})

	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.St.File())
	})

	mux.HandleFunc("POST /config", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Key, Value string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, 400, err)
			return
		}
		var setErr error
		if err := d.St.Update(func(f *config.File) { setErr = config.Set(f, req.Key, req.Value) }); err != nil {
			httpError(w, 500, err)
			return
		}
		if setErr != nil {
			httpError(w, 400, setErr)
			return
		}
		if f, err := FilterFrom(d.St.Settings()); err == nil {
			d.Watcher.SetFilter(f)
		}
		switch req.Key {
		case "relay_url", "lan", "lan_port", "name":
			d.Engine.Regroup() // restart transports with the new settings
		}
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /leave", func(w http.ResponseWriter, r *http.Request) {
		if err := d.St.LeaveGroup(); err != nil {
			httpError(w, 500, err)
			return
		}
		d.Engine.Regroup()
		writeJSON(w, map[string]bool{"ok": true})
	})

	// GET /events is a Server-Sent Events stream of engine events.
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			httpError(w, 500, fmt.Errorf("streaming unsupported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ch, cancel := d.Engine.Subscribe()
		defer cancel()
		fl.Flush()
		for {
			select {
			case ev := <-ch:
				b, _ := json.Marshal(ev)
				fmt.Fprintf(w, "data: %s\n\n", b)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	return mux
}

func (d *Daemon) runCtx() context.Context {
	if d.ctx != nil {
		return d.ctx
	}
	return context.Background()
}
