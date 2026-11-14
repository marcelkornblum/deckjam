package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcelkornblum/deckjam/internal/player"
	"github.com/marcelkornblum/deckjam/internal/spotify"
	"github.com/marcelkornblum/deckjam/internal/store"
)

func setupTestServer(t *testing.T) (*Server, *store.Store, *player.Coordinator) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open store failed: %v", err)
	}

	res := spotify.NewResolver(st)
	coord := player.NewCoordinator(st, "", "mock")

	srv := NewServer(Config{
		Addr:        "127.0.0.1:0",
		Store:       st,
		Resolver:    res,
		Coordinator: coord,
		HostPIN:     "9876",
	})

	return srv, st, coord
}

func TestServerEndpoints(t *testing.T) {
	srv, st, coord := setupTestServer(t)
	defer st.Close()
	defer coord.Close()

	mux := http.NewServeMux()
	srv.routes(mux)
	ts := httptest.NewServer(srv.corsMiddleware(mux))
	defer ts.Close()

	client := ts.Client()

	// 1. GET /api/state
	req, _ := http.NewRequest("GET", ts.URL+"/api/state", nil)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/state failed: status=%d, err=%v", resp.StatusCode, err)
	}
	var snap StateSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("Failed to decode state: %v", err)
	}
	if snap.Settings.RoomName != "Steam Deck Jam" {
		t.Errorf("Unexpected room name: %s", snap.Settings.RoomName)
	}

	// 2. POST /api/guest
	guestPayload := `{"guest_id":"guest-123","name":"Tester","color":"#ff00ff"}`
	resp, err = client.Post(ts.URL+"/api/guest", "application/json", strings.NewReader(guestPayload))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/guest failed: status=%d, err=%v", resp.StatusCode, err)
	}

	// 3. POST /api/queue (resilient enqueuing offline/placeholder)
	queuePayload := `{"input":"https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT","guest_id":"guest-123","guest_name":"Tester"}`
	resp, err = client.Post(ts.URL+"/api/queue", "application/json", strings.NewReader(queuePayload))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/queue failed: status=%d, err=%v", resp.StatusCode, err)
	}
	var qResp map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&qResp)
	if !qResp["ok"].(bool) {
		t.Errorf("POST /api/queue returned ok=false: %+v", qResp)
	}

	// 4. GET /share redirect test
	req, _ = http.NewRequest("GET", ts.URL+"/share?url=https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT", nil)
	// Don't follow redirect
	clientNoRedirect := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err = clientNoRedirect.Do(req)
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /share expected 303 See Other, got %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if !strings.Contains(location, "shared=1") {
		t.Errorf("Unexpected redirect location: %s", location)
	}

	// 5. POST /api/quick-share test
	qsPayload := `{"url":"https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT","guest_id":"g1"}`
	resp, err = client.Post(ts.URL+"/api/quick-share", "application/json", strings.NewReader(qsPayload))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/quick-share failed: status=%d", resp.StatusCode)
	}

	// 6. Host Verify (with PIN header)
	req, _ = http.NewRequest("GET", ts.URL+"/api/host/verify", nil)
	req.Header.Set("X-Host-Pin", "9876")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/host/verify with valid pin failed: status=%d", resp.StatusCode)
	}

	// 7. Host Control (Pause / Resume / Settings)
	controlPayload := `{"action":"pause"}`
	req, _ = http.NewRequest("POST", ts.URL+"/api/host/control", strings.NewReader(controlPayload))
	req.Header.Set("X-Host-Pin", "9876")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/host/control pause failed: status=%d", resp.StatusCode)
	}

	// 8. GET /api/artwork/unknown returns SVG vinyl cover
	resp, err = client.Get(ts.URL + "/api/artwork/unknown")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/artwork/unknown failed: status=%d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "image/svg+xml" {
		t.Errorf("Expected image/svg+xml for artwork placeholder, got %s", resp.Header.Get("Content-Type"))
	}

	// 9. GET /api/qr.svg returns crisp SVG QR
	resp, err = client.Get(ts.URL + "/api/qr.svg?data=http://192.168.1.50:8080")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/qr.svg failed: status=%d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "image/svg+xml" {
		t.Errorf("Expected image/svg+xml for QR code, got %s", resp.Header.Get("Content-Type"))
	}

	// 10. GET /api/shortcuts/http-shortcuts.json
	resp, err = client.Get(ts.URL + "/api/shortcuts/http-shortcuts.json?guest_id=g1&guest_name=Alex")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/shortcuts/http-shortcuts.json failed: status=%d", resp.StatusCode)
	}
	var shortcutConfig map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&shortcutConfig); err != nil {
		t.Fatalf("Failed to decode shortcuts json: %v", err)
	}
	if shortcutConfig["schema"].(float64) != 3 {
		t.Errorf("Unexpected shortcut schema: %v", shortcutConfig["schema"])
	}
}

func TestSSEEvents(t *testing.T) {
	srv, st, coord := setupTestServer(t)
	defer st.Close()
	defer coord.Close()

	mux := http.NewServeMux()
	srv.routes(mux)
	ts := httptest.NewServer(srv.corsMiddleware(mux))
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/events?guest_id=sse-guest", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/events failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}

	// Read first event (state)
	buf := make([]byte, 1024)
	n, err := resp.Body.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("Failed to read initial SSE event: %v", err)
	}

	initialEvent := string(buf[:n])
	if !strings.Contains(initialEvent, "event: state") {
		t.Errorf("Expected initial event to be 'state', got: %s", initialEvent)
	}
}

func TestStaticRoutingAndMIMETypes(t *testing.T) {
	srv, st, coord := setupTestServer(t)
	defer st.Close()
	defer coord.Close()

	mux := http.NewServeMux()
	srv.routes(mux)
	ts := httptest.NewServer(srv.corsMiddleware(mux))
	defer ts.Close()

	client := ts.Client()

	tests := []struct {
		path        string
		expectedCode int
		expectedMIME string
	}{
		{"/", http.StatusOK, "text/html"},
		{"/deck", http.StatusOK, "text/html"},
		{"/styles.css", http.StatusOK, "text/css"},
		{"/app.js", http.StatusOK, "text/javascript"},
		{"/deck.css", http.StatusOK, "text/css"},
		{"/deck.js", http.StatusOK, "text/javascript"},
		{"/manifest.webmanifest", http.StatusOK, "application/manifest+json"},
		{"/sw.js", http.StatusOK, "text/javascript"},
		{"/icon.svg", http.StatusOK, "image/svg+xml"},
	}

	for _, tt := range tests {
		resp, err := client.Get(ts.URL + tt.path)
		if err != nil {
			t.Errorf("GET %s failed: %v", tt.path, err)
			continue
		}
		if resp.StatusCode != tt.expectedCode {
			t.Errorf("GET %s status: got %d, want %d", tt.path, resp.StatusCode, tt.expectedCode)
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, tt.expectedMIME) {
			t.Errorf("GET %s Content-Type: got %q, want containing %q", tt.path, ct, tt.expectedMIME)
		}
		resp.Body.Close()
	}
}

func TestHostAuthRules(t *testing.T) {
	// Remote non-localhost IP test
	req, _ := http.NewRequest("GET", "http://example.com/api/host/verify", nil)
	req.RemoteAddr = "192.168.1.105:54321"

	// 1. Without pin -> not host
	if IsHostRequest(req, "1234") {
		t.Errorf("Remote request without pin should not be host")
	}

	// 2. With valid X-Host-Pin -> is host
	req.Header.Set("X-Host-Pin", "1234")
	if !IsHostRequest(req, "1234") {
		t.Errorf("Remote request with valid X-Host-Pin should be host")
	}

	// 3. Localhost request -> always host
	localReq, _ := http.NewRequest("GET", "http://127.0.0.1:8080/api/host/verify", nil)
	localReq.RemoteAddr = "127.0.0.1:54321"
	if !IsHostRequest(localReq, "1234") {
		t.Errorf("127.0.0.1 request should always be host")
	}
}
