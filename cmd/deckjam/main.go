package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/marcelkornblum/deckjam/internal/player"
	"github.com/marcelkornblum/deckjam/internal/qr"
	"github.com/marcelkornblum/deckjam/internal/server"
	"github.com/marcelkornblum/deckjam/internal/spotify"
	"github.com/marcelkornblum/deckjam/internal/store"
)

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return defaultVal
}

func defaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "deckjam")
	}
	return "./data"
}

func main() {
	// Flags & Env vars per SPEC.md
	addrFlag := flag.String("addr", getEnv("DECKJAM_ADDR", "0.0.0.0:8080"), "Server listen address")
	dataDirFlag := flag.String("data-dir", getEnv("DECKJAM_DATA_DIR", defaultDataDir()), "Data directory for SQLite database")
	librespotURLFlag := flag.String("librespot-url", getEnv("DECKJAM_LIBRESPOT_URL", "http://127.0.0.1:3678"), "go-librespot REST API URL")
	backendFlag := flag.String("backend", getEnv("DECKJAM_BACKEND", "auto"), "Playback backend: auto | librespot | mpris | mock")
	audioFIFOFlag := flag.String("audio-fifo", player.DefaultAudioFIFOPath(), "Named pipe (FIFO) path for go-librespot PCM verification bridge")
	audioCmdFlag := flag.String("audio-cmd", getEnv("DECKJAM_AUDIO_CMD", ""), "Optional override command for raw s16le 44.1kHz stereo audio output")
	testAudioFlag := flag.Bool("test-audio", false, "Play a 0.7s stereo verification chime to SteamOS audio out and exit")
	hostPINFlag := flag.String("host-pin", getEnv("DECKJAM_HOST_PIN", ""), "Host PIN override (optional; 4-digit PIN generated in SQLite if unset)")
	hostnameFlag := flag.String("hostname", getEnv("DECKJAM_HOSTNAME", ""), "Hostname override for join URL (default auto-detected LAN IP)")
	qrFlag := flag.String("qr", "", "Print terminal QR code for the given URL/string to stdout and exit")

	flag.Parse()

	// If --qr flag provided, print terminal QR code directly and exit
	if *qrFlag != "" {
		terminalQR, err := qr.GenerateTerminal(*qrFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating QR: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(terminalQR)
		os.Exit(0)
	}

	// If --test-audio flag provided, run standalone hardware speaker chime test and exit
	if *testAudioFlag {
		fifo := *audioFIFOFlag
		if fifo == "" {
			fifo = "/tmp/deckjam-audio-test.fifo"
		}
		bridge, err := player.NewAudioBridge(fifo, *audioCmdFlag, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "AudioBridge init error: %v\n", err)
			os.Exit(1)
		}
		defer bridge.Close()
		stats, err := bridge.PlayTestTone(700)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Audio output test FAILED: %v (sink=%s player=%s)\n", err, stats.SinkName, stats.PlayerCmd)
			os.Exit(1)
		}
		fmt.Printf("Audio output VERIFIED: sink=%q player=%q rate=%d B/s peak=%d%%\n",
			stats.SinkName, stats.PlayerCmd, stats.BytesPerSec, stats.PeakPct)
		os.Exit(0)
	}

	log.Printf("Starting DeckJam backend...")
	log.Printf("Listen Address: %s", *addrFlag)
	log.Printf("Data Directory: %s", *dataDirFlag)
	log.Printf("Playback Backend: %s", *backendFlag)

	// Ensure data directory exists
	if err := os.MkdirAll(*dataDirFlag, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	dbPath := filepath.Join(*dataDirFlag, "deckjam.db")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("Failed to open SQLite store at %s: %v", dbPath, err)
	}
	defer st.Close()

	if removed, dErr := st.DeduplicateActiveQueue(); dErr == nil && removed > 0 {
		log.Printf("Cleaned %d duplicate active queue entries from previous session", removed)
	}

	// Update backend setting if specified in flag
	if *backendFlag != "" {
		currentSettings, _ := st.GetSettings()
		currentSettings.PlayerBackend = *backendFlag
		_ = st.UpdateSettings(currentSettings)
	}

	// Host PIN
	if *hostPINFlag != "" {
		_ = st.SetHostPIN(*hostPINFlag)
	}

	activePIN, _ := st.GetHostPIN()
	log.Printf("Host PIN: %s", activePIN)

	fifoPath := *audioFIFOFlag
	if *backendFlag == "mock" && os.Getenv("DECKJAM_AUDIO_FIFO") == "" {
		fifoPath = ""
	}

	// Resolver & Player Coordinator
	resolver := spotify.NewResolver(st)
	coordinator := player.NewCoordinatorWithAudio(st, *librespotURLFlag, *backendFlag, fifoPath, *audioCmdFlag)
	defer coordinator.Close()

	// Server
	srv := server.NewServer(server.Config{
		Addr:         *addrFlag,
		DataDir:      *dataDirFlag,
		Store:        st,
		Resolver:     resolver,
		Coordinator:  coordinator,
		Hostname:     *hostnameFlag,
		HostPIN:      activePIN,
		LibrespotURL: *librespotURLFlag,
	})

	// Graceful shutdown handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("\nReceived shutdown signal. Stopping DeckJam...")
		_ = srv.Close()
		os.Exit(0)
	}()

	if err := srv.Start(); err != nil {
		fmt.Printf("DeckJam stopped: %v\n", err)
	}
}
