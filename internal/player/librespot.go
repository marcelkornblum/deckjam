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
type LibrespotBackend struct {
	mu           sync.RWMutex
	baseURL      string
	client       *http.Client
	wsConn       *websocket.Conn
	playing      bool
	paused       bool
	currentURI   string
	positionMS   int
	durationMS   int
	volume       int
	pairCode     string
	connected    bool
	stopCh       chan struct{}
	onTrackEnded func()
}

// NewLibrespotBackend creates a new LibrespotBackend instance.
func NewLibrespotBackend(baseURL string, onTrackEnded func()) *LibrespotBackend {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:3678"
	}

	b := &LibrespotBackend{
		baseURL:      baseURL,
		client:       &http.Client{Timeout: 3 * time.Second},
		volume:       80,
		durationMS:   180000,
		onTrackEnded: onTrackEnded,
		stopCh:       make(chan struct{}),
	}

	go b.monitorLoop()
	return b
}

func (b *LibrespotBackend) monitorLoop() {
	ticker := time.NewTicker(3 * time.Second)
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

func (b *LibrespotBackend) checkStatus() {
	req, err := http.NewRequest("GET", b.baseURL+"/status", nil)
	if err != nil {
		return
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.mu.Lock()
		b.connected = false
		b.mu.Unlock()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var s struct {
			Playing    bool   `json:"playing"`
			Paused     bool   `json:"paused"`
			PositionMS int    `json:"position_ms"`
			DurationMS int    `json:"duration_ms"`
			Volume     int    `json:"volume"`
			PairCode   string `json:"pair_code"`
			URI        string `json:"uri"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&s); err == nil {
			b.mu.Lock()
			b.connected = true
			b.playing = s.Playing
			b.paused = s.Paused
			if s.PositionMS > 0 {
				b.positionMS = s.PositionMS
			}
			if s.DurationMS > 0 {
				b.durationMS = s.DurationMS
			}
			if s.Volume > 0 {
				b.volume = s.Volume
			}
			if s.PairCode != "" {
				b.pairCode = s.PairCode
			}
			if s.URI != "" {
				b.currentURI = s.URI
			}
			b.mu.Unlock()
		}
	}
}

func (b *LibrespotBackend) Name() string {
	return "librespot"
}

func (b *LibrespotBackend) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected
}

func (b *LibrespotBackend) Play(uri string) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"uri": uri,
	})
	resp, err := b.client.Post(b.baseURL+"/player/play", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("librespot play returned %d", resp.StatusCode)
	}

	b.mu.Lock()
	b.playing = true
	b.paused = false
	b.currentURI = uri
	b.positionMS = 0
	b.mu.Unlock()
	return nil
}

func (b *LibrespotBackend) Pause() error {
	resp, err := b.client.Post(b.baseURL+"/player/pause", "application/json", nil)
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
	resp, err := b.client.Post(b.baseURL+"/player/resume", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b.mu.Lock()
	b.paused = false
	b.mu.Unlock()
	return nil
}

func (b *LibrespotBackend) PlayPause() error {
	b.mu.RLock()
	paused := b.paused
	b.mu.RUnlock()

	if paused {
		return b.Resume()
	}
	return b.Pause()
}

func (b *LibrespotBackend) Stop() error {
	resp, err := b.client.Post(b.baseURL+"/player/stop", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b.mu.Lock()
	b.playing = false
	b.paused = false
	b.positionMS = 0
	b.mu.Unlock()
	return nil
}

func (b *LibrespotBackend) Seek(positionMS int) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"position_ms": positionMS,
	})
	resp, err := b.client.Post(b.baseURL+"/player/seek", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b.mu.Lock()
	b.positionMS = positionMS
	b.mu.Unlock()
	return nil
}

func (b *LibrespotBackend) SetVolume(volume int) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"volume": volume,
	})
	resp, err := b.client.Post(b.baseURL+"/player/volume", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
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
		LibrespotPairCode:  b.pairCode,
	}
}

func (b *LibrespotBackend) Close() error {
	close(b.stopCh)
	if b.wsConn != nil {
		b.wsConn.Close()
	}
	return nil
}
