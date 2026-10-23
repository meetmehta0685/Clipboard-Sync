package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	s, err := Open(path, []byte("secret-secret-secret-secret-1234"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, txt := range []string{"alpha", "Bravo charlie", "delta"} {
		if err := s.Add(Clip{ID: fmt.Sprintf("id-%d", i), Origin: "a", Clock: uint64(i + 1), Type: "text", Size: int64(len(txt)), Data: []byte(txt)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Add(Clip{ID: "id-0", Clock: 99, Type: "text", Data: []byte("dup")}) // ignored

	all, _ := s.List(0, "")
	if len(all) != 3 || all[0].ID != "id-2" {
		t.Fatalf("list order wrong: %+v", all)
	}
	hits, _ := s.List(0, "CHARLIE")
	if len(hits) != 1 || hits[0].ID != "id-1" {
		t.Fatalf("search failed: %+v", hits)
	}
	if c, _ := s.MaxClock(); c != 3 {
		t.Fatalf("max clock %d", c)
	}

	// Contents are encrypted at rest.
	raw, _ := os.ReadFile(path)
	wal, _ := os.ReadFile(path + "-wal")
	if bytes.Contains(append(raw, wal...), []byte("Bravo charlie")) {
		t.Fatal("plaintext found in database file")
	}

	if n, _ := s.Prune(2, 0); n != 1 {
		t.Fatalf("prune removed %d", n)
	}
	time.Sleep(5 * time.Millisecond)
	if n, _ := s.Prune(0, time.Millisecond); n != 2 {
		t.Fatalf("ttl prune removed %d", n)
	}
}

func TestWrongKeySkipsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	s, _ := Open(path, []byte("key-one"))
	_ = s.Add(Clip{ID: "x", Type: "text", Data: []byte("hi")})
	s.Close()
	s2, _ := Open(path, []byte("key-two"))
	defer s2.Close()
	if l, err := s2.List(0, ""); err != nil || len(l) != 0 {
		t.Fatalf("expected unreadable rows to be skipped, got %v %v", l, err)
	}
}
