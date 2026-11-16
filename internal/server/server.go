package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marcelkornblum/deckjam/internal/player"
	"github.com/marcelkornblum/deckjam/internal/qr"
	"github.com/marcelkornblum/deckjam/internal/spotify"
	"github.com/marcelkornblum/deckjam/internal/store"
	"github.com/marcelkornblum/deckjam/internal/web"
)

// Server coordinates the HTTP router, middleware, SSE broadcasting, and background workers.
type Server struct {
	addr        string
	port        string
	hostname    string
	dataDir     string
	store       *store.Store
	resolver    *spotify.Resolver
	worker      *spotify.BackgroundWorker
	coordinator *player.Coordinator
	sse         *SSEManager
	httpServer  *http.Server
	joinURL     string
	lanURLs     []string
}

// Config provides configuration parameters for the DeckJam server.
type Config struct {
	Addr         string
	DataDir      string
	Store        *store.Store
	Resolver     *spotify.Resolver
	Coordinator  *player.Coordinator
	Hostname     string
	HostPIN      string
	LibrespotURL string
}

// NewServer creates a new DeckJam HTTP/SSE Server.
func NewServer(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = "0.0.0.0:8080"
	}

	port := "8080"
	parts := strings.Split(cfg.Addr, ":")
	if len(parts) == 2 {
		port = parts[1]
	}

	joinURL, lanURLs := DetectNetworkURLs(port, cfg.Hostname, cfg.DataDir)

	s := &Server{
		addr:        cfg.Addr,
		port:        port,
		hostname:    cfg.Hostname,
		dataDir:     cfg.DataDir,
		store:       cfg.Store,
		resolver:    cfg.Resolver,
		coordinator: cfg.Coordinator,
		sse:         NewSSEManager(),
		joinURL:     joinURL,
		lanURLs:     lanURLs,
	}

	if cfg.HostPIN != "" {
		_ = cfg.Store.SetHostPIN(cfg.HostPIN)
	}

	s.worker = spotify.NewBackgroundWorker(cfg.Resolver, cfg.Store, 10*time.Second)

	return s
}

// Start launches background loops and listens for HTTP requests.
func (s *Server) Start() error {
	s.worker.Start()

	// Subscribe to player state changes to broadcast SSE state updates
	stateChangeCh := make(chan struct{}, 10)
	s.coordinator.SubscribeStateChange(stateChangeCh)

	go func() {
		for range stateChangeCh {
			s.BroadcastState()
		}
	}()

	// 1-second progress ticker for SSE progress events
	go s.progressLoop()

	mux := http.NewServeMux()
	s.routes(mux)

	handler := s.corsMiddleware(mux)

	s.httpServer = &http.Server{
		Addr:         s.addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE requires no write timeout
	}

	log.Printf("[deckjam] Server listening on %s (Join URL: %s)", s.addr, s.joinURL)
	return s.httpServer.ListenAndServe()
}

// Close gracefully shuts down the server.
func (s *Server) Close() error {
	if s.worker != nil {
		s.worker.Stop()
	}
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Host-Pin")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes(mux *http.ServeMux) {
	// Register custom / critical MIME types
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	_ = mime.AddExtensionType(".svg", "image/svg+xml")
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".css", "text/css; charset=utf-8")

	// 1. Static Files (internal/web/static embedded)
	staticSub, err := fs.Sub(web.StaticFS, "static")
	if err == nil {
		fileServer := http.FileServer(http.FS(staticSub))

		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				indexFile, err := staticSub.Open("index.html")
				if err == nil {
					defer indexFile.Close()
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					_, _ = io.Copy(w, indexFile)
					return
				}
			}
			if r.URL.Path == "/manifest.webmanifest" {
				w.Header().Set("Content-Type", "application/manifest+json")
			}
			fileServer.ServeHTTP(w, r)
		})

		mux.HandleFunc("/deck", func(w http.ResponseWriter, r *http.Request) {
			deckFile, err := staticSub.Open("deck.html")
			if err == nil {
				defer deckFile.Close()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.Copy(w, deckFile)
				return
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
		})
	}

	// 2. API Endpoints
	mux.HandleFunc("/api/state", s.handleGetState)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/guest", s.handleGuest)
	mux.HandleFunc("/api/queue", s.handleQueue)
	mux.HandleFunc("/share", s.handleShare)
	mux.HandleFunc("/api/quick-share", s.handleQuickShare)
	mux.HandleFunc("/api/queue/", s.handleQueueItemActions) // POST /api/queue/{id}/vote, DELETE /api/queue/{id}
	mux.HandleFunc("/api/skip-vote", s.handleSkipVote)
	mux.HandleFunc("/api/host/verify", s.handleHostVerify)
	mux.HandleFunc("/api/host/control", s.handleHostControl)
	mux.HandleFunc("/api/audio-test", s.handleAudioTest)
	mux.HandleFunc("/api/artwork/", s.handleArtwork)
	mux.HandleFunc("/api/qr.svg", s.handleQRSVG)
	mux.HandleFunc("/api/shortcuts/http-shortcuts.json", s.handleHTTPShortcuts)
}

func (s *Server) getSnapshot(guestID string, isHost bool) (StateSnapshot, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		return StateSnapshot{}, err
	}

	joinURL, lanURLs := DetectNetworkURLs(s.port, s.hostname, s.dataDir)

	np, _ := s.store.GetNowPlaying(guestID)
	queued, _ := s.store.GetQueuedItems(settings.QueueMode, guestID)
	history, _ := s.store.GetHistory(guestID)
	fallbackCount, _ := s.store.CountFallbackTracks()
	playerStatus := s.coordinator.GetStatus(guestID)
	onlineGuests, _ := s.store.CountOnlineGuests(5 * time.Minute)

	hostPin := ""
	if isHost {
		hostPin = settings.HostPIN
	}

	return StateSnapshot{
		NowPlaying:    np,
		Queue:         queued,
		History:       history,
		FallbackCount: fallbackCount,
		Player:        playerStatus,
		Settings:      settings,
		IsHost:        isHost,
		HostPIN:       hostPin,
		JoinURL:       joinURL,
		LanURLs:       lanURLs,
		OnlineGuests:  onlineGuests,
		ServerTimeMS:  time.Now().UnixMilli(),
	}, nil
}

// BroadcastState sends updated StateSnapshot to all SSE clients preserving their guestID and host privileges.
func (s *Server) BroadcastState() {
	s.sse.BroadcastPerClient("state", func(c SSEClient) ([]byte, error) {
		snap, err := s.getSnapshot(c.GuestID, c.IsHost)
		if err != nil {
			return nil, err
		}
		return json.Marshal(snap)
	})
}

func (s *Server) progressLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if s.sse.ClientCount() == 0 {
			continue
		}

		status := s.coordinator.GetStatus("")
		onlineGuests, _ := s.store.CountOnlineGuests(5 * time.Minute)

		payload := ProgressPayload{
			Playing:           status.Playing,
			Paused:            status.Paused,
			Buffering:         status.Buffering,
			AudioVerified:     status.AudioVerified,
			AudioSinkName:     status.AudioSinkName,
			AudioPeakPct:      status.AudioPeakPct,
			InternetChecked:   status.InternetChecked,
			InternetOK:        status.InternetOK,
			InternetLatencyMS: status.InternetLatencyMS,
			InternetDetail:    status.InternetDetail,
			LastError:         status.LastError,
			PositionMS:        status.PositionMS,
			DurationMS:        status.DurationMS,
			SkipVotes:         status.SkipVotes,
			SkipVoteThreshold: status.SkipVoteThreshold,
			OnlineGuests:      onlineGuests,
			ServerTimeMS:      time.Now().UnixMilli(),
		}
		bytes, _ := json.Marshal(payload)
		s.sse.Broadcast("progress", bytes)
	}
}

func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	guestID := r.URL.Query().Get("guest_id")
	if guestID != "" {
		s.store.TouchGuest(guestID)
	}

	settings, _ := s.store.GetSettings()
	isHost := IsHostRequest(r, settings.HostPIN)

	snap, err := s.getSnapshot(guestID, isHost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	guestID := r.URL.Query().Get("guest_id")
	if guestID != "" {
		s.store.TouchGuest(guestID)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	settings, _ := s.store.GetSettings()
	isHost := IsHostRequest(r, settings.HostPIN)

	clientChan := make(chan []byte, 10)
	s.sse.AddClient(clientChan, guestID, isHost)
	defer s.sse.RemoveClient(clientChan)

	// Send initial state snapshot immediately
	initialSnap, err := s.getSnapshot(guestID, isHost)
	if err == nil {
		snapBytes, _ := json.Marshal(initialSnap)
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", string(snapBytes))
		flusher.Flush()
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-clientChan:
			if !ok {
				return
			}
			_, _ = w.Write(msg)
			flusher.Flush()
		}
	}
}

func (s *Server) handleGuest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var g store.Guest
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	if g.GuestID == "" {
		http.Error(w, "guest_id is required", http.StatusBadRequest)
		return
	}
	if g.Name == "" {
		g.Name = "Party Guest"
	}
	if g.Color == "" {
		g.Color = "#2ebd85"
	}

	if err := s.store.UpsertGuest(g); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(g)
	s.BroadcastState()
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Input     string `json:"input"`
		GuestID   string `json:"guest_id"`
		GuestName string `json:"guest_name"`
		Target    string `json:"target"` // "queue" or "fallback"
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	req.Input = strings.TrimSpace(req.Input)
	if req.Input == "" {
		http.Error(w, "input cannot be empty", http.StatusBadRequest)
		return
	}

	guest := store.Guest{
		GuestID: req.GuestID,
		Name:    req.GuestName,
		Color:   "#2ebd85",
	}
	if guest.GuestID != "" {
		s.store.TouchGuest(guest.GuestID)
		if g, err := s.store.GetGuest(guest.GuestID); err == nil && g != nil {
			guest = *g
		}
	}
	if guest.Name == "" {
		guest.Name = "Guest"
	}

	target := req.Target
	if target == "" {
		target = "queue"
	}

	settings, _ := s.store.GetSettings()

	count, items, msg, err := spotify.EnqueueWithResilience(
		r.Context(),
		s.resolver,
		s.store,
		req.Input,
		guest,
		target,
		settings.QueueMode,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Trigger autoplay if nothing is playing
	s.coordinator.CheckAutoPlay()
	if count > 0 {
		s.BroadcastState()
	}

	resp := map[string]interface{}{
		"ok":          true,
		"added_count": count,
		"items":       items,
		"message":     msg,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	var input, guestID, guestName string

	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		u, txt, ttl := r.FormValue("url"), r.FormValue("text"), r.FormValue("title")
		input = strings.Join([]string{u, txt, ttl}, " ")
		guestID = r.FormValue("guest")
		guestName = r.FormValue("guest_name")
	} else {
		u, txt, ttl := r.URL.Query().Get("url"), r.URL.Query().Get("text"), r.URL.Query().Get("title")
		input = strings.Join([]string{u, txt, ttl}, " ")
		guestID = r.URL.Query().Get("guest")
		guestName = r.URL.Query().Get("guest_name")
	}

	if guestName == "" {
		guestName = "Shared Guest"
	}

	guest := store.Guest{
		GuestID: guestID,
		Name:    guestName,
		Color:   "#2ebd85",
	}

	settings, _ := s.store.GetSettings()
	count, _, msg, err := spotify.EnqueueWithResilience(
		r.Context(),
		s.resolver,
		s.store,
		input,
		guest,
		"queue",
		settings.QueueMode,
	)

	s.coordinator.CheckAutoPlay()
	if count > 0 {
		s.BroadcastState()
	}

	redirectURL := fmt.Sprintf("/?shared=1&msg=%s", url.QueryEscape(msg))
	if err != nil {
		redirectURL = fmt.Sprintf("/?shared=0&msg=%s", url.QueryEscape(err.Error()))
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (s *Server) handleQuickShare(w http.ResponseWriter, r *http.Request) {
	var input, guestID, guestName string

	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			URL       string `json:"url"`
			Text      string `json:"text"`
			GuestID   string `json:"guest_id"`
			GuestName string `json:"guest_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		input = body.URL
		if input == "" {
			input = body.Text
		}
		guestID = body.GuestID
		guestName = body.GuestName
	} else if r.Method == http.MethodPost {
		_ = r.ParseForm()
		input = r.FormValue("url")
		if input == "" {
			input = r.FormValue("text")
		}
		guestID = r.FormValue("guest")
		guestName = r.FormValue("guest_name")
	} else {
		input = r.URL.Query().Get("url")
		if input == "" {
			input = r.URL.Query().Get("text")
		}
		guestID = r.URL.Query().Get("guest")
		guestName = r.URL.Query().Get("guest_name")
	}

	if guestName == "" {
		guestName = "Quick Share"
	}

	guest := store.Guest{
		GuestID: guestID,
		Name:    guestName,
		Color:   "#2ebd85",
	}

	settings, _ := s.store.GetSettings()
	count, _, msg, err := spotify.EnqueueWithResilience(
		r.Context(),
		s.resolver,
		s.store,
		input,
		guest,
		"queue",
		settings.QueueMode,
	)

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":      false,
			"message": err.Error(),
		})
		return
	}

	s.coordinator.CheckAutoPlay()
	if count > 0 {
		s.BroadcastState()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          true,
		"added_count": count,
		"message":     msg,
	})
}

func (s *Server) handleQueueItemActions(w http.ResponseWriter, r *http.Request) {
	// Path format: /api/queue/{id} or /api/queue/{id}/vote
	path := strings.TrimPrefix(r.URL.Path, "/api/queue/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}

	itemID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid item ID", http.StatusBadRequest)
		return
	}

	// Case 1: POST /api/queue/{id}/vote
	if len(parts) == 2 && parts[1] == "vote" && r.Method == http.MethodPost {
		var req struct {
			GuestID string `json:"guest_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.GuestID == "" {
			http.Error(w, "guest_id required", http.StatusBadRequest)
			return
		}

		votes, voted, err := s.store.ToggleVote(itemID, req.GuestID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.BroadcastState()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":    true,
			"votes": votes,
			"voted": voted,
		})
		return
	}

	// Case 2: DELETE /api/queue/{id}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		settings, _ := s.store.GetSettings()
		isHost := IsHostRequest(r, settings.HostPIN)
		guestID := r.URL.Query().Get("guest_id")

		item, err := s.store.GetItemByID(itemID)
		if err != nil || item == nil {
			http.NotFound(w, r)
			return
		}

		// Check permission: is_host OR guest_id == item.added_by_id
		if !isHost && (guestID == "" || guestID != item.AddedByID) {
			http.Error(w, "Unauthorized to delete item", http.StatusForbidden)
			return
		}

		if err := s.store.RemoveItem(itemID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.BroadcastState()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": true,
		})
		return
	}

	http.NotFound(w, r)
}

func (s *Server) handleSkipVote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		GuestID string `json:"guest_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GuestID == "" {
		http.Error(w, "guest_id required", http.StatusBadRequest)
		return
	}

	np, err := s.store.GetNowPlaying(req.GuestID)
	if err != nil || np == nil {
		http.Error(w, "No track currently playing", http.StatusBadRequest)
		return
	}

	settings, _ := s.store.GetSettings()
	threshold := settings.SkipVoteThreshold
	if threshold <= 0 {
		threshold = 3
	}

	// Add skip vote
	skipVotes, _, err := s.store.AddSkipVote(np.ID, req.GuestID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Check if immediate skip conditions met:
	// If guest_id == np.added_by_id OR skipVotes >= threshold OR !playerStatus.Playing (stuck/paused track escape hatch)
	playerStatus := s.coordinator.GetStatus(req.GuestID)
	skipped := false
	if req.GuestID == np.AddedByID || skipVotes >= threshold || !playerStatus.Playing {
		_ = s.coordinator.SkipCurrentTrack()
		skipped = true
	}

	s.BroadcastState()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":         true,
		"skipped":    skipped,
		"skip_votes": skipVotes,
		"threshold":  threshold,
	})
}

func (s *Server) handleHostVerify(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.GetSettings()
	isHost := IsHostRequest(r, settings.HostPIN)

	if !isHost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"is_host": false,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"is_host":  true,
		"host_pin": settings.HostPIN,
	})
}

func (s *Server) handleHostControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	settings, _ := s.store.GetSettings()
	if !IsHostRequest(r, settings.HostPIN) {
		http.Error(w, "Unauthorized: Host access required", http.StatusUnauthorized)
		return
	}

	var req struct {
		Action   string         `json:"action"`
		ItemID   int64          `json:"item_id"`
		Value    int            `json:"value"`
		Settings store.Settings `json:"settings"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "play":
		_ = s.coordinator.Resume()
	case "pause":
		_ = s.coordinator.Pause()
	case "toggle":
		_ = s.coordinator.PlayPause()
	case "skip":
		_ = s.coordinator.SkipCurrentTrack()
	case "restart":
		_ = s.coordinator.Seek(0)
	case "seek":
		_ = s.coordinator.Seek(req.Value)
	case "volume":
		_ = s.coordinator.SetVolume(req.Value)
	case "play_now":
		if req.ItemID > 0 {
			_ = s.coordinator.PlayNowItem(req.ItemID)
		}
	case "move_top":
		if req.ItemID > 0 {
			_ = s.store.MoveTop(req.ItemID)
		}
	case "clear_queue":
		_ = s.store.ClearQueue()
	case "dedupe_queue":
		_, _ = s.store.DeduplicateActiveQueue()
	case "clear_fallback":
		_ = s.store.ClearFallbackTracks()
	case "update_settings":
		_ = s.store.UpdateSettings(req.Settings)
		if req.Settings.PlayerBackend != "" {
			s.coordinator.SetBackend(req.Settings.PlayerBackend)
		}
	case "test_audio":
		_, _ = s.coordinator.PlayTestTone(650)
	default:
		http.Error(w, fmt.Sprintf("Unknown action: %s", req.Action), http.StatusBadRequest)
		return
	}

	s.BroadcastState()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true,
	})
}

func (s *Server) handleAudioTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats, err := s.coordinator.PlayTestTone(650)
	s.BroadcastState()

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":             false,
			"error":          err.Error(),
			"audio_verified": stats.Verified,
			"audio_player":   stats.PlayerCmd,
			"audio_sink":     stats.SinkName,
			"bytes_per_sec":  stats.BytesPerSec,
			"peak_pct":       stats.PeakPct,
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":             true,
		"audio_verified": stats.Verified,
		"audio_player":   stats.PlayerCmd,
		"audio_sink":     stats.SinkName,
		"bytes_per_sec":  stats.BytesPerSec,
		"peak_pct":       stats.PeakPct,
	})
}

func (s *Server) handleArtwork(w http.ResponseWriter, r *http.Request) {
	trackID := strings.TrimPrefix(r.URL.Path, "/api/artwork/")
	trackID = strings.TrimSpace(trackID)

	if trackID == "" || trackID == "unknown" {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(DefaultVinylSVG())
		return
	}

	// Check cache in store
	cached, err := s.store.GetTrackCache(trackID)
	if err == nil && cached != nil && len(cached.LocalThumb) > 0 {
		mime := cached.ThumbMIME
		if mime == "" {
			mime = "image/jpeg"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(cached.LocalThumb)
		return
	}

	// If not in byte cache but has thumbnail_url, attempt download
	if cached != nil && cached.ThumbnailURL != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		bytes, mime, err := s.resolver.EnsureLocalThumbnail(ctx, trackID, cached.ThumbnailURL)
		if err == nil && len(bytes) > 0 {
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(bytes)
			return
		}
	}

	// Offline / unresolvable: Serve crisp SVG vinyl cover placeholder
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(DefaultVinylSVG())
}

func (s *Server) handleQRSVG(w http.ResponseWriter, r *http.Request) {
	data := r.URL.Query().Get("data")
	if data == "" {
		joinURL, _ := DetectNetworkURLs(s.port, s.hostname, s.dataDir)
		data = joinURL
	}

	svg, err := qr.GenerateSVG(data, 256)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(svg))
}

func (s *Server) handleHTTPShortcuts(w http.ResponseWriter, r *http.Request) {
	guestID := r.URL.Query().Get("guest_id")
	guestName := r.URL.Query().Get("guest_name")

	joinURL, _ := DetectNetworkURLs(s.port, s.hostname, s.dataDir)
	configJSON := GenerateHTTPShortcutsConfig(joinURL, guestID, guestName)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="deckjam-shortcuts.json"`)
	_, _ = w.Write(configJSON)
}
