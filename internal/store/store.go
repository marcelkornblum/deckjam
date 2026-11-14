package store

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store provides thread-safe persistent SQLite storage for DeckJam.
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// Open initializes SQLite database at dbPath with WAL mode and busy_timeout.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	// SQLite connection string with WAL mode and busy timeout (5000ms)
	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	db.SetMaxOpenConns(1) // modernc sqlite performs best with WAL and 1 writer or coordinated access

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return s, nil
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS guests (
		guest_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		color TEXT NOT NULL,
		last_seen INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS queue_items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		spotify_uri TEXT NOT NULL,
		track_id TEXT NOT NULL,
		raw_input TEXT NOT NULL,
		title TEXT NOT NULL,
		artist TEXT NOT NULL,
		album TEXT NOT NULL,
		thumbnail_url TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		added_by_id TEXT NOT NULL,
		added_by_name TEXT NOT NULL,
		added_by_color TEXT NOT NULL,
		status TEXT NOT NULL, -- 'playing', 'queued', 'resolving', 'played', 'skipped', 'failed'
		round_number INTEGER NOT NULL DEFAULT 1,
		votes INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_queue_status_round_votes ON queue_items(status, round_number, votes DESC, id ASC);
	CREATE INDEX IF NOT EXISTS idx_queue_added_by ON queue_items(added_by_id, status);

	CREATE TABLE IF NOT EXISTS votes (
		item_id INTEGER NOT NULL,
		guest_id TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY(item_id, guest_id),
		FOREIGN KEY(item_id) REFERENCES queue_items(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS skip_votes (
		item_id INTEGER NOT NULL,
		guest_id TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY(item_id, guest_id),
		FOREIGN KEY(item_id) REFERENCES queue_items(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS track_cache (
		track_id TEXT PRIMARY KEY,
		spotify_uri TEXT NOT NULL,
		title TEXT NOT NULL,
		artist TEXT NOT NULL,
		album TEXT NOT NULL,
		thumbnail_url TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		local_thumb BLOB,
		thumb_mime TEXT,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS fallback_tracks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		spotify_uri TEXT NOT NULL UNIQUE,
		track_id TEXT NOT NULL,
		title TEXT NOT NULL,
		artist TEXT NOT NULL,
		album TEXT NOT NULL,
		thumbnail_url TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		played_count INTEGER NOT NULL DEFAULT 0,
		last_played_at INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}

	// Initialize default settings if not already present
	defaults := map[string]string{
		"room_name":           "Steam Deck Jam",
		"queue_mode":          "round_robin",
		"skip_vote_threshold": "3",
		"autoplay_fallback":   "1",
		"player_backend":      "auto",
	}

	for k, v := range defaults {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`, k, v)
		if err != nil {
			return err
		}
	}

	// Generate persistent 4-digit host PIN if not set
	var hostPin string
	err = s.db.QueryRow(`SELECT value FROM settings WHERE key = 'host_pin'`).Scan(&hostPin)
	if err == sql.ErrNoRows {
		n, _ := rand.Int(rand.Reader, big.NewInt(9000))
		pin := fmt.Sprintf("%04d", n.Int64()+1000)
		_, err = s.db.Exec(`INSERT INTO settings (key, value) VALUES ('host_pin', ?)`, pin)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetSettings retrieves current room settings.
func (s *Store) GetSettings() (Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var st Settings
	st.RoomName = "Steam Deck Jam"
	st.QueueMode = "round_robin"
	st.SkipVoteThreshold = 3
	st.AutoplayFallback = true
	st.PlayerBackend = "auto"

	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return st, err
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		switch k {
		case "room_name":
			st.RoomName = v
		case "queue_mode":
			st.QueueMode = v
		case "skip_vote_threshold":
			fmt.Sscanf(v, "%d", &st.SkipVoteThreshold)
		case "autoplay_fallback":
			st.AutoplayFallback = (v == "1" || v == "true")
		case "player_backend":
			st.PlayerBackend = v
		case "host_pin":
			st.HostPIN = v
		}
	}
	return st, nil
}

// UpdateSettings updates room settings in database.
func (s *Store) UpdateSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if st.RoomName != "" {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('room_name', ?)`, st.RoomName); err != nil {
			return err
		}
	}
	if st.QueueMode != "" {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('queue_mode', ?)`, st.QueueMode); err != nil {
			return err
		}
	}
	if st.SkipVoteThreshold > 0 {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('skip_vote_threshold', ?)`, fmt.Sprintf("%d", st.SkipVoteThreshold)); err != nil {
			return err
		}
	}
	autoFallbackVal := "0"
	if st.AutoplayFallback {
		autoFallbackVal = "1"
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('autoplay_fallback', ?)`, autoFallbackVal); err != nil {
		return err
	}
	if st.PlayerBackend != "" {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('player_backend', ?)`, st.PlayerBackend); err != nil {
			return err
		}
	}
	if st.HostPIN != "" {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('host_pin', ?)`, st.HostPIN); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetHostPIN returns the configured or generated host PIN.
func (s *Store) GetHostPIN() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var pin string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = 'host_pin'`).Scan(&pin)
	return pin, err
}

// SetHostPIN sets the host PIN.
func (s *Store) SetHostPIN(pin string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('host_pin', ?)`, pin)
	return err
}

// UpsertGuest creates or updates a guest record.
func (s *Store) UpsertGuest(g Guest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	if g.CreatedAt == 0 {
		g.CreatedAt = now
	}
	g.LastSeen = now

	_, err := s.db.Exec(`
		INSERT INTO guests (guest_id, name, color, last_seen, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(guest_id) DO UPDATE SET
			name = excluded.name,
			color = excluded.color,
			last_seen = excluded.last_seen
	`, g.GuestID, g.Name, g.Color, g.LastSeen, g.CreatedAt)
	return err
}

// TouchGuest updates guest's last_seen timestamp.
func (s *Store) TouchGuest(guestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec(`UPDATE guests SET last_seen = ? WHERE guest_id = ?`, time.Now().Unix(), guestID)
}

// GetGuest retrieves guest info by ID.
func (s *Store) GetGuest(guestID string) (*Guest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var g Guest
	err := s.db.QueryRow(`SELECT guest_id, name, color, last_seen, created_at FROM guests WHERE guest_id = ?`, guestID).
		Scan(&g.GuestID, &g.Name, &g.Color, &g.LastSeen, &g.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// CountOnlineGuests returns number of guests active within the last 5 minutes.
func (s *Store) CountOnlineGuests(activeDuration time.Duration) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	since := time.Now().Add(-activeDuration).Unix()
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM guests WHERE last_seen >= ?`, since).Scan(&count)
	return count, err
}
