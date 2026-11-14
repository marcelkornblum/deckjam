#!/usr/bin/env bash
# ==============================================================================
# DeckJam Steam Deck / SteamOS Zero-Root User-Space Installer
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

FORCE=0
for arg in "$@"; do
    case "$arg" in
        --force|-f)
            FORCE=1
            ;;
    esac
done

echo "=========================================================="
echo "      DeckJam — Steam Deck Jukebox Setup & Installer     "
echo "=========================================================="
echo "[*] Repo Root: $REPO_ROOT"
echo "[*] Install mode: user-space (zero root / read-only rootfs safe)"
echo ""

# 1. Target Directories
BIN_DIR="$HOME/.local/bin"
CONFIG_DIR="$HOME/.config/go-librespot"
SYSTEMD_USER_DIR="$HOME/.config/systemd/user"
APP_DIR="$HOME/.local/share/applications"
SHARE_DECKJAM_DIR="$HOME/.local/share/deckjam"
DESKTOP_DIR="$HOME/Desktop"

mkdir -p "$BIN_DIR" "$CONFIG_DIR" "$SYSTEMD_USER_DIR" "$APP_DIR" "$SHARE_DECKJAM_DIR"
[ -d "$DESKTOP_DIR" ] || mkdir -p "$DESKTOP_DIR"

# Ensure ~/.local/bin is on PATH in current shell if not already
export PATH="$BIN_DIR:$PATH"

# 2. Build or Copy deckjam Binary
echo "[+] Step 1: Installing deckjam binary..."
if [ -f "$REPO_ROOT/deckjam" ] && [ -x "$REPO_ROOT/deckjam" ]; then
    echo "    Found prebuilt binary at $REPO_ROOT/deckjam. Installing..."
    cp -f "$REPO_ROOT/deckjam" "$BIN_DIR/deckjam"
    chmod +x "$BIN_DIR/deckjam"
elif command -v go >/dev/null 2>&1 && [ -f "$REPO_ROOT/go.mod" ]; then
    echo "    Building deckjam from source with Go..."
    (
        cd "$REPO_ROOT"
        go build -o "$BIN_DIR/deckjam" ./cmd/deckjam
    )
    chmod +x "$BIN_DIR/deckjam"
    echo "    Successfully built and installed $BIN_DIR/deckjam."
else
    echo "    [!] Warning: Neither prebuilt './deckjam' binary nor 'go' compiler found."
    echo "    If you plan to run deckjam, build it or place the compiled binary into $BIN_DIR/deckjam."
fi

# 3. Download or Verify go-librespot
echo "[+] Step 2: Checking go-librespot..."
LIBRESPOT_BIN="$BIN_DIR/go-librespot"
if [ -x "$LIBRESPOT_BIN" ] && [ "$FORCE" -eq 0 ]; then
    echo "    go-librespot is already installed at $LIBRESPOT_BIN (use --force to overwrite)."
else
    echo "    Fetching go-librespot release info from GitHub..."
    TMP_DIR="$(mktemp -d /tmp/deckjam-install.XXXXXX)"
    DOWNLOAD_SUCCESS=0

    if command -v curl >/dev/null 2>&1; then
        TARBALL_URL=""
        LATEST_JSON="$(curl -fsSL -m 10 "https://api.github.com/repos/devgianlu/go-librespot/releases/latest" 2>/dev/null || true)"
        if [ -n "$LATEST_JSON" ]; then
            TARBALL_URL="$(echo "$LATEST_JSON" | grep -o 'https://[^"]*linux_x86_64\.tar\.gz' | head -n 1 || true)"
        fi

        # Fallback to known stable URL if API rate-limited or JSON parse failed
        if [ -z "$TARBALL_URL" ]; then
            TARBALL_URL="https://github.com/devgianlu/go-librespot/releases/latest/download/go-librespot_linux_x86_64.tar.gz"
        fi

        echo "    Downloading from: $TARBALL_URL ..."
        if curl -fSL -m 30 "$TARBALL_URL" -o "$TMP_DIR/go-librespot.tar.gz" 2>/dev/null; then
            tar -xzf "$TMP_DIR/go-librespot.tar.gz" -C "$TMP_DIR"
            if [ -f "$TMP_DIR/go-librespot" ]; then
                cp -f "$TMP_DIR/go-librespot" "$LIBRESPOT_BIN"
                chmod +x "$LIBRESPOT_BIN"
                DOWNLOAD_SUCCESS=1
                echo "    go-librespot installed successfully to $LIBRESPOT_BIN."
            fi
        fi
    fi

    rm -rf "$TMP_DIR"

    if [ "$DOWNLOAD_SUCCESS" -ne 1 ] && [ ! -x "$LIBRESPOT_BIN" ]; then
        echo "    ------------------------------------------------------------------"
        echo "    [!] Notice: Could not download go-librespot automatically (offline?)."
        echo "        DeckJam can operate with the Official Spotify Flatpak via MPRIS"
        echo "        (com.spotify.Client) as an alternative player backend!"
        echo "        To install go-librespot manually later, place the binary in:"
        echo "        $LIBRESPOT_BIN"
        echo "    ------------------------------------------------------------------"
    fi
fi

# 4. Deploy go-librespot config
echo "[+] Step 3: Configuring go-librespot..."
CONFIG_FILE="$CONFIG_DIR/config.yml"
CONFIG_SRC="$REPO_ROOT/packaging/go-librespot-config.yml"

if [ -f "$CONFIG_FILE" ] && [ "$FORCE" -eq 0 ]; then
    echo "    Existing configuration preserved at $CONFIG_FILE (pass --force to overwrite)."
else
    if [ -f "$CONFIG_SRC" ]; then
        cp -f "$CONFIG_SRC" "$CONFIG_FILE"
        echo "    Wrote SteamOS configuration to $CONFIG_FILE."
    else
        echo "    [!] Warning: Template $CONFIG_SRC not found."
    fi
fi

# 5. Install systemd user services
echo "[+] Step 4: Installing systemd --user service units..."
cp -f "$REPO_ROOT/packaging/systemd/go-librespot.service" "$SYSTEMD_USER_DIR/"
cp -f "$REPO_ROOT/packaging/systemd/deckjam.service" "$SYSTEMD_USER_DIR/"

if command -v systemctl >/dev/null 2>&1; then
    echo "    Reloading user systemd daemon and enabling services..."
    systemctl --user daemon-reload || true
    systemctl --user enable --now go-librespot.service 2>/dev/null || true
    systemctl --user enable --now deckjam.service 2>/dev/null || true
fi

# 6. Install Helper & UI Launcher
echo "[+] Step 5: Installing UI launcher and shortcuts..."
cp -f "$REPO_ROOT/scripts/launch-jukebox-ui.sh" "$SHARE_DECKJAM_DIR/launch-jukebox-ui.sh"
chmod +x "$SHARE_DECKJAM_DIR/launch-jukebox-ui.sh"

ln -sf "$SHARE_DECKJAM_DIR/launch-jukebox-ui.sh" "$BIN_DIR/deckjam-ui"
chmod +x "$BIN_DIR/deckjam-ui"

# Install .desktop file
DESKTOP_SRC="$REPO_ROOT/packaging/desktop/deckjam.desktop"
if [ -f "$DESKTOP_SRC" ]; then
    cp -f "$DESKTOP_SRC" "$APP_DIR/deckjam.desktop"
    chmod +x "$APP_DIR/deckjam.desktop"

    if [ -d "$DESKTOP_DIR" ]; then
        cp -f "$DESKTOP_SRC" "$DESKTOP_DIR/deckjam.desktop"
        chmod +x "$DESKTOP_DIR/deckjam.desktop" 2>/dev/null || true
    fi

    # SteamOS Non-Steam Game Integrator
    if command -v steamos-add-to-steam >/dev/null 2>&1; then
        echo "    Registering DeckJam with Steam Gaming Mode via steamos-add-to-steam..."
        steamos-add-to-steam "$APP_DIR/deckjam.desktop" || true
    fi
fi

# 7. Detect Network IP for Summary
DETECTED_IP="127.0.0.1"
if command -v ip >/dev/null 2>&1; then
    PRIMARY_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{print $7; exit}' || true)"
    if [ -n "$PRIMARY_IP" ]; then
        DETECTED_IP="$PRIMARY_IP"
    else
        DETECTED_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || echo "127.0.0.1")"
    fi
elif command -v hostname >/dev/null 2>&1; then
    DETECTED_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || echo "127.0.0.1")"
fi

echo ""
echo "=========================================================="
echo "          DeckJam Installation Completed! 🎉             "
echo "=========================================================="
echo ""
echo "📱 Mobile Party URL (Guests):"
echo "   http://${DETECTED_IP}:8080"
echo "   http://steamdeck.local:8080"
echo ""
echo "🎮 Steam Deck Kiosk / Jukebox UI (Deck Screen / TV Dock):"
echo "   http://127.0.0.1:8080/deck"
echo "   Command: deckjam-ui"
echo ""
echo "🎵 One-Time Spotify Connect Pairing:"
echo "   1. Open the Spotify app on your phone, tablet, or laptop on the same Wi-Fi."
echo "   2. Tap 'Devices' (Connect icon) and select 'Steam Deck Jam'."
echo "   3. Credentials will persist automatically across reboots!"
echo ""
echo "🚗 Road Trip / Off-Grid Hotspot Mode:"
echo "   If there is no Wi-Fi router, start the Deck hotspot:"
echo "   ./scripts/hotspot-steamdeck.sh start"
echo "=========================================================="
