package player

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// LibrespotBackend controls a local go-librespot daemon over HTTP REST and WebSocket events.
// Conforms to go-librespot 0.10.5 OpenAPI specification.
type LibrespotBackend struct {
	mu             sync.RWMutex
	baseURL        string
	pollClient     *http.Client
	cmdClient      *http.Client
	wsConn         *websocket.Conn
	playing        bool
	paused         bool
	hasLoadedTrack bool
	currentURI     string
	positionMS     int
	durationMS     int
	volume         int
	pairCode       string
	pairURL        string
	daemonUp       bool
	connected      bool // true when GET / returns 200 OK and playback_ready == true
	lastError      string
	lastPlayTime   time.Time
	stopCh         chan struct{}
	onTrackEnded   func()
}

// NewLibrespotBackend creates a new LibrespotBackend instance.
func NewLibrespotBackend(baseURL string, onTrackEnded func()) *LibrespotBackend {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:3678"
	}

	b := &LibrespotBackend{
		baseURL:      baseURL,
		pollClient:   &http.Client{Timeout: 2500 * time.Millisecond},
		cmdClient:    &http.Client{Timeout: 12 * time.Second},
		volume:       80,
		durationMS:   180000,
		onTrackEnded: onTrackEnded,
		stopCh:       make(chan struct{}),
	}

	go b.monitorLoop()
	go b.wsLoop()
	return b
}

func (b *LibrespotBackend) monitorLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// initial check
	b.checkStatus()

	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.checkStatus()
		}
	}
}

// checkStatus polls go-librespot daemon endpoints:
// 1. GET / -> {"playback_ready": bool}
// 2. GET /auth/code -> {"code": "...", "url": "..."}
// 3. GET /status -> 204 No Content when idle, or 200 OK with {stopped, paused, volume, volume_steps, track: {uri, position, duration}}
func (b *LibrespotBackend) checkStatus() {
	// 1. Poll GET / for playback_ready
	reqRoot, err := http.NewRequest("GET", b.baseURL+"/", nil)
	if err != nil {
		return
	}
	respRoot, err := b.pollClient.Do(reqRoot)
	if err != nil {
		b.mu.Lock()
		b.daemonUp = false
		b.connected = false
		b.mu.Unlock()
		return
	}
	defer respRoot.Body.Close()

	daemonUp := respRoot.StatusCode == http.StatusOK
	playbackReady := false
	if daemonUp {
		var rootData struct {
			PlaybackReady bool `json:"playback_ready"`
		}
		if err := json.NewDecoder(respRoot.Body).Decode(&rootData); err == nil {
			playbackReady = rootData.PlaybackReady
		}
	}

	// 2. Poll GET /auth/code for pairing info (even when playback_ready is false)
	pairCode := ""
	pairURL := ""
	reqAuth, err := http.NewRequest("GET", b.baseURL+"/auth/code", nil)
	if err == nil {
		respAuth, err := b.pollClient.Do(reqAuth)
		if err == nil {
			defer respAuth.Body.Close()
			if respAuth.StatusCode == http.StatusOK {
				var authData struct {
					Code string `json:"code"`
					URL  string `json:"url"`
				}
				if err := json.NewDecoder(respAuth.Body).Decode(&authData); err == nil {
					pairCode = authData.Code
					pairURL = authData.URL
				}
			}
		}
	}

	// 3. Poll GET /status (204 No Content when idle, 200 OK with track info when active)
	var trackURI string
	var trackPos, trackDur, volVal int
	isPaused := false
	isStopped := false
	hasStatusUpdate := false
	status204 := false
	hasTrack := false

	reqStatus, err := http.NewRequest("GET", b.baseURL+"/status", nil)
	if err == nil {
		respStatus, err := b.pollClient.Do(reqStatus)
		if err == nil {
			defer respStatus.Body.Close()
			if respStatus.StatusCode == http.StatusOK {
				hasStatusUpdate = true
				var statusData struct {
					Stopped     bool `json:"stopped"`
					Paused      bool `json:"paused"`
					Volume      int  `json:"volume"`
					VolumeSteps int  `json:"volume_steps"`
					Track       *struct {
						URI      string `json:"uri"`
						Position int    `json:"position"`
						Duration int    `json:"duration"`
					} `json:"track"`
				}
				if err := json.NewDecoder(respStatus.Body).Decode(&statusData); err == nil {
					isStopped = statusData.Stopped
					isPaused = statusData.Paused
					volVal = statusData.Volume
					hasTrack = statusData.Track != nil && statusData.Track.URI != ""
					if statusData.Track != nil {
						trackURI = statusData.Track.URI
						trackPos = statusData.Track.Position
						trackDur = statusData.Track.Duration
					}
				}
			} else if respStatus.StatusCode == http.StatusNoContent {
				hasStatusUpdate = true
				status204 = true
			}
		}
	}

	b.mu.Lock()
	b.daemonUp = daemonUp
	b.connected = daemonUp && playbackReady
	if pairCode != "" {
		b.pairCode = pairCode
		b.pairURL = pairURL
	} else if playbackReady {
		// Pairing completed! Clear pair code/URL
		b.pairCode = ""
		b.pairURL = ""
	}

	if hasStatusUpdate {
		if volVal > 0 {
			b.volume = volVal
		}
		loadingGrace := time.Since(b.lastPlayTime) < 6*time.Second && b.lastError == "" && b.currentURI != ""
		if status204 || isStopped || !hasTrack {
			if !loadingGrace {
				b.playing = false
				b.paused = false
				b.hasLoadedTrack = false
				b.currentURI = ""
			}
		} else {
			b.hasLoadedTrack = true
			b.paused = isPaused
			b.playing = !isPaused
			b.currentURI = trackURI
			if trackPos >= 0 {
				b.positionMS = trackPos
			}
			if trackDur > 0 {
				b.durationMS = trackDur
			}
		}
	}
	b.mu.Unlock()
}

// wsLoop manages persistent WebSocket connection to /events with auto-reconnect.
func (b *LibrespotBackend) wsLoop() {
	wsURL := strings.Replace(b.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1) + "/events"

	for {
		select {
		case <-b.stopCh:
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		b.mu.Lock()
		b.wsConn = conn
		b.mu.Unlock()

		b.consumeEvents(conn)

		b.mu.Lock()
		if b.wsConn == conn {
			b.wsConn = nil
		}
		b.mu.Unlock()

		time.Sleep(1 * time.Second)
	}
}

// consumeEvents reads and processes events from /events WebSocket.
func (b *LibrespotBackend) consumeEvents(conn *websocket.Conn) {
	defer conn.Close()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		// go-librespot event structure: {"event": "playing", "data": {...}}
		var ev struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(msg, &ev); err != nil {
			continue
		}

		switch ev.Event {
		case "playback_ready":
			b.mu.Lock()
			b.daemonUp = true
			b.connected = true
			b.pairCode = ""
			b.pairURL = ""
			b.lastError = ""
			b.mu.Unlock()

		case "playback_error", "playback_failed", "error":
			var d struct {
				Error   string `json:"error"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			}
			errText := "Spotify playback error"
			if json.Unmarshal(ev.Data, &d) == nil {
				if d.Error != "" {
					errText = d.Error
				} else if d.Reason != "" {
					errText = d.Reason
				} else if d.Message != "" {
					errText = d.Message
				}
			}
			b.mu.Lock()
			b.playing = false
			b.paused = false
			b.hasLoadedTrack = false
			b.lastError = errText
			b.mu.Unlock()

		case "playing":
			b.mu.Lock()
			b.playing = true
			b.paused = false
			b.hasLoadedTrack = true
			b.lastError = ""
			var d struct {
				URI string `json:"uri"`
			}
			if json.Unmarshal(ev.Data, &d) == nil && d.URI != "" {
				b.currentURI = d.URI
			}
			b.mu.Unlock()

		case "paused":
			b.mu.Lock()
			b.paused = true
			b.mu.Unlock()

		case "seek":
			var d struct {
				Position int    `json:"position"`
				Duration int    `json:"duration"`
				URI      string `json:"uri"`
			}
			if json.Unmarshal(ev.Data, &d) == nil {
				b.mu.Lock()
				b.positionMS = d.Position
				if d.Duration > 0 {
					b.durationMS = d.Duration
				}
				if d.URI != "" {
					b.currentURI = d.URI
				}
				b.mu.Unlock()
			}

		case "volume":
			var d struct {
				Value int `json:"value"`
			}
			if json.Unmarshal(ev.Data, &d) == nil {
				b.mu.Lock()
				b.volume = d.Value
				b.mu.Unlock()
			}

		case "not_playing", "stopped":
			b.mu.Lock()
			b.playing = false
			b.paused = false
			b.hasLoadedTrack = false
			recentPlay := time.Since(b.lastPlayTime) < 1500*time.Millisecond
			b.mu.Unlock()

			// Advance queue cleanly without double-advancing when Play() just switched tracks within the last 1.5s
			if !recentPlay && b.onTrackEnded != nil {
				go b.onTrackEnded()
			}
		}
	}
}

// PairInfo returns pairing code, pair URL, and daemon reachability.
func (b *LibrespotBackend) PairInfo() (code, url string, daemonUp bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.pairCode, b.pairURL, b.daemonUp
}

func (b *LibrespotBackend) Name() string {
	return "librespot"
}

func (b *LibrespotBackend) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected
}

// DaemonUp reports whether the local go-librespot HTTP API is reachable.
func (b *LibrespotBackend) DaemonUp() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.daemonUp
}

// LastError returns the most recent playback/API error reported by go-librespot.
func (b *LibrespotBackend) LastError() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.lastError
}

// IsTransportPlaying reports whether go-librespot metadata indicates active unpaused playback.
func (b *LibrespotBackend) IsTransportPlaying() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.playing && !b.paused && b.hasLoadedTrack
}

// HasLoadedTrack reports whether librespot currently has a track loaded (matching uri if specified).
func (b *LibrespotBackend) HasLoadedTrack(uri string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.hasLoadedTrack {
		return false
	}
	return uri == "" || b.currentURI == uri
}

func (b *LibrespotBackend) Play(uri string) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"uri": uri,
	})
	resp, err := b.cmdClient.Post(b.baseURL+"/player/play", "application/json", bytes.NewReader(payload))
	if err != nil {
		b.mu.Lock()
		b.lastError = fmt.Sprintf("go-librespot play request failed: %v", err)
		b.mu.Unlock()
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errBuf [256]byte
		n, _ := resp.Body.Read(errBuf[:])
		bodyMsg := strings.TrimSpace(string(errBuf[:n]))
		errMsg := fmt.Sprintf("librespot play returned %d", resp.StatusCode)
		if bodyMsg != "" {
			errMsg = fmt.Sprintf("librespot play (%d): %s", resp.StatusCode, bodyMsg)
		}
		b.mu.Lock()
		b.lastError = errMsg
		b.mu.Unlock()
		return fmt.Errorf("%s", errMsg)
	}

	b.mu.Lock()
	b.playing = true
	b.paused = false
	b.hasLoadedTrack = true
	b.currentURI = uri
	b.positionMS = 0
	b.lastError = ""
	b.lastPlayTime = time.Now()
	b.mu.Unlock()
	return nil
}

func (b *LibrespotBackend) Pause() error {
	resp, err := b.cmdClient.Post(b.baseURL+"/player/pause", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b.mu.Lock()
	b.paused = true
	b.mu.Unlock()
	return nil
}

func (b *LibrespotBackend) Resume() error {
	b.mu.RLock()
	hasLoaded := b.hasLoadedTrack
	currURI := b.currentURI
	b.mu.RUnlock()

	if !hasLoaded {
		if currURI != "" {
			return b.Play(currURI)
		}
		return fmt.Errorf("no track loaded in librespot")
	}

	resp, err := b.cmdClient.Post(b.baseURL+"/player/resume", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("librespot resume returned %d", resp.StatusCode)
	}

	b.mu.Lock()
	b.paused = false
	b.playing = true
	b.mu.Unlock()
	return nil
}

func (b *LibrespotBackend) PlayPause() error {
	b.mu.RLock()
	playing := b.playing
	paused := b.paused
	hasLoaded := b.hasLoadedTrack
	currURI := b.currentURI
	b.mu.RUnlock()

	if playing && !paused {
		return b.Pause()
	}
	if paused && hasLoaded {
		return b.Resume()
	}
	if currURI != "" {
		return b.Play(currURI)
	}
	return fmt.Errorf("no track loaded in librespot")
}

func (b *LibrespotBackend) Stop() error {
	resp, err := b.cmdClient.Post(b.baseURL+"/player/stop", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b.mu.Lock()
	b.playing = false
	b.paused = false
	b.hasLoadedTrack = false
	b.positionMS = 0
	b.mu.Unlock()
	return nil
}

// Seek sends {"position": posMS} to POST /player/seek per go-librespot spec
func (b *LibrespotBackend) Seek(positionMS int) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"position": positionMS,
	})
	resp, err := b.cmdClient.Post(b.baseURL+"/player/seek", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("librespot seek returned %d", resp.StatusCode)
	}

	b.mu.Lock()
	b.positionMS = positionMS
	b.mu.Unlock()
	return nil
}

// SetVolume sends {"volume": vol} to POST /player/volume per go-librespot spec
func (b *LibrespotBackend) SetVolume(volume int) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"volume": volume,
	})
	resp, err := b.cmdClient.Post(b.baseURL+"/player/volume", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("librespot volume returned %d", resp.StatusCode)
	}

	b.mu.Lock()
	b.volume = volume
	b.mu.Unlock()
	return nil
}

func (b *LibrespotBackend) GetStatus() PlayerStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return PlayerStatus{
		ActiveBackend:      "librespot",
		ConfiguredBackend:  "librespot",
		Playing:            b.playing,
		Paused:             b.paused,
		PositionMS:         b.positionMS,
		DurationMS:         b.durationMS,
		Volume:             b.volume,
		LibrespotConnected: b.connected,
		LibrespotDaemonUp:  b.daemonUp,
		LibrespotPairCode:  b.pairCode,
		LibrespotPairURL:   b.pairURL,
		LastError:          b.lastError,
	}
}

func (b *LibrespotBackend) Close() error {
	select {
	case <-b.stopCh:
		// already closed
	default:
		close(b.stopCh)
	}
	b.mu.Lock()
	if b.wsConn != nil {
		b.wsConn.Close()
		b.wsConn = nil
	}
	b.mu.Unlock()
	return nil
}
