package player

// PlayerStatus represents the unified player engine status sent to UI and SSE.
type PlayerStatus struct {
	ActiveBackend      string `json:"active_backend"`
	ConfiguredBackend  string `json:"configured_backend"`
	Playing            bool   `json:"playing"`
	Paused             bool   `json:"paused"`
	Buffering          bool   `json:"buffering"`
	AudioVerified      bool   `json:"audio_verified"`
	AudioPlayer        string `json:"audio_player"`
	AudioSinkName      string `json:"audio_sink_name"`
	AudioBytesPerSec   int    `json:"audio_bytes_per_sec"`
	AudioPeakPct       int    `json:"audio_peak_pct"`
	PositionMS         int    `json:"position_ms"`
	DurationMS         int    `json:"duration_ms"`
	Volume             int    `json:"volume"`
	SkipVotes          int    `json:"skip_votes"`
	SkipVoteThreshold  int    `json:"skip_vote_threshold"`
	SkipVotedByMe      bool   `json:"skip_voted_by_me"`
	LibrespotConnected bool   `json:"librespot_connected"`
	LibrespotDaemonUp  bool   `json:"librespot_daemon_up"`
	LibrespotPairCode  string `json:"librespot_pair_code"`
	LibrespotPairURL   string `json:"librespot_pair_url"`
	MPRISAvailable     bool   `json:"mpris_available"`
	InternetChecked    bool   `json:"internet_checked"`
	InternetOK         bool   `json:"internet_ok"`
	InternetLatencyMS  int    `json:"internet_latency_ms"`
	InternetDetail     string `json:"internet_detail"`
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
