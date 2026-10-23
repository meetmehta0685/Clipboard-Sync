// Package config persists a device's settings and group membership. Secrets
// live in the keystore; this file only holds public data.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/meetmehta/clipsync/internal/crypto"
	"github.com/meetmehta/clipsync/internal/keystore"
)

// Device is a member of the sync group.
type Device struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Pub   []byte    `json:"pub"`
	Kind  string    `json:"kind"` // "desktop" | "phone"
	Added time.Time `json:"added"`
}

// Settings are user-tunable options.
type Settings struct {
	RelayURL      string   `json:"relay_url"`
	LAN           bool     `json:"lan"`
	LANPort       int      `json:"lan_port"` // 0 = random
	MaxClipBytes  int64    `json:"max_clip_bytes"`
	SyncImages    bool     `json:"sync_images"`
	FilterSecrets bool     `json:"filter_secrets"` // regex filter for card numbers, API keys...
	ExtraFilters  []string `json:"extra_filters,omitempty"`
	HistoryLimit  int      `json:"history_limit"`
	HistoryTTL    Duration `json:"history_ttl"`
	PollInterval  Duration `json:"poll_interval"`
	DownloadDir   string   `json:"download_dir"`
}

// Defaults returns sensible settings.
func Defaults() Settings {
	home, _ := os.UserHomeDir()
	return Settings{
		RelayURL:      "ws://localhost:8787/ws",
		LAN:           true,
		MaxClipBytes:  25 << 20,
		SyncImages:    true,
		FilterSecrets: true,
		HistoryLimit:  500,
		HistoryTTL:    Duration(7 * 24 * time.Hour),
		PollInterval:  Duration(300 * time.Millisecond),
		DownloadDir:   filepath.Join(home, "Downloads", "ClipSync"),
	}
}

// File is the on-disk config.json.
type File struct {
	DeviceID string   `json:"device_id"`
	Name     string   `json:"name"`
	Settings Settings `json:"settings"`
	Epoch    uint32   `json:"epoch"`
	Devices  []Device `json:"devices"`
	// LastClock is the highest Lamport time seen, restored on restart.
	LastClock uint64 `json:"last_clock"`
	// RelayGen/RelaySeq is the relay catch-up cursor for the current room.
	RelayGen string `json:"relay_gen,omitempty"`
	RelaySeq uint64 `json:"relay_seq,omitempty"`
}

const (
	secretIdentity = "identity"
	secretGroup    = "group-key"
	secretHistory  = "history-secret"
)

// State is the loaded config plus secrets. Methods are safe for concurrent use.
type State struct {
	Dir  string
	keys keystore.Store

	mu       sync.RWMutex
	file     File
	identity *crypto.Identity
	group    *crypto.GroupKey
}

// DefaultDir is ~/.config/clipsync (or the OS equivalent). CLIPSYNC_HOME
// overrides it, which is how you run two daemons on one machine.
func DefaultDir() string {
	if d := os.Getenv("CLIPSYNC_HOME"); d != "" {
		return d
	}
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "clipsync")
}

// Load opens or initialises the state in dir. A brand-new device gets an
// identity and a solo group, so it can be paired either way afterwards.
func Load(dir string, keys keystore.Store) (*State, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &State{Dir: dir, keys: keys}
	b, err := os.ReadFile(s.path())
	switch {
	case errors.Is(err, os.ErrNotExist):
		host, _ := os.Hostname()
		s.file = File{DeviceID: uuid.NewString(), Name: host, Settings: Defaults()}
	case err != nil:
		return nil, err
	default:
		s.file.Settings = Defaults()
		if err := json.Unmarshal(b, &s.file); err != nil {
			return nil, fmt.Errorf("config: %s: %w", s.path(), err)
		}
	}

	if raw, err := keys.Get(secretIdentity); err == nil {
		if s.identity, err = crypto.IdentityFromBytes(raw); err != nil {
			return nil, err
		}
	} else if errors.Is(err, keystore.ErrNotFound) {
		if s.identity, err = crypto.NewIdentity(); err != nil {
			return nil, err
		}
		if err := keys.Set(secretIdentity, s.identity.Bytes()); err != nil {
			return nil, fmt.Errorf("config: store identity: %w", err)
		}
	} else {
		return nil, err
	}

	if raw, err := keys.Get(secretGroup); err == nil && len(raw) == crypto.KeySize {
		s.group = &crypto.GroupKey{Epoch: s.file.Epoch}
		copy(s.group.Key[:], raw)
	} else if err == nil || errors.Is(err, keystore.ErrNotFound) {
		if err := s.resetGroupLocked(); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	if _, err := keys.Get(secretHistory); errors.Is(err, keystore.ErrNotFound) {
		if err := keys.Set(secretHistory, crypto.RandomBytes(32)); err != nil {
			return nil, err
		}
	}

	s.ensureSelfLocked()
	return s, s.saveLocked()
}

func (s *State) path() string { return filepath.Join(s.Dir, "config.json") }

func (s *State) resetGroupLocked() error {
	g, err := crypto.NewGroupKey(1)
	if err != nil {
		return err
	}
	if err := s.keys.Set(secretGroup, g.Key[:]); err != nil {
		return fmt.Errorf("config: store group key: %w", err)
	}
	s.group = g
	s.file.Epoch = g.Epoch
	s.file.Devices = nil
	s.file.RelayGen, s.file.RelaySeq = "", 0
	return nil
}

func (s *State) ensureSelfLocked() {
	for i, d := range s.file.Devices {
		if d.ID == s.file.DeviceID {
			s.file.Devices[i].Pub = s.identity.Public()
			s.file.Devices[i].Name = s.file.Name
			return
		}
	}
	s.file.Devices = append(s.file.Devices, Device{
		ID: s.file.DeviceID, Name: s.file.Name, Pub: s.identity.Public(), Kind: "desktop", Added: time.Now().UTC(),
	})
}

func (s *State) saveLocked() error {
	b, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// DeviceID returns this device's ID.
func (s *State) DeviceID() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.file.DeviceID }

// Name returns this device's display name.
func (s *State) Name() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.file.Name }

// Identity returns the device keypair.
func (s *State) Identity() *crypto.Identity { return s.identity }

// Group returns the current group key.
func (s *State) Group() *crypto.GroupKey { s.mu.RLock(); defer s.mu.RUnlock(); return s.group }

// Settings returns a copy of the settings.
func (s *State) Settings() Settings { s.mu.RLock(); defer s.mu.RUnlock(); return s.file.Settings }

// Devices returns a copy of the device list.
func (s *State) Devices() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.file.Devices)
}

// Device looks up a member by ID.
func (s *State) Device(id string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.file.Devices {
		if d.ID == id {
			return d, true
		}
	}
	return Device{}, false
}

// DeviceByPub looks up a member by public key.
func (s *State) DeviceByPub(pub []byte) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.file.Devices {
		if bytes.Equal(d.Pub, pub) {
			return d, true
		}
	}
	return Device{}, false
}

// HistorySecret returns the random secret for at-rest history encryption.
func (s *State) HistorySecret() ([]byte, error) { return s.keys.Get(secretHistory) }

// LastClock returns the persisted Lamport high-water mark.
func (s *State) LastClock() uint64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.file.LastClock }

// SetLastClock persists the Lamport high-water mark if it grew.
func (s *State) SetLastClock(c uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c <= s.file.LastClock {
		return nil
	}
	s.file.LastClock = c
	return s.saveLocked()
}

// RelayCursor returns the relay catch-up position.
func (s *State) RelayCursor() (gen string, seq uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.file.RelayGen, s.file.RelaySeq
}

// SetRelayCursor records the last relay sequence number processed. A new
// generation resets the cursor.
func (s *State) SetRelayCursor(gen string, seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen == s.file.RelayGen && seq <= s.file.RelaySeq {
		return nil
	}
	s.file.RelayGen, s.file.RelaySeq = gen, seq
	return s.saveLocked()
}

// Update applies f to the settings and saves.
func (s *State) Update(f func(*File)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.file)
	s.ensureSelfLocked()
	return s.saveLocked()
}

// MergeDevices adds devices we didn't know about (control messages only ever
// add; removal goes through a rekey).
func (s *State) MergeDevices(devs []Device) (added int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range devs {
		if !slices.ContainsFunc(s.file.Devices, func(x Device) bool { return x.ID == d.ID }) {
			s.file.Devices = append(s.file.Devices, d)
			added++
		}
	}
	if added == 0 {
		return 0, nil
	}
	return added, s.saveLocked()
}

// JoinGroup replaces the group with one received during pairing or a rekey.
func (s *State) JoinGroup(g *crypto.GroupKey, devices []Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.keys.Set(secretGroup, g.Key[:]); err != nil {
		return err
	}
	s.group = g
	s.file.Epoch = g.Epoch
	s.file.Devices = slices.Clone(devices)
	s.file.RelayGen, s.file.RelaySeq = "", 0 // new key, new room
	s.ensureSelfLocked()
	return s.saveLocked()
}

// LeaveGroup discards the group and starts a fresh solo one.
func (s *State) LeaveGroup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.resetGroupLocked(); err != nil {
		return err
	}
	s.ensureSelfLocked()
	return s.saveLocked()
}

// Duration marshals as a Go duration string ("300ms").
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }
