package player

import (
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcelkornblum/deckjam/internal/store"
)

func TestMockBackendAndCoordinator(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	coord := NewCoordinator(st, "http://127.0.0.1:9999", "mock")
	defer coord.Close()

	// Initially idle
	status := coord.GetStatus("")
	if status.ActiveBackend != "mock" {
		t.Errorf("Expected active backend 'mock', got '%s'", status.ActiveBackend)
	}
	if status.Playing {
		t.Errorf("Expected player not to be playing initially")
	}

	// Add track to store
	qItem := &store.QueueItem{
		SpotifyURI:  "spotify:track:testSong1",
		TrackID:     "testSong1",
		Title:       "Test Song 1",
		Artist:      "Test Artist",
		DurationMS:  1000, // 1 second for fast test!
		AddedByID:   "user1",
		AddedByName: "Alice",
	}
	if err := st.AddToQueue(qItem, "round_robin"); err != nil {
		t.Fatalf("AddToQueue failed: %v", err)
	}

	// Trigger CheckAutoPlay
	coord.CheckAutoPlay()

	np, err := st.GetNowPlaying("")
	if err != nil || np == nil {
		t.Fatalf("Expected track to start playing: %+v", np)
	}
	if np.SpotifyURI != "spotify:track:testSong1" {
		t.Errorf("Unexpected now playing URI: %s", np.SpotifyURI)
	}

	// Verify player is now playing
	status = coord.GetStatus("")
	if !status.Playing {
		t.Errorf("Expected coordinator status playing=true")
	}

	// Pause and resume
	if err := coord.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}
	if !coord.GetStatus("").Paused {
		t.Errorf("Expected paused=true")
	}
	if err := coord.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if coord.GetStatus("").Paused {
		t.Errorf("Expected paused=false after resume")
	}

	// Wait 1.5 seconds for 1-second track to finish and end
	time.Sleep(1600 * time.Millisecond)

	// After track ended, now playing should be nil (or fallback), and item status should be 'played'
	item, err := st.GetItemByID(qItem.ID)
	if err != nil {
		t.Fatalf("GetItemByID failed: %v", err)
	}
	if item.Status != "played" {
		t.Errorf("Expected track status to be 'played' after completion, got '%s'", item.Status)
	}
}

func TestCoordinatorSkipAndPlayNow(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	coord := NewCoordinator(st, "", "mock")
	defer coord.Close()

	// Add 2 songs
	item1 := &store.QueueItem{
		SpotifyURI:  "spotify:track:song1",
		TrackID:     "song1",
		Title:       "Song 1",
		DurationMS:  100000,
		AddedByID:   "u1",
		AddedByName: "User 1",
	}
	item2 := &store.QueueItem{
		SpotifyURI:  "spotify:track:song2",
		TrackID:     "song2",
		Title:       "Song 2",
		DurationMS:  100000,
		AddedByID:   "u2",
		AddedByName: "User 2",
	}
	_ = st.AddToQueue(item1, "round_robin")
	_ = st.AddToQueue(item2, "round_robin")

	coord.CheckAutoPlay()

	np, _ := st.GetNowPlaying("")
	if np == nil || np.ID != item1.ID {
		t.Fatalf("Expected song 1 to play first")
	}

	// Skip current track
	if err := coord.SkipCurrentTrack(); err != nil {
		t.Fatalf("SkipCurrentTrack failed: %v", err)
	}

	// Now song 2 should be playing
	np2, _ := st.GetNowPlaying("")
	if np2 == nil || np2.ID != item2.ID {
		t.Fatalf("Expected song 2 to play after skip, got: %+v", np2)
	}

	// Verify song 1 marked as 'skipped'
	s1, _ := st.GetItemByID(item1.ID)
	if s1.Status != "skipped" {
		t.Errorf("Expected song 1 status 'skipped', got '%s'", s1.Status)
	}
}

// 1. LibrespotBackend receiving paused: true, track: null from /status does NOT report Paused: true
func TestLibrespotNullTrackNotPaused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"playback_ready":true}`))
	})
	mux.HandleFunc("/auth/code", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// Simulate go-librespot failed loadContext: stopped: false, paused: true, track: null
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stopped":false,"paused":true,"volume":80,"volume_steps":100,"track":null}`))
	})

	server := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()

	b := NewLibrespotBackend("http://"+ln.Addr().String(), nil)
	defer b.Close()

	// Run checkStatus
	b.checkStatus()

	st := b.GetStatus()
	if st.Paused {
		t.Errorf("Expected Paused=false when track is null, but got Paused=true")
	}
	if st.Playing {
		t.Errorf("Expected Playing=false when track is null, but got Playing=true")
	}
	if b.HasLoadedTrack("") {
		t.Errorf("Expected HasLoadedTrack to be false when track is null")
	}
}

// 2. Coordinator.PlayPause() / Resume() when now_playing exists in SQLite while backend is stopped calls Play(np.SpotifyURI)
func TestCoordinatorResumeWhenStoppedWithNowPlaying(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	coord := NewCoordinator(st, "", "mock")
	defer coord.Close()

	// Add an item directly marked as 'playing' (now playing in DB)
	item := &store.QueueItem{
		SpotifyURI:  "spotify:track:stuckSong",
		TrackID:     "stuckSong",
		Title:       "Stuck Song",
		DurationMS:  120000,
		AddedByID:   "u1",
		AddedByName: "Alice",
		Status:      "playing",
	}
	if err := st.AddToQueue(item, "round_robin"); err != nil {
		t.Fatalf("AddToQueue failed: %v", err)
	}

	// Backend is currently stopped
	status := coord.GetStatus("")
	if status.Playing || status.Paused {
		t.Fatalf("Expected backend initially stopped, got playing=%v paused=%v", status.Playing, status.Paused)
	}

	// Calling Resume() should detect !status.Playing && !status.Paused and start playing the track
	if err := coord.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	status = coord.GetStatus("")
	if !status.Playing {
		t.Errorf("Expected player to start playing after Resume(), got Playing=%v", status.Playing)
	}
}

// 3. Watchdog recovery when librespot fails first POST /player/play (HTTP 500) and succeeds on retry
func TestWatchdogRecoveryOnPlayFailure(t *testing.T) {
	var playCount int
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"playback_ready":true}`))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count := playCount
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if count > 1 {
			// Succeeded
			_, _ = w.Write([]byte(`{"stopped":false,"paused":false,"volume":80,"track":{"uri":"spotify:track:watchdogSong","position":100,"duration":100000}}`))
		} else {
			// Stopped/stuck
			_, _ = w.Write([]byte(`{"stopped":true,"paused":false,"volume":80,"track":null}`))
		}
	})
	mux.HandleFunc("/player/play", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		playCount++
		current := playCount
		mu.Unlock()
		if current == 1 {
			http.Error(w, "Temporary network timeout", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()

	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	coord := NewCoordinator(st, "http://"+ln.Addr().String(), "librespot")
	defer coord.Close()

	// Initial checkStatus so librespot backend marks connected=true
	coord.librespot.checkStatus()

	item := &store.QueueItem{
		SpotifyURI:  "spotify:track:watchdogSong",
		TrackID:     "watchdogSong",
		Title:       "Watchdog Song",
		DurationMS:  100000,
		AddedByID:   "u1",
		AddedByName: "Alice",
		Status:      "playing",
	}
	_ = st.AddToQueue(item, "round_robin")

	// Set lastPlayAttempt to 4 seconds ago so watchdog can trigger immediately
	coord.stateMu.Lock()
	coord.lastPlayAttempt = time.Now().Add(-4 * time.Second)
	coord.stateMu.Unlock()

	// Wait up to 12 seconds for watchdog to trigger play and retry (watchdog ticks every 3s and waits >=3.5s cooldown)
	deadline := time.Now().Add(12 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		mu.Lock()
		count := playCount
		mu.Unlock()
		if count >= 2 {
			recovered = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	if !recovered {
		mu.Lock()
		count := playCount
		mu.Unlock()
		t.Errorf("Expected watchdog to retry play at least twice, got playCount=%d", count)
	}
}
