// Package store keeps clip history in SQLite. Clip contents are encrypted at
// rest with a device-local key, so the database file alone reveals only
// metadata (sizes, types, timestamps).
package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo needed

	"github.com/meetmehta/clipsync/internal/crypto"
)

// Clip is one history entry.
type Clip struct {
	ID      string    `json:"id"`
	Origin  string    `json:"origin"`
	Clock   uint64    `json:"clock"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
	Local   bool      `json:"local"` // copied on this device
	Data    []byte    `json:"-"`
}

// Preview returns a short human-readable summary.
func (c Clip) Preview(n int) string {
	switch c.Type {
	case "text":
		s := strings.Join(strings.Fields(string(c.Data)), " ")
		if len([]rune(s)) > n {
			s = string([]rune(s)[:n]) + "…"
		}
		return s
	case "file":
		name, _, _ := bytes.Cut(c.Data, []byte{0})
		return fmt.Sprintf("[file %s, %d bytes]", name, c.Size)
	default:
		return fmt.Sprintf("[%s, %d bytes]", c.Type, c.Size)
	}
}

// Store is the history database.
type Store struct {
	db  *sql.DB
	key []byte
}

// Open opens (or creates) the database at path. secret is the random
// history secret from the keystore.
func Open(path string, secret []byte) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS clips (
		id      TEXT PRIMARY KEY,
		origin  TEXT NOT NULL,
		clock   INTEGER NOT NULL,
		type    TEXT NOT NULL,
		size    INTEGER NOT NULL,
		created INTEGER NOT NULL,
		local   INTEGER NOT NULL,
		nonce   BLOB NOT NULL,
		data    BLOB NOT NULL
	);
	CREATE INDEX IF NOT EXISTS clips_order ON clips(clock DESC, origin DESC);`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, key: crypto.LocalKey(secret)}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func aad(id string) []byte { return []byte("clipsync/history|" + id) }

// Add inserts a clip; duplicates (same ID) are ignored.
func (s *Store) Add(c Clip) error {
	nonce, ct, err := crypto.SealLocal(s.key, c.Data, aad(c.ID))
	if err != nil {
		return err
	}
	if c.Created.IsZero() {
		c.Created = time.Now()
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO clips (id, origin, clock, type, size, created, local, nonce, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Origin, int64(c.Clock), c.Type, c.Size, c.Created.UnixMilli(), c.Local, nonce, ct)
	return err
}

// Get loads one clip by ID.
func (s *Store) Get(id string) (Clip, error) {
	rows, err := s.db.Query(`SELECT id, origin, clock, type, size, created, local, nonce, data FROM clips WHERE id = ?`, id)
	if err != nil {
		return Clip{}, err
	}
	clips, err := s.scan(rows, "", 1)
	if err != nil {
		return Clip{}, err
	}
	if len(clips) == 0 {
		return Clip{}, sql.ErrNoRows
	}
	return clips[0], nil
}

// List returns up to limit clips, newest first. A non-empty query keeps only
// text clips containing it (case-insensitive). Contents are encrypted, so the
// search runs after decryption rather than in SQL.
func (s *Store) List(limit int, query string) ([]Clip, error) {
	rows, err := s.db.Query(`SELECT id, origin, clock, type, size, created, local, nonce, data FROM clips ORDER BY clock DESC, origin DESC`)
	if err != nil {
		return nil, err
	}
	return s.scan(rows, strings.ToLower(query), limit)
}

func (s *Store) scan(rows *sql.Rows, query string, limit int) ([]Clip, error) {
	defer rows.Close()
	var out []Clip
	for rows.Next() && (limit <= 0 || len(out) < limit) {
		var c Clip
		var clock, created int64
		var nonce, ct []byte
		if err := rows.Scan(&c.ID, &c.Origin, &clock, &c.Type, &c.Size, &created, &c.Local, &nonce, &ct); err != nil {
			return nil, err
		}
		pt, err := crypto.OpenLocal(s.key, nonce, ct, aad(c.ID))
		if err != nil {
			// A different history key (e.g. keychain reset): skip, don't fail.
			continue
		}
		c.Clock, c.Created, c.Data = uint64(clock), time.UnixMilli(created), pt
		if query != "" && (c.Type != "text" || !strings.Contains(strings.ToLower(string(pt)), query)) {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MaxClock returns the highest clock in history.
func (s *Store) MaxClock() (uint64, error) {
	var c sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(clock) FROM clips`).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) || !c.Valid {
		return 0, nil
	}
	return uint64(c.Int64), err
}

// Prune deletes clips older than ttl (if > 0) and keeps at most limit.
func (s *Store) Prune(limit int, ttl time.Duration) (int64, error) {
	var n int64
	if ttl > 0 {
		r, err := s.db.Exec(`DELETE FROM clips WHERE created < ?`, time.Now().Add(-ttl).UnixMilli())
		if err != nil {
			return 0, err
		}
		d, _ := r.RowsAffected()
		n += d
	}
	if limit > 0 {
		r, err := s.db.Exec(`DELETE FROM clips WHERE id NOT IN (SELECT id FROM clips ORDER BY clock DESC, origin DESC LIMIT ?)`, limit)
		if err != nil {
			return n, err
		}
		d, _ := r.RowsAffected()
		n += d
	}
	return n, nil
}

// Clear deletes all history.
func (s *Store) Clear() error {
	_, err := s.db.Exec(`DELETE FROM clips`)
	return err
}
