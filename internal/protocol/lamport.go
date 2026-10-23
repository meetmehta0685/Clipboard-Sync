package protocol

import "sync"

// Lamport is a thread-safe Lamport clock.
type Lamport struct {
	mu sync.Mutex
	t  uint64
}

// NewLamport starts a clock at t (e.g. restored from history).
func NewLamport(t uint64) *Lamport { return &Lamport{t: t} }

// Tick advances the clock for a local event and returns the new time.
func (l *Lamport) Tick() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.t++
	return l.t
}

// Observe merges a remote timestamp so the next Tick is later than it.
func (l *Lamport) Observe(remote uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if remote > l.t {
		l.t = remote
	}
}

// Now returns the current time without advancing it.
func (l *Lamport) Now() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.t
}

// Stamp is a total order over clips: Lamport time, then origin device ID.
type Stamp struct {
	Clock  uint64
	Origin string
}

// After reports whether s wins over o. The zero Stamp loses to everything.
func (s Stamp) After(o Stamp) bool {
	if s.Clock != o.Clock {
		return s.Clock > o.Clock
	}
	return s.Origin > o.Origin
}
