#!/usr/bin/env bash
# ==============================================================================
# DeckJam Steam Deck Offline / Road-Trip Wi-Fi Hotspot Helper
# ==============================================================================
# Uses NetworkManager's built-in nmcli on SteamOS to create a local Wi-Fi hotspot
# without requiring an external internet connection or router.
# ==============================================================================
set -euo pipefail

CONNECTION_NAME="DeckJam-Hotspot"
DEFAULT_SSID="DeckJam"
DEFAULT_PASSWORD="steamdeckjam"
HOTSPOT_GATEWAY="10.42.0.1"

ACTION="${1:-status}"
SSID="${2:-$DEFAULT_SSID}"
PASSWORD="${3:-$DEFAULT_PASSWORD}"

ensure_nmcli() {
    if ! command -v nmcli >/dev/null 2>&1; then
        echo "Error: 'nmcli' command not found."
        echo "NetworkManager (nmcli) is pre-installed on SteamOS by default."
        exit 1
    fi
}

get_wifi_device() {
    # Find active or available Wi-Fi interface (e.g., wlan0)
    nmcli -t -f DEVICE,TYPE device | awk -F: '$2=="wifi"{print $1; exit}'
}

start_hotspot() {
    ensure_nmcli
    WIFI_IF="$(get_wifi_device)"

    if [ -z "$WIFI_IF" ]; then
        echo "Error: No Wi-Fi network device detected on this system."
        exit 1
    fi

    echo "=========================================================="
    echo "       Starting Steam Deck Wi-Fi Hotspot for DeckJam      "
    echo "=========================================================="
    echo "[*] Interface: $WIFI_IF"
    echo "[*] SSID:      $SSID"
    echo "[*] Password:  $PASSWORD"

    # Stop any existing hotspot connection if active
    nmcli connection down "$CONNECTION_NAME" 2>/dev/null || true
    nmcli connection delete "$CONNECTION_NAME" 2>/dev/null || true

    # Validate WPA2 password length requirement (minimum 8 characters)
    if [ "${#PASSWORD}" -lt 8 ]; then
        echo "Error: Wi-Fi password must be at least 8 characters long."
        exit 1
    fi

    # Create & bring up Wi-Fi hotspot
    nmcli device wifi hotspot \
        ifname "$WIFI_IF" \
        con-name "$CONNECTION_NAME" \
        ssid "$SSID" \
        password "$PASSWORD"

    echo ""
    echo "=========================================================="
    echo "            Hotspot is LIVE & Broadcasting!               "
    echo "=========================================================="
    echo "Instruct party guests to connect to:"
    echo "  SSID:     $SSID"
    echo "  Password: $PASSWORD"
    echo ""
    echo "Then scan or open in their mobile phone browser:"
    echo "  http://${HOTSPOT_GATEWAY}:8080"
    echo ""
    echo "To stop the hotspot and reconnect to home/dock Wi-Fi:"
    echo "  $0 stop"
    echo "=========================================================="
}

stop_hotspot() {
    ensure_nmcli
    echo "[*] Stopping Wi-Fi Hotspot '$CONNECTION_NAME'..."
    if nmcli connection show --active | grep -q "$CONNECTION_NAME"; then
        nmcli connection down "$CONNECTION_NAME" || true
    fi
    nmcli connection delete "$CONNECTION_NAME" 2>/dev/null || true

    echo "[*] Hotspot stopped. Reconnecting to default Wi-Fi..."
    WIFI_IF="$(get_wifi_device)"
    if [ -n "$WIFI_IF" ]; then
        nmcli device reapply "$WIFI_IF" 2>/dev/null || true
        nmcli device connect "$WIFI_IF" 2>/dev/null || true
    fi
    echo "[+] Done. System is ready to connect to standard Wi-Fi networks."
}

show_status() {
    ensure_nmcli
    echo "=========================================================="
    echo "          Steam Deck Hotspot Status                       "
    echo "=========================================================="
    if nmcli connection show --active | grep -q "$CONNECTION_NAME"; then
        echo "[+] State: ACTIVE"
        echo "[*] Connection Name: $CONNECTION_NAME"
        
        # Get active IP address on wifi device
        WIFI_IF="$(get_wifi_device)"
        HOTSPOT_IP=""
        if [ -n "$WIFI_IF" ]; then
            HOTSPOT_IP="$(nmcli -g IP4.ADDRESS device show "$WIFI_IF" 2>/dev/null | head -n 1 | cut -d/ -f1 || true)"
        fi
        [ -z "$HOTSPOT_IP" ] && HOTSPOT_IP="$HOTSPOT_GATEWAY"

        echo "[*] Hotspot Gateway IP: $HOTSPOT_IP"
        echo "[*] Party URL for phones: http://${HOTSPOT_IP}:8080"
    else
        echo "[-] State: INACTIVE (Normal Wi-Fi mode)"
        echo ""
        echo "Usage:"
        echo "  $0 start [SSID] [PASSWORD]  - Start Wi-Fi Hotspot"
        echo "  $0 stop                     - Stop Hotspot and restore Wi-Fi"
        echo "  $0 status                   - Show current hotspot status"
    fi
    echo "=========================================================="
}

case "$ACTION" in
    start)
        start_hotspot
        ;;
    stop)
        stop_hotspot
        ;;
    status)
        show_status
        ;;
    *)
        echo "Unknown command: $ACTION"
        echo "Usage: $0 {start [SSID] [PASSWORD]|stop|status}"
        exit 1
        ;;
esac
