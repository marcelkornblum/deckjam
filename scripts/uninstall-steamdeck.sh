#!/usr/bin/env bash
# ==============================================================================
# DeckJam Steam Deck / SteamOS Uninstaller
# ==============================================================================
set -euo pipefail

PURGE=0
for arg in "$@"; do
    case "$arg" in
        --purge)
            PURGE=1
            ;;
    esac
done

echo "=========================================================="
echo "          DeckJam — Steam Deck Uninstaller               "
echo "=========================================================="

# 1. Stop and disable systemd user services
echo "[+] Stopping and disabling DeckJam systemd user services..."
if command -v systemctl >/dev/null 2>&1; then
    systemctl --user stop deckjam-tunnel.service 2>/dev/null || true
    systemctl --user stop deckjam.service 2>/dev/null || true
    systemctl --user stop go-librespot.service 2>/dev/null || true
    systemctl --user disable deckjam-tunnel.service 2>/dev/null || true
    systemctl --user disable deckjam.service 2>/dev/null || true
    systemctl --user disable go-librespot.service 2>/dev/null || true
fi

# Stop any running processes
pkill -x deckjam 2>/dev/null || true
pkill -x go-librespot 2>/dev/null || true
pkill -f "cloudflared tunnel" 2>/dev/null || true

# 2. Remove service unit files
echo "[+] Removing service unit files..."
rm -f "$HOME/.config/systemd/user/deckjam-tunnel.service"
rm -f "$HOME/.config/systemd/user/deckjam.service"
rm -f "$HOME/.config/systemd/user/go-librespot.service"

if command -v systemctl >/dev/null 2>&1; then
    systemctl --user daemon-reload 2>/dev/null || true
fi

# 3. Remove desktop entries and launchers
echo "[+] Removing desktop entries and launcher scripts..."
rm -f "$HOME/.local/share/applications/deckjam.desktop"
rm -f "$HOME/Desktop/deckjam.desktop"
rm -f "$HOME/.local/bin/deckjam-ui"
rm -rf "$HOME/.local/share/deckjam/launch-jukebox-ui.sh"

# 4. Remove installed binaries
echo "[+] Removing binaries from ~/.local/bin..."
rm -f "$HOME/.local/bin/deckjam"
# Note: we leave go-librespot alone unless user purges or explicitly wants to remove it
if [ "$PURGE" -eq 1 ]; then
    rm -f "$HOME/.local/bin/go-librespot"
fi

# 5. Handle configurations and persistent data
if [ "$PURGE" -eq 1 ]; then
    echo "[!] Purge flag provided: Removing config, cache, and database state..."
    rm -rf "$HOME/.config/go-librespot"
    rm -rf "$HOME/.cache/go-librespot"
    rm -rf "$HOME/.local/share/deckjam"
else
    echo "[*] Preserving SQLite database history and go-librespot config."
    echo "    To wipe all database history and configs, re-run with: $0 --purge"
fi

echo "=========================================================="
echo "          DeckJam Uninstalled Cleanly!                    "
echo "=========================================================="
