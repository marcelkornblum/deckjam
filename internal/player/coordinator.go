package player

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/marcelkornblum/deckjam/internal/store"
)

// Coordinator coordinates between the active player backend, the SQLite store,
// real-time PipeWire PCM audio verification, and queue advancement.
type Coordinator struct {
	store       *store.Store
	librespot   *LibrespotBackend
	mpris       *MPRISBackend
	mock        *MockBackend
	waiting     *WaitingBackend
	audioBridge *AudioBridge
	netProbe    *NetProbe
	hostProbes  bool

	modeMu     sync.RWMutex
	activeMode string // "auto", "librespot", "mpris", "mock"

	stateMu         sync.RWMutex
	userPaused      bool
	lastPlayAttempt time.Time
	retryTrackID    int64
	retryCount      int
	lastJournalAt   time.Time
	lastJournalHint string

	stopCh   chan struct{}
	stopOnce sync.Once

	actionMu    sync.Mutex // serializes track transitions and queue popping
	listenersMu sync.RWMutex
	listeners   []chan struct{}
}

// NewCoordinator creates a new multi-backend Player Coordinator without an OS FIFO bridge (used in unit tests).
func NewCoordinator(st *store.Store, librespotURL string, configuredBackend string) *Coordinator {
	return NewCoordinatorWithAudio(st, librespotURL, configuredBackend, "", "")
}

// NewCoordinatorWithAudio creates a new Coordinator wired to a real-time PCM FIFO -> PipeWire AudioBridge.
func NewCoordinatorWithAudio(st *store.Store, librespotURL string, configuredBackend string, audioFIFOPath string, audioCmd string) *Coordinator {
	if configuredBackend == "" {
		configuredBackend = "auto"
	}

	enableHostProbes := audioFIFOPath != "" && audioCmd == "" && configuredBackend != "mock"

	c := &Coordinator{
		store:      st,
		activeMode: configuredBackend,
		waiting:    NewWaitingBackend(),
		hostProbes: enableHostProbes,
		stopCh:     make(chan struct{}),
	}

	if enableHostProbes {
		c.netProbe = NewNetProbe(nil)
	}

	onTrackEnded := func() {
		go c.HandleTrackEnded()
	}

	if audioFIFOPath != "" {
		ab, err := NewAudioBridge(audioFIFOPath, audioCmd, func() {
			c.notifyStateChanged()
		})
		if err != nil {
			log.Printf("[player] Warning: failed to initialize AudioBridge at %s: %v", audioFIFOPath, err)
		} else {
			c.audioBridge = ab
		}
	}

	c.librespot = NewLibrespotBackend(librespotURL, onTrackEnded)
	c.mpris = NewMPRISBackend(onTrackEnded)
	c.mock = NewMockBackend(onTrackEnded)

	go c.watchdogLoop()

	return c
}

// PlayTestTone triggers a hardware speaker test chime through the PipeWire AudioBridge.
func (c *Coordinator) PlayTestTone(durationMS int) (AudioBridgeStats, error) {
	if c.audioBridge == nil {
		return AudioBridgeStats{}, fmt.Errorf("audio bridge is not enabled on this server instance")
	}
	return c.audioBridge.PlayTestTone(durationMS)
}

func (c *Coordinator) isUserPaused() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.userPaused
}

func (c *Coordinator) setUserPaused(val bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.userPaused = val
}

func (c *Coordinator) getLastPlayAttempt() time.Time {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.lastPlayAttempt
}

func (c *Coordinator) refreshJournalHint() string {
	if !c.hostProbes {
		return ""
	}
	c.stateMu.RLock()
	recent := time.Since(c.lastJournalAt) < 5*time.Second
	cached := c.lastJournalHint
	c.stateMu.RUnlock()
	if recent {
		return cached
	}

	hint := ReadLibrespotJournalHint()
	c.stateMu.Lock()
	c.lastJournalAt = time.Now()
	c.lastJournalHint = hint
	c.stateMu.Unlock()
	return hint
}

func (c *Coordinator) watchdogLoop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			if c.isUserPaused() {
				continue
			}

			np, err := c.store.GetNowPlaying("")
			if err != nil {
				continue
			}

			status := c.GetStatus("")
			if np == nil {
				settings, err := c.store.GetSettings()
				if err == nil {
					queued, _ := c.store.GetQueuedItems(settings.QueueMode, "")
					if len(queued) > 0 && status.ActiveBackend != "waiting" {
						c.HandleTrackEnded()
					}
				}
				continue
			}

			// Do not burn retries or skip guest tracks while waiting for Spotify pairing / daemon connection!
			if status.ActiveBackend == "waiting" {
				continue
			}

			if np.SpotifyURI != "" {
				var needsPlay bool
				cooldown := 8 * time.Second

				if status.ActiveBackend == "librespot" {
					lsStatus := c.librespot.GetStatus()
					if lsStatus.Paused && c.librespot.HasLoadedTrack(np.SpotifyURI) {
						// Daemon loaded track in paused state while party is unpaused — resume without resetting CDN buffer!
						_ = c.librespot.Resume()
						c.notifyStateChanged()
						continue
					}

					transportPlaying := c.librespot.IsTransportPlaying() && c.librespot.HasLoadedTrack(np.SpotifyURI)
					if !transportPlaying {
						needsPlay = true
						cooldown = 3500 * time.Millisecond
					} else if c.audioBridge.Enabled() && !c.audioBridge.Stats().StreamActive {
						// Transport is fetching encrypted audio over hotspot cellular — allow 18s uninterrupted CDN buffer window!
						cooldown = 18 * time.Second
						needsPlay = true
					} else {
						// Real PCM is actively streaming! Reset retry counter.
						c.stateMu.Lock()
						c.retryCount = 0
						c.lastJournalHint = ""
						c.stateMu.Unlock()
					}
				} else {
					cooldown = 3500 * time.Millisecond
					needsPlay = !status.Playing && !status.Paused
				}

				if !needsPlay {
					continue
				}

				if time.Since(c.getLastPlayAttempt()) < cooldown {
					continue
				}

				if c.hostProbes && c.netProbe != nil {
					c.netProbe.CheckNow()
				}
				_ = c.refreshJournalHint()

				c.stateMu.Lock()
				if c.retryTrackID != np.ID {
					c.retryTrackID = np.ID
					c.retryCount = 1
				} else {
					c.retryCount++
				}
				count := c.retryCount
				c.stateMu.Unlock()

				if count > 3 {
					settings, _ := c.store.GetSettings()
					queued, _ := c.store.GetQueuedItems(settings.QueueMode, "")
					if len(queued) > 0 {
						log.Printf("[player] Track %s failed after %d attempts; advancing past stuck track", np.SpotifyURI, count-1)
						_ = c.store.SetItemStatus(np.ID, "skipped")
						// Also skip any immediate duplicate copies of the exact same stuck URI so queue doesn't loop forever
						for _, dup := range queued {
							if dup.SpotifyURI == np.SpotifyURI && dup.SpotifyURI != "" {
								_ = c.store.SetItemStatus(dup.ID, "skipped")
							}
						}
						c.stateMu.Lock()
						c.retryCount = 0
						c.stateMu.Unlock()
						c.HandleTrackEnded()
						continue
					}
					if count > 5 && time.Since(c.getLastPlayAttempt()) < 15*time.Second {
						continue
					}
				}

				err := c.PlaySong(np.SpotifyURI, np.DurationMS)
				if err == nil {
					c.notifyStateChanged()
				}
			}
		}
	}
}

// SetBackend changes configured backend mode.
func (c *Coordinator) SetBackend(mode string) {
	c.modeMu.Lock()
	c.activeMode = mode
	c.modeMu.Unlock()
	c.notifyStateChanged()
}

// GetActiveBackend determines and returns the active backend based on configuration and availability.
// CRITICAL: "auto" NEVER falls back to MockBackend so DeckJam never fakes "Now Playing" without real audio!
func (c *Coordinator) GetActiveBackend() Backend {
	c.modeMu.RLock()
	mode := c.activeMode
	c.modeMu.RUnlock()

	switch mode {
	case "librespot":
		return c.librespot
	case "mpris":
		return c.mpris
	case "mock":
		return c.mock
	case "auto":
		if c.librespot.Available() {
			return c.librespot
		}
		if c.mpris.Available() {
			return c.mpris
		}
		return c.waiting
	default:
		return c.waiting
	}
}

// PlaySong plays the specified Spotify URI on the active backend.
func (c *Coordinator) PlaySong(uri string, durationMS int) error {
	c.stateMu.Lock()
	c.userPaused = false
	c.lastPlayAttempt = time.Now()
	c.stateMu.Unlock()

	go EnsureBluetoothSink()

	backend := c.GetActiveBackend()
	if mock, ok := backend.(*MockBackend); ok {
		mock.SetTrackDuration(durationMS)
	}
	err := backend.Play(uri)
	if err != nil {
		log.Printf("[player] Play error for %s on %s: %v", uri, backend.Name(), err)
	}
	return err
}

// Pause pauses playback.
func (c *Coordinator) Pause() error {
	c.setUserPaused(true)
	err := c.GetActiveBackend().Pause()
	c.notifyStateChanged()
	return err
}

// Resume resumes playback.
func (c *Coordinator) Resume() error {
	c.stateMu.Lock()
	c.userPaused = false
	c.retryCount = 0
	c.stateMu.Unlock()

	np, _ := c.store.GetNowPlaying("")
	if np == nil {
		c.HandleTrackEnded()
		return nil
	}

	status := c.GetStatus("")
	if status.ActiveBackend == "librespot" && !c.librespot.HasLoadedTrack(np.SpotifyURI) {
		err := c.PlaySong(np.SpotifyURI, np.DurationMS)
		c.notifyStateChanged()
		return err
	}
	if !status.Playing && !status.Buffering && !status.Paused && np.SpotifyURI != "" {
		err := c.PlaySong(np.SpotifyURI, np.DurationMS)
		c.notifyStateChanged()
		return err
	}

	err := c.GetActiveBackend().Resume()
	if err != nil && np.SpotifyURI != "" {
		err = c.PlaySong(np.SpotifyURI, np.DurationMS)
	}
	c.notifyStateChanged()
	return err
}

// PlayPause toggles playback.
func (c *Coordinator) PlayPause() error {
	status := c.GetStatus("")
	if (status.Playing || status.Buffering) && !status.Paused {
		return c.Pause()
	}
	return c.Resume()
}

// Stop stops playback.
func (c *Coordinator) Stop() error {
	return c.GetActiveBackend().Stop()
}

// Seek seeks to positionMS.
func (c *Coordinator) Seek(positionMS int) error {
	return c.GetActiveBackend().Seek(positionMS)
}

// SetVolume sets player volume (0-100) and ensures the OS sink is unmuted.
func (c *Coordinator) SetVolume(volume int) error {
	go EnsureBluetoothSink()
	err := c.GetActiveBackend().SetVolume(volume)
	c.notifyStateChanged()
	return err
}

// GetStatus returns the current consolidated PlayerStatus with ground-truth audio verification.
func (c *Coordinator) GetStatus(guestID string) PlayerStatus {
	c.modeMu.RLock()
	mode := c.activeMode
	c.modeMu.RUnlock()

	backend := c.GetActiveBackend()
	st := backend.GetStatus()
	st.ConfiguredBackend = mode
	st.ActiveBackend = backend.Name()
	st.LibrespotConnected = c.librespot.Available()
	st.LibrespotDaemonUp = c.librespot.DaemonUp()
	st.MPRISAvailable = c.mpris.Available()

	// Always expose librespot pairing info so UI can guide 1-tap pairing when in waiting state
	if pairCode, pairURL, _ := c.librespot.PairInfo(); pairCode != "" {
		st.LibrespotPairCode = pairCode
		st.LibrespotPairURL = pairURL
	}

	// Enrich with AudioBridge ground-truth PipeWire PCM metrics
	if c.audioBridge != nil && c.audioBridge.Enabled() {
		ab := c.audioBridge.Stats()
		st.AudioPlayer = ab.PlayerCmd
		st.AudioSinkName = ab.SinkName
		st.AudioBytesPerSec = ab.BytesPerSec
		st.AudioPeakPct = ab.PeakPct
		st.AudioVerified = ab.Verified
		if ab.LastError != "" {
			st.LastError = ab.LastError
		}

		// Ground-truth verification gate for go-librespot:
		// NEVER claim Playing=true unless real PCM audio bytes are actively streaming into SteamOS audio out!
		if st.ActiveBackend == "librespot" && st.Playing && !st.Paused {
			if ab.StreamActive {
				st.Playing = true
				st.Buffering = false
				if !st.AudioVerified {
					// Stream is actively clocking 44.1kHz frames to PipeWire during a quiet passage
					st.AudioVerified = true
				}
			} else {
				st.Playing = false
				st.Buffering = true
				st.AudioVerified = false
			}
		}
	} else {
		sink := GetCachedSinkInfo()
		if sink.DisplayName != "" {
			st.AudioSinkName = sink.DisplayName
		} else if st.ActiveBackend == "mock" {
			st.AudioSinkName = "Simulated Preview (No Speaker Out)"
		}
		if st.ActiveBackend == "mpris" && st.Playing && !st.Paused {
			st.AudioVerified = true
		}
	}

	if c.netProbe != nil {
		ns := c.netProbe.Status()
		st.InternetChecked = ns.Checked
		st.InternetOK = ns.Online
		st.InternetLatencyMS = ns.LatencyMS
		st.InternetDetail = ns.Detail
		if ns.Checked && !ns.Online && st.LastError == "" {
			st.LastError = ns.Detail
		}
	}

	if st.LastError == "" && c.librespot.LastError() != "" {
		st.LastError = c.librespot.LastError()
	}
	if st.LastError == "" && st.Buffering && time.Since(c.getLastPlayAttempt()) > 5*time.Second {
		if hint := c.refreshJournalHint(); hint != "" {
			st.LastError = hint
		}
	}
	if st.ActiveBackend == "waiting" && st.LastError == "" {
		if st.LibrespotPairCode != "" {
			st.LastError = "Tap 'Pair Spotify' above to connect your Spotify account"
		} else if st.LibrespotDaemonUp {
			st.LastError = "Connecting to Spotify servers over hotspot..."
		} else {
			st.LastError = "Waiting for local go-librespot audio daemon..."
		}
	}

	// Enrich with skip vote stats from store if now_playing exists
	settings, _ := c.store.GetSettings()
	st.SkipVoteThreshold = settings.SkipVoteThreshold
	if st.SkipVoteThreshold <= 0 {
		st.SkipVoteThreshold = 3
	}

	np, _ := c.store.GetNowPlaying(guestID)
	if np != nil {
		skipCount, votedByMe, _ := c.store.GetSkipVotes(np.ID, guestID)
		st.SkipVotes = skipCount
		st.SkipVotedByMe = votedByMe
		if np.DurationMS > 0 {
			st.DurationMS = np.DurationMS
		}
	}

	return st
}

// HandleTrackEnded handles current track completion by advancing the queue or loading a fallback track.
func (c *Coordinator) HandleTrackEnded() {
	c.actionMu.Lock()
	defer c.actionMu.Unlock()

	settings, err := c.store.GetSettings()
	if err != nil {
		log.Printf("[player] Failed to get settings: %v", err)
		return
	}

	// Advance queue
	nextTrack, err := c.store.PopNextTrack(settings.QueueMode)
	if err != nil {
		log.Printf("[player] PopNextTrack error: %v", err)
		return
	}

	if nextTrack != nil {
		log.Printf("[player] Playing next queued track: %s (%s)", nextTrack.Title, nextTrack.SpotifyURI)
		_ = c.PlaySong(nextTrack.SpotifyURI, nextTrack.DurationMS)
		c.notifyStateChanged()
		return
	}

	// Queue is empty! Check fallback pool if autoplay_fallback is enabled
	if settings.AutoplayFallback {
		fb, err := c.store.PickFallbackTrack()
		if err == nil && fb != nil {
			log.Printf("[player] Queue empty, playing fallback track: %s (%s)", fb.Title, fb.SpotifyURI)
			qi := &store.QueueItem{
				SpotifyURI:   fb.SpotifyURI,
				TrackID:      fb.TrackID,
				RawInput:     fb.SpotifyURI,
				Title:        fb.Title,
				Artist:       fb.Artist,
				Album:        fb.Album,
				ThumbnailURL: fb.ThumbnailURL,
				DurationMS:   fb.DurationMS,
				AddedByID:    "system",
				AddedByName:  "DeckJam Autoplay",
				AddedByColor: "#1db954",
				Status:       "playing",
				RoundNumber:  1,
				CreatedAt:    time.Now().Unix(),
			}
			_ = c.store.AddToQueue(qi, settings.QueueMode)
			_ = c.PlaySong(fb.SpotifyURI, fb.DurationMS)
			c.notifyStateChanged()
			return
		}
	}

	// No next track and no fallback track
	log.Printf("[player] Queue and fallback pool empty, stopping playback.")
	_ = c.Stop()
	c.notifyStateChanged()
}

// CheckAutoPlay triggers playback if currently idle and items exist in the queue.
func (c *Coordinator) CheckAutoPlay() {
	np, err := c.store.GetNowPlaying("")
	if err != nil {
		return
	}

	if np == nil {
		c.setUserPaused(false)
		c.HandleTrackEnded()
		return
	}

	status := c.GetStatus("")
	if !status.Playing && !status.Buffering && !c.isUserPaused() {
		if np.SpotifyURI != "" {
			_ = c.PlaySong(np.SpotifyURI, np.DurationMS)
			c.notifyStateChanged()
		} else {
			c.HandleTrackEnded()
		}
	}
}

// SkipCurrentTrack skips currently playing track immediately.
func (c *Coordinator) SkipCurrentTrack() error {
	c.stateMu.Lock()
	c.userPaused = false
	c.retryCount = 0
	c.stateMu.Unlock()

	np, err := c.store.GetNowPlaying("")
	if err != nil || np == nil {
		settings, sErr := c.store.GetSettings()
		if sErr == nil {
			queued, _ := c.store.GetQueuedItems(settings.QueueMode, "")
			if len(queued) > 0 {
				c.HandleTrackEnded()
				return nil
			}
		}
		return fmt.Errorf("nothing currently playing")
	}

	_ = c.store.SetItemStatus(np.ID, "skipped")
	c.HandleTrackEnded()
	return nil
}

// PlayNowItem immediately plays the specified queued item.
func (c *Coordinator) PlayNowItem(itemID int64) error {
	c.stateMu.Lock()
	c.userPaused = false
	c.retryCount = 0
	c.stateMu.Unlock()

	c.actionMu.Lock()
	defer c.actionMu.Unlock()

	item, err := c.store.PlayNow(itemID)
	if err != nil {
		return err
	}

	_ = c.PlaySong(item.SpotifyURI, item.DurationMS)
	c.notifyStateChanged()
	return nil
}

// SubscribeStateChange allows server to receive notifications on state transitions.
func (c *Coordinator) SubscribeStateChange(ch chan struct{}) {
	c.listenersMu.Lock()
	defer c.listenersMu.Unlock()
	c.listeners = append(c.listeners, ch)
}

// UnsubscribeStateChange removes listener channel.
func (c *Coordinator) UnsubscribeStateChange(ch chan struct{}) {
	c.listenersMu.Lock()
	defer c.listenersMu.Unlock()
	for i, l := range c.listeners {
		if l == ch {
			c.listeners = append(c.listeners[:i], c.listeners[i+1:]...)
			break
		}
	}
}

func (c *Coordinator) notifyStateChanged() {
	c.listenersMu.RLock()
	defer c.listenersMu.RUnlock()
	for _, l := range c.listeners {
		select {
		case l <- struct{}{}:
		default:
		}
	}
}

// Close closes all backends, network probes, and the audio bridge idempotently.
func (c *Coordinator) Close() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
		if c.netProbe != nil {
			c.netProbe.Close()
		}
		if c.librespot != nil {
			_ = c.librespot.Close()
		}
		if c.mpris != nil {
			_ = c.mpris.Close()
		}
		if c.mock != nil {
			_ = c.mock.Close()
		}
		if c.audioBridge != nil {
			_ = c.audioBridge.Close()
		}
	})
}
