package player

import (
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	mprisObjectPath = "/org/mpris/MediaPlayer2"
	mprisInterface  = "org.mpris.MediaPlayer2.Player"
	mprisProp       = "org.freedesktop.DBus.Properties"
	spotifyBusName  = "org.mpris.MediaPlayer2.spotify"
)

// MPRISBackend controls Spotify over Linux D-Bus MPRIS.
type MPRISBackend struct {
	mu           sync.RWMutex
	conn         *dbus.Conn
	playing      bool
	paused       bool
	currentURI   string
	positionMS   int
	durationMS   int
	volume       int
	available    bool
	stopCh       chan struct{}
	onTrackEnded func()
}

// NewMPRISBackend creates a new MPRISBackend instance.
func NewMPRISBackend(onTrackEnded func()) *MPRISBackend {
	b := &MPRISBackend{
		volume:       80,
		durationMS:   180000,
		onTrackEnded: onTrackEnded,
		stopCh:       make(chan struct{}),
	}

	// Try connecting to session bus
	conn, err := dbus.ConnectSessionBus()
	if err == nil {
		b.conn = conn
		b.available = b.pingSpotify()
	}

	go b.pollLoop()
	return b
}

func (b *MPRISBackend) pingSpotify() bool {
	if b.conn == nil {
		return false
	}
	var names []string
	err := b.conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		return false
	}
	for _, n := range names {
		if n == spotifyBusName {
			return true
		}
	}
	return false
}

func (b *MPRISBackend) pollLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.updateState()
		}
	}
}

func (b *MPRISBackend) updateState() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn == nil {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			b.available = false
			return
		}
		b.conn = conn
	}

	b.available = b.pingSpotify()
	if !b.available {
		return
	}

	obj := b.conn.Object(spotifyBusName, mprisObjectPath)
	propVariant, err := obj.GetProperty(mprisInterface + ".PlaybackStatus")
	if err == nil {
		status := propVariant.Value().(string)
		switch status {
		case "Playing":
			b.playing = true
			b.paused = false
		case "Paused":
			b.playing = true
			b.paused = true
		default:
			b.playing = false
			b.paused = false
		}
	}

	posVariant, err := obj.GetProperty(mprisInterface + ".Position")
	if err == nil {
		if posUS, ok := posVariant.Value().(int64); ok {
			b.positionMS = int(posUS / 1000)
		}
	}

	metaVariant, err := obj.GetProperty(mprisInterface + ".Metadata")
	if err == nil {
		if metaMap, ok := metaVariant.Value().(map[string]dbus.Variant); ok {
			if lenVariant, ok := metaMap["mpris:length"]; ok {
				if lenUS, ok := lenVariant.Value().(uint64); ok {
					b.durationMS = int(lenUS / 1000)
				}
			}
			if uriVariant, ok := metaMap["xesam:url"]; ok {
				if uri, ok := uriVariant.Value().(string); ok {
					// Check if track ended or changed unexpectedly
					if b.currentURI != "" && uri != b.currentURI && b.playing {
						// Song finished and switched!
						b.currentURI = uri
						if b.onTrackEnded != nil {
							go b.onTrackEnded()
						}
					}
				}
			}
		}
	}
}

func (b *MPRISBackend) Name() string {
	return "mpris"
}

func (b *MPRISBackend) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.available
}

func (b *MPRISBackend) Play(uri string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.available || b.conn == nil {
		return fmt.Errorf("mpris spotify player not available")
	}

	obj := b.conn.Object(spotifyBusName, mprisObjectPath)
	call := obj.Call(mprisInterface+".OpenUri", 0, uri)
	if call.Err != nil {
		return call.Err
	}

	b.currentURI = uri
	b.playing = true
	b.paused = false
	b.positionMS = 0
	return nil
}

func (b *MPRISBackend) Pause() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.available || b.conn == nil {
		return fmt.Errorf("mpris spotify player not available")
	}
	obj := b.conn.Object(spotifyBusName, mprisObjectPath)
	call := obj.Call(mprisInterface+".Pause", 0)
	if call.Err == nil {
		b.paused = true
	}
	return call.Err
}

func (b *MPRISBackend) Resume() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.available || b.conn == nil {
		return fmt.Errorf("mpris spotify player not available")
	}
	obj := b.conn.Object(spotifyBusName, mprisObjectPath)
	call := obj.Call(mprisInterface+".Play", 0)
	if call.Err == nil {
		b.paused = false
		b.playing = true
	}
	return call.Err
}

func (b *MPRISBackend) PlayPause() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.available || b.conn == nil {
		return fmt.Errorf("mpris spotify player not available")
	}
	obj := b.conn.Object(spotifyBusName, mprisObjectPath)
	call := obj.Call(mprisInterface+".PlayPause", 0)
	return call.Err
}

func (b *MPRISBackend) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.available || b.conn == nil {
		return fmt.Errorf("mpris spotify player not available")
	}
	obj := b.conn.Object(spotifyBusName, mprisObjectPath)
	call := obj.Call(mprisInterface+".Stop", 0)
	if call.Err == nil {
		b.playing = false
		b.paused = false
		b.positionMS = 0
	}
	return call.Err
}

func (b *MPRISBackend) Seek(positionMS int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.available || b.conn == nil {
		return fmt.Errorf("mpris spotify player not available")
	}
	obj := b.conn.Object(spotifyBusName, mprisObjectPath)
	// SetPosition takes trackId (object path) and position in microseconds
	call := obj.Call(mprisInterface+".SetPosition", 0, dbus.ObjectPath("/"), int64(positionMS)*1000)
	if call.Err == nil {
		b.positionMS = positionMS
	}
	return call.Err
}

func (b *MPRISBackend) SetVolume(volume int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.available || b.conn == nil {
		return fmt.Errorf("mpris spotify player not available")
	}
	volFraction := float64(volume) / 100.0
	obj := b.conn.Object(spotifyBusName, mprisObjectPath)
	call := obj.Call(mprisProp+".Set", 0, mprisInterface, "Volume", dbus.MakeVariant(volFraction))
	if call.Err == nil {
		b.volume = volume
	}
	return call.Err
}

func (b *MPRISBackend) GetStatus() PlayerStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return PlayerStatus{
		ActiveBackend:     "mpris",
		ConfiguredBackend: "mpris",
		Playing:           b.playing,
		Paused:            b.paused,
		PositionMS:        b.positionMS,
		DurationMS:        b.durationMS,
		Volume:            b.volume,
		MPRISAvailable:    b.available,
	}
}

func (b *MPRISBackend) Close() error {
	select {
	case <-b.stopCh:
		return nil
	default:
		close(b.stopCh)
	}
	if b.conn != nil {
		return b.conn.Close()
	}
	return nil
}
