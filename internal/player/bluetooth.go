package player

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AudioSinkInfo describes the currently active SteamOS PipeWire / PulseAudio output sink.
type AudioSinkInfo struct {
	SinkID      string // e.g. "bluez_output.AA_BB_CC_DD_EE_FF.1" or "alsa_output..."
	DisplayName string // e.g. "JBL PartyBox" or "Steam Deck Speakers"
	IsBluetooth bool
}

var (
	btMu           sync.Mutex
	lastBtSync     time.Time
	cachedSinkInfo AudioSinkInfo
	pctRegex       = regexp.MustCompile(`(\d+)%`)
)

// EnsureBluetoothSink is a throttled wrapper around DiscoverAndEnsureSink for backward compatibility.
func EnsureBluetoothSink() {
	_ = DiscoverAndEnsureSink(false)
}

// GetCachedSinkInfo returns the most recently discovered PipeWire/PulseAudio sink info.
func GetCachedSinkInfo() AudioSinkInfo {
	btMu.Lock()
	defer btMu.Unlock()
	return cachedSinkInfo
}

// DiscoverAndEnsureSink inspects PipeWire / PulseAudio sinks, activates A2DP if a Bluetooth
// speaker card is stuck on "off", sets any connected bluez_output.* (e.g. JBL PartyBox) as default,
// unmutes the active sink, raises near-zero AVRCP hardware volume (<25% -> 75%), and returns sink details.
func DiscoverAndEnsureSink(force bool) AudioSinkInfo {
	btMu.Lock()
	now := time.Now()
	if !force && now.Sub(lastBtSync) < 4*time.Second {
		info := cachedSinkInfo
		btMu.Unlock()
		return info
	}
	lastBtSync = now
	prev := cachedSinkInfo
	btMu.Unlock()

	// Unmute WirePlumber default sink if wpctl is available (SteamOS native)
	if wpctlPath, err := exec.LookPath("wpctl"); err == nil && wpctlPath != "" {
		ctxWp, cancelWp := context.WithTimeout(context.Background(), 1200*time.Millisecond)
		_ = exec.CommandContext(ctxWp, wpctlPath, "set-mute", "@DEFAULT_AUDIO_SINK@", "0").Run()
		cancelWp()
	}

	pactlPath, err := exec.LookPath("pactl")
	if err != nil || pactlPath == "" {
		return prev
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	// 1. If a bluez_card.* exists with Active Profile: off, nudge it to a2dp-sink
	if cardsOut, err := exec.CommandContext(ctx, pactlPath, "list", "cards").Output(); err == nil {
		ensureBluezCardA2DP(ctx, pactlPath, string(cardsOut))
	}

	// 2. Parse full `pactl list sinks` to extract sink names, human-readable descriptions, mute & volume
	sinksOut, err := exec.CommandContext(ctx, pactlPath, "list", "sinks").Output()
	if err != nil {
		return prev
	}

	defaultSinkBytes, _ := exec.CommandContext(ctx, pactlPath, "get-default-sink").Output()
	defaultSink := strings.TrimSpace(string(defaultSinkBytes))

	parsed := parsePactlSinks(string(sinksOut))
	var chosen *parsedSink
	for i := range parsed {
		if strings.HasPrefix(parsed[i].name, "bluez_output.") {
			chosen = &parsed[i]
			break
		}
	}
	if chosen == nil {
		for i := range parsed {
			if parsed[i].name == defaultSink && parsed[i].name != "" {
				chosen = &parsed[i]
				break
			}
		}
	}
	if chosen == nil && len(parsed) > 0 {
		chosen = &parsed[0]
	}

	if chosen == nil {
		return prev
	}

	isBT := strings.HasPrefix(chosen.name, "bluez_output.")

	// 3. Ensure chosen sink is default (if Bluetooth), unmuted, and not stuck at 0% AVRCP volume
	if isBT && chosen.name != defaultSink {
		_ = exec.CommandContext(ctx, pactlPath, "set-default-sink", chosen.name).Run()
	}
	_ = exec.CommandContext(ctx, pactlPath, "set-sink-mute", chosen.name, "0").Run()
	if chosen.volumePct >= 0 && chosen.volumePct < 25 {
		// JBL / Bluetooth AVRCP quirk: speakers often initialize at 0-10% hardware sink volume on Linux
		_ = exec.CommandContext(ctx, pactlPath, "set-sink-volume", chosen.name, "75%").Run()
	}

	// 4. Move any active sink-inputs onto Bluetooth sink when connected
	if isBT {
		if inputsOut, err := exec.CommandContext(ctx, pactlPath, "list", "short", "sink-inputs").Output(); err == nil {
			for _, line := range strings.Split(string(inputsOut), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 1 && fields[0] != "" {
					_ = exec.CommandContext(ctx, pactlPath, "move-sink-input", fields[0], chosen.name).Run()
				}
			}
		}
	}

	display := strings.TrimSpace(chosen.description)
	if display == "" {
		if isBT {
			display = "Bluetooth Speaker"
		} else {
			display = "Steam Deck Audio Out"
		}
	}

	info := AudioSinkInfo{
		SinkID:      chosen.name,
		DisplayName: display,
		IsBluetooth: isBT,
	}

	btMu.Lock()
	cachedSinkInfo = info
	btMu.Unlock()

	return info
}

type parsedSink struct {
	name        string
	description string
	volumePct   int
}

func parsePactlSinks(raw string) []parsedSink {
	var sinks []parsedSink
	var cur *parsedSink

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Sink #") {
			if cur != nil && cur.name != "" {
				sinks = append(sinks, *cur)
			}
			cur = &parsedSink{volumePct: -1}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(trimmed, "Name:") {
			cur.name = strings.TrimSpace(strings.TrimPrefix(trimmed, "Name:"))
		} else if strings.HasPrefix(trimmed, "Description:") {
			cur.description = strings.TrimSpace(strings.TrimPrefix(trimmed, "Description:"))
		} else if strings.HasPrefix(trimmed, "Volume:") && cur.volumePct < 0 {
			if m := pctRegex.FindStringSubmatch(trimmed); len(m) == 2 {
				if v, err := strconv.Atoi(m[1]); err == nil {
					cur.volumePct = v
				}
			}
		} else if strings.HasPrefix(trimmed, "device.description =") && cur.description == "" {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "device.description ="))
			cur.description = strings.Trim(val, `"`)
		}
	}
	if cur != nil && cur.name != "" {
		sinks = append(sinks, *cur)
	}
	return sinks
}

func ensureBluezCardA2DP(ctx context.Context, pactlPath string, cardsRaw string) {
	var cardName string
	for _, line := range strings.Split(cardsRaw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Name:") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "Name:"))
			if strings.HasPrefix(name, "bluez_card.") {
				cardName = name
			} else {
				cardName = ""
			}
		} else if cardName != "" && strings.HasPrefix(trimmed, "Active Profile:") {
			prof := strings.TrimSpace(strings.TrimPrefix(trimmed, "Active Profile:"))
			if prof == "off" {
				_ = exec.CommandContext(ctx, pactlPath, "set-card-profile", cardName, "a2dp-sink").Run()
			}
			cardName = ""
		}
	}
}
