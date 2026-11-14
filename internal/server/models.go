package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/marcelkornblum/deckjam/internal/player"
	"github.com/marcelkornblum/deckjam/internal/store"
)

// StateSnapshot represents the response payload for GET /api/state and SSE state events.
type StateSnapshot struct {
	NowPlaying    *store.QueueItem    `json:"now_playing"`
	Queue         []store.QueueItem   `json:"queue"`
	History       []store.QueueItem   `json:"history"`
	FallbackCount int                 `json:"fallback_count"`
	Player        player.PlayerStatus `json:"player"`
	Settings      store.Settings      `json:"settings"`
	IsHost        bool                `json:"is_host"`
	HostPIN       string              `json:"host_pin"`
	JoinURL       string              `json:"join_url"`
	LanURLs       []string            `json:"lan_urls"`
	OnlineGuests  int                 `json:"online_guests"`
	ServerTimeMS  int64               `json:"server_time_ms"`
}

// ProgressPayload represents the 1s ticker progress event payload.
type ProgressPayload struct {
	Playing           bool  `json:"playing"`
	Paused            bool  `json:"paused"`
	PositionMS        int   `json:"position_ms"`
	DurationMS        int   `json:"duration_ms"`
	SkipVotes         int   `json:"skip_votes"`
	SkipVoteThreshold int   `json:"skip_vote_threshold"`
	OnlineGuests      int   `json:"online_guests"`
	ServerTimeMS      int64 `json:"server_time_ms"`
}

// SSEClient stores metadata for a connected SSE listener.
type SSEClient struct {
	GuestID string
	IsHost  bool
}

// SSEManager manages active SSE client connections and event broadcasting.
type SSEManager struct {
	mu      sync.RWMutex
	clients map[chan []byte]SSEClient
}

// NewSSEManager creates a new SSEManager.
func NewSSEManager() *SSEManager {
	return &SSEManager{
		clients: make(map[chan []byte]SSEClient),
	}
}

// AddClient registers an SSE client.
func (m *SSEManager) AddClient(ch chan []byte, guestID string, isHost bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[ch] = SSEClient{GuestID: guestID, IsHost: isHost}
}

// RemoveClient unregisters an SSE client.
func (m *SSEManager) RemoveClient(ch chan []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, ch)
	close(ch)
}

// ClientCount returns number of connected SSE clients.
func (m *SSEManager) ClientCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients)
}

// Broadcast sends an identical SSE formatted event to all connected clients.
func (m *SSEManager) Broadcast(event string, data []byte) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(data))
	msgBytes := []byte(msg)

	for ch := range m.clients {
		select {
		case ch <- msgBytes:
		default:
		}
	}
}

// BroadcastPerClient sends a per-client customized SSE payload.
func (m *SSEManager) BroadcastPerClient(event string, build func(SSEClient) ([]byte, error)) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for ch, info := range m.clients {
		data, err := build(info)
		if err != nil {
			continue
		}
		msgBytes := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(data)))
		select {
		case ch <- msgBytes:
		default:
		}
	}
}

// IsHostRequest determines if the HTTP request is from local host (127.0.0.1 / ::1) or carries valid X-Host-Pin / ?pin=.
func IsHostRequest(r *http.Request, validPIN string) bool {
	// 1. Check client IP
	remoteIP := r.RemoteAddr
	if host, _, err := netSplitHost(remoteIP); err == nil {
		remoteIP = host
	}
	if remoteIP == "127.0.0.1" || remoteIP == "::1" || remoteIP == "localhost" {
		return true
	}

	// 2. Check header or query pin
	if validPIN != "" {
		pinHeader := r.Header.Get("X-Host-Pin")
		if pinHeader == validPIN {
			return true
		}
		pinQuery := r.URL.Query().Get("pin")
		if pinQuery == validPIN {
			return true
		}
	}

	return false
}

func netSplitHost(addr string) (string, string, error) {
	if strings.HasPrefix(addr, "[") && strings.Contains(addr, "]:") {
		// ipv6 host:port
		parts := strings.Split(addr, "]:")
		return strings.TrimPrefix(parts[0], "["), parts[1], nil
	}
	if strings.Contains(addr, ":") {
		parts := strings.Split(addr, ":")
		return parts[0], parts[1], nil
	}
	return addr, "", nil
}

// DefaultVinylSVG returns a clean placeholder SVG for vinyl/album covers when offline.
func DefaultVinylSVG() []byte {
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 300 300" width="300" height="300">
  <defs>
    <radialGradient id="vinylGrad" cx="50%" cy="50%" r="50%">
      <stop offset="0%" stop-color="#121212"/>
      <stop offset="60%" stop-color="#181818"/>
      <stop offset="90%" stop-color="#282828"/>
      <stop offset="100%" stop-color="#121212"/>
    </radialGradient>
  </defs>
  <rect width="300" height="300" fill="#121212"/>
  <circle cx="150" cy="150" r="140" fill="url(#vinylGrad)"/>
  <circle cx="150" cy="150" r="130" stroke="#333333" stroke-width="1.5" fill="none" opacity="0.6"/>
  <circle cx="150" cy="150" r="110" stroke="#333333" stroke-width="1.5" fill="none" opacity="0.6"/>
  <circle cx="150" cy="150" r="90" stroke="#333333" stroke-width="1.5" fill="none" opacity="0.6"/>
  <circle cx="150" cy="150" r="70" stroke="#333333" stroke-width="1.5" fill="none" opacity="0.6"/>
  <circle cx="150" cy="150" r="48" fill="#1db954"/>
  <circle cx="150" cy="150" r="12" fill="#121212"/>
</svg>`)
}
