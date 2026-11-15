#!/usr/bin/env bash
# ==============================================================================
# DeckJam Cloudflare Quick Tunnel Daemon
# Establishes an ephemeral trycloudflare.com HTTPS tunnel to local port 8080,
# writes the dynamic public URL to ~/.local/share/deckjam/public_url.txt, and
# ensures clean removal on shutdown/restart.
# ==============================================================================
set -euo pipefail

SHARE_DIR="$HOME/.local/share/deckjam"
PUB_FILE="$SHARE_DIR/public_url.txt"
BIN_CLOUDFLARED="$HOME/.local/bin/cloudflared"

mkdir -p "$SHARE_DIR"

cleanup() {
    echo "[deckjam-tunnel] Stopping tunnel and removing $PUB_FILE..."
    rm -f "$PUB_FILE"
}
trap cleanup EXIT INT TERM

# Remove stale file on startup
rm -f "$PUB_FILE"

# Locate cloudflared binary
CLOUDFLARED_CMD=""
if [ -x "$BIN_CLOUDFLARED" ]; then
    CLOUDFLARED_CMD="$BIN_CLOUDFLARED"
elif command -v cloudflared >/dev/null 2>&1; then
    CLOUDFLARED_CMD="$(command -v cloudflared)"
else
    echo "[deckjam-tunnel] Error: cloudflared not found in $BIN_CLOUDFLARED or on PATH."
    exit 1
fi

echo "[deckjam-tunnel] Launching Cloudflare Quick Tunnel to http://127.0.0.1:8080..."

# Run cloudflared with stderr redirected to stdout and parse each line
"$CLOUDFLARED_CMD" tunnel --no-autoupdate --url http://127.0.0.1:8080 2>&1 | while IFS= read -r line; do
    echo "$line"
    # Search for https://*.trycloudflare.com URL pattern
    if [[ "$line" =~ (https://[a-zA-Z0-9-]+\.trycloudflare\.com) ]]; then
        TUNNEL_URL="${BASH_REMATCH[1]}"
        echo "[deckjam-tunnel] Detected Quick Tunnel URL: $TUNNEL_URL"
        echo "$TUNNEL_URL" > "$PUB_FILE"
    fi
done
