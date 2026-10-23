// Package watcher polls the clipboard, filters what it finds and prevents the
// echo loop where a synced clip is detected as a new local copy.
package watcher

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meetmehta0685/Clipboard-Sync/internal/clipboard"
)

// Watcher turns clipboard changes into a stream of local clips.
type Watcher struct {
	cb       clipboard.Clipboard
	interval time.Duration
	log      *slog.Logger

	// ioMu serialises Poll and WriteRemote so a poll that read an older
	// value can never be evaluated after a newer remote write.
	ioMu sync.Mutex

	mu sync.Mutex
	// filter is read on every change and can be swapped at runtime.
	filter Filter
	// lastHash is the content we last observed or wrote.
	lastHash [32]byte
	// lastWritten is what sync last wrote. Seeing it again is our own echo,
	// not the user copying. It is cleared as soon as the user copies
	// something else, so re-copying an old synced value still works.
	lastWritten [32]byte
	lastCount   int64

	// pending holds a local copy discovered just before a remote write
	// would have overwritten it.
	pending chan clipboard.Content

	paused  atomic.Bool
	skipped atomic.Int64
}

// New creates a watcher. interval defaults to 300ms.
func New(cb clipboard.Clipboard, interval time.Duration, f Filter, log *slog.Logger) *Watcher {
	if interval <= 0 {
		interval = 300 * time.Millisecond
	}
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{cb: cb, interval: interval, filter: f, log: log, lastCount: -1, pending: make(chan clipboard.Content, 4)}
}

// SetFilter replaces the filter.
func (w *Watcher) SetFilter(f Filter) { w.mu.Lock(); w.filter = f; w.mu.Unlock() }

// Pause stops emitting local clips (remote clips are still written).
func (w *Watcher) Pause(p bool) { w.paused.Store(p) }

// Paused reports whether sync is paused.
func (w *Watcher) Paused() bool { return w.paused.Load() }

// Skipped counts clips the filter blocked.
func (w *Watcher) Skipped() int64 { return w.skipped.Load() }

// Run polls until ctx is cancelled, sending each new local clip to out. The
// content already on the clipboard at start-up is not sent.
func (w *Watcher) Run(ctx context.Context, out chan<- clipboard.Content) error {
	if c, err := w.cb.Read(); err == nil {
		w.mu.Lock()
		w.lastHash = c.Hash()
		w.mu.Unlock()
	}
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		var c clipboard.Content
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c = <-w.pending:
		case <-t.C:
			var ok bool
			if c, ok = w.Poll(); !ok {
				continue
			}
		}
		select {
		case out <- c:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Poll checks the clipboard once. It is exported so tests can drive the
// watcher deterministically.
func (w *Watcher) Poll() (clipboard.Content, bool) {
	w.ioMu.Lock()
	defer w.ioMu.Unlock()
	return w.pollLocked()
}

func (w *Watcher) pollLocked() (clipboard.Content, bool) {
	if cc, ok := w.cb.(clipboard.ChangeCounter); ok {
		n := cc.ChangeCount()
		w.mu.Lock()
		same := n == w.lastCount
		w.lastCount = n
		w.mu.Unlock()
		if same {
			return clipboard.Content{}, false
		}
	}
	c, err := w.cb.Read()
	if err != nil {
		w.log.Debug("clipboard read failed", "err", err)
		return clipboard.Content{}, false
	}
	h := c.Hash()

	w.mu.Lock()
	if h == w.lastHash {
		w.mu.Unlock()
		return clipboard.Content{}, false
	}
	w.lastHash = h
	if h == w.lastWritten {
		// Our own write coming back around: the echo loop.
		w.mu.Unlock()
		return clipboard.Content{}, false
	}
	w.lastWritten = [32]byte{}
	filter := w.filter
	w.mu.Unlock()

	if w.paused.Load() {
		return clipboard.Content{}, false
	}
	if reason := filter.Check(c); reason != "" {
		if reason != "empty" {
			w.skipped.Add(1)
			w.log.Info("not syncing clip", "reason", reason, "type", c.Type)
		}
		return clipboard.Content{}, false
	}
	return c, true
}

// Restore makes sure a local clip that was just published (and is therefore
// the newest clip in the group) is on the clipboard. A remote clip can be
// written between the watcher emitting c and the engine publishing it; in
// that case c is put back. A newer unseen user copy takes precedence.
func (w *Watcher) Restore(c clipboard.Content) error {
	w.ioMu.Lock()
	defer w.ioMu.Unlock()
	if local, ok := w.pollLocked(); ok {
		select {
		case w.pending <- local:
		default:
		}
		return nil
	}
	h := c.Hash()
	w.mu.Lock()
	same := w.lastHash == h
	w.mu.Unlock()
	if same {
		return nil
	}
	return w.writeLocked(c, h)
}

// WriteRemote puts a synced clip on the clipboard and arms the loop guard.
//
// If the user copied something since the last poll, the remote write is
// skipped and the local copy is queued for sending instead. It will be
// published with a higher Lamport clock, so every device converges on it;
// writing the remote value would have destroyed the user's copy unseen.
func (w *Watcher) WriteRemote(c clipboard.Content) error {
	w.ioMu.Lock()
	defer w.ioMu.Unlock()
	if local, ok := w.pollLocked(); ok {
		select {
		case w.pending <- local:
		default:
		}
		return nil
	}
	return w.writeLocked(c, c.Hash())
}

func (w *Watcher) writeLocked(c clipboard.Content, h [32]byte) error {
	w.mu.Lock()
	w.lastWritten = h
	w.lastHash = h
	w.mu.Unlock()
	if err := w.cb.Write(c); err != nil {
		return err
	}
	if cc, ok := w.cb.(clipboard.ChangeCounter); ok {
		w.mu.Lock()
		w.lastCount = cc.ChangeCount()
		w.mu.Unlock()
	}
	return nil
}
