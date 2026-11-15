#!/usr/bin/env bash
# ==============================================================================
# DeckJam Cloudflare Quick Tunnel Daemon
# Establishes an ephemeral trycloudflare.com HTTPS tunnel to local port 8080,
# writes the dynamic public URL to ~/.local/share/deckjam/public_url.txt, and
# ensures clean process & file removal on shutdown/restart.
# ==============================================================================
set -euo pipefail

SHARE_DIR="$HOME/.local/share/deckjam"
PUB_FILE="$SHARE_DIR/public_url.txt"
LOG_FIFO="$(mktemp -u /tmp/deckjam-cf-fifo.XXXXXX)"
BIN_CLOUDFLARED="$HOME/.local/bin/cloudflared"
CF_PID=""

mkdir -p "$SHARE_DIR"
rm -f "$PUB_FILE"

cleanup() {
    rm -f "$PUB_FILE" "$LOG_FIFO"
    if [ -n "$CF_PID" ] && kill -0 "$CF_PID" 2>/dev/null; then
        kill "$CF_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM

CLOUDFLARED_CMD=""
if [ -x "$BIN_CLOUDFLARED" ]; then
    CLOUDFLARED_CMD="$BIN_CLOUDFLARED"
elif command -v cloudflared >/dev/null 2>&1; then
    CLOUDFLARED_CMD="$(command -v cloudflared)"
else
    echo "[deckjam-tunnel] Error: cloudflared not found in $BIN_CLOUDFLARED or on PATH."
    exit 1
fi

mkfifo "$LOG_FIFO"
echo "[deckjam-tunnel] Launching Cloudflare Quick Tunnel to http://127.0.0.1:8080..."

"$CLOUDFLARED_CMD" tunnel --no-autoupdate --url http://127.0.0.1:8080 >"$LOG_FIFO" 2>&1 &
CF_PID=$!

while IFS= read -r line; do
    echo "$line"
    if [[ "$line" =~ (https://[a-zA-Z0-9-]+\.trycloudflare\.com) ]]; then
        TUNNEL_URL="${BASH_REMATCH[1]}"
        echo "[deckjam-tunnel] Quick Tunnel Ready: $TUNNEL_URL"
        echo "$TUNNEL_URL" > "$PUB_FILE.tmp"
        mv -f "$PUB_FILE.tmp" "$PUB_FILE"
    fi
done < "$LOG_FIFO"

wait "$CF_PID"
