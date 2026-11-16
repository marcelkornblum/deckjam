package store

import (
	"database/sql"
	"fmt"
	"time"
)

// AddToQueue inserts a new item into the queue.
// It implements Fair Round-Robin calculation when queue_mode is "round_robin".
func (s *Store) AddToQueue(item *QueueItem, queueMode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	if item.CreatedAt == 0 {
		item.CreatedAt = now
	}
	if item.Status == "" {
		item.Status = "queued"
	}

	// Calculate round number for Fair Round-Robin
	if queueMode == "round_robin" {
		// round_number = max(max_queued_round_for_G + 1, min_queued_round_overall)
		// starting at 1 if queue empty

		var maxGuestRound sql.NullInt64
		err := s.db.QueryRow(`
			SELECT MAX(round_number) FROM queue_items 
			WHERE added_by_id = ? AND status IN ('playing', 'queued', 'resolving')
		`, item.AddedByID).Scan(&maxGuestRound)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to get max round for guest: %w", err)
		}

		var minOverallRound sql.NullInt64
		err = s.db.QueryRow(`
			SELECT MIN(round_number) FROM queue_items 
			WHERE status IN ('playing', 'queued', 'resolving')
		`).Scan(&minOverallRound)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to get min overall round: %w", err)
		}

		round := 1
		candidateGuest := 1
		if maxGuestRound.Valid {
			candidateGuest = int(maxGuestRound.Int64) + 1
		}

		candidateOverall := 1
		if minOverallRound.Valid && minOverallRound.Int64 > 1 {
			candidateOverall = int(minOverallRound.Int64)
		}

		if candidateGuest > candidateOverall {
			round = candidateGuest
		} else {
			round = candidateOverall
		}
		item.RoundNumber = round
	} else {
		item.RoundNumber = 1
	}

	res, err := s.db.Exec(`
		INSERT INTO queue_items (
			spotify_uri, track_id, raw_input, title, artist, album,
			thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
			status, round_number, votes, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		item.SpotifyURI, item.TrackID, item.RawInput, item.Title, item.Artist, item.Album,
		item.ThumbnailURL, item.DurationMS, item.AddedByID, item.AddedByName, item.AddedByColor,
		item.Status, item.RoundNumber, item.Votes, item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert queue item: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	item.ID = id
	return nil
}

// GetNowPlaying returns the currently playing queue item, if any.
func (s *Store) GetNowPlaying(guestID string) (*QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT id, spotify_uri, track_id, raw_input, title, artist, album,
		       thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
		       status, round_number, votes, created_at
		FROM queue_items
		WHERE status = 'playing'
		ORDER BY id DESC LIMIT 1
	`)

	var it QueueItem
	err := row.Scan(
		&it.ID, &it.SpotifyURI, &it.TrackID, &it.RawInput, &it.Title, &it.Artist, &it.Album,
		&it.ThumbnailURL, &it.DurationMS, &it.AddedByID, &it.AddedByName, &it.AddedByColor,
		&it.Status, &it.RoundNumber, &it.Votes, &it.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if guestID != "" {
		var cnt int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM votes WHERE item_id = ? AND guest_id = ?`, it.ID, guestID).Scan(&cnt)
		it.VotedByMe = (cnt > 0)
	}

	return &it, nil
}

// GetQueuedItems returns queued items in proper priority order for the given queue mode.
// In round_robin: ordered by round_number ASC, votes DESC, id ASC.
// In fifo: ordered by votes DESC, id ASC.
func (s *Store) GetQueuedItems(queueMode, guestID string) ([]QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var orderBy string
	if queueMode == "round_robin" {
		orderBy = "round_number ASC, votes DESC, id ASC"
	} else {
		orderBy = "votes DESC, id ASC"
	}

	query := fmt.Sprintf(`
		SELECT id, spotify_uri, track_id, raw_input, title, artist, album,
		       thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
		       status, round_number, votes, created_at
		FROM queue_items
		WHERE status IN ('queued', 'resolving')
		ORDER BY %s
	`, orderBy)

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []QueueItem
	for rows.Next() {
		var it QueueItem
		if err := rows.Scan(
			&it.ID, &it.SpotifyURI, &it.TrackID, &it.RawInput, &it.Title, &it.Artist, &it.Album,
			&it.ThumbnailURL, &it.DurationMS, &it.AddedByID, &it.AddedByName, &it.AddedByColor,
			&it.Status, &it.RoundNumber, &it.Votes, &it.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, it)
	}

	if guestID != "" && len(items) > 0 {
		for i := range items {
			var cnt int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM votes WHERE item_id = ? AND guest_id = ?`, items[i].ID, guestID).Scan(&cnt)
			items[i].VotedByMe = (cnt > 0)
		}
	}

	if items == nil {
		items = []QueueItem{}
	}

	return items, nil
}

// GetHistory returns the last 20 played or skipped items, newest first.
func (s *Store) GetHistory(guestID string) ([]QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, spotify_uri, track_id, raw_input, title, artist, album,
		       thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
		       status, round_number, votes, created_at
		FROM queue_items
		WHERE status IN ('played', 'skipped')
		ORDER BY id DESC LIMIT 20
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []QueueItem
	for rows.Next() {
		var it QueueItem
		if err := rows.Scan(
			&it.ID, &it.SpotifyURI, &it.TrackID, &it.RawInput, &it.Title, &it.Artist, &it.Album,
			&it.ThumbnailURL, &it.DurationMS, &it.AddedByID, &it.AddedByName, &it.AddedByColor,
			&it.Status, &it.RoundNumber, &it.Votes, &it.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, it)
	}

	if guestID != "" && len(items) > 0 {
		for i := range items {
			var cnt int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM votes WHERE item_id = ? AND guest_id = ?`, items[i].ID, guestID).Scan(&cnt)
			items[i].VotedByMe = (cnt > 0)
		}
	}

	if items == nil {
		items = []QueueItem{}
	}

	return items, nil
}

// PopNextTrack retrieves the next track to play according to queueMode,
// marks it as 'playing' (and any currently 'playing' track as 'played'),
// and returns the newly active QueueItem. Returns nil if queue is empty.
func (s *Store) PopNextTrack(queueMode string) (*QueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Mark any current playing tracks as played
	_, err = tx.Exec(`UPDATE queue_items SET status = 'played' WHERE status = 'playing'`)
	if err != nil {
		return nil, err
	}

	var orderBy string
	if queueMode == "round_robin" {
		orderBy = "round_number ASC, votes DESC, id ASC"
	} else {
		orderBy = "votes DESC, id ASC"
	}

	query := fmt.Sprintf(`
		SELECT id, spotify_uri, track_id, raw_input, title, artist, album,
		       thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
		       status, round_number, votes, created_at
		FROM queue_items
		WHERE status = 'queued'
		ORDER BY %s
		LIMIT 1
	`, orderBy)

	var it QueueItem
	err = tx.QueryRow(query).Scan(
		&it.ID, &it.SpotifyURI, &it.TrackID, &it.RawInput, &it.Title, &it.Artist, &it.Album,
		&it.ThumbnailURL, &it.DurationMS, &it.AddedByID, &it.AddedByName, &it.AddedByColor,
		&it.Status, &it.RoundNumber, &it.Votes, &it.CreatedAt,
	)
	if err == sql.ErrNoRows {
		// Nothing queued
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Update status to 'playing'
	_, err = tx.Exec(`UPDATE queue_items SET status = 'playing' WHERE id = ?`, it.ID)
	if err != nil {
		return nil, err
	}
	it.Status = "playing"

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &it, nil
}

// SetItemStatus updates the status of an item (e.g. "played", "skipped", "failed").
func (s *Store) SetItemStatus(itemID int64, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE queue_items SET status = ? WHERE id = ?`, status, itemID)
	return err
}

// GetItemByID returns a queue item by ID.
func (s *Store) GetItemByID(itemID int64) (*QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var it QueueItem
	err := s.db.QueryRow(`
		SELECT id, spotify_uri, track_id, raw_input, title, artist, album,
		       thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
		       status, round_number, votes, created_at
		FROM queue_items WHERE id = ?
	`, itemID).Scan(
		&it.ID, &it.SpotifyURI, &it.TrackID, &it.RawInput, &it.Title, &it.Artist, &it.Album,
		&it.ThumbnailURL, &it.DurationMS, &it.AddedByID, &it.AddedByName, &it.AddedByColor,
		&it.Status, &it.RoundNumber, &it.Votes, &it.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// RemoveItem removes a queued item by ID.
func (s *Store) RemoveItem(itemID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM queue_items WHERE id = ?`, itemID)
	return err
}

// ToggleVote toggles an upvote for guestID on itemID.
// Returns the updated total votes and whether the guest is now voting for it.
func (s *Store) ToggleVote(itemID int64, guestID string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var exists int
	err = tx.QueryRow(`SELECT COUNT(*) FROM votes WHERE item_id = ? AND guest_id = ?`, itemID, guestID).Scan(&exists)
	if err != nil {
		return 0, false, err
	}

	var voted bool
	if exists > 0 {
		_, err = tx.Exec(`DELETE FROM votes WHERE item_id = ? AND guest_id = ?`, itemID, guestID)
		if err != nil {
			return 0, false, err
		}
		voted = false
	} else {
		_, err = tx.Exec(`INSERT INTO votes (item_id, guest_id, created_at) VALUES (?, ?, ?)`, itemID, guestID, time.Now().Unix())
		if err != nil {
			return 0, false, err
		}
		voted = true
	}

	var count int
	err = tx.QueryRow(`SELECT COUNT(*) FROM votes WHERE item_id = ?`, itemID).Scan(&count)
	if err != nil {
		return 0, false, err
	}

	_, err = tx.Exec(`UPDATE queue_items SET votes = ? WHERE id = ?`, count, itemID)
	if err != nil {
		return 0, false, err
	}

	if err := tx.Commit(); err != nil {
		return 0, false, err
	}

	return count, voted, nil
}

// AddSkipVote records a skip vote for guestID on itemID.
// Returns current skip votes count and whether guest already voted.
func (s *Store) AddSkipVote(itemID int64, guestID string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var exists int
	err = tx.QueryRow(`SELECT COUNT(*) FROM skip_votes WHERE item_id = ? AND guest_id = ?`, itemID, guestID).Scan(&exists)
	if err != nil {
		return 0, false, err
	}

	alreadyVoted := exists > 0
	if !alreadyVoted {
		_, err = tx.Exec(`INSERT INTO skip_votes (item_id, guest_id, created_at) VALUES (?, ?, ?)`, itemID, guestID, time.Now().Unix())
		if err != nil {
			return 0, false, err
		}
	}

	var count int
	err = tx.QueryRow(`SELECT COUNT(*) FROM skip_votes WHERE item_id = ?`, itemID).Scan(&count)
	if err != nil {
		return 0, false, err
	}

	if err := tx.Commit(); err != nil {
		return 0, false, err
	}

	return count, alreadyVoted, nil
}

// GetSkipVotes returns the current count of skip votes for itemID and whether guestID voted.
func (s *Store) GetSkipVotes(itemID int64, guestID string) (int, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM skip_votes WHERE item_id = ?`, itemID).Scan(&count)
	if err != nil {
		return 0, false, err
	}

	var votedByMe bool
	if guestID != "" {
		var cnt int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM skip_votes WHERE item_id = ? AND guest_id = ?`, itemID, guestID).Scan(&cnt)
		votedByMe = (cnt > 0)
	}

	return count, votedByMe, nil
}

// MoveTop moves an item to the top of the queue.
func (s *Store) MoveTop(itemID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var minRound int
	var maxVotes int
	_ = s.db.QueryRow(`SELECT MIN(round_number) FROM queue_items WHERE status IN ('queued', 'resolving')`).Scan(&minRound)
	_ = s.db.QueryRow(`SELECT MAX(votes) FROM queue_items WHERE status IN ('queued', 'resolving')`).Scan(&maxVotes)

	targetRound := minRound
	if targetRound <= 0 {
		targetRound = 1
	}
	targetVotes := maxVotes + 10

	_, err := s.db.Exec(`UPDATE queue_items SET round_number = ?, votes = ? WHERE id = ?`, targetRound, targetVotes, itemID)
	return err
}

// PlayNow marks any currently playing track as played, and marks itemID as 'playing'.
func (s *Store) PlayNow(itemID int64) (*QueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`UPDATE queue_items SET status = 'played' WHERE status = 'playing'`)
	if err != nil {
		return nil, err
	}

	_, err = tx.Exec(`UPDATE queue_items SET status = 'playing' WHERE id = ?`, itemID)
	if err != nil {
		return nil, err
	}

	var it QueueItem
	err = tx.QueryRow(`
		SELECT id, spotify_uri, track_id, raw_input, title, artist, album,
		       thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
		       status, round_number, votes, created_at
		FROM queue_items WHERE id = ?
	`, itemID).Scan(
		&it.ID, &it.SpotifyURI, &it.TrackID, &it.RawInput, &it.Title, &it.Artist, &it.Album,
		&it.ThumbnailURL, &it.DurationMS, &it.AddedByID, &it.AddedByName, &it.AddedByColor,
		&it.Status, &it.RoundNumber, &it.Votes, &it.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &it, nil
}

// ClearQueue marks all currently queued or resolving items as skipped or removes them.
func (s *Store) ClearQueue() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM queue_items WHERE status IN ('queued', 'resolving')`)
	return err
}

// UpdateItemMetadata updates the title, artist, album, duration, etc. of an item.
func (s *Store) UpdateItemMetadata(itemID int64, spotifyURI, trackID, title, artist, album, thumbnailURL string, durationMS int, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		UPDATE queue_items SET
			spotify_uri = COALESCE(NULLIF(?, ''), spotify_uri),
			track_id = COALESCE(NULLIF(?, ''), track_id),
			title = COALESCE(NULLIF(?, ''), title),
			artist = COALESCE(NULLIF(?, ''), artist),
			album = COALESCE(NULLIF(?, ''), album),
			thumbnail_url = COALESCE(NULLIF(?, ''), thumbnail_url),
			duration_ms = CASE WHEN ? > 0 THEN ? ELSE duration_ms END,
			status = COALESCE(NULLIF(?, ''), status)
		WHERE id = ?
	`
	_, err := s.db.Exec(query, spotifyURI, trackID, title, artist, album, thumbnailURL, durationMS, durationMS, status, itemID)
	return err
}

// GetResolvingItems returns all items currently in 'resolving' or placeholder state that need metadata.
func (s *Store) GetResolvingItems() ([]QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, spotify_uri, track_id, raw_input, title, artist, album,
		       thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
		       status, round_number, votes, created_at
		FROM queue_items
		WHERE status = 'resolving' OR title LIKE 'Spotify Track (%)'
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []QueueItem
	for rows.Next() {
		var it QueueItem
		if err := rows.Scan(
			&it.ID, &it.SpotifyURI, &it.TrackID, &it.RawInput, &it.Title, &it.Artist, &it.Album,
			&it.ThumbnailURL, &it.DurationMS, &it.AddedByID, &it.AddedByName, &it.AddedByColor,
			&it.Status, &it.RoundNumber, &it.Votes, &it.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}

// FindActiveDuplicate checks if an identical track/link is already actively playing, queued, or resolving,
// making Share Sheet, iOS Shortcut, and offline outbox retries 100% idempotent.
// Once a track finishes or is skipped ('played'/'skipped'), it can be re-queued again freely.
func (s *Store) FindActiveDuplicate(addedByID, trackID, spotifyURI, rawInput string) (*QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, spotify_uri, track_id, raw_input, title, artist, album,
		       thumbnail_url, duration_ms, added_by_id, added_by_name, added_by_color,
		       status, round_number, votes, created_at
		FROM queue_items
		WHERE status IN ('playing', 'queued', 'resolving')
		ORDER BY CASE WHEN status = 'playing' THEN 0 ELSE 1 END, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var it QueueItem
		if err := rows.Scan(
			&it.ID, &it.SpotifyURI, &it.TrackID, &it.RawInput, &it.Title, &it.Artist, &it.Album,
			&it.ThumbnailURL, &it.DurationMS, &it.AddedByID, &it.AddedByName, &it.AddedByColor,
			&it.Status, &it.RoundNumber, &it.Votes, &it.CreatedAt,
		); err != nil {
			return nil, err
		}

		sameTrack := (trackID != "" && it.TrackID == trackID) ||
			(spotifyURI != "" && it.SpotifyURI == spotifyURI) ||
			(rawInput != "" && it.RawInput == rawInput)
		if sameTrack {
			return &it, nil
		}
	}
	return nil, nil
}

// DeduplicateActiveQueue removes duplicate active ('queued' / 'resolving') copies of tracks that are already
// playing or earlier in the queue, returning the number of duplicate rows removed.
func (s *Store) DeduplicateActiveQueue() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`
		SELECT id, spotify_uri, track_id, raw_input, status
		FROM queue_items
		WHERE status IN ('playing', 'queued', 'resolving')
		ORDER BY CASE WHEN status = 'playing' THEN 0 ELSE 1 END, id ASC
	`)
	if err != nil {
		return 0, err
	}

	seen := make(map[string]bool)
	var dupIDs []int64
	for rows.Next() {
		var id int64
		var uri, tid, raw, status string
		if err := rows.Scan(&id, &uri, &tid, &raw, &status); err != nil {
			rows.Close()
			return 0, err
		}
		key := ""
		if tid != "" {
			key = "tid:" + tid
		} else if uri != "" {
			key = "uri:" + uri
		} else if raw != "" {
			key = "raw:" + raw
		}
		if key == "" {
			continue
		}
		if seen[key] && status != "playing" {
			dupIDs = append(dupIDs, id)
		} else {
			seen[key] = true
		}
	}
	rows.Close()

	for _, id := range dupIDs {
		_, _ = s.db.Exec(`DELETE FROM queue_items WHERE id = ?`, id)
	}
	return len(dupIDs), nil
}

