package watcher

import (
	"testing"

	"github.com/meetmehta/clipsync/internal/clipboard"
)

func TestDetectsUserCopies(t *testing.T) {
	cb := clipboard.NewMemory()
	w := New(cb, 0, Filter{}, nil)
	if _, ok := w.Poll(); ok {
		t.Fatal("empty clipboard should not emit")
	}
	cb.SetText("hello")
	c, ok := w.Poll()
	if !ok || string(c.Data) != "hello" {
		t.Fatalf("got %q %v", c.Data, ok)
	}
	if _, ok := w.Poll(); ok {
		t.Fatal("unchanged clipboard emitted twice")
	}
}

func TestEchoLoopGuard(t *testing.T) {
	cb := clipboard.NewMemory()
	w := New(cb, 0, Filter{}, nil)

	// A clip arrives from another device and we write it.
	if err := w.WriteRemote(clipboard.Content{Type: clipboard.Text, Data: []byte("from laptop")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Poll(); ok {
		t.Fatal("our own write was detected as a new local copy (echo loop)")
	}

	// Simulate an app re-writing the same value (bumping the change count).
	cb.SetText("from laptop")
	if _, ok := w.Poll(); ok {
		t.Fatal("re-write of synced value echoed")
	}

	// The user copies something else, then deliberately copies the synced
	// value again: that one should sync.
	cb.SetText("other")
	if _, ok := w.Poll(); !ok {
		t.Fatal("user copy missed")
	}
	cb.SetText("from laptop")
	if _, ok := w.Poll(); !ok {
		t.Fatal("re-copying an old synced value should sync")
	}
}

func TestPause(t *testing.T) {
	cb := clipboard.NewMemory()
	w := New(cb, 0, Filter{}, nil)
	w.Pause(true)
	cb.SetText("private")
	if _, ok := w.Poll(); ok {
		t.Fatal("paused watcher emitted")
	}
	w.Pause(false)
	if _, ok := w.Poll(); ok {
		t.Fatal("content copied while paused should not sync on resume")
	}
}

func TestFilter(t *testing.T) {
	f := Filter{MaxBytes: 100, Images: false, Secrets: true}
	cases := []struct {
		name  string
		c     clipboard.Content
		block bool
	}{
		{"plain", text("hello world"), false},
		{"concealed", clipboard.Content{Type: clipboard.Text, Data: []byte("hunter2"), Concealed: true}, true},
		{"too big", text(string(make([]byte, 101))), true},
		{"whitespace", text("  \n\t "), true},
		{"image disabled", clipboard.Content{Type: clipboard.Image, Data: []byte{1}}, true},
		{"aws", text("key AKIAIOSFODNN7EXAMPLE here"), true},
		{"github", text("ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"), true},
		{"pem", text("-----BEGIN OPENSSH PRIVATE KEY-----"), true},
		{"card", text("4111 1111 1111 1111"), true},
		{"not a card", text("4111 1111 1111 1112"), false},
		{"phone number", text("call 555-123-4567"), false},
	}
	for _, c := range cases {
		if got := f.Check(c.c) != ""; got != c.block {
			t.Errorf("%s: blocked=%v want %v (%s)", c.name, got, c.block, f.Check(c.c))
		}
	}
}

func text(s string) clipboard.Content { return clipboard.Content{Type: clipboard.Text, Data: []byte(s)} }

func TestRemoteWriteDoesNotEatUnseenLocalCopy(t *testing.T) {
	cb := clipboard.NewMemory()
	w := New(cb, 0, Filter{}, nil)
	cb.SetText("typed just now") // the watcher has not polled yet
	if err := w.WriteRemote(clipboard.Content{Type: clipboard.Text, Data: []byte("remote")}); err != nil {
		t.Fatal(err)
	}
	if cb.Text() != "typed just now" {
		t.Fatalf("remote write clobbered the user's copy: %q", cb.Text())
	}
	select {
	case c := <-w.pending:
		if string(c.Data) != "typed just now" {
			t.Fatalf("queued %q", c.Data)
		}
	default:
		t.Fatal("local copy made before the remote write was lost")
	}
}
