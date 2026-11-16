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

// EnqueueWithResilience handles enqueuing tracks or shortlinks gracefully even when offline or network is flaky,
// with built-in idempotency so mobile Share Sheet / offline outbox retries never add duplicate copies.
func EnqueueWithResilience(ctx context.Context, r *Resolver, s *store.Store, input string, guest store.Guest, target string, queueMode string) (int, []store.QueueItem, string, error) {
	input = strings.TrimSpace(input)
	parsed := ParseInput(input)

	// Fast pre-check before hitting network: if this exact single track/link is already queued or playing for this guest, return idempotently!
	if target != "fallback" && parsed.Type != "playlist" && parsed.Type != "album" {
		preURI := ""
		if parsed.Type == "track" && parsed.ID != "" {
			preURI = "spotify:track:" + parsed.ID
		}
		if dup, err := s.FindActiveDuplicate(guest.GuestID, parsed.ID, preURI, input); err == nil && dup != nil {
			msg := fmt.Sprintf("Already in queue: %s", dup.Title)
			return 0, []store.QueueItem{*dup}, msg, nil
		}
	}

	// Attempt resolution with crisp 2.5s timeout so slow/offline hotspots never cause phone browser timeouts
	resolveCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	tracks, err := r.ResolveInput(resolveCtx, input)
	cancel()

	now := time.Now().Unix()

	if err == nil && len(tracks) > 0 {
		// Safety guard: if user shared a single track link, never enqueue more than 1 track
		if parsed.Type == "track" && len(tracks) > 1 {
			tracks = tracks[:1]
		}

		var enqueued []store.QueueItem
		skippedDups := 0
		var firstDup *store.QueueItem

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
				rawRef := input
				if len(tracks) > 1 {
					rawRef = t.SpotifyURI
				}
				if dup, dErr := s.FindActiveDuplicate(guest.GuestID, t.TrackID, t.SpotifyURI, rawRef); dErr == nil && dup != nil {
					skippedDups++
					if firstDup == nil {
						firstDup = dup
					}
					continue
				}

				qi := store.QueueItem{
					SpotifyURI:   t.SpotifyURI,
					TrackID:      t.TrackID,
					RawInput:     rawRef,
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

		if target == "fallback" {
			msg := fmt.Sprintf("Added %d track(s) to fallback pool", len(tracks))
			return len(tracks), enqueued, msg, nil
		}
		if len(enqueued) == 0 && firstDup != nil {
			msg := fmt.Sprintf("Already in queue: %s — %s", firstDup.Title, firstDup.Artist)
			return 0, []store.QueueItem{*firstDup}, msg, nil
		}
		var msg string
		if len(enqueued) == 1 {
			msg = fmt.Sprintf("Queued: %s — %s", enqueued[0].Title, enqueued[0].Artist)
		} else {
			msg = fmt.Sprintf("Queued %d tracks: %s...", len(enqueued), enqueued[0].Title)
		}
		if skippedDups > 0 && len(enqueued) > 0 {
			msg = fmt.Sprintf("%s (%d duplicate skipped)", msg, skippedDups)
		}
		return len(enqueued), enqueued, msg, nil
	}

	// Network resolution failed! Apply Shaky-Connection Resilience:
	// Case A: Track ID is extractable from URL or URI
	if parsed.Type == "track" && parsed.ID != "" {
		spotifyURI := fmt.Sprintf("spotify:track:%s", parsed.ID)
		if dup, dErr := s.FindActiveDuplicate(guest.GuestID, parsed.ID, spotifyURI, input); dErr == nil && dup != nil {
			return 0, []store.QueueItem{*dup}, fmt.Sprintf("Already in queue: %s", dup.Title), nil
		}

		shortID := parsed.ID
		if len(shortID) > 6 {
			shortID = shortID[:6]
		}
		placeholderTitle := fmt.Sprintf("Spotify Track (%s)", shortID)
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
		if dup, dErr := s.FindActiveDuplicate(guest.GuestID, "", "", input); dErr == nil && dup != nil {
			return 0, []store.QueueItem{*dup}, "Link already in queue", nil
		}

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
