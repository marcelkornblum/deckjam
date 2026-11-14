package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreBasics(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "deckjam.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// Verify settings
	settings, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if settings.RoomName != "Steam Deck Jam" {
		t.Errorf("expected room name 'Steam Deck Jam', got '%s'", settings.RoomName)
	}
	if settings.HostPIN == "" {
		t.Errorf("expected generated host pin, got empty")
	}

	// Update settings
	settings.RoomName = "Road Trip Jam"
	settings.SkipVoteThreshold = 5
	if err := s.UpdateSettings(settings); err != nil {
		t.Fatalf("UpdateSettings failed: %v", err)
	}
	updated, err := s.GetSettings()
	if err != nil || updated.RoomName != "Road Trip Jam" || updated.SkipVoteThreshold != 5 {
		t.Fatalf("Settings update not persisted: %+v", updated)
	}

	// Guest registration
	g := Guest{
		GuestID: "guest-1",
		Name:    "Alex",
		Color:   "#2ebd85",
	}
	if err := s.UpsertGuest(g); err != nil {
		t.Fatalf("UpsertGuest failed: %v", err)
	}
	gotGuest, err := s.GetGuest("guest-1")
	if err != nil || gotGuest.Name != "Alex" {
		t.Fatalf("GetGuest failed: %v, %+v", err, gotGuest)
	}
	online, err := s.CountOnlineGuests(time.Minute)
	if err != nil || online != 1 {
		t.Fatalf("CountOnlineGuests expected 1, got %d (err: %v)", online, err)
	}
}

func TestFairRoundRobinQueue(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "deckjam.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// Guest A queues 3 songs
	for i := 1; i <= 3; i++ {
		item := &QueueItem{
			SpotifyURI:  "spotify:track:A",
			TrackID:     "trackA",
			Title:       "Song A",
			AddedByID:   "guestA",
			AddedByName: "Alice",
		}
		if err := s.AddToQueue(item, "round_robin"); err != nil {
			t.Fatalf("AddToQueue A%d failed: %v", i, err)
		}
		if item.RoundNumber != i {
			t.Errorf("Expected A%d round to be %d, got %d", i, i, item.RoundNumber)
		}
	}

	// Guest B now arrives and queues 1 song.
	// Since min overall queued round is 1, Guest B's first song should get Round 1!
	itemB1 := &QueueItem{
		SpotifyURI:  "spotify:track:B",
		TrackID:     "trackB",
		Title:       "Song B",
		AddedByID:   "guestB",
		AddedByName: "Bob",
	}
	if err := s.AddToQueue(itemB1, "round_robin"); err != nil {
		t.Fatalf("AddToQueue B1 failed: %v", err)
	}
	if itemB1.RoundNumber != 1 {
		t.Fatalf("Expected Bob's first song to be round 1, got %d", itemB1.RoundNumber)
	}

	// Check queue order: Round 1 (Alice), Round 1 (Bob), Round 2 (Alice), Round 3 (Alice)
	items, err := s.GetQueuedItems("round_robin", "")
	if err != nil {
		t.Fatalf("GetQueuedItems failed: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("Expected 4 items, got %d", len(items))
	}
	if items[0].AddedByID != "guestA" || items[1].AddedByID != "guestB" {
		t.Errorf("Unexpected order for round 1: first=%s, second=%s", items[0].AddedByID, items[1].AddedByID)
	}

	// Bob upvotes his song
	cnt, voted, err := s.ToggleVote(itemB1.ID, "guestB")
	if err != nil || cnt != 1 || !voted {
		t.Fatalf("ToggleVote failed: %v, cnt=%d, voted=%v", err, cnt, voted)
	}

	// Now Bob's song has 1 vote, Alice's round 1 has 0 votes.
	// In Round 1, Bob's song should move before Alice's song!
	itemsAfterVote, err := s.GetQueuedItems("round_robin", "guestB")
	if err != nil {
		t.Fatalf("GetQueuedItems failed: %v", err)
	}
	if itemsAfterVote[0].ID != itemB1.ID {
		t.Errorf("Expected Bob's voted song to be first, got ID=%d", itemsAfterVote[0].ID)
	}
	if !itemsAfterVote[0].VotedByMe {
		t.Errorf("Expected VotedByMe to be true for guestB")
	}

	// Pop next track
	popped, err := s.PopNextTrack("round_robin")
	if err != nil || popped == nil {
		t.Fatalf("PopNextTrack failed: %v, item: %+v", err, popped)
	}
	if popped.ID != itemB1.ID {
		t.Errorf("Expected popped track to be Bob's song, got ID=%d", popped.ID)
	}
	if popped.Status != "playing" {
		t.Errorf("Expected popped status 'playing', got '%s'", popped.Status)
	}

	// Verify Now Playing
	np, err := s.GetNowPlaying("guestB")
	if err != nil || np == nil || np.ID != itemB1.ID {
		t.Fatalf("GetNowPlaying mismatch: %+v", np)
	}

	// Skip vote
	skipCnt, already, err := s.AddSkipVote(np.ID, "guestB")
	if err != nil || skipCnt != 1 || already {
		t.Fatalf("AddSkipVote failed: %v, cnt=%d, already=%v", err, skipCnt, already)
	}
	skipCnt2, already2, err := s.AddSkipVote(np.ID, "guestB")
	if err != nil || skipCnt2 != 1 || !already2 {
		t.Fatalf("Duplicate AddSkipVote failed: %v, cnt=%d, already=%v", err, skipCnt2, already2)
	}

	// Pop next (advances Bob's song to 'played')
	popped2, err := s.PopNextTrack("round_robin")
	if err != nil || popped2 == nil {
		t.Fatalf("PopNextTrack 2 failed: %v", err)
	}

	// Check history
	hist, err := s.GetHistory("")
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(hist) != 1 || hist[0].ID != itemB1.ID || hist[0].Status != "played" {
		t.Fatalf("History mismatch: %+v", hist)
	}
}

func TestTrackCacheAndFallback(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "deckjam.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// Cache test
	meta := &TrackMetadata{
		TrackID:      "t123",
		SpotifyURI:   "spotify:track:t123",
		Title:        "Cached Title",
		Artist:       "Cached Artist",
		Album:        "Cached Album",
		ThumbnailURL: "https://example.com/cover.jpg",
		DurationMS:   180000,
		LocalThumb:   []byte("fakepngbytes"),
		ThumbMIME:    "image/jpeg",
	}
	if err := s.SaveTrackCache(meta); err != nil {
		t.Fatalf("SaveTrackCache failed: %v", err)
	}
	gotMeta, err := s.GetTrackCache("t123")
	if err != nil || gotMeta == nil || gotMeta.Title != "Cached Title" || string(gotMeta.LocalThumb) != "fakepngbytes" {
		t.Fatalf("GetTrackCache failed: %v, %+v", err, gotMeta)
	}

	// Fallback pool test
	fb1 := &FallbackTrack{
		SpotifyURI:  "spotify:track:fb1",
		TrackID:     "fb1",
		Title:       "Fallback 1",
		Artist:      "Artist 1",
		DurationMS:  200000,
		PlayedCount: 0,
	}
	fb2 := &FallbackTrack{
		SpotifyURI:  "spotify:track:fb2",
		TrackID:     "fb2",
		Title:       "Fallback 2",
		Artist:      "Artist 2",
		DurationMS:  190000,
		PlayedCount: 0,
	}
	if err := s.AddFallbackTrack(fb1); err != nil {
		t.Fatalf("AddFallbackTrack 1 failed: %v", err)
	}
	if err := s.AddFallbackTrack(fb2); err != nil {
		t.Fatalf("AddFallbackTrack 2 failed: %v", err)
	}

	count, err := s.CountFallbackTracks()
	if err != nil || count != 2 {
		t.Fatalf("CountFallbackTracks expected 2, got %d", count)
	}

	// Pick fallback (should pick fb1 or fb2 with played_count=0)
	picked1, err := s.PickFallbackTrack()
	if err != nil || picked1 == nil {
		t.Fatalf("PickFallbackTrack 1 failed: %v", err)
	}
	// Next pick should pick the other one
	picked2, err := s.PickFallbackTrack()
	if err != nil || picked2 == nil {
		t.Fatalf("PickFallbackTrack 2 failed: %v", err)
	}
	if picked1.ID == picked2.ID {
		t.Errorf("Expected different fallback track to be picked, got same ID: %d", picked1.ID)
	}
}
