package store

import (
	"database/sql"
	"fmt"
	"time"
)

// SaveTrackCache stores track metadata and optional cover thumbnail into cache.
func (s *Store) SaveTrackCache(m *TrackMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	_, err := s.db.Exec(`
		INSERT INTO track_cache (
			track_id, spotify_uri, title, artist, album, thumbnail_url,
			duration_ms, local_thumb, thumb_mime, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(track_id) DO UPDATE SET
			spotify_uri = excluded.spotify_uri,
			title = excluded.title,
			artist = excluded.artist,
			album = excluded.album,
			thumbnail_url = excluded.thumbnail_url,
			duration_ms = excluded.duration_ms,
			local_thumb = COALESCE(excluded.local_thumb, track_cache.local_thumb),
			thumb_mime = COALESCE(excluded.thumb_mime, track_cache.thumb_mime)
	`,
		m.TrackID, m.SpotifyURI, m.Title, m.Artist, m.Album, m.ThumbnailURL,
		m.DurationMS, m.LocalThumb, m.ThumbMIME, now,
	)
	return err
}

// SaveTrackThumb updates only the thumbnail bytes for a track in track_cache.
func (s *Store) SaveTrackThumb(trackID string, thumb []byte, mimeType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE track_cache SET local_thumb = ?, thumb_mime = ? WHERE track_id = ?
	`, thumb, mimeType, trackID)
	return err
}

// GetTrackCache retrieves cached track metadata by track_id.
func (s *Store) GetTrackCache(trackID string) (*TrackMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var m TrackMetadata
	var localThumb []byte
	var thumbMIME sql.NullString

	err := s.db.QueryRow(`
		SELECT track_id, spotify_uri, title, artist, album, thumbnail_url, duration_ms, local_thumb, thumb_mime
		FROM track_cache WHERE track_id = ?
	`, trackID).Scan(
		&m.TrackID, &m.SpotifyURI, &m.Title, &m.Artist, &m.Album, &m.ThumbnailURL,
		&m.DurationMS, &localThumb, &thumbMIME,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	m.LocalThumb = localThumb
	if thumbMIME.Valid {
		m.ThumbMIME = thumbMIME.String
	}
	return &m, nil
}

// AddFallbackTrack adds a track to the fallback pool.
func (s *Store) AddFallbackTrack(t *FallbackTrack) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	if t.CreatedAt == 0 {
		t.CreatedAt = now
	}

	_, err := s.db.Exec(`
		INSERT INTO fallback_tracks (
			spotify_uri, track_id, title, artist, album, thumbnail_url,
			duration_ms, played_count, last_played_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(spotify_uri) DO UPDATE SET
			title = excluded.title,
			artist = excluded.artist,
			album = excluded.album,
			thumbnail_url = excluded.thumbnail_url,
			duration_ms = excluded.duration_ms
	`,
		t.SpotifyURI, t.TrackID, t.Title, t.Artist, t.Album, t.ThumbnailURL,
		t.DurationMS, t.PlayedCount, t.LastPlayedAt, t.CreatedAt,
	)
	return err
}

// CountFallbackTracks returns the number of tracks in the fallback pool.
func (s *Store) CountFallbackTracks() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM fallback_tracks`).Scan(&count)
	return count, err
}

// ClearFallbackTracks empties the fallback tracks pool.
func (s *Store) ClearFallbackTracks() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM fallback_tracks`)
	return err
}

// PickFallbackTrack picks the least recently played track from fallback pool, updates played_count and last_played_at, and returns it.
func (s *Store) PickFallbackTrack() (*FallbackTrack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(`
		SELECT id, spotify_uri, track_id, title, artist, album, thumbnail_url,
		       duration_ms, played_count, last_played_at, created_at
		FROM fallback_tracks
		ORDER BY played_count ASC, last_played_at ASC, id ASC
		LIMIT 1
	`)

	var t FallbackTrack
	err := row.Scan(
		&t.ID, &t.SpotifyURI, &t.TrackID, &t.Title, &t.Artist, &t.Album,
		&t.ThumbnailURL, &t.DurationMS, &t.PlayedCount, &t.LastPlayedAt, &t.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	_, err = s.db.Exec(`
		UPDATE fallback_tracks
		SET played_count = played_count + 1, last_played_at = ?
		WHERE id = ?
	`, now, t.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to update fallback track played stats: %w", err)
	}

	t.PlayedCount++
	t.LastPlayedAt = now
	return &t, nil
}
