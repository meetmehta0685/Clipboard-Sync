// Command relay runs the clipsync relay server. It forwards encrypted
// envelopes between devices in a room, keeps a short replay buffer for
// devices that were offline, and serves the phone PWA.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/meetmehta0685/Clipboard-Sync/internal/hub"
)

func main() {
	addr := flag.String("addr", ":8787", "listen address")
	web := flag.String("web", "web/out", "directory with the built PWA (empty to disable)")
	origins := flag.String("origins", "", "extra allowed browser origins, comma separated (e.g. localhost:3000 for next dev)")
	history := flag.Int("history", 50, "clips kept per room for offline catch-up")
	roomMB := flag.Int64("room-mb", 64, "max stored ciphertext per room, in MiB")
	totalMB := flag.Int64("total-mb", 512, "max stored ciphertext across all rooms, in MiB")
	tlsCert := flag.String("tls-cert", "", "TLS certificate (or terminate TLS in a reverse proxy)")
	tlsKey := flag.String("tls-key", "", "TLS key")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := hub.DefaultConfig()
	cfg.RoomHistory = *history
	cfg.RoomBytes = *roomMB << 20
	cfg.TotalBytes = *totalMB << 20
	if *origins != "" {
		cfg.Origins = strings.Split(*origins, ",")
	}
	h := hub.New(cfg, log)

	mux := http.NewServeMux()
	mux.Handle("/ws", h)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.Stats())
	})
	if *web != "" {
		mux.Handle("/", spa(*web))
	}

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Info("relay listening", "addr", *addr, "web", *web)
	var err error
	if *tlsCert != "" {
		err = srv.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("relay stopped", "err", err)
		os.Exit(1)
	}
}

// spa serves the static Next.js export, mapping /pair to pair.html and
// falling back to index.html for unknown routes.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		p := filepath.Clean("/" + r.URL.Path)
		// Next exports both pair.html and a pair/ directory of RSC data;
		// the page wins, served directly so FileServer doesn't redirect.
		if p != "/" {
			if page := filepath.Join(dir, p+".html"); fileExists(page) {
				http.ServeFile(w, r, page)
				return
			}
			if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
				r.URL.Path = "/"
			}
		}
		if strings.HasSuffix(r.URL.Path, "sw.js") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fs.ServeHTTP(w, r)
	})
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
