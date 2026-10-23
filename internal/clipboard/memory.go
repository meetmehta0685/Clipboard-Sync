package clipboard

import "sync"

// Memory is an in-process clipboard for tests and simulations.
type Memory struct {
	mu      sync.Mutex
	content Content
	count   int64
}

// NewMemory returns an empty in-memory clipboard.
func NewMemory() *Memory { return &Memory{} }

func (m *Memory) Read() (Content, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.content
	c.Data = append([]byte(nil), c.Data...)
	return c, nil
}

func (m *Memory) Write(c Content) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.Data = append([]byte(nil), c.Data...)
	m.content = c
	m.count++
	return nil
}

func (m *Memory) ChangeCount() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// SetText simulates the user copying text.
func (m *Memory) SetText(s string) { _ = m.Write(Content{Type: Text, Data: []byte(s)}) }

// Text returns the current text content, or "" if it holds something else.
func (m *Memory) Text() string {
	c, _ := m.Read()
	if c.Type != Text {
		return ""
	}
	return string(c.Data)
}
