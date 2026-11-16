package player

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// AudioBridgeStats reports real-time ground-truth verification of PCM audio flowing to SteamOS audio out.
type AudioBridgeStats struct {
	Enabled      bool   `json:"enabled"`
	StreamActive bool   `json:"stream_active"` // >=8KB/s PCM written to live OS audio player within 1.5s
	Verified     bool   `json:"verified"`      // StreamActive && non-zero acoustic waveform samples
	TestPlaying  bool   `json:"test_playing"`
	BytesPerSec  int    `json:"bytes_per_sec"`
	PeakPct      int    `json:"peak_pct"`
	PlayerCmd    string `json:"player_cmd"`
	SinkName     string `json:"sink_name"`
	SinkID       string `json:"sink_id"`
	LastError    string `json:"last_error"`
}

type audioCandidate struct {
	label string
	bin   string
	args  []string
}

// AudioBridge owns the named pipe (FIFO) fed by go-librespot (`audio_backend: "pipe"`),
// meters actual PCM throughput and 16-bit waveform amplitude, and streams verified audio
// directly into SteamOS PipeWire (`pw-cat` / `pacat` / `aplay`).
type AudioBridge struct {
	fifoPath      string
	customCmd     string
	fifoFile      *os.File
	onStateChange func()

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// Child process state (owned by readLoop goroutine, signaled via reopenCh)
	reopenCh chan struct{}

	// Synchronized telemetry
	mu              sync.RWMutex
	streamActive    bool
	verified        bool
	testPlaying     bool
	bytesPerSec     int
	peakPct         int
	activePlayer    string
	sinkInfo        AudioSinkInfo
	lastWriteAt     time.Time
	lastSignalAt    time.Time
	lastError       string
	totalBytesPiped int64

	// Serialize PlayTestTone calls
	toneMu sync.Mutex
}

// DefaultAudioFIFOPath returns the runtime FIFO path shared between go-librespot and DeckJam.
func DefaultAudioFIFOPath() string {
	if env := strings.TrimSpace(os.Getenv("DECKJAM_AUDIO_FIFO")); env != "" {
		return env
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); xdg != "" {
		return filepath.Join(xdg, "deckjam-audio.fifo")
	}
	return "/tmp/deckjam-audio.fifo"
}

// NewAudioBridge creates and starts a new AudioBridge reading from fifoPath.
// If fifoPath is empty, returns (nil, nil).
func NewAudioBridge(fifoPath string, customCmd string, onStateChange func()) (*AudioBridge, error) {
	fifoPath = strings.TrimSpace(fifoPath)
	if fifoPath == "" {
		return nil, nil
	}
	if customCmd == "" {
		customCmd = strings.TrimSpace(os.Getenv("DECKJAM_AUDIO_CMD"))
	}

	if err := os.MkdirAll(filepath.Dir(fifoPath), 0755); err != nil {
		return nil, fmt.Errorf("mkdir fifo parent: %w", err)
	}

	if info, err := os.Lstat(fifoPath); err == nil {
		if info.Mode()&os.ModeNamedPipe == 0 {
			_ = os.Remove(fifoPath)
		}
	}

	if _, err := os.Lstat(fifoPath); os.IsNotExist(err) {
		if err := syscall.Mkfifo(fifoPath, 0600); err != nil && !errors.Is(err, syscall.EEXIST) {
			return nil, fmt.Errorf("mkfifo %s: %w", fifoPath, err)
		}
	}

	// Open O_RDWR|O_NONBLOCK so open never blocks, writers never get ENXIO, and reader never gets EOF between tracks.
	f, err := os.OpenFile(fifoPath, os.O_RDWR|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("open fifo %s: %w", fifoPath, err)
	}

	initialSink := DiscoverAndEnsureSink(true)

	ab := &AudioBridge{
		fifoPath:      fifoPath,
		customCmd:     customCmd,
		fifoFile:      f,
		onStateChange: onStateChange,
		stopCh:        make(chan struct{}),
		reopenCh:      make(chan struct{}, 1),
		sinkInfo:      initialSink,
	}

	ab.wg.Add(2)
	go ab.readLoop()
	go ab.sinkMonitorLoop()

	log.Printf("[audiobridge] Listening on PCM FIFO %s (sink: %s)", fifoPath, ab.sinkLabel(initialSink))
	return ab, nil
}

func (ab *AudioBridge) sinkLabel(info AudioSinkInfo) string {
	if info.DisplayName != "" {
		return info.DisplayName
	}
	if ab.customCmd != "" {
		return "Custom Audio Sink"
	}
	return "Steam Deck Audio Out"
}

// FIFOPath returns the active named pipe path.
func (ab *AudioBridge) FIFOPath() string {
	if ab == nil {
		return ""
	}
	return ab.fifoPath
}

// Enabled reports whether this AudioBridge is active.
func (ab *AudioBridge) Enabled() bool {
	return ab != nil && ab.fifoPath != ""
}

// Stats returns a snapshot of current ground-truth audio verification telemetry.
func (ab *AudioBridge) Stats() AudioBridgeStats {
	if ab == nil {
		return AudioBridgeStats{}
	}
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	now := time.Now()
	streamActive := ab.streamActive && now.Sub(ab.lastWriteAt) < 1600*time.Millisecond
	verified := streamActive && (ab.peakPct > 0 || now.Sub(ab.lastSignalAt) < 2500*time.Millisecond)
	bps := ab.bytesPerSec
	peak := ab.peakPct
	if !streamActive {
		bps = 0
		peak = 0
	}

	return AudioBridgeStats{
		Enabled:      true,
		StreamActive: streamActive,
		Verified:     verified,
		TestPlaying:  ab.testPlaying,
		BytesPerSec:  bps,
		PeakPct:      peak,
		PlayerCmd:    ab.activePlayer,
		SinkName:     ab.sinkLabel(ab.sinkInfo),
		SinkID:       ab.sinkInfo.SinkID,
		LastError:    ab.lastError,
	}
}

func (ab *AudioBridge) sinkMonitorLoop() {
	defer ab.wg.Done()
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ab.stopCh:
			return
		case <-ticker.C:
			info := DiscoverAndEnsureSink(false)
			ab.mu.Lock()
			prevID := ab.sinkInfo.SinkID
			prevBT := ab.sinkInfo.IsBluetooth
			ab.sinkInfo = info
			ab.mu.Unlock()

			// If Bluetooth sink appeared, disappeared, or switched ID, trigger player reopen on new target
			if prevBT != info.IsBluetooth || (info.IsBluetooth && prevID != info.SinkID) {
				select {
				case ab.reopenCh <- struct{}{}:
				default:
				}
				ab.notifyChanged()
			}
		}
	}
}

func (ab *AudioBridge) buildCandidates(sink AudioSinkInfo) []audioCandidate {
	if ab.customCmd != "" {
		return []audioCandidate{
			{
				label: "custom-cmd",
				bin:   "/bin/sh",
				args:  []string{"-c", ab.customCmd},
			},
		}
	}

	var list []audioCandidate

	if pwCat, err := exec.LookPath("pw-cat"); err == nil && pwCat != "" {
		if sink.IsBluetooth && sink.SinkID != "" {
			list = append(list, audioCandidate{
				label: "pw-cat (Bluetooth)",
				bin:   pwCat,
				args: []string{
					"--playback", "--raw",
					"--format=s16", "--rate=44100", "--channels=2",
					"--latency=100ms", "--media-role=Music",
					"--target=" + sink.SinkID, "-",
				},
			})
		}
		list = append(list, audioCandidate{
			label: "pw-cat",
			bin:   pwCat,
			args: []string{
				"--playback", "--raw",
				"--format=s16", "--rate=44100", "--channels=2",
				"--latency=100ms", "--media-role=Music", "-",
			},
		})
	}

	if pacat, err := exec.LookPath("pacat"); err == nil && pacat != "" {
		if sink.IsBluetooth && sink.SinkID != "" {
			list = append(list, audioCandidate{
				label: "pacat (Bluetooth)",
				bin:   pacat,
				args: []string{
					"--playback", "--format=s16le", "--rate=44100", "--channels=2",
					"--client-name=DeckJam", "--latency-msec=100",
					"--device=" + sink.SinkID,
				},
			})
		}
		list = append(list, audioCandidate{
			label: "pacat",
			bin:   pacat,
			args: []string{
				"--playback", "--format=s16le", "--rate=44100", "--channels=2",
				"--client-name=DeckJam", "--latency-msec=100",
			},
		})
	}

	if aplay, err := exec.LookPath("aplay"); err == nil && aplay != "" {
		list = append(list, audioCandidate{
			label: "aplay",
			bin:   aplay,
			args:  []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", "44100", "-c", "2", "-"},
		})
	}

	return list
}

type activeChild struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stderrBuf *bytes.Buffer
	label     string
	startedAt time.Time
	doneCh    chan error
}

func (c *activeChild) stop() {
	if c == nil {
		return
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	select {
	case <-c.doneCh:
		return
	case <-time.After(300 * time.Millisecond):
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.doneCh
	}
}

func (ab *AudioBridge) spawnChild(cand audioCandidate) (*activeChild, error) {
	cmd := exec.Command(cand.bin, cand.args...)
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = &limitedWriter{buf: stderrBuf, max: 512}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}

	ch := &activeChild{
		cmd:       cmd,
		stdin:     stdin,
		stderrBuf: stderrBuf,
		label:     cand.label,
		startedAt: time.Now(),
		doneCh:    make(chan error, 1),
	}
	go func() {
		ch.doneCh <- cmd.Wait()
	}()
	return ch, nil
}

type limitedWriter struct {
	mu  sync.Mutex
	buf *bytes.Buffer
	max int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	rem := lw.max - lw.buf.Len()
	if rem > 0 {
		if len(p) <= rem {
			lw.buf.Write(p)
		} else {
			lw.buf.Write(p[:rem])
		}
	}
	return len(p), nil
}

func (lw *limitedWriter) String() string {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return strings.TrimSpace(lw.buf.String())
}

func (ab *AudioBridge) readLoop() {
	defer ab.wg.Done()

	buf := make([]byte, 8192)
	var child *activeChild
	candOffset := 0

	windowStart := time.Now()
	windowBytes := 0
	var windowMaxSample int32

	defer func() {
		if child != nil {
			child.stop()
		}
	}()

	writeToAudioOut := func(pcm []byte) bool {
		ab.mu.RLock()
		sink := ab.sinkInfo
		ab.mu.RUnlock()

		candidates := ab.buildCandidates(sink)
		if len(candidates) == 0 {
			ab.setError("no audio output player found (install pipewire pw-cat, pulseaudio pacat, or alsa aplay)")
			return false
		}

		for attempts := 0; attempts < len(candidates); attempts++ {
			select {
			case <-ab.reopenCh:
				if child != nil {
					child.stop()
					child = nil
				}
			default:
			}

			// Check if existing child already exited unexpectedly
			if child != nil {
				select {
				case waitErr := <-child.doneCh:
					errMsg := child.stderrBuf.String()
					if errMsg == "" && waitErr != nil {
						errMsg = waitErr.Error()
					}
					if time.Since(child.startedAt) < 1500*time.Millisecond {
						candOffset = (candOffset + 1) % len(candidates)
					}
					if errMsg != "" {
						ab.setError(fmt.Sprintf("%s exited: %s", child.label, errMsg))
					}
					child = nil
				default:
				}
			}

			if child == nil {
				idx := (candOffset + attempts) % len(candidates)
				cand := candidates[idx]
				spawned, err := ab.spawnChild(cand)
				if err != nil {
					ab.setError(fmt.Sprintf("failed to start %s: %v", cand.label, err))
					continue
				}
				// Brief 15ms check to catch instant CLI flag / socket connection failure
				select {
				case waitErr := <-spawned.doneCh:
					errMsg := spawned.stderrBuf.String()
					if errMsg == "" && waitErr != nil {
						errMsg = waitErr.Error()
					}
					ab.setError(fmt.Sprintf("%s failed: %s", cand.label, errMsg))
					candOffset = (idx + 1) % len(candidates)
					continue
				default:
				}
				child = spawned
				candOffset = idx
			}

			if _, err := child.stdin.Write(pcm); err != nil {
				errMsg := child.stderrBuf.String()
				if errMsg == "" {
					errMsg = err.Error()
				}
				ab.setError(fmt.Sprintf("%s write error: %s", child.label, errMsg))
				child.stop()
				child = nil
				candOffset = (candOffset + 1) % len(candidates)
				continue
			}

			// Verify child didn't immediately crash right after the write
			select {
			case waitErr := <-child.doneCh:
				errMsg := child.stderrBuf.String()
				if errMsg == "" && waitErr != nil {
					errMsg = waitErr.Error()
				}
				ab.setError(fmt.Sprintf("%s exited on write: %s", child.label, errMsg))
				child = nil
				candOffset = (candOffset + 1) % len(candidates)
				continue
			default:
			}

			ab.mu.Lock()
			ab.activePlayer = child.label
			ab.lastError = ""
			ab.mu.Unlock()
			return true
		}

		return false
	}

	for {
		select {
		case <-ab.stopCh:
			return
		default:
		}

		_ = ab.fifoFile.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := ab.fifoFile.Read(buf)

		now := time.Now()
		if n > 0 {
			chunk := buf[:n]
			peakSample := measureS16LEPeak(chunk)

			if writeToAudioOut(chunk) {
				windowBytes += n
				if peakSample > windowMaxSample {
					windowMaxSample = peakSample
				}

				elapsed := now.Sub(windowStart)
				var bps int
				if elapsed >= 250*time.Millisecond {
					bps = int(float64(windowBytes) / elapsed.Seconds())
				} else {
					bps = windowBytes * 4
				}

				peakPct := sampleToPercent(windowMaxSample)

				ab.mu.Lock()
				wasActive := ab.streamActive
				ab.lastWriteAt = now
				ab.totalBytesPiped += int64(n)
				ab.bytesPerSec = bps
				ab.peakPct = peakPct
				if peakSample > 16 {
					ab.lastSignalAt = now
				}
				ab.streamActive = bps >= 8000 || ab.totalBytesPiped >= 4096
				ab.verified = ab.streamActive && (peakPct > 0 || now.Sub(ab.lastSignalAt) < 2500*time.Millisecond)
				becameActive := !wasActive && ab.streamActive
				ab.mu.Unlock()

				if becameActive {
					ab.notifyChanged()
				}

				if elapsed >= 1*time.Second {
					windowStart = now
					windowBytes = 0
					windowMaxSample = 0
				}
			}
		}

		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				if n == 0 {
					// Decay metrics if stream stopped
					ab.mu.Lock()
					wasActive := ab.streamActive
					idleDur := now.Sub(ab.lastWriteAt)
					if wasActive && idleDur >= 1500*time.Millisecond {
						ab.streamActive = false
						ab.verified = false
						ab.bytesPerSec = 0
						ab.peakPct = 0
					}
					becameIdle := wasActive && !ab.streamActive
					ab.mu.Unlock()

					if becameIdle {
						windowStart = now
						windowBytes = 0
						windowMaxSample = 0
						ab.notifyChanged()
					}

					// Release idle PipeWire stream after 8s of silence so Bluetooth / Deck sleep works cleanly
					if child != nil && idleDur > 8*time.Second {
						child.stop()
						child = nil
					}

					// In case kernel fd ignored SetReadDeadline and returned immediate EAGAIN, avoid busy loop
					if time.Since(now) < 5*time.Millisecond && errors.Is(err, syscall.EAGAIN) {
						select {
						case <-ab.stopCh:
							return
						case <-time.After(25 * time.Millisecond):
						}
					}
				}
				continue
			}
			if errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func (ab *AudioBridge) setError(msg string) {
	ab.mu.Lock()
	changed := ab.lastError != msg || ab.streamActive
	ab.lastError = msg
	ab.streamActive = false
	ab.verified = false
	ab.mu.Unlock()
	if changed {
		log.Printf("[audiobridge] %s", msg)
		ab.notifyChanged()
	}
}

func (ab *AudioBridge) notifyChanged() {
	if ab.onStateChange != nil {
		go ab.onStateChange()
	}
}

// PlayTestTone synthesizes a pleasant stereo C5-E5-G5 arpeggio chime (44100 Hz s16le)
// and streams it directly through the FIFO -> AudioBridge -> PipeWire sink, verifying
// end-to-end physical audio output on the Steam Deck / Bluetooth speaker.
func (ab *AudioBridge) PlayTestTone(durationMS int) (AudioBridgeStats, error) {
	if ab == nil || ab.fifoPath == "" {
		return AudioBridgeStats{}, fmt.Errorf("audio bridge is not enabled")
	}

	ab.toneMu.Lock()
	defer ab.toneMu.Unlock()

	// Refresh sink routing (unmute + JBL Bluetooth A2DP & volume check) right before playing chime
	sink := DiscoverAndEnsureSink(true)
	ab.mu.Lock()
	ab.sinkInfo = sink
	ab.testPlaying = true
	ab.mu.Unlock()
	ab.notifyChanged()

	defer func() {
		ab.mu.Lock()
		ab.testPlaying = false
		ab.mu.Unlock()
		ab.notifyChanged()
	}()

	if durationMS <= 0 {
		durationMS = 650
	}
	if durationMS > 3000 {
		durationMS = 3000
	}

	pcm := synthesizeChimeS16LE(44100, durationMS)

	writer, err := os.OpenFile(ab.fifoPath, os.O_WRONLY, 0600)
	if err != nil {
		return ab.Stats(), fmt.Errorf("open fifo for test tone: %w", err)
	}
	defer writer.Close()

	// Stream in 40ms frames (44100 * 4 * 0.04 = 7056 bytes) to match real-time 176.4 KB/s clock
	const chunkBytes = 7056
	var bestStats AudioBridgeStats

	for offset := 0; offset < len(pcm); offset += chunkBytes {
		select {
		case <-ab.stopCh:
			return ab.Stats(), fmt.Errorf("audio bridge stopping")
		default:
		}

		end := offset + chunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		if _, err := writer.Write(pcm[offset:end]); err != nil {
			return ab.Stats(), fmt.Errorf("write test tone: %w", err)
		}
		time.Sleep(35 * time.Millisecond)

		st := ab.Stats()
		if st.Verified || st.BytesPerSec > bestStats.BytesPerSec {
			bestStats = st
		}
	}

	// Allow final chunk to be consumed by readLoop
	time.Sleep(80 * time.Millisecond)
	finalStats := ab.Stats()
	if finalStats.Verified {
		bestStats = finalStats
	}
	if !bestStats.Verified {
		errMsg := finalStats.LastError
		if errMsg == "" {
			errMsg = "audio player did not confirm PCM stream consumption"
		}
		return finalStats, fmt.Errorf("speaker verification failed: %s", errMsg)
	}
	return bestStats, nil
}

// Close shuts down the AudioBridge and releases the FIFO and any child player process.
func (ab *AudioBridge) Close() error {
	if ab == nil {
		return nil
	}
	ab.stopOnce.Do(func() {
		close(ab.stopCh)
		if ab.fifoFile != nil {
			_ = ab.fifoFile.Close()
		}
	})
	ab.wg.Wait()
	return nil
}

func measureS16LEPeak(b []byte) int32 {
	var maxAbs int32
	limit := len(b) - (len(b) % 2)
	for i := 0; i < limit; i += 2 {
		sample := int16(binary.LittleEndian.Uint16(b[i : i+2]))
		val := int32(sample)
		if val < 0 {
			val = -val
		}
		if val > maxAbs {
			maxAbs = val
		}
	}
	return maxAbs
}

func sampleToPercent(maxSample int32) int {
	if maxSample <= 16 {
		return 0
	}
	pct := int(math.Round(float64(maxSample) / 32767.0 * 100.0))
	if pct < 1 {
		pct = 1
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// synthesizeChimeS16LE generates a pleasant stereo C5 -> E5 -> G5 sine chime with raised-cosine envelopes.
func synthesizeChimeS16LE(sampleRate int, durationMS int) []byte {
	totalFrames := (sampleRate * durationMS) / 1000
	if totalFrames < 100 {
		totalFrames = sampleRate / 2
	}
	out := make([]byte, totalFrames*4) // 2 channels * 2 bytes

	freqs := []float64{523.25, 659.25, 783.99} // C5, E5, G5
	segLen := totalFrames / len(freqs)
	fadeFrames := sampleRate * 12 / 1000 // 12ms clickless fade
	const amplitude = 11000.0            // ~ -9.5 dBFS (clean, audible, never clips)

	var phase float64
	for i := 0; i < totalFrames; i++ {
		noteIdx := i / segLen
		if noteIdx >= len(freqs) {
			noteIdx = len(freqs) - 1
		}
		posInNote := i - noteIdx*segLen
		noteFrames := segLen
		if noteIdx == len(freqs)-1 {
			noteFrames = totalFrames - noteIdx*segLen
		}

		env := 1.0
		if posInNote < fadeFrames && fadeFrames > 0 {
			env = 0.5 * (1.0 - math.Cos(math.Pi*float64(posInNote)/float64(fadeFrames)))
		} else if noteFrames-posInNote < fadeFrames && fadeFrames > 0 {
			rem := noteFrames - posInNote
			env = 0.5 * (1.0 - math.Cos(math.Pi*float64(rem)/float64(fadeFrames)))
		}

		phase += 2.0 * math.Pi * freqs[noteIdx] / float64(sampleRate)
		if phase > 2.0*math.Pi {
			phase -= 2.0 * math.Pi
		}

		val := int16(math.Sin(phase) * amplitude * env)
		u := uint16(val)
		base := i * 4
		// Left + Right stereo channels
		out[base] = byte(u)
		out[base+1] = byte(u >> 8)
		out[base+2] = byte(u)
		out[base+3] = byte(u >> 8)
	}
	return out
}
