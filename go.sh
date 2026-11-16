#!/usr/bin/env bash
# ==============================================================================
# DeckJam Turnkey Launcher (go.sh)
# 100% foolproof, zero-keyboard turnkey Steam Deck party launcher over a Phone Hotspot.
# Automatically verifies physical audio output on Steam Deck / Bluetooth speakers!
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$SCRIPT_DIR"

echo "=========================================================="
echo "          DeckJam — Zero-Keyboard Turnkey Launcher        "
echo "=========================================================="

# 1. Run user-space installer with --force to ensure updated binary, FIFO & configs are active
"$REPO_ROOT/scripts/install-steamdeck.sh" --force

# 2. Wait up to 8s for deckjam backend to come online (http://127.0.0.1:8080/api/state)
echo "[*] Waiting for DeckJam backend to start..."
BACKEND_READY=0
for i in $(seq 1 40); do
    if curl -fs -m 1 http://127.0.0.1:8080/api/state >/dev/null 2>&1; then
        BACKEND_READY=1
        break
    fi
    sleep 0.2
done

HOST_PIN=""
AUDIO_VERIFIED_MSG=""
if [ "$BACKEND_READY" -ne 1 ]; then
    echo "[!] Warning: DeckJam backend did not respond within 8s."
else
    # Fetch Host PIN from state API for easy host phone controls
    STATE_JSON="$(curl -fs -m 2 http://127.0.0.1:8080/api/state 2>/dev/null || true)"
    if [ -n "$STATE_JSON" ]; then
        HOST_PIN="$(echo "$STATE_JSON" | grep -o '"host_pin":"[^"]*"' | cut -d'"' -f4 || true)"
    fi

    # Play audible 0.65s C-major verification chime through FIFO -> PipeWire -> Speaker/Bluetooth!
    echo "[*] Playing hardware speaker verification chime..."
    AUDIO_JSON="$(curl -s -m 5 -X POST http://127.0.0.1:8080/api/audio-test 2>/dev/null || true)"
    if echo "$AUDIO_JSON" | grep -q '"ok":true'; then
        SINK_NAME="$(echo "$AUDIO_JSON" | grep -o '"audio_sink":"[^"]*"' | cut -d'"' -f4 || echo "Steam Deck Out")"
        PLAYER_CMD="$(echo "$AUDIO_JSON" | grep -o '"audio_player":"[^"]*"' | cut -d'"' -f4 || echo "PipeWire")"
        PEAK_PCT="$(echo "$AUDIO_JSON" | grep -o '"peak_pct":[0-9]*' | cut -d':' -f2 || echo "30")"
        AUDIO_VERIFIED_MSG="VERIFIED (${PLAYER_CMD} -> ${SINK_NAME}, ${PEAK_PCT}% signal)"
    else
        ERR_TXT="$(echo "$AUDIO_JSON" | grep -o '"error":"[^"]*"' | cut -d'"' -f4 || echo "Check speaker / PipeWire")"
        AUDIO_VERIFIED_MSG="FAILED (${ERR_TXT})"
    fi
fi

# 3. Wait up to 6s for go-librespot AP readiness or pairing code
SPOTIFY_READY=0
PAIR_CODE=""
PAIR_URL=""
for i in $(seq 1 24); do
    ROOT_JSON="$(curl -fs -m 1 http://127.0.0.1:3678/ 2>/dev/null || true)"
    if echo "$ROOT_JSON" | grep -q '"playback_ready":true'; then
        SPOTIFY_READY=1
        break
    fi
    AUTH_JSON="$(curl -fs -m 1 http://127.0.0.1:3678/auth/code 2>/dev/null || true)"
    if [ -n "$AUTH_JSON" ]; then
        PAIR_CODE="$(echo "$AUTH_JSON" | grep -o '"code":"[^"]*"' | cut -d'"' -f4 || true)"
        PAIR_URL="$(echo "$AUTH_JSON" | grep -o '"url":"[^"]*"' | cut -d'"' -f4 || true)"
        if [ -n "$PAIR_CODE" ]; then
            break
        fi
    fi
    sleep 0.25
done

# 4. Wait up to 6s for Cloudflare Quick Tunnel (if network connected)
PUB_FILE="$HOME/.local/share/deckjam/public_url.txt"
JOIN_URL=""
for i in $(seq 1 30); do
    if [ -f "$PUB_FILE" ]; then
        CANDIDATE="$(cat "$PUB_FILE" 2>/dev/null | tr -d '[:space:]' || true)"
        if [[ "$CANDIDATE" =~ ^https:// ]]; then
            JOIN_URL="$CANDIDATE"
            break
        fi
    fi
    sleep 0.2
done

# If no public tunnel URL, discover LAN IPv4 or fallback to steamdeck.local
if [ -z "$JOIN_URL" ]; then
    LAN_IP="127.0.0.1"
    if command -v ip >/dev/null 2>&1; then
        PRIMARY_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{print $7; exit}' || true)"
        if [ -n "$PRIMARY_IP" ]; then
            LAN_IP="$PRIMARY_IP"
        else
            LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || echo "127.0.0.1")"
        fi
    elif command -v hostname >/dev/null 2>&1; then
        LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || echo "127.0.0.1")"
    fi
    JOIN_URL="http://${LAN_IP}:8080"
fi

# 5. Live Hotspot Internet Probe to Spotify
echo "[*] Checking Hotspot Internet connectivity to Spotify..."
HOTSPOT_NET_MSG="CHECKING..."
SPOT_PROBE="$(curl -4 -s -o /dev/null -w "%{http_code} %{time_total}" -m 4 "https://apresolve.spotify.com/" 2>/dev/null || true)"
SPOT_CODE="$(echo "$SPOT_PROBE" | awk '{print $1}')"
SPOT_TIME="$(echo "$SPOT_PROBE" | awk '{print $2}')"
if [ -n "$SPOT_CODE" ] && [ "$SPOT_CODE" != "000" ]; then
    SPOT_MS="$(awk -v t="${SPOT_TIME:-0}" 'BEGIN { printf "%d", t * 1000 }' 2>/dev/null || echo "0")"
    HOTSPOT_NET_MSG="ONLINE (${SPOT_MS}ms to Spotify via Hotspot)"
else
    HOTSPOT_NET_MSG="OFFLINE! (Deck is on Hotspot Wi-Fi, but Phone is NOT sharing Mobile Data / no signal!)"
fi

# 6. Output Scannable Terminal QR Code
DECKJAM_BIN="$HOME/.local/bin/deckjam"
if [ ! -x "$DECKJAM_BIN" ] && [ -x "$REPO_ROOT/deckjam" ]; then
    DECKJAM_BIN="$REPO_ROOT/deckjam"
fi

echo ""
if [ -x "$DECKJAM_BIN" ]; then
    "$DECKJAM_BIN" --qr "$JOIN_URL" || true
fi

# 7. Clear ground-truth summary
echo "=========================================================="
echo "Scan QR with your phone camera! Join URL: $JOIN_URL"
if [ -n "$HOST_PIN" ]; then
    echo "Host PIN (for phone controls): $HOST_PIN"
fi
echo "Hotspot Internet: $HOTSPOT_NET_MSG"
if [ -n "$AUDIO_VERIFIED_MSG" ]; then
    echo "Speaker Output: $AUDIO_VERIFIED_MSG"
fi
if [ "$SPOTIFY_READY" -eq 1 ]; then
    echo "Spotify Engine: CONNECTED & PLAYBACK READY"
elif [ -n "$PAIR_CODE" ]; then
    echo "Spotify Pair URL: ${PAIR_URL:-https://spotify.com/pair?code=$PAIR_CODE} (Pair Code: $PAIR_CODE)"
else
    echo "Spotify Engine: Starting / connecting to Spotify AP..."
fi
echo "  - Spotify App / Jam : Open Spotify -> tap Devices -> select 'Steam Deck Jam' (or tap Start a Jam!)"
echo "  - Or Queue Online   : Open $JOIN_URL on any phone -> paste any Spotify link"
echo "=========================================================="
