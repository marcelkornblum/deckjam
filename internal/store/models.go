package store

// QueueItem represents a track in the DeckJam queue.
type QueueItem struct {
	ID           int64  `json:"id"`
	SpotifyURI   string `json:"spotify_uri"`
	TrackID      string `json:"track_id"`
	RawInput     string `json:"raw_input"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
	ThumbnailURL string `json:"thumbnail_url"`
	DurationMS   int    `json:"duration_ms"`
	AddedByID    string `json:"added_by_id"`
	AddedByName  string `json:"added_by_name"`
	AddedByColor string `json:"added_by_color"`
	Status       string `json:"status"` // "playing", "queued", "resolving", "played", "skipped", "failed"
	RoundNumber  int    `json:"round_number"`
	Votes        int    `json:"votes"`
	VotedByMe    bool   `json:"voted_by_me"`
	CreatedAt    int64  `json:"created_at"` // Unix timestamp in seconds
}

// Guest represents a registered party guest.
type Guest struct {
	GuestID   string `json:"guest_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	LastSeen  int64  `json:"last_seen"`
	CreatedAt int64  `json:"created_at"`
}

// Settings holds persistent configuration for the room.
type Settings struct {
	RoomName          string `json:"room_name"`
	QueueMode         string `json:"queue_mode"` // "round_robin" or "fifo"
	SkipVoteThreshold int    `json:"skip_vote_threshold"`
	AutoplayFallback  bool   `json:"autoplay_fallback"`
	PlayerBackend     string `json:"player_backend"` // "auto", "librespot", "mpris", "mock"
	HostPIN           string `json:"host_pin,omitempty"`
}

// TrackMetadata represents resolved metadata for a track.
type TrackMetadata struct {
	TrackID      string `json:"track_id"`
	SpotifyURI   string `json:"spotify_uri"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
	ThumbnailURL string `json:"thumbnail_url"`
	DurationMS   int    `json:"duration_ms"`
	LocalThumb   []byte `json:"-"`
	ThumbMIME    string `json:"-"`
}

// FallbackTrack represents a fallback pool track.
type FallbackTrack struct {
	ID           int64  `json:"id"`
	SpotifyURI   string `json:"spotify_uri"`
	TrackID      string `json:"track_id"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
	ThumbnailURL string `json:"thumbnail_url"`
	DurationMS   int    `json:"duration_ms"`
	PlayedCount  int    `json:"played_count"`
	LastPlayedAt int64  `json:"last_played_at"`
	CreatedAt    int64  `json:"created_at"`
}
