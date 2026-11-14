package spotify

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/marcelkornblum/deckjam/internal/store"
)

// BackgroundWorker resolves pending items in the background when connectivity resumes.
type BackgroundWorker struct {
	resolver *Resolver
	store    *store.Store
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// NewBackgroundWorker creates a background resolution worker.
func NewBackgroundWorker(r *Resolver, s *store.Store, interval time.Duration) *BackgroundWorker {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &BackgroundWorker{
		resolver: r,
		store:    s,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start runs the background resolution worker loop.
func (bw *BackgroundWorker) Start() {
	bw.wg.Add(1)
	go func() {
		defer bw.wg.Done()
		ticker := time.NewTicker(bw.interval)
		defer ticker.Stop()

		for {
			select {
			case <-bw.stopCh:
				return
			case <-ticker.C:
				bw.ProcessResolvingItems()
			}
		}
	}()
}

// Stop shuts down the background worker.
func (bw *BackgroundWorker) Stop() {
	close(bw.stopCh)
	bw.wg.Wait()
}

// ProcessResolvingItems checks for unresolved or placeholder items and attempts to enrich them.
func (bw *BackgroundWorker) ProcessResolvingItems() {
	items, err := bw.store.GetResolvingItems()
	if err != nil || len(items) == 0 {
		return
	}

	for _, item := range items {
		inputToResolve := item.RawInput
		if inputToResolve == "" {
			inputToResolve = item.SpotifyURI
		}
		if inputToResolve == "" {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		tracks, err := bw.resolver.ResolveInput(ctx, inputToResolve)
		cancel()

		if err != nil || len(tracks) == 0 {
			// If still failing (e.g. offline), skip for next interval
			continue
		}

		first := tracks[0]
		status := item.Status
		if status == "resolving" {
			status = "queued"
		}

		thumbURL := first.ThumbnailURL
		if thumbURL != "" {
			thumbURL = fmt.Sprintf("/api/artwork/%s", first.TrackID)
		}

		err = bw.store.UpdateItemMetadata(
			item.ID,
			first.SpotifyURI,
			first.TrackID,
			first.Title,
			first.Artist,
			first.Album,
			thumbURL,
			first.DurationMS,
			status,
		)
		if err != nil {
			log.Printf("[worker] Failed to enrich item %d: %v", item.ID, err)
		} else {
			log.Printf("[worker] Successfully enriched item %d: %s - %s", item.ID, first.Title, first.Artist)
		}
	}
}

// EnqueueWithResilience handles enqueuing tracks or shortlinks gracefully even when offline or network is flaky.
func EnqueueWithResilience(ctx context.Context, r *Resolver, s *store.Store, input string, guest store.Guest, target string, queueMode string) (int, []store.QueueItem, string, error) {
	parsed := ParseInput(input)

	// Attempt resolution with timeout
	resolveCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	tracks, err := r.ResolveInput(resolveCtx, input)
	cancel()

	now := time.Now().Unix()

	if err == nil && len(tracks) > 0 {
		// Successful resolution!
		var enqueued []store.QueueItem
		for _, t := range tracks {
			thumbURL := t.ThumbnailURL
			if thumbURL != "" {
				thumbURL = fmt.Sprintf("/api/artwork/%s", t.TrackID)
			}
			duration := t.DurationMS
			if duration <= 0 {
				duration = 180000
			}

			if target == "fallback" {
				fb := &store.FallbackTrack{
					SpotifyURI:   t.SpotifyURI,
					TrackID:      t.TrackID,
					Title:        t.Title,
					Artist:       t.Artist,
					Album:        t.Album,
					ThumbnailURL: thumbURL,
					DurationMS:   duration,
					CreatedAt:    now,
				}
				_ = s.AddFallbackTrack(fb)
			} else {
				qi := store.QueueItem{
					SpotifyURI:   t.SpotifyURI,
					TrackID:      t.TrackID,
					RawInput:     input,
					Title:        t.Title,
					Artist:       t.Artist,
					Album:        t.Album,
					ThumbnailURL: thumbURL,
					DurationMS:   duration,
					AddedByID:    guest.GuestID,
					AddedByName:  guest.Name,
					AddedByColor: guest.Color,
					Status:       "queued",
					CreatedAt:    now,
				}
				_ = s.AddToQueue(&qi, queueMode)
				enqueued = append(enqueued, qi)
			}
		}

		var msg string
		if target == "fallback" {
			msg = fmt.Sprintf("Added %d track(s) to fallback pool", len(tracks))
		} else if len(tracks) == 1 {
			msg = fmt.Sprintf("Queued: %s — %s", tracks[0].Title, tracks[0].Artist)
		} else {
			msg = fmt.Sprintf("Queued %d tracks: %s...", len(tracks), tracks[0].Title)
		}
		return len(tracks), enqueued, msg, nil
	}

	// Network resolution failed! Apply Shaky-Connection Resilience:
	// Case A: Track ID is extractable from URL or URI
	if parsed.Type == "track" && parsed.ID != "" {
		shortID := parsed.ID
		if len(shortID) > 6 {
			shortID = shortID[:6]
		}
		placeholderTitle := fmt.Sprintf("Spotify Track (%s)", shortID)
		spotifyURI := fmt.Sprintf("spotify:track:%s", parsed.ID)
		thumbURL := fmt.Sprintf("/api/artwork/%s", parsed.ID)

		qi := store.QueueItem{
			SpotifyURI:   spotifyURI,
			TrackID:      parsed.ID,
			RawInput:     input,
			Title:        placeholderTitle,
			Artist:       "Resolving...",
			Album:        "Spotify",
			ThumbnailURL: thumbURL,
			DurationMS:   180000,
			AddedByID:    guest.GuestID,
			AddedByName:  guest.Name,
			AddedByColor: guest.Color,
			Status:       "queued",
			CreatedAt:    now,
		}
		_ = s.AddToQueue(&qi, queueMode)
		msg := fmt.Sprintf("Queued: %s (metadata will update shortly)", placeholderTitle)
		return 1, []store.QueueItem{qi}, msg, nil
	}

	// Case B: Unexpanded shortlink or arbitrary link
	if parsed.Type == "shortlink" || strings.Contains(input, "http") {
		qi := store.QueueItem{
			SpotifyURI:   "",
			TrackID:      "",
			RawInput:     input,
			Title:        "Resolving link...",
			Artist:       "Spotify",
			Album:        "",
			ThumbnailURL: "/api/artwork/unknown",
			DurationMS:   180000,
			AddedByID:    guest.GuestID,
			AddedByName:  guest.Name,
			AddedByColor: guest.Color,
			Status:       "resolving",
			CreatedAt:    now,
		}
		_ = s.AddToQueue(&qi, queueMode)
		msg := "Queued link (will resolve when network returns)"
		return 1, []store.QueueItem{qi}, msg, nil
	}

	return 0, nil, "", fmt.Errorf("unable to parse or enqueue Spotify link: %s", input)
}
