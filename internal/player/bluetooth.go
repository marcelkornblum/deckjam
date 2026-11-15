package player

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	btMu       sync.Mutex
	lastBtSync time.Time
)

// EnsureBluetoothSink is a safe best-effort helper that routes audio output to any connected
// Bluetooth audio sink (e.g., JBL PartyBox) via pactl.
// It is throttled to run at most once every 5 seconds.
func EnsureBluetoothSink() {
	btMu.Lock()
	now := time.Now()
	if now.Sub(lastBtSync) < 5*time.Second {
		btMu.Unlock()
		return
	}
	lastBtSync = now
	btMu.Unlock()

	pactlPath, err := exec.LookPath("pactl")
	if err != nil || pactlPath == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	// 1. pactl list short sinks
	out, err := exec.CommandContext(ctx, pactlPath, "list", "short", "sinks").Output()
	if err != nil {
		return
	}

	var bluetoothSink string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			sinkName := fields[1]
			if strings.HasPrefix(sinkName, "bluez_output.") {
				bluetoothSink = sinkName
				break
			}
		}
	}

	if bluetoothSink == "" {
		return
	}

	// 2. Set as default sink and unmute
	_ = exec.CommandContext(ctx, pactlPath, "set-default-sink", bluetoothSink).Run()
	_ = exec.CommandContext(ctx, pactlPath, "set-sink-mute", bluetoothSink, "0").Run()

	// 3. Move existing sink-inputs to bluetoothSink
	inputsOut, err := exec.CommandContext(ctx, pactlPath, "list", "short", "sink-inputs").Output()
	if err == nil {
		inputLines := strings.Split(string(inputsOut), "\n")
		for _, line := range inputLines {
			fields := strings.Fields(line)
			if len(fields) >= 1 && fields[0] != "" {
				inputID := fields[0]
				_ = exec.CommandContext(ctx, pactlPath, "move-sink-input", inputID, bluetoothSink).Run()
			}
		}
	}
}
