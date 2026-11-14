# DeckJam Technical Contract & API Specification

DeckJam is a self-hosted, local-first Spotify Jam party queue server built specifically for the Steam Deck (SteamOS / Linux). Guests share Spotify track, album, or playlist links over local Wi-Fi (or Steam Deck Hotspot) using their phone's OS Share Sheet or mobile browser. Queue state lives 100% locally in SQLite so shaky internet or phones dropping off Wi-Fi never lose tracks or kick people out.

## Directory Ownership
- **Backend (`cmd/deckjam`, `internal/store`, `internal/spotify`, `internal/player`, `internal/qr`, `internal/server`, `go.mod`)**: Go HTTP/SSE server, SQLite store, Spotify zero-key link/embed resolver, `go-librespot` + D-Bus MPRIS + Mock player engine, offline SVG QR generator. Do NOT modify files inside `internal/web/static/` or `docs/` or `scripts/` or `packaging/`.
- **Frontend & OS Share Shim (`internal/web/static/*`, `docs/*`)**: Vanilla HTML, CSS, JS mobile party UI (`index.html`), Steam Deck 1280x800 touchscreen/TV Jukebox UI (`deck.html`), local PWA manifest + service worker, and GitHub Pages HTTPS Android OS Share Target shim (`docs/`).
- **SteamOS Packaging & Docs (`scripts/*`, `packaging/*`, `README.md`, `.gitignore`)**: Steam Deck user-space installer (`go-librespot` + `deckjam`), systemd `--user` services, NetworkManager offline Wi-Fi hotspot helper, Gaming Mode `.desktop` launcher, and full documentation.

---

## CLI Flags & Environment Variables (`cmd/deckjam/main.go`)
- `--addr` / `DECKJAM_ADDR` (default `0.0.0.0:8080`)
- `--data-dir` / `DECKJAM_DATA_DIR` (default `~/.local/share/deckjam`)
- `--librespot-url` / `DECKJAM_LIBRESPOT_URL` (default `http://127.0.0.1:3678`)
- `--backend` / `DECKJAM_BACKEND` (default `auto`: `auto` | `librespot` | `mpris` | `mock`)
- `--host-pin` / `DECKJAM_HOST_PIN` (optional override; if unset, persisted 4-digit PIN generated in SQLite on first run)
- `--hostname` / `DECKJAM_HOSTNAME` (default auto-detected LAN IP + `steamdeck.local`)

---

## JSON Data Models

### `QueueItem`
```json
{
  "id": 12,
  "spotify_uri": "spotify:track:4cOdK2wGLETKBW3PvgPWqT",
  "track_id": "4cOdK2wGLETKBW3PvgPWqT",
  "raw_input": "https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT",
  "title": "Never Gonna Give You Up",
  "artist": "Rick Astley",
  "album": "Whenever You Need Somebody",
  "thumbnail_url": "/api/artwork/4cOdK2wGLETKBW3PvgPWqT",
  "duration_ms": 213573,
  "added_by_id": "guest-uuid-123",
  "added_by_name": "Alex",
  "added_by_color": "#2ebd85",
  "status": "queued",
  "round_number": 1,
  "votes": 3,
  "voted_by_me": true,
  "created_at": 1763160000
}
```
- `status` values: `"playing"`, `"queued"`, `"resolving"`, `"played"`, `"skipped"`, `"failed"`.

### `PlayerStatus`
```json
{
  "active_backend": "librespot",
  "configured_backend": "auto",
  "playing": true,
  "paused": false,
  "position_ms": 48200,
  "duration_ms": 213573,
  "volume": 80,
  "skip_votes": 1,
  "skip_vote_threshold": 3,
  "skip_voted_by_me": false,
  "librespot_connected": true,
  "librespot_pair_code": "",
  "mpris_available": false,
  "last_error": ""
}
```

### `Settings`
```json
{
  "room_name": "Steam Deck Jam",
  "queue_mode": "round_robin",
  "skip_vote_threshold": 3,
  "autoplay_fallback": true,
  "player_backend": "auto"
}
```

### `StateSnapshot` (`GET /api/state?guest_id=<id>`)
```json
{
  "now_playing": null,
  "queue": [],
  "history": [],
  "fallback_count": 14,
  "player": {},
  "settings": {},
  "is_host": false,
  "host_pin": "",
  "join_url": "http://192.168.1.42:8080",
  "lan_urls": ["http://192.168.1.42:8080", "http://steamdeck.local:8080"],
  "online_guests": 4,
  "server_time_ms": 1763160000123
}
```
Note: `host_pin` is ONLY populated in `StateSnapshot` when `is_host == true` (i.e. request is from `127.0.0.1` / `::1` on the Steam Deck screen itself, OR carries valid `X-Host-Pin` header). Remote guests without PIN receive `"host_pin": ""` and `"is_host": false`.

---

## HTTP & SSE Endpoints

1. **`GET /`** -> serves `static/index.html`; other static files in `static/*` served directly (`/styles.css`, `/app.js`, `/deck.css`, `/deck.js`, `/manifest.webmanifest`, `/sw.js`, `/icons/*`).
2. **`GET /deck`** -> serves `static/deck.html` (Steam Deck 1280x800 touchscreen / TV stage view).
3. **`GET /api/state?guest_id=<id>`** -> returns `StateSnapshot`.
4. **`GET /api/events?guest_id=<id>`** -> Server-Sent Events (`text/event-stream`):
   - `event: state` -> `data: <StateSnapshot JSON>` (sent immediately on connect and whenever queue/track/settings/votes change)
   - `event: progress` -> `data: {"playing":true,"paused":false,"position_ms":49200,"duration_ms":213573,"skip_votes":1,"online_guests":4}` (sent every 1s while playing)
5. **`POST /api/guest`**
   - Body: `{"guest_id": "uuid", "name": "Alex", "color": "#2ebd85"}`
   - Response: `{"guest_id": "uuid", "name": "Alex", "color": "#2ebd85"}`
6. **`POST /api/queue`**
   - Body: `{"input": "https://open.spotify.com/track/...", "guest_id": "uuid", "guest_name": "Alex", "target": "queue"}` (`target` is `"queue"` default or `"fallback"` to load tracks into fallback pool)
   - Accepts single track URL/URI, arbitrary share text containing a Spotify URL, or album/playlist URL (enqueues up to 50 tracks!).
   - If internet is shaky/offline and metadata cannot be fetched immediately:
     - If track ID is extractable from URL/URI, inserts immediately as `"queued"` with placeholder title and queues background metadata enrichment.
     - If input is an unexpanded `spotify.link` / `spoti.fi` shortlink while offline, inserts as `"resolving"` and resolves in background worker when connectivity returns.
   - Response (`200 OK`): `{"ok": true, "added_count": 1, "items": [...], "message": "Queued: Never Gonna Give You Up — Rick Astley"}`
7. **`GET /share` & `POST /share` (Browser / Android PWA OS Share Target)**
   - Accepts query params or form fields: `url`, `text`, `title`, `guest`, `guest_name`.
   - Combines `url` + `text` + `title` to locate the Spotify link, enqueues it (defaulting `guest_name` to `"Shared Guest"` if unknown), and responds with `303 See Other` redirecting to `/?shared=1&msg=<url-encoded-message>`.
8. **`GET /api/quick-share` & `POST /api/quick-share` (Headless OS Share Target for iOS Shortcuts & HTTP Shortcuts)**
   - Accepts query params, form body, or JSON (`url`, `text`, `guest`, `guest_name`).
   - Enqueues the Spotify link and returns `200 OK` JSON: `{"ok": true, "added_count": 1, "message": "Queued: Song — Artist"}`.
9. **`POST /api/queue/{id}/vote`**
   - Body: `{"guest_id": "uuid"}` -> toggles upvote on item `{id}`. Returns `{"ok": true, "votes": 4, "voted": true}`.
10. **`DELETE /api/queue/{id}?guest_id=<id>`**
    - Allowed if `is_host` (`127.0.0.1` or valid `X-Host-Pin` header) OR if `guest_id` matches `item.added_by_id`. Removes queued item. Returns `{"ok": true}`.
11. **`POST /api/skip-vote`**
    - Body: `{"guest_id": "uuid"}` -> records skip vote on current track. If `guest_id == now_playing.added_by_id` OR skip votes >= `skip_vote_threshold`, skips immediately! Returns `{"ok": true, "skipped": false, "skip_votes": 2, "threshold": 3}`.
12. **`GET /api/host/verify`**
    - Checks `127.0.0.1` or `X-Host-Pin` header. Returns `{"is_host": true, "host_pin": "4821"}` or `401 Unauthorized` (`{"is_host": false}`).
13. **`POST /api/host/control`** (Requires `127.0.0.1` or valid `X-Host-Pin` header)
    - Body:
      ```json
      {
        "action": "play | pause | toggle | skip | restart | seek | volume | play_now | move_top | clear_queue | clear_fallback | update_settings",
        "item_id": 12,
        "value": 80,
        "settings": {
          "room_name": "Road Trip Jam",
          "queue_mode": "round_robin",
          "skip_vote_threshold": 3,
          "autoplay_fallback": true,
          "player_backend": "auto",
          "host_pin": "4821"
        }
      }
      ```
    - Returns `{"ok": true}`.
14. **`GET /api/artwork/{track_id}`**
    - Serves locally cached cover art bytes from SQLite (`track_cache.local_thumb`). If not yet cached in bytes, fetches `thumbnail_url` once, stores bytes in SQLite, and serves them; if offline and uncached, returns a clean generated SVG vinyl cover placeholder (`image/svg+xml`).
15. **`GET /api/qr.svg?data=<url-encoded-string>`**
    - Pure-Go offline QR code encoder returning crisp `image/svg+xml` (never calls external internet APIs!).
16. **`GET /api/shortcuts/http-shortcuts.json?guest_id=<id>&guest_name=<name>`**
    - Returns a valid importable JSON configuration for the open-source Android "HTTP Shortcuts" app pre-configured to share any text/URL from Spotify's Share Sheet via `POST <join_url>/api/quick-share`.
