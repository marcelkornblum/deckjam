package player

import (
	"fmt"
	"net"
	"net/http"
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
