package player

import (
	"sync"
	"time"
)

// MockBackend implements a simulated player with a 500ms ticker advancing position_ms.
type MockBackend struct {
	mu            sync.RWMutex
	playing       bool
	paused        bool
	currentURI    string
	positionMS    int
	durationMS    int
	volume        int
	ticker        *time.Ticker
	stopCh        chan struct{}
	onTrackEnded  func()
}

// NewMockBackend creates a new MockBackend.
func NewMockBackend(onTrackEnded func()) *MockBackend {
	m := &MockBackend{
		volume:       80,
		durationMS:   180000,
		onTrackEnded: onTrackEnded,
		stopCh:       make(chan struct{}),
	}
	m.startTicker()
	return m
}

func (m *MockBackend) startTicker() {
	m.ticker = time.NewTicker(500 * time.Millisecond)
	go func() {
		for {
			select {
			case <-m.stopCh:
				return
			case <-m.ticker.C:
				m.tick()
			}
		}
	}()
}

func (m *MockBackend) tick() {
	m.mu.Lock()
	if !m.playing || m.paused {
		m.mu.Unlock()
		return
	}

	m.positionMS += 500
	ended := false
	if m.durationMS > 0 && m.positionMS >= m.durationMS {
		m.playing = false
		m.positionMS = 0
		ended = true
	}
	callback := m.onTrackEnded
	m.mu.Unlock()

	if ended && callback != nil {
		callback()
	}
}

func (m *MockBackend) Name() string {
	return "mock"
}

func (m *MockBackend) Available() bool {
	return true
}

func (m *MockBackend) Play(uri string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.currentURI = uri
	m.playing = true
	m.paused = false
	m.positionMS = 0
	if m.durationMS <= 0 {
		m.durationMS = 180000
	}
	return nil
}

// SetTrackDuration sets simulated duration for current track.
func (m *MockBackend) SetTrackDuration(durationMS int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if durationMS > 0 {
		m.durationMS = durationMS
	} else {
		m.durationMS = 180000
	}
}

func (m *MockBackend) Pause() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.playing {
		m.paused = true
	}
	return nil
}

func (m *MockBackend) Resume() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.playing {
		m.paused = false
	} else if m.currentURI != "" {
		m.playing = true
		m.paused = false
	}
	return nil
}

func (m *MockBackend) PlayPause() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.playing {
		if m.currentURI != "" {
			m.playing = true
			m.paused = false
		}
	} else {
		m.paused = !m.paused
	}
	return nil
}

func (m *MockBackend) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.playing = false
	m.paused = false
	m.positionMS = 0
	return nil
}

func (m *MockBackend) Seek(positionMS int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if positionMS < 0 {
		positionMS = 0
	}
	m.positionMS = positionMS
	return nil
}

func (m *MockBackend) SetVolume(volume int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if volume < 0 {
		volume = 0
	}
	if volume > 100 {
		volume = 100
	}
	m.volume = volume
	return nil
}

func (m *MockBackend) GetStatus() PlayerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return PlayerStatus{
		ActiveBackend:     "mock",
		ConfiguredBackend: "mock",
		Playing:           m.playing,
		Paused:            m.paused,
		PositionMS:        m.positionMS,
		DurationMS:        m.durationMS,
		Volume:            m.volume,
	}
}

func (m *MockBackend) Close() error {
	if m.ticker != nil {
		m.ticker.Stop()
	}
	close(m.stopCh)
	return nil
}
