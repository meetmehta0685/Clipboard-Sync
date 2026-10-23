// Package clipboard abstracts the OS clipboard behind a small interface so the
// rest of clipsync can be tested against an in-memory implementation.
package clipboard

import (
	"crypto/sha256"
	"errors"
)

// Type is the kind of content on the clipboard.
type Type string

const (
	Text  Type = "text"
	Image Type = "image" // always PNG-encoded
	File  Type = "file"  // only produced by explicit sends, never by the watcher
)

// Content is a single clipboard value.
type Content struct {
	Type Type
	Data []byte
	// Name is the file name for File content.
	Name string
	// Concealed is set when the source app marked the value as sensitive
	// (password managers set org.nspasteboard.ConcealedType on macOS or
	// ExcludeClipboardContentFromMonitorProcessing on Windows). Concealed
	// content must never leave the device.
	Concealed bool
}

// Empty reports whether there is nothing worth syncing.
func (c Content) Empty() bool { return len(c.Data) == 0 }

// Hash is a local, unkeyed fingerprint used for change detection and the
// echo-loop guard. It never goes on the wire (see protocol for the keyed hash).
func (c Content) Hash() [32]byte {
	h := sha256.New()
	h.Write([]byte(c.Type))
	h.Write([]byte{0})
	h.Write(c.Data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Clipboard reads and writes the system clipboard.
type Clipboard interface {
	// Read returns the current clipboard content. An empty Content with a nil
	// error means the clipboard holds nothing we understand.
	Read() (Content, error)
	// Write replaces the clipboard content.
	Write(Content) error
}

// ChangeCounter is implemented by clipboards that expose a cheap,
// monotonically changing counter (NSPasteboard.changeCount on macOS,
// GetClipboardSequenceNumber on Windows). The watcher uses it to avoid
// reading and hashing the full clipboard on every poll.
type ChangeCounter interface {
	ChangeCount() int64
}

// ErrUnsupported is returned when the platform has no clipboard backend.
var ErrUnsupported = errors.New("clipboard: unsupported platform")
