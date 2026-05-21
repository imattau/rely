package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
	_ "modernc.org/sqlite"
)

// SQLiteStore persists events and reputation in a SQLite database.
// Use ":memory:" as path for an ephemeral in-process database.
type SQLiteStore struct {
	db *sql.DB
}

var _ EventStore = (*SQLiteStore)(nil)

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = ":memory:"
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := migrateSQLite(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func migrateSQLite(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id TEXT PRIMARY KEY,
			event_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS reputation (
			pubkey TEXT PRIMARY KEY,
			score REAL NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("sqlite migrate: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) Save(e nostr.Event) {
	if s == nil || s.db == nil {
		return
	}

	data, err := json.Marshal(e)
	if err != nil {
		return
	}

	_, _ = s.db.Exec(`INSERT INTO events (id, event_json) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET event_json = excluded.event_json`, e.ID, string(data))
}

func (s *SQLiteStore) Get(id string) (nostr.Event, bool) {
	if s == nil || s.db == nil {
		return nostr.Event{}, false
	}

	var raw string
	err := s.db.QueryRow(`SELECT event_json FROM events WHERE id = ?`, id).Scan(&raw)
	if err != nil {
		return nostr.Event{}, false
	}

	var e nostr.Event
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return nostr.Event{}, false
	}
	return e, true
}

func (s *SQLiteStore) Query(filters nostr.Filters) []nostr.Event {
	if s == nil || s.db == nil {
		return nil
	}

	rows, err := s.db.Query(`SELECT event_json FROM events`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]nostr.Event, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var e nostr.Event
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			continue
		}
		if filters.Match(&e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *SQLiteStore) Delete(id string) {
	if s == nil || s.db == nil {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM events WHERE id = ?`, id)
}

func (s *SQLiteStore) SetReputation(pubkey string, score float64) {
	if s == nil || s.db == nil {
		return
	}
	_, _ = s.db.Exec(`INSERT INTO reputation (pubkey, score) VALUES (?, ?)
		ON CONFLICT(pubkey) DO UPDATE SET score = excluded.score`, pubkey, clamp(score, -1, 1))
}

func (s *SQLiteStore) GetReputation(pubkey string) float64 {
	if s == nil || s.db == nil {
		return 0
	}

	var score float64
	if err := s.db.QueryRow(`SELECT score FROM reputation WHERE pubkey = ?`, pubkey).Scan(&score); err != nil {
		return 0
	}
	return score
}

func (s *SQLiteStore) AllReputation() map[string]float64 {
	if s == nil || s.db == nil {
		return map[string]float64{}
	}

	rows, err := s.db.Query(`SELECT pubkey, score FROM reputation`)
	if err != nil {
		return map[string]float64{}
	}
	defer rows.Close()

	out := make(map[string]float64)
	for rows.Next() {
		var pubkey string
		var score float64
		if err := rows.Scan(&pubkey, &score); err != nil {
			continue
		}
		out[pubkey] = score
	}
	return out
}

func (s *SQLiteStore) MergeReputation(incoming map[string]float64, weight float64) {
	if s == nil || s.db == nil {
		return
	}
	if weight < 0 {
		weight = 0
	}
	if weight > 1 {
		weight = 1
	}

	for pubkey, incomingScore := range incoming {
		existing := s.GetReputation(pubkey)
		merged := clamp(existing*(1-weight)+incomingScore*weight, -1, 1)
		s.SetReputation(pubkey, merged)
	}
}
