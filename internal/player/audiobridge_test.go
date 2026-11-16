package player

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelkornblum/deckjam/internal/store"
)

// 1. Prove "auto" mode NEVER falls back to MockBackend or fakes Playing=true when Spotify is disconnected.
func TestAutoModeNeverFakesPlayingWithMock(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open store failed: %v", err)
	}
	defer st.Close()

	// Unreachable librespot URL in "auto" mode
	coord := NewCoordinator(st, "http://127.0.0.1:1", "auto")
	defer coord.Close()

	qItem := &store.QueueItem{
		SpotifyURI:  "spotify:track:neverFakePlay",
		TrackID:     "neverFakePlay",
		Title:       "Real Song",
		Artist:      "Real Artist",
		DurationMS:  180000,
		AddedByID:   "u1",
		AddedByName: "Marcel",
	}
	if err := st.AddToQueue(qItem, "round_robin"); err != nil {
		t.Fatalf("AddToQueue failed: %v", err)
	}

	coord.CheckAutoPlay()

	status := coord.GetStatus("")
	if status.ActiveBackend == "mock" {
		t.Fatalf("CRITICAL: auto mode fell back to mock backend! got ActiveBackend=%q", status.ActiveBackend)
	}
	if status.ActiveBackend != "waiting" && status.ActiveBackend != "mpris" {
		t.Fatalf("Expected ActiveBackend 'waiting' (or 'mpris' if desktop Spotify running), got %q", status.ActiveBackend)
	}
	if status.ActiveBackend == "waiting" && status.Playing {
		t.Fatalf("CRITICAL: auto mode reported Playing=true while waiting for Spotify!")
	}
}

// 2. Prove PlayTestTone writes real non-zero 44.1kHz s16le stereo PCM through FIFO -> AudioBridge -> OS player stdin.
func TestAudioBridgeTestToneWritesVerifiedPCMToAudioOut(t *testing.T) {
	tmpDir := t.TempDir()
	fifoPath := filepath.Join(tmpDir, "deckjam-test.fifo")
	capturePath := filepath.Join(tmpDir, "captured-out.pcm")

	// Simulate pw-cat consuming raw s16le PCM from stdin and writing to hardware sink file
	audioCmd := fmt.Sprintf("cat > %q", capturePath)

	bridge, err := NewAudioBridge(fifoPath, audioCmd, nil)
	if err != nil {
		t.Fatalf("NewAudioBridge failed: %v", err)
	}
	defer bridge.Close()

	stats, err := bridge.PlayTestTone(500)
	if err != nil {
		t.Fatalf("PlayTestTone failed: %v (stats=%+v)", err, stats)
	}
	if !stats.Verified || !stats.StreamActive {
		t.Fatalf("Expected Verified=true & StreamActive=true, got %+v", stats)
	}
	if stats.PeakPct < 20 {
		t.Errorf("Expected non-zero chime waveform peak >= 20%%, got %d%%", stats.PeakPct)
	}

	// Close bridge so `cat` flushes captured file cleanly
	_ = bridge.Close()

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("Failed reading captured audio output: %v", err)
	}

	// 500ms at 44100 Hz * 2ch * 2B = 88,200 bytes
	expectedBytes := (44100 * 500 / 1000) * 4
	if len(captured) != expectedBytes {
		t.Errorf("Captured PCM byte count mismatch: got %d, want %d", len(captured), expectedBytes)
	}

	peak := measureS16LEPeak(captured)
	if peak < 9000 {
		t.Errorf("Captured PCM waveform peak too low (silent?): got %d, want >= 9000", peak)
	}
}

// 3. Prove Coordinator + Librespot + AudioBridge ONLY reports Playing=true when real PCM flows to audio out,
// and drops Playing=false when audio output stops even if go-librespot HTTP API claims playing!
func TestCoordinatorLibrespotRequiresRealPCMBeforeReportingPlaying(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"playback_ready":true}`))
	})
	mux.HandleFunc("/auth/code", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// go-librespot claims it is unpaused & playing track!
		_, _ = w.Write([]byte(`{"stopped":false,"paused":false,"volume":90,"volume_steps":100,"track":{"uri":"spotify:track:partyJam","position":1200,"duration":200000}}`))
	})
	mux.HandleFunc("/player/play", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()

	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open store failed: %v", err)
	}
	defer st.Close()

	fifoPath := filepath.Join(tmpDir, "librespot.fifo")
	capturePath := filepath.Join(tmpDir, "pw-cat-out.pcm")
	audioCmd := fmt.Sprintf("cat > %q", capturePath)

	coord := NewCoordinatorWithAudio(st, "http://"+ln.Addr().String(), "auto", fifoPath, audioCmd)
	defer coord.Close()

	// Sync librespot HTTP state
	coord.librespot.checkStatus()

	// Step A: go-librespot says "playing", BUT zero PCM bytes have been written to FIFO yet!
	stBefore := coord.GetStatus("")
	if stBefore.ActiveBackend != "librespot" {
		t.Fatalf("Expected ActiveBackend 'librespot', got %q", stBefore.ActiveBackend)
	}
	if stBefore.Playing {
		t.Fatalf("CRITICAL: Coordinator reported Playing=true with ZERO PCM audio output!")
	}
	if !stBefore.Buffering {
		t.Errorf("Expected Buffering=true while waiting for PCM stream, got false")
	}
	if stBefore.AudioVerified {
		t.Fatalf("CRITICAL: AudioVerified=true with zero PCM bytes!")
	}

	// Step B: Simulate go-librespot writing real 44.1kHz s16le stereo audio into FIFO
	pcm := synthesizeChimeS16LE(44100, 450)
	writer, err := os.OpenFile(fifoPath, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("Open FIFO writer failed: %v", err)
	}
	for off := 0; off < len(pcm); off += 7056 {
		end := off + 7056
		if end > len(pcm) {
			end = len(pcm)
		}
		if _, err := writer.Write(pcm[off:end]); err != nil {
			t.Fatalf("FIFO write failed: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = writer.Close()

	// Poll briefly for readLoop to flush chunk to child
	var stDuring PlayerStatus
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		stDuring = coord.GetStatus("")
		if stDuring.Playing && stDuring.AudioVerified {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !stDuring.Playing {
		t.Fatalf("Expected Playing=true while real PCM flows to audio out, got %+v", stDuring)
	}
	if !stDuring.AudioVerified {
		t.Fatalf("Expected AudioVerified=true while real PCM flows to audio out, got %+v", stDuring)
	}
	if stDuring.AudioPeakPct <= 0 {
		t.Errorf("Expected AudioPeakPct > 0, got %d", stDuring.AudioPeakPct)
	}

	// Step C: Wait 1.8s after PCM stops flowing -> Playing & AudioVerified must drop back to false!
	time.Sleep(1800 * time.Millisecond)
	stAfter := coord.GetStatus("")
	if stAfter.Playing || stAfter.AudioVerified {
		t.Fatalf("Expected Playing=false & AudioVerified=false after PCM stream dried up, got Playing=%v AudioVerified=%v",
			stAfter.Playing, stAfter.AudioVerified)
	}

	coord.Close()
	captured, err := os.ReadFile(capturePath)
	if err != nil || len(captured) != len(pcm) {
		t.Fatalf("Expected pw-cat capture file to have %d bytes, got %d (err=%v)", len(pcm), len(captured), err)
	}
}

// 4. Verify pactl sink parser extracts JBL PartyBox description and low AVRCP volume accurately.
func TestParsePactlSinksJBLPartyBox(t *testing.T) {
	sample := `
Sink #46
	State: SUSPENDED
	Name: alsa_output.pci-0000_04_00.5-platform-acp5x_mach.0.HiFi__hw_acp5x_1__sink
	Description: Valve Software Steam Deck Speakers
	Mute: no
	Volume: front-left: 52428 /  80% / -5.81 dB,   front-right: 52428 /  80% / -5.81 dB

Sink #89
	State: RUNNING
	Name: bluez_output.AA_BB_CC_11_22_33.1
	Description: JBL PartyBox 110
	Mute: no
	Volume: front-left: 3276 /   5% / -78.00 dB,   front-right: 3276 /   5% / -78.00 dB
`
	sinks := parsePactlSinks(sample)
	if len(sinks) != 2 {
		t.Fatalf("Expected 2 parsed sinks, got %d", len(sinks))
	}
	var bt *parsedSink
	for i := range sinks {
		if strings.HasPrefix(sinks[i].name, "bluez_output.") {
			bt = &sinks[i]
		}
	}
	if bt == nil {
		t.Fatalf("Expected bluez_output sink to be parsed")
	}
	if bt.description != "JBL PartyBox 110" {
		t.Errorf("Expected description 'JBL PartyBox 110', got %q", bt.description)
	}
	if bt.volumePct != 5 {
		t.Errorf("Expected volumePct=5, got %d", bt.volumePct)
	}
}

// 5. Verify slow-hotspot 204 responses right after Play() do NOT prematurely wipe Playing=false during 6s loadingGrace.
func TestLibrespotLoadingGracePreventsPrematureStopOnSlowHotspot(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"playback_ready":true}`))
	})
	mux.HandleFunc("/auth/code", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		// Simulate go-librespot still resolving track over slow cellular (204 No Content)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/player/play", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()

	lb := NewLibrespotBackend("http://"+ln.Addr().String(), nil)
	defer lb.Close()

	lb.checkStatus()
	if err := lb.Play("spotify:track:slowHotspotSong"); err != nil {
		t.Fatalf("lb.Play failed: %v", err)
	}

	// Immediately poll /status (returns 204 while resolving over slow 4G/5G): must remain Playing=true inside loadingGrace!
	lb.checkStatus()
	if !lb.IsTransportPlaying() {
		t.Fatalf("CRITICAL: checkStatus() wiped Playing=false on 204 during slow hotspot track load!")
	}
	if !lb.HasLoadedTrack("spotify:track:slowHotspotSong") {
		t.Errorf("Expected slowHotspotSong preserved during loadingGrace")
	}
}

// 6. Verify go-librespot journal log parser and NetProbe connectivity diagnostics.
func TestParseLibrespotLogHintAndNetProbe(t *testing.T) {
	if got := ParseLibrespotLogHint(`panic: unsupported context type`); !strings.Contains(got, "single-track play") {
		t.Errorf("Expected single-track panic hint, got %q", got)
	}
	if got := ParseLibrespotLogHint(`time="2026-11-15" level=error msg="failed to open fifo: no such device or address"`); !strings.Contains(got, "FIFO") {
		t.Errorf("Expected FIFO hint, got %q", got)
	}
	if got := ParseLibrespotLogHint(`time="2026-11-15" level=warning msg="reconnecting" error="dial tcp: i/o timeout"`); !strings.Contains(got, "hotspot") {
		t.Errorf("Expected hotspot timeout hint, got %q", got)
	}

	// Test NetProbe Online=true with local HTTP server
	okSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()
	go func() { _ = okSrv.Serve(ln) }()
	defer func() { _ = okSrv.Close() }()

	probeOK := NewNetProbe([]string{"http://" + ln.Addr().String()})
	defer probeOK.Close()
	stOK := probeOK.CheckNow()
	if !stOK.Checked || !stOK.Online {
		t.Fatalf("Expected NetProbe Online=true for reachable endpoint, got %+v", stOK)
	}

	// Test NetProbe Online=false for closed port
	probeOff := NewNetProbe([]string{"http://127.0.0.1:1"})
	defer probeOff.Close()
	stOff := probeOff.CheckNow()
	if !stOff.Checked || stOff.Online {
		t.Fatalf("Expected NetProbe Online=false for unreachable endpoint, got %+v", stOff)
	}
}

// 7. Verify StartIPv4Proxy handles HTTP and CONNECT requests over IPv4 (127.0.0.1).
func TestIPv4ProxyTunnelsHTTPAndConnect(t *testing.T) {
	// Start upstream test server
	upstreamSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Echo", r.Header.Get("X-Test-Send"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream-ok"))
	})}
	upstreamLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen upstream failed: %v", err)
	}
	defer upstreamLn.Close()
	go func() { _ = upstreamSrv.Serve(upstreamLn) }()
	defer func() { _ = upstreamSrv.Close() }()

	upstreamAddr := upstreamLn.Addr().String()

	// Start IPv4 proxy on random port
	proxyCloser, proxyAddr, err := StartIPv4Proxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartIPv4Proxy failed: %v", err)
	}
	defer proxyCloser.Close()

	// Test 1: Plain HTTP proxy request
	proxyURL := http.ProxyURL(&url.URL{Scheme: "http", Host: proxyAddr})
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: proxyURL,
		},
		Timeout: 3 * time.Second,
	}

	req, _ := http.NewRequest("GET", "http://"+upstreamAddr+"/test", nil)
	req.Header.Set("X-Test-Send", "deckjam-ipv4")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Test-Echo"); got != "deckjam-ipv4" {
		t.Errorf("Expected echo 'deckjam-ipv4', got %q", got)
	}

	// Test 2: CONNECT tunnel simulation
	conn, err := net.Dial("tcp4", proxyAddr)
	if err != nil {
		t.Fatalf("Dial proxy failed: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstreamAddr, upstreamAddr)
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read CONNECT response failed: %v", err)
	}
	connectResp := string(buf[:n])
	if !strings.Contains(connectResp, "200 Connection Established") {
		t.Fatalf("Expected 200 Connection Established, got %q", connectResp)
	}

	// Send plain HTTP over hijacked tunnel to upstream
	fmt.Fprintf(conn, "GET /tunnel HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", upstreamAddr)
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Read tunneled response failed: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "upstream-ok") {
		t.Errorf("Expected 'upstream-ok' over tunnel, got %q", string(buf[:n]))
	}
}

// 8. Verify LibrespotBackend parses "type": "playing", "type": "metadata", and "type": "playback_error"
func TestLibrespotWebSocketEventParsing(t *testing.T) {
	backend := &LibrespotBackend{
		currentURI: "spotify:track:initial",
	}

	// Simulate consumeEvents logic directly
	msgPlaying := []byte(`{"type":"playing","data":{"uri":"spotify:track:song123","play_origin":"spotify_connect"}}`)
	backend.consumeEventsMock(msgPlaying)

	if !backend.playing || backend.currentURI != "spotify:track:song123" || backend.playOrigin != "spotify_connect" {
		t.Errorf("Failed parsing 'playing' event: playing=%v uri=%s playOrigin=%s",
			backend.playing, backend.currentURI, backend.playOrigin)
	}

	msgMeta := []byte(`{"type":"metadata","data":{"uri":"spotify:track:song123","name":"Track A","artist_names":["Artist 1","Artist 2"],"album_name":"Album X","duration":210000,"position":5000}}`)
	backend.consumeEventsMock(msgMeta)

	if backend.trackTitle != "Track A" || backend.trackArtist != "Artist 1, Artist 2" || backend.trackAlbum != "Album X" || backend.durationMS != 210000 {
		t.Errorf("Failed parsing 'metadata' event: title=%s artist=%s album=%s dur=%d",
			backend.trackTitle, backend.trackArtist, backend.trackAlbum, backend.durationMS)
	}

	msgErr := []byte(`{"type":"playback_error","data":{"uri":"spotify:track:song123","message":"CDN error","stage":"fetch"}}`)
	backend.consumeEventsMock(msgErr)

	if backend.playing || !strings.Contains(backend.lastError, "CDN error") {
		t.Errorf("Failed parsing 'playback_error' event: playing=%v lastError=%s",
			backend.playing, backend.lastError)
	}
}

// 9. Verify External Spotify Connect / Spotify Jam playback is NOT overwritten by Coordinator.watchdogLoop
func TestExternalPlaybackProtection(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "deckjam.db"))
	if err != nil {
		t.Fatalf("Open store failed: %v", err)
	}
	defer st.Close()

	coord := NewCoordinator(st, "http://127.0.0.1:1", "auto")
	defer coord.Close()

	// Simulate external track playing on librespot backend
	coord.librespot.mu.Lock()
	coord.librespot.hasLoadedTrack = true
	coord.librespot.playing = true
	coord.librespot.paused = false
	coord.librespot.currentURI = "spotify:track:jamTrack999"
	coord.librespot.trackTitle = "Jamming Out"
	coord.librespot.trackArtist = "DJ Jam"
	coord.librespot.playOrigin = "spotify_connect"
	coord.librespot.lastPlayTime = time.Now().Add(-10 * time.Second)
	coord.librespot.mu.Unlock()

	// DeckJam has a queued item in SQLite
	qItem := &store.QueueItem{
		SpotifyURI:  "spotify:track:queuedItem",
		TrackID:     "queuedItem",
		Title:       "Queued Item",
		Artist:      "Queued Artist",
		DurationMS:  180000,
		AddedByID:   "guest1",
		AddedByName: "Guest",
	}
	if err := st.AddToQueue(qItem, "round_robin"); err != nil {
		t.Fatalf("AddToQueue failed: %v", err)
	}

	// Check IsExternalPlayback
	if !coord.IsExternalPlayback("spotify:track:queuedItem") {
		t.Fatalf("Expected IsExternalPlayback to be TRUE for remote Spotify Connect playback")
	}

	// ExternalNowPlaying must synthesize the remote track info
	extNP := coord.ExternalNowPlaying()
	if extNP == nil {
		t.Fatalf("Expected ExternalNowPlaying to be non-nil")
	}
	if extNP.SpotifyURI != "spotify:track:jamTrack999" || extNP.Title != "Jamming Out" || extNP.AddedByName != "Spotify Connect" {
		t.Fatalf("Unexpected ExternalNowPlaying item: %+v", extNP)
	}
}
