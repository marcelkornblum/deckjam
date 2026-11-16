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

# 2. Build or Copy deckjam Binary (atomic mv avoids 'Text file busy' if running)
echo "[+] Step 1: Installing deckjam binary..."
if [ -f "$REPO_ROOT/bin/deckjam-linux-amd64.gz" ] && { [ ! -x "$REPO_ROOT/deckjam" ] || [ "$REPO_ROOT/bin/deckjam-linux-amd64.gz" -nt "$REPO_ROOT/deckjam" ]; }; then
    echo "    Extracting prebuilt binary from bin/deckjam-linux-amd64.gz to $BIN_DIR/deckjam..."
    gunzip -c "$REPO_ROOT/bin/deckjam-linux-amd64.gz" > "$BIN_DIR/deckjam.tmp"
    chmod +x "$BIN_DIR/deckjam.tmp"
    mv -f "$BIN_DIR/deckjam.tmp" "$BIN_DIR/deckjam"
    echo "    Successfully installed $BIN_DIR/deckjam."
elif [ -f "$REPO_ROOT/deckjam" ] && [ -x "$REPO_ROOT/deckjam" ]; then
    echo "    Found fresher local binary at $REPO_ROOT/deckjam. Installing..."
    cp -f "$REPO_ROOT/deckjam" "$BIN_DIR/deckjam.tmp"
    chmod +x "$BIN_DIR/deckjam.tmp"
    mv -f "$BIN_DIR/deckjam.tmp" "$BIN_DIR/deckjam"
elif command -v go >/dev/null 2>&1 && [ -f "$REPO_ROOT/go.mod" ]; then
    echo "    Building deckjam from source with Go..."
    (
        cd "$REPO_ROOT"
        CGO_ENABLED=0 go build -o "$BIN_DIR/deckjam.tmp" ./cmd/deckjam
    )
    chmod +x "$BIN_DIR/deckjam.tmp"
    mv -f "$BIN_DIR/deckjam.tmp" "$BIN_DIR/deckjam"
    echo "    Successfully built and installed $BIN_DIR/deckjam."
else
    echo "    [!] Warning: Neither prebuilt binary nor 'go' compiler found."
fi

# 3. Download or Verify cloudflared
echo "[+] Step 2: Checking cloudflared..."
CLOUDFLARED_BIN="$BIN_DIR/cloudflared"
if [ -x "$CLOUDFLARED_BIN" ]; then
    echo "    cloudflared is already installed at $CLOUDFLARED_BIN."
else
    echo "    Downloading cloudflared-linux-amd64..."
    if command -v curl >/dev/null 2>&1; then
        curl -fSL -m 45 "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64" -o "$CLOUDFLARED_BIN.tmp" 2>/dev/null || true
        if [ -f "$CLOUDFLARED_BIN.tmp" ] && [ -s "$CLOUDFLARED_BIN.tmp" ]; then
            chmod +x "$CLOUDFLARED_BIN.tmp"
            mv -f "$CLOUDFLARED_BIN.tmp" "$CLOUDFLARED_BIN"
            echo "    cloudflared installed successfully to $CLOUDFLARED_BIN."
        else
            rm -f "$CLOUDFLARED_BIN.tmp"
            echo "    [!] Warning: Could not download cloudflared (offline?); local LAN IP will still work."
        fi
    fi
fi

# 4. Download or Refresh go-librespot (refresh on --force if internet is up so bare-track-URI fix is present)
echo "[+] Step 3: Checking go-librespot..."
LIBRESPOT_BIN="$BIN_DIR/go-librespot"
NEED_LIBRESPOT_DL=0
if [ ! -x "$LIBRESPOT_BIN" ]; then
    NEED_LIBRESPOT_DL=1
elif [ "$FORCE" -eq 1 ] && command -v curl >/dev/null 2>&1; then
    if curl -4 -fsI -m 5 "https://github.com" >/dev/null 2>&1; then
        NEED_LIBRESPOT_DL=1
    else
        echo "    go-librespot already installed (offline hotspot — keeping existing binary)."
    fi
else
    echo "    go-librespot is already installed at $LIBRESPOT_BIN."
fi

if [ "$NEED_LIBRESPOT_DL" -eq 1 ]; then
    echo "    Ensuring latest go-librespot binary..."
    TMP_DIR="$(mktemp -d /tmp/deckjam-install.XXXXXX)"
    DOWNLOAD_SUCCESS=0

    if command -v curl >/dev/null 2>&1; then
        TARBALL_URL="https://github.com/devgianlu/go-librespot/releases/latest/download/go-librespot_linux_x86_64.tar.gz"
        echo "    Downloading from: $TARBALL_URL ..."
        if curl -4 -fSL -m 35 "$TARBALL_URL" -o "$TMP_DIR/go-librespot.tar.gz" 2>/dev/null || curl -fSL -m 35 "$TARBALL_URL" -o "$TMP_DIR/go-librespot.tar.gz" 2>/dev/null; then
            tar -xzf "$TMP_DIR/go-librespot.tar.gz" -C "$TMP_DIR"
            if [ -f "$TMP_DIR/go-librespot" ]; then
                cp -f "$TMP_DIR/go-librespot" "$LIBRESPOT_BIN.tmp"
                chmod +x "$LIBRESPOT_BIN.tmp"
                mv -f "$LIBRESPOT_BIN.tmp" "$LIBRESPOT_BIN"
                DOWNLOAD_SUCCESS=1
                echo "    go-librespot updated successfully at $LIBRESPOT_BIN."
            fi
        fi
    fi

    rm -rf "$TMP_DIR"

    if [ "$DOWNLOAD_SUCCESS" -ne 1 ] && [ ! -x "$LIBRESPOT_BIN" ]; then
        echo "    ------------------------------------------------------------------"
        echo "    [!] Notice: Could not download go-librespot automatically (offline?)."
        echo "        To install go-librespot manually later, place the binary in:"
        echo "        $LIBRESPOT_BIN"
        echo "    ------------------------------------------------------------------"
    fi
fi

# 5. Create Audio FIFO & Deploy go-librespot config
echo "[+] Step 4: Configuring PipeWire Audio FIFO & go-librespot..."
RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
if [ ! -d "$RUNTIME_DIR" ]; then
    RUNTIME_DIR="/tmp"
fi
AUDIO_FIFO="${RUNTIME_DIR}/deckjam-audio.fifo"
if [ ! -p "$AUDIO_FIFO" ]; then
    rm -f "$AUDIO_FIFO" 2>/dev/null || true
    mkfifo -m 0600 "$AUDIO_FIFO"
fi

CONFIG_FILE="$CONFIG_DIR/config.yml"
CONFIG_SRC="$REPO_ROOT/packaging/go-librespot-config.yml"

# Upgrade config when --force is set or when older pulseaudio / firewall-port / zeroconf keys exist
NEEDS_CONFIG_UPDATE=0
if [ ! -f "$CONFIG_FILE" ] || [ "$FORCE" -eq 1 ]; then
    NEEDS_CONFIG_UPDATE=1
elif grep -Eq 'type: "zeroconf"|audio_backend: "pulseaudio"|prefer_firewall_friendly_ports: true|normalisation_enabled:' "$CONFIG_FILE" 2>/dev/null; then
    echo "    Upgrading $CONFIG_FILE to verified PipeWire FIFO bridge configuration..."
    NEEDS_CONFIG_UPDATE=1
fi

if [ "$NEEDS_CONFIG_UPDATE" -eq 1 ]; then
    if [ -f "$CONFIG_SRC" ]; then
        sed "s|audio_output_pipe: .*|audio_output_pipe: \"${AUDIO_FIFO}\"|" "$CONFIG_SRC" > "$CONFIG_FILE.tmp"
        mv -f "$CONFIG_FILE.tmp" "$CONFIG_FILE"
        echo "    Wrote SteamOS PipeWire FIFO configuration to $CONFIG_FILE (preserved state.json if present)."
    fi
else
    echo "    Existing configuration preserved at $CONFIG_FILE."
fi

# 6. Install systemd user services
echo "[+] Step 5: Installing systemd --user service units..."
cp -f "$REPO_ROOT/packaging/systemd/go-librespot.service" "$SYSTEMD_USER_DIR/"
cp -f "$REPO_ROOT/packaging/systemd/deckjam.service" "$SYSTEMD_USER_DIR/"
cp -f "$REPO_ROOT/packaging/systemd/deckjam-tunnel.service" "$SYSTEMD_USER_DIR/"

# Install run-tunnel.sh helper
cp -f "$REPO_ROOT/scripts/run-tunnel.sh" "$SHARE_DECKJAM_DIR/run-tunnel.sh"
chmod +x "$SHARE_DECKJAM_DIR/run-tunnel.sh"

if command -v systemctl >/dev/null 2>&1; then
    echo "    Reloading user systemd daemon and restarting services..."
    systemctl --user daemon-reload || true
    systemctl --user enable deckjam.service go-librespot.service deckjam-tunnel.service 2>/dev/null || true
    systemctl --user restart deckjam.service 2>/dev/null || true
    systemctl --user restart go-librespot.service 2>/dev/null || true
    systemctl --user restart deckjam-tunnel.service 2>/dev/null || true
fi

# 7. Install Helper & UI Launcher
echo "[+] Step 6: Installing UI launcher and shortcuts..."
cp -f "$REPO_ROOT/scripts/launch-jukebox-ui.sh" "$SHARE_DECKJAM_DIR/launch-jukebox-ui.sh"
chmod +x "$SHARE_DECKJAM_DIR/launch-jukebox-ui.sh"

ln -sf "$SHARE_DECKJAM_DIR/launch-jukebox-ui.sh" "$BIN_DIR/deckjam-ui"
chmod +x "$BIN_DIR/deckjam-ui"

# Generate .desktop file with expanded $HOME for Exec to fix Valve SteamOS bug #8164
cat > "$APP_DIR/deckjam.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=DeckJam Jukebox
GenericName=Party Jukebox
Comment=Self-hosted Spotify party queue for Steam Deck
Exec=${HOME}/.local/bin/deckjam-ui
Icon=multimedia-player
Terminal=false
Categories=AudioVideo;Audio;Player;
Keywords=spotify;jam;music;jukebox;party;deck;
StartupNotify=true
EOF
chmod +x "$APP_DIR/deckjam.desktop"

if [ -d "$DESKTOP_DIR" ]; then
    cp -f "$APP_DIR/deckjam.desktop" "$DESKTOP_DIR/deckjam.desktop"
    chmod +x "$DESKTOP_DIR/deckjam.desktop" 2>/dev/null || true
fi

# SteamOS Non-Steam Game Integrator
if command -v steamos-add-to-steam >/dev/null 2>&1; then
    echo "    Registering DeckJam with Steam Gaming Mode via steamos-add-to-steam..."
    steamos-add-to-steam "$APP_DIR/deckjam.desktop" || true
fi

# 8. Detect Network IP for Summary
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

PUBLIC_TUNNEL_URL=""
if [ -f "$SHARE_DECKJAM_DIR/public_url.txt" ]; then
    PUBLIC_TUNNEL_URL="$(cat "$SHARE_DECKJAM_DIR/public_url.txt" 2>/dev/null | tr -d '[:space:]')"
fi

echo ""
echo "=========================================================="
echo "          DeckJam Installation Completed! 🎉             "
echo "=========================================================="
echo ""
echo "📱 Mobile Party URL (Guests):"
if [ -n "$PUBLIC_TUNNEL_URL" ]; then
    echo "   $PUBLIC_TUNNEL_URL (Cloudflare Quick Tunnel)"
fi
echo "   http://${DETECTED_IP}:8080"
echo "   http://steamdeck.local:8080"
echo ""
echo "🎮 Steam Deck Kiosk / Jukebox UI (Deck Screen / TV Dock):"
echo "   http://127.0.0.1:8080/deck"
echo "   Command: deckjam-ui"
echo ""
echo "🎵 Spotify Pairing: Open DeckJam on your phone or Deck screen and tap 'Pair Spotify in 1 Tap'!"
echo "=========================================================="
