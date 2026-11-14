package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelkornblum/deckjam/internal/store"
)

func TestParseInput(t *testing.T) {
	cases := []struct {
		input    string
		wantType string
		wantID   string
	}{
		{
			input:    "spotify:track:4cOdK2wGLETKBW3PvgPWqT",
			wantType: "track",
			wantID:   "4cOdK2wGLETKBW3PvgPWqT",
		},
		{
			input:    "https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT?si=12345",
			wantType: "track",
			wantID:   "4cOdK2wGLETKBW3PvgPWqT",
		},
		{
			input:    "Listen to this track https://spotify.link/AbCdEf on Spotify!",
			wantType: "shortlink",
			wantID:   "",
		},
		{
			input:    "Check out album: https://open.spotify.com/album/3RQQmkQEvNCY4prGKE6oc5",
			wantType: "album",
			wantID:   "3RQQmkQEvNCY4prGKE6oc5",
		},
	}

	for _, c := range cases {
		p := ParseInput(c.input)
		if p.Type != c.wantType {
			t.Errorf("For input %s: expected type %s, got %s", c.input, c.wantType, p.Type)
		}
		if c.wantID != "" && p.ID != c.wantID {
			t.Errorf("For input %s: expected ID %s, got %s", c.input, c.wantID, p.ID)
		}
	}
}

func TestResolverScrapeAndOfflineFallback(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	// Mock server returning Spotify embed HTML with __NEXT_DATA__
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/shortlink") {
			// Redirect to mock track
			http.Redirect(w, r, "/track/test12345", http.StatusFound)
			return
		}
		if strings.Contains(r.URL.Path, "oembed") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"title":"Mock oEmbed Song","thumbnail_url":"http://mock/thumb.jpg"}`))
			return
		}
		// Return __NEXT_DATA__
		html := `
		<!doctype html>
		<html>
		<head>
		<script id="__NEXT_DATA__" type="application/json">
		{
			"props": {
				"pageProps": {
					"state": {
						"data": {
							"entity": {
								"type": "track",
								"name": "Mock Test Song",
								"uri": "spotify:track:test12345",
								"id": "test12345",
								"duration": 210000,
								"coverArt": {
									"sources": [{"url": "http://mock/art.jpg"}]
								},
								"artists": [{"name": "Mock Artist"}]
							}
						}
					}
				}
			}
		}
		</script>
		</head>
		<body></body>
		</html>`
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer mockServer.Close()

	resolver := NewResolver(st)

	// Direct test of scrapeEmbed with mock server
	tracks, err := resolver.scrapeEmbed(context.Background(), mockServer.URL+"/embed/track/test12345")
	if err != nil {
		t.Fatalf("scrapeEmbed failed: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Title != "Mock Test Song" || tracks[0].Artist != "Mock Artist" {
		t.Fatalf("Unexpected tracks: %+v", tracks)
	}

	// Test EnqueueWithResilience when network fails completely
	offlineResolver := NewResolver(st)
	// Point to non-routable port to simulate instant offline network failure
	offlineResolver.client.Timeout = 50 * time.Millisecond

	guest := store.Guest{GuestID: "g1", Name: "Guest 1", Color: "#ff0000"}

	// Case 1: Spotify Track URL offline -> Should enqueue with placeholder title!
	count, items, msg, err := EnqueueWithResilience(
		context.Background(),
		offlineResolver,
		st,
		"https://open.spotify.com/track/deadbeef9999",
		guest,
		"queue",
		"round_robin",
	)
	if err != nil {
		t.Fatalf("EnqueueWithResilience failed for track URL offline: %v", err)
	}
	if count != 1 || len(items) != 1 {
		t.Fatalf("Expected 1 item queued, got count=%d", count)
	}
	if items[0].Title != "Spotify Track (deadbe)" {
		t.Errorf("Expected placeholder title, got '%s'", items[0].Title)
	}
	if !strings.Contains(msg, "Spotify Track (deadbe)") {
		t.Errorf("Unexpected msg: %s", msg)
	}

	// Case 2: Spotify Shortlink offline -> Should enqueue with status "resolving"!
	count2, items2, _, err2 := EnqueueWithResilience(
		context.Background(),
		offlineResolver,
		st,
		"https://spotify.link/offlineShortlink",
		guest,
		"queue",
		"round_robin",
	)
	if err2 != nil {
		t.Fatalf("EnqueueWithResilience failed for shortlink offline: %v", err2)
	}
	if count2 != 1 || len(items2) != 1 {
		t.Fatalf("Expected 1 item queued, got count=%d", count2)
	}
	if items2[0].Status != "resolving" {
		t.Errorf("Expected status 'resolving', got '%s'", items2[0].Status)
	}

	// Verify items are in store resolving list
	resolving, err := st.GetResolvingItems()
	if err != nil {
		t.Fatalf("GetResolvingItems failed: %v", err)
	}
	if len(resolving) != 2 {
		t.Fatalf("Expected 2 resolving items, got %d", len(resolving))
	}
}
