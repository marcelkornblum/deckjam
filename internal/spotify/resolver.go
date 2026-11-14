package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/marcelkornblum/deckjam/internal/store"
)

var (
	urlRegex      = regexp.MustCompile(`https?://[^\s<>"]+`)
	spotifyURIReg = regexp.MustCompile(`spotify:(track|album|playlist):([a-zA-Z0-9]+)`)
	webURLReg     = regexp.MustCompile(`open\.spotify\.com/(track|album|playlist)/([a-zA-Z0-9]+)`)
	nextDataReg   = regexp.MustCompile(`(?s)<script\s+id="__NEXT_DATA__"\s+type="application/json">\s*({.+?})\s*</script>`)
	ogTitleReg    = regexp.MustCompile(`<meta\s+property="og:title"\s+content="([^"]+)"`)
	ogImageReg    = regexp.MustCompile(`<meta\s+property="og:image"\s+content="([^"]+)"`)
	ogDescReg     = regexp.MustCompile(`<meta\s+property="og:description"\s+content="([^"]+)"`)
)

// ParsedInput holds information parsed from user input text.
type ParsedInput struct {
	Type      string // "track", "album", "playlist", "shortlink", "unknown"
	ID        string // Spotify ID (if extracted directly)
	TargetURL string // Canonical or extracted URL
	RawInput  string
}

// Resolver resolves Spotify tracks and links to metadata with caching and fallbacks.
type Resolver struct {
	client    *http.Client
	store     *store.Store
	UserAgent string
}

// NewResolver creates a new Resolver.
func NewResolver(st *store.Store) *Resolver {
	return &Resolver{
		client: &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
				return nil
			},
		},
		store:     st,
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	}
}

// ParseInput extracts Spotify type, ID, and URL from arbitrary text.
func ParseInput(text string) ParsedInput {
	text = strings.TrimSpace(text)
	pi := ParsedInput{
		Type:     "unknown",
		RawInput: text,
	}

	// 1. Check URI: spotify:track:xxx
	if m := spotifyURIReg.FindStringSubmatch(text); len(m) > 2 {
		pi.Type = m[1]
		pi.ID = m[2]
		pi.TargetURL = fmt.Sprintf("https://open.spotify.com/%s/%s", m[1], m[2])
		return pi
	}

	// 2. Check standard URL: open.spotify.com/track/xxx
	if m := webURLReg.FindStringSubmatch(text); len(m) > 2 {
		pi.Type = m[1]
		pi.ID = m[2]
		pi.TargetURL = fmt.Sprintf("https://open.spotify.com/%s/%s", m[1], m[2])
		return pi
	}

	// 3. Find any URL in text (e.g. spotify.link or spoti.fi)
	foundURL := urlRegex.FindString(text)
	if foundURL != "" {
		pi.TargetURL = foundURL
		if strings.Contains(foundURL, "spotify.link") || strings.Contains(foundURL, "spoti.fi") {
			pi.Type = "shortlink"
			return pi
		}
		// If web URL inside text
		if m := webURLReg.FindStringSubmatch(foundURL); len(m) > 2 {
			pi.Type = m[1]
			pi.ID = m[2]
			pi.TargetURL = fmt.Sprintf("https://open.spotify.com/%s/%s", m[1], m[2])
			return pi
		}
	}

	return pi
}

// ResolveInput resolves an arbitrary Spotify string, URL, or shortlink into a list of TrackMetadata.
// For tracks, returns 1 item. For albums/playlists, returns up to 50 tracks.
func (r *Resolver) ResolveInput(ctx context.Context, input string) ([]store.TrackMetadata, error) {
	parsed := ParseInput(input)

	// If shortlink, follow redirects
	if parsed.Type == "shortlink" || (parsed.Type == "unknown" && strings.Contains(parsed.TargetURL, "spotify")) {
		expandedURL, err := r.followRedirect(ctx, parsed.TargetURL)
		if err == nil && expandedURL != "" {
			parsed = ParseInput(expandedURL)
			parsed.RawInput = input
		}
	}

	// If single track and cached in SQLite, return cached metadata immediately!
	if parsed.Type == "track" && parsed.ID != "" && r.store != nil {
		cached, err := r.store.GetTrackCache(parsed.ID)
		if err == nil && cached != nil && cached.Title != "" {
			// Trigger background thumbnail fetch if local_thumb is missing
			if len(cached.LocalThumb) == 0 && cached.ThumbnailURL != "" {
				go r.EnsureLocalThumbnail(context.Background(), cached.TrackID, cached.ThumbnailURL)
			}
			return []store.TrackMetadata{*cached}, nil
		}
	}

	// If we still don't have track/album/playlist or ID, try scraping target URL
	if parsed.Type == "unknown" || parsed.ID == "" {
		if parsed.TargetURL != "" {
			meta, err := r.scrapeEmbed(ctx, parsed.TargetURL)
			if err == nil && len(meta) > 0 {
				return meta, nil
			}
		}
		return nil, fmt.Errorf("unable to identify Spotify entity in: %s", input)
	}

	embedURL := fmt.Sprintf("https://open.spotify.com/embed/%s/%s", parsed.Type, parsed.ID)
	tracks, err := r.scrapeEmbed(ctx, embedURL)
	if err == nil && len(tracks) > 0 {
		r.cacheTracks(tracks)
		return tracks, nil
	}

	// If embed failed and it's a track, try oEmbed fallback
	if parsed.Type == "track" {
		track, err := r.fetchOEmbed(ctx, parsed.TargetURL)
		if err == nil && track != nil {
			tracks = []store.TrackMetadata{*track}
			r.cacheTracks(tracks)
			return tracks, nil
		}

		// Try OpenGraph scrape fallback
		track, err = r.scrapeOpenGraph(ctx, parsed.TargetURL, parsed.ID)
		if err == nil && track != nil {
			tracks = []store.TrackMetadata{*track}
			r.cacheTracks(tracks)
			return tracks, nil
		}
	}

	return nil, fmt.Errorf("failed to resolve Spotify metadata for %s: %w", input, err)
}

func (r *Resolver) cacheTracks(tracks []store.TrackMetadata) {
	if r.store == nil {
		return
	}
	for i := range tracks {
		t := tracks[i]
		_ = r.store.SaveTrackCache(&t)
		if t.ThumbnailURL != "" {
			go r.EnsureLocalThumbnail(context.Background(), t.TrackID, t.ThumbnailURL)
		}
	}
}

func (r *Resolver) followRedirect(ctx context.Context, targetURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	return resp.Request.URL.String(), nil
}

// NextData structures for parsing __NEXT_DATA__ JSON
type nextData struct {
	Props struct {
		PageProps struct {
			State struct {
				Data struct {
					Entity struct {
						Type      string `json:"type"`
						Name      string `json:"name"`
						URI       string `json:"uri"`
						ID        string `json:"id"`
						Duration  int    `json:"duration"` // duration_ms
						CoverArt struct {
							Sources []struct {
								URL string `json:"url"`
							} `json:"sources"`
						} `json:"coverArt"`
						VisualIdentity struct {
							Image []struct {
								URL      string `json:"url"`
								MaxWidth int    `json:"maxWidth"`
							} `json:"image"`
						} `json:"visualIdentity"`
						Artists []struct {
							Name string `json:"name"`
						} `json:"artists"`
						Album struct {
							Name     string `json:"name"`
							CoverArt struct {
								Sources []struct {
									URL string `json:"url"`
								} `json:"sources"`
							} `json:"coverArt"`
						} `json:"album"`
						TrackList []struct {
							URI      string `json:"uri"`
							ID       string `json:"id"`
							Title    string `json:"title"`
							Subtitle string `json:"subtitle"` // artist
							Duration int    `json:"duration"`
						} `json:"trackList"`
					} `json:"entity"`
				} `json:"data"`
			} `json:"state"`
		} `json:"pageProps"`
	} `json:"props"`
}

func (r *Resolver) scrapeEmbed(ctx context.Context, embedURL string) ([]store.TrackMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", embedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed responded with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	match := nextDataReg.FindSubmatch(body)
	if len(match) < 2 {
		return nil, fmt.Errorf("__NEXT_DATA__ script not found in embed")
	}

	var data nextData
	if err := json.Unmarshal(match[1], &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal __NEXT_DATA__: %w", err)
	}

	entity := data.Props.PageProps.State.Data.Entity
	if entity.Name == "" && entity.URI == "" {
		return nil, fmt.Errorf("empty entity in __NEXT_DATA__")
	}

	// Cover art URL (prefer highest-resolution visualIdentity.image from modern Spotify embeds)
	coverURL := ""
	bestWidth := -1
	for _, img := range entity.VisualIdentity.Image {
		if img.URL != "" && img.MaxWidth >= bestWidth {
			bestWidth = img.MaxWidth
			coverURL = img.URL
		}
	}
	if coverURL == "" {
		if len(entity.CoverArt.Sources) > 0 {
			coverURL = entity.CoverArt.Sources[0].URL
		} else if len(entity.Album.CoverArt.Sources) > 0 {
			coverURL = entity.Album.CoverArt.Sources[0].URL
		}
	}

	// Single track
	if entity.Type == "track" || (len(entity.TrackList) == 0 && entity.URI != "") {
		artistNames := make([]string, 0, len(entity.Artists))
		for _, a := range entity.Artists {
			artistNames = append(artistNames, a.Name)
		}
		artist := strings.Join(artistNames, ", ")
		if artist == "" && entity.Album.Name != "" {
			artist = entity.Album.Name
		}

		trackID := entity.ID
		if trackID == "" && strings.HasPrefix(entity.URI, "spotify:track:") {
			trackID = strings.TrimPrefix(entity.URI, "spotify:track:")
		}

		spotifyURI := entity.URI
		if spotifyURI == "" && trackID != "" {
			spotifyURI = "spotify:track:" + trackID
		}

		return []store.TrackMetadata{{
			TrackID:      trackID,
			SpotifyURI:   spotifyURI,
			Title:        entity.Name,
			Artist:       artist,
			Album:        entity.Album.Name,
			ThumbnailURL: coverURL,
			DurationMS:   entity.Duration,
		}}, nil
	}

	// Album or Playlist
	var tracks []store.TrackMetadata
	limit := 50
	if len(entity.TrackList) < limit {
		limit = len(entity.TrackList)
	}

	albumOrPlaylistName := entity.Name

	for i := 0; i < limit; i++ {
		t := entity.TrackList[i]
		trackID := t.ID
		if trackID == "" && strings.HasPrefix(t.URI, "spotify:track:") {
			trackID = strings.TrimPrefix(t.URI, "spotify:track:")
		}
		if trackID == "" {
			continue
		}
		spotifyURI := t.URI
		if spotifyURI == "" {
			spotifyURI = "spotify:track:" + trackID
		}

		tracks = append(tracks, store.TrackMetadata{
			TrackID:      trackID,
			SpotifyURI:   spotifyURI,
			Title:        t.Title,
			Artist:       t.Subtitle,
			Album:        albumOrPlaylistName,
			ThumbnailURL: coverURL,
			DurationMS:   t.Duration,
		})
	}

	return tracks, nil
}

type oEmbedResponse struct {
	Title        string `json:"title"`
	ThumbnailURL string `json:"thumbnail_url"`
}

func (r *Resolver) fetchOEmbed(ctx context.Context, targetURL string) (*store.TrackMetadata, error) {
	oembedAPI := fmt.Sprintf("https://open.spotify.com/oembed?url=%s", url.QueryEscape(targetURL))
	req, err := http.NewRequestWithContext(ctx, "GET", oembedAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oembed status %d", resp.StatusCode)
	}

	var o oEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		return nil, err
	}

	parsed := ParseInput(targetURL)
	return &store.TrackMetadata{
		TrackID:      parsed.ID,
		SpotifyURI:   "spotify:track:" + parsed.ID,
		Title:        o.Title,
		Artist:       "Spotify Artist",
		ThumbnailURL: o.ThumbnailURL,
		DurationMS:   180000,
	}, nil
}

func (r *Resolver) scrapeOpenGraph(ctx context.Context, targetURL, trackID string) (*store.TrackMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var title, image, desc string
	if m := ogTitleReg.FindSubmatch(body); len(m) > 1 {
		title = html.UnescapeString(string(m[1]))
	}
	if m := ogImageReg.FindSubmatch(body); len(m) > 1 {
		image = string(m[1])
	}
	if m := ogDescReg.FindSubmatch(body); len(m) > 1 {
		desc = html.UnescapeString(string(m[1]))
	}

	if title == "" {
		return nil, fmt.Errorf("no og:title found")
	}

	artist := desc
	if artist == "" {
		artist = "Spotify Artist"
	}

	return &store.TrackMetadata{
		TrackID:      trackID,
		SpotifyURI:   "spotify:track:" + trackID,
		Title:        title,
		Artist:       artist,
		ThumbnailURL: image,
		DurationMS:   180000,
	}, nil
}

// EnsureLocalThumbnail fetches cover image bytes from coverURL and saves to SQLite track_cache.
func (r *Resolver) EnsureLocalThumbnail(ctx context.Context, trackID, coverURL string) ([]byte, string, error) {
	if r.store != nil {
		cached, err := r.store.GetTrackCache(trackID)
		if err == nil && cached != nil && len(cached.LocalThumb) > 0 {
			mime := cached.ThumbMIME
			if mime == "" {
				mime = "image/jpeg"
			}
			return cached.LocalThumb, mime, nil
		}
	}

	if coverURL == "" {
		return nil, "", fmt.Errorf("no cover url")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", coverURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("failed to fetch thumbnail: status %d", resp.StatusCode)
	}

	bytes, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // limit to 512KB
	if err != nil {
		return nil, "", err
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/jpeg"
	}

	if r.store != nil {
		_ = r.store.SaveTrackThumb(trackID, bytes, mimeType)
	}

	return bytes, mimeType, nil
}
