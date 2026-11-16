package player

import (
	"fmt"
	"sync"
)

// WaitingBackend is used in "auto" mode when neither go-librespot nor MPRIS is ready yet.
// Unlike MockBackend, it NEVER fakes playback, ensuring DeckJam only reports "Now Playing"
// when a real audio engine is connected and outputting audio.
type WaitingBackend struct {
	mu     sync.RWMutex
	volume int
}

// NewWaitingBackend creates a new WaitingBackend.
func NewWaitingBackend() *WaitingBackend {
	return &WaitingBackend{volume: 85}
}

func (w *WaitingBackend) Name() string {
	return "waiting"
}

func (w *WaitingBackend) Available() bool {
	return false
}

func (w *WaitingBackend) Play(uri string) error {
	return fmt.Errorf("waiting for Spotify audio engine (pair Spotify or check connection)")
}

func (w *WaitingBackend) Pause() error {
	return nil
}

func (w *WaitingBackend) Resume() error {
	return fmt.Errorf("waiting for Spotify audio engine")
}

func (w *WaitingBackend) PlayPause() error {
	return fmt.Errorf("waiting for Spotify audio engine")
}

func (w *WaitingBackend) Stop() error {
	return nil
}

func (w *WaitingBackend) Seek(positionMS int) error {
	return nil
}

func (w *WaitingBackend) SetVolume(volume int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if volume < 0 {
		volume = 0
	}
	if volume > 100 {
		volume = 100
	}
	w.volume = volume
	return nil
}

func (w *WaitingBackend) GetStatus() PlayerStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return PlayerStatus{
		ActiveBackend: "waiting",
		Playing:       false,
		Paused:        false,
		Volume:        w.volume,
	}
}

func (w *WaitingBackend) Close() error {
	return nil
}
