#!/usr/bin/env bash
# ==============================================================================
# DeckJam Turnkey Launcher (scripts/go.sh)
# Delegates directly to ../go.sh
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/../go.sh" "$@"
