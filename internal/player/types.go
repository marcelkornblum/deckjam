package player

// PlayerStatus represents the unified player engine status sent to UI and SSE.
type PlayerStatus struct {
	ActiveBackend      string `json:"active_backend"`
	ConfiguredBackend  string `json:"configured_backend"`
	Playing            bool   `json:"playing"`
	Paused             bool   `json:"paused"`
	PositionMS         int    `json:"position_ms"`
	DurationMS         int    `json:"duration_ms"`
	Volume             int    `json:"volume"`
	SkipVotes          int    `json:"skip_votes"`
	SkipVoteThreshold  int    `json:"skip_vote_threshold"`
	SkipVotedByMe      bool   `json:"skip_voted_by_me"`
	LibrespotConnected bool   `json:"librespot_connected"`
	LibrespotPairCode  string `json:"librespot_pair_code"`
	LibrespotPairURL   string `json:"librespot_pair_url"`
	MPRISAvailable     bool   `json:"mpris_available"`
	LastError          string `json:"last_error"`
}

// Backend defines the common interface implemented by librespot, mpris, and mock backends.
type Backend interface {
	Name() string
	Available() bool
	Play(uri string) error
	Pause() error
	Resume() error
	PlayPause() error
	Stop() error
	Seek(positionMS int) error
	SetVolume(volume int) error
	GetStatus() PlayerStatus
	Close() error
}
