#!/usr/bin/env bash
# ==============================================================================
# DeckJam Jukebox UI Launcher for Steam Deck (Gaming Mode & Desktop Mode)
# ==============================================================================
set -euo pipefail

TARGET_URL="http://127.0.0.1:8080/deck"
HEALTH_URL="http://127.0.0.1:8080/api/state"

# 1. Ensure deckjam server is running
if command -v systemctl >/dev/null 2>&1 && systemctl --user is-system-running >/dev/null 2>&1; then
    if ! systemctl --user is-active --quiet deckjam.service; then
        echo "[DeckJam UI] Starting deckjam.service via systemd..."
        systemctl --user start deckjam.service || true
    fi
else
    # Fallback if systemd user manager is not active in current session
    if ! pgrep -x deckjam >/dev/null 2>&1; then
        echo "[DeckJam UI] Starting deckjam binary in background..."
        BIN_PATH=""
        if [ -x "$HOME/.local/bin/deckjam" ]; then
            BIN_PATH="$HOME/.local/bin/deckjam"
        elif command -v deckjam >/dev/null 2>&1; then
            BIN_PATH="$(command -v deckjam)"
        fi

        if [ -n "$BIN_PATH" ]; then
            nohup "$BIN_PATH" --addr 0.0.0.0:8080 >/dev/null 2>&1 &
        else
            echo "[DeckJam UI] Warning: deckjam binary not found in standard paths."
        fi
    fi
fi

# 2. Wait up to 5 seconds for backend to be ready
echo "[DeckJam UI] Waiting for DeckJam backend ($HEALTH_URL)..."
for i in $(seq 1 25); do
    if command -v curl >/dev/null 2>&1; then
        if curl -fs -m 1 "$HEALTH_URL" >/dev/null 2>&1; then
            echo "[DeckJam UI] DeckJam backend is ready!"
            break
        fi
    fi
    sleep 0.2
done

# 3. Detect browser and open UI in kiosk / standalone app window mode
# Flatpak browser IDs common on Steam Deck
FLATPAK_CHROME="com.google.Chrome"
FLATPAK_BRAVE="com.brave.Browser"
FLATPAK_EDGE="com.microsoft.Edge"
FLATPAK_FIREFOX="org.mozilla.firefox"

APP_FLAGS="--app=${TARGET_URL} --window-size=1280,800 --start-maximized --class=DeckJam"

# Check Flatpak Chromium-based browsers first (great kiosk support on SteamOS)
if command -v flatpak >/dev/null 2>&1; then
    if flatpak info "$FLATPAK_CHROME" >/dev/null 2>&1; then
        echo "[DeckJam UI] Launching via Flatpak Chrome..."
        exec flatpak run "$FLATPAK_CHROME" --app="$TARGET_URL" --window-size=1280,800 --start-maximized
    fi

    if flatpak info "$FLATPAK_BRAVE" >/dev/null 2>&1; then
        echo "[DeckJam UI] Launching via Flatpak Brave..."
        exec flatpak run "$FLATPAK_BRAVE" --app="$TARGET_URL" --window-size=1280,800 --start-maximized
    fi

    if flatpak info "$FLATPAK_EDGE" >/dev/null 2>&1; then
        echo "[DeckJam UI] Launching via Flatpak Edge..."
        exec flatpak run "$FLATPAK_EDGE" --app="$TARGET_URL" --window-size=1280,800 --start-maximized
    fi

    if flatpak info "$FLATPAK_FIREFOX" >/dev/null 2>&1; then
        echo "[DeckJam UI] Launching via Flatpak Firefox..."
        exec flatpak run "$FLATPAK_FIREFOX" --kiosk "$TARGET_URL"
    fi
fi

# Check native browsers on PATH
for browser in google-chrome google-chrome-stable chromium chromium-browser brave-browser microsoft-edge; do
    if command -v "$browser" >/dev/null 2>&1; then
        echo "[DeckJam UI] Launching via native $browser..."
        exec "$browser" --app="$TARGET_URL" --window-size=1280,800 --start-maximized
    fi
done

if command -v firefox >/dev/null 2>&1; then
    echo "[DeckJam UI] Launching via native Firefox..."
    exec firefox --kiosk "$TARGET_URL"
fi

# Fallback to system xdg-open
if command -v xdg-open >/dev/null 2>&1; then
    echo "[DeckJam UI] Falling back to xdg-open..."
    exec xdg-open "$TARGET_URL"
fi

echo "[DeckJam UI] Error: Could not detect any suitable web browser to open $TARGET_URL"
exit 1
