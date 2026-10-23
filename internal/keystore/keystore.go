// Package keystore stores secrets (identity key, group key, history key) in
// the OS keychain, falling back to a 0600 file where no keychain exists
// (headless Linux, CI).
package keystore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// ErrNotFound is returned for a missing secret.
var ErrNotFound = errors.New("keystore: not found")

// Store is a tiny secret store.
type Store interface {
	Get(name string) ([]byte, error)
	Set(name string, value []byte) error
	Delete(name string) error
}

// Keyring uses macOS Keychain, Windows Credential Manager or the Secret
// Service on Linux. Each profile gets its own service name so several
// daemons can run on one machine during development.
type Keyring struct{ Service string }

func (k Keyring) Get(name string) ([]byte, error) {
	s, err := keyring.Get(k.Service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(s)
}

func (k Keyring) Set(name string, v []byte) error {
	return keyring.Set(k.Service, name, base64.StdEncoding.EncodeToString(v))
}

func (k Keyring) Delete(name string) error {
	err := keyring.Delete(k.Service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// File keeps secrets in a JSON file readable only by the current user.
type File struct {
	Path string
	mu   sync.Mutex
}

func (f *File) load() (map[string][]byte, error) {
	m := map[string][]byte{}
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (f *File) save(m map[string][]byte) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

func (f *File) Get(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return nil, err
	}
	v, ok := m[name]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (f *File) Set(name string, v []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[name] = v
	return f.save(m)
}

func (f *File) Delete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, name)
	return f.save(m)
}

// Memory is an in-memory store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *Memory) Get(name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[name]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (s *Memory) Set(name string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[name] = append([]byte(nil), v...)
	return nil
}

func (s *Memory) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
	return nil
}

// Open returns the OS keychain if it works, otherwise a file store at
// fallbackPath.
func Open(service, fallbackPath string) Store {
	k := Keyring{Service: service}
	if _, err := k.Get("__probe__"); err == nil || errors.Is(err, ErrNotFound) {
		return k
	}
	return &File{Path: fallbackPath}
}
