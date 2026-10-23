package protocol

import (
	"container/list"
	"sync"
)

// Seen is a bounded LRU set of message IDs. A clip that arrives over both the
// LAN and the relay is processed once.
type Seen struct {
	mu    sync.Mutex
	cap   int
	order *list.List
	index map[string]*list.Element
}

// NewSeen returns a set that remembers the last capacity IDs.
func NewSeen(capacity int) *Seen {
	return &Seen{cap: capacity, order: list.New(), index: make(map[string]*list.Element)}
}

// Add records id and reports whether it was new.
func (s *Seen) Add(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.index[id]; ok {
		s.order.MoveToFront(e)
		return false
	}
	s.index[id] = s.order.PushFront(id)
	for s.order.Len() > s.cap {
		last := s.order.Back()
		s.order.Remove(last)
		delete(s.index, last.Value.(string))
	}
	return true
}

// Has reports whether id is in the set without touching recency.
func (s *Seen) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.index[id]
	return ok
}
