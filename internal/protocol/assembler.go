package protocol

import (
	"sync"
	"time"
)

// partial is a clip whose chunks are still arriving.
type partial struct {
	env      *Envelope
	chunks   [][]byte
	got      int
	progress time.Time
	asked    int
}

// Assembler reassembles chunked clips. Callers decrypt each chunk before
// handing it over, so the assembler only ever sees verified plaintext.
type Assembler struct {
	mu      sync.Mutex
	parts   map[string]*partial
	maxSize int64
}

// NewAssembler limits the total size of a single clip.
func NewAssembler(maxSize int64) *Assembler {
	return &Assembler{parts: make(map[string]*partial), maxSize: maxSize}
}

// Start registers a clip header and its first chunk. It returns the full
// plaintext immediately for single-chunk clips.
func (a *Assembler) Start(env *Envelope, first []byte) (complete []byte, ok bool) {
	if env.Chunks <= 1 {
		return first, true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.parts[env.ID]; exists {
		return nil, false
	}
	p := &partial{env: env, chunks: make([][]byte, env.Chunks), got: 1, progress: time.Now()}
	p.chunks[0] = first
	a.parts[env.ID] = p
	return nil, false
}

// Header returns the envelope of an in-progress clip.
func (a *Assembler) Header(id string) *Envelope {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.parts[id]; p != nil {
		return p.env
	}
	return nil
}

// Add stores chunk index of clip id and returns the plaintext once all chunks
// are present.
func (a *Assembler) Add(id string, index int, data []byte) (env *Envelope, complete []byte, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.parts[id]
	if p == nil || index <= 0 || index >= len(p.chunks) || p.chunks[index] != nil {
		return nil, nil, false
	}
	p.chunks[index] = data
	p.got++
	p.progress = time.Now()
	if p.got < len(p.chunks) {
		return nil, nil, false
	}
	delete(a.parts, id)
	size := 0
	for _, c := range p.chunks {
		size += len(c)
	}
	if int64(size) > a.maxSize {
		return nil, nil, false
	}
	out := make([]byte, 0, size)
	for _, c := range p.chunks {
		out = append(out, c...)
	}
	return p.env, out, true
}

// Stalled is a clip that stopped making progress.
type Stalled struct {
	Env     *Envelope
	Missing []int
}

// Sweep returns clips idle for longer than idle so the caller can request
// their missing chunks, and drops clips that have been asked for too often.
func (a *Assembler) Sweep(idle time.Duration, maxAsks int) []Stalled {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Stalled
	now := time.Now()
	for id, p := range a.parts {
		if now.Sub(p.progress) < idle {
			continue
		}
		if p.asked >= maxAsks {
			delete(a.parts, id)
			continue
		}
		p.asked++
		p.progress = now
		var missing []int
		for i, c := range p.chunks {
			if c == nil {
				missing = append(missing, i)
			}
		}
		out = append(out, Stalled{Env: p.env, Missing: missing})
	}
	return out
}

// Pending reports how many clips are being assembled.
func (a *Assembler) Pending() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.parts)
}
