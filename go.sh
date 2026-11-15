#!/usr/bin/env bash
# ==============================================================================
# DeckJam Turnkey Launcher (go.sh)
# 100% foolproof, zero-keyboard turnkey Steam Deck party launcher over a Phone Hotspot.
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$SCRIPT_DIR"

echo "=========================================================="
echo "          DeckJam — Zero-Keyboard Turnkey Launcher        "
echo "=========================================================="

# 1. Run user-space installer with --force to ensure updated units & configs are active
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

if [ "$BACKEND_READY" -ne 1 ]; then
    echo "[!] Warning: DeckJam backend did not respond within 8s."
fi

# 3. Wait up to 8s for Cloudflare Quick Tunnel (if network connected)
PUB_FILE="$HOME/.local/share/deckjam/public_url.txt"
JOIN_URL=""
for i in $(seq 1 40); do
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

# 4. Check for Spotify Pair Code from go-librespot daemon
PAIR_CODE=""
PAIR_URL=""
if curl -fs -m 2 http://127.0.0.1:3678/auth/code >/dev/null 2>&1; then
    AUTH_JSON="$(curl -fs -m 2 http://127.0.0.1:3678/auth/code 2>/dev/null || true)"
    if [ -n "$AUTH_JSON" ]; then
        PAIR_CODE="$(echo "$AUTH_JSON" | grep -o '"code":"[^"]*"' | cut -d'"' -f4 || true)"
        PAIR_URL="$(echo "$AUTH_JSON" | grep -o '"url":"[^"]*"' | cut -d'"' -f4 || true)"
    fi
fi

# 5. Output Scannable Terminal QR Code
DECKJAM_BIN="$HOME/.local/bin/deckjam"
if [ ! -x "$DECKJAM_BIN" ] && [ -x "$REPO_ROOT/deckjam" ]; then
    DECKJAM_BIN="$REPO_ROOT/deckjam"
fi

echo ""
if [ -x "$DECKJAM_BIN" ]; then
    "$DECKJAM_BIN" --qr "$JOIN_URL" || true
fi

# 6. Clear 2-line summary
echo "=========================================================="
echo "Scan QR with your phone camera! Join URL: $JOIN_URL"
if [ -n "$PAIR_CODE" ]; then
    echo "Spotify Pair URL: ${PAIR_URL:-https://spotify.com/pair?code=$PAIR_CODE} (Pair Code: $PAIR_CODE)"
else
    echo "Deck UI: http://127.0.0.1:8080/deck (Launch with: deckjam-ui)"
fi
echo "=========================================================="
