package player

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// NetStatus captures live upstream internet & Spotify reachability over the Steam Deck's Wi-Fi / Hotspot.
type NetStatus struct {
	Checked   bool   `json:"checked"`
	Online    bool   `json:"online"`
	LatencyMS int    `json:"latency_ms"`
	Detail    string `json:"detail"`
	CheckedAt time.Time
}

// NetProbe monitors whether the phone hotspot is actually providing upstream internet to Spotify.
type NetProbe struct {
	mu      sync.RWMutex
	status  NetStatus
	client  *http.Client
	targets []string
	stopCh  chan struct{}
	once    sync.Once
}

// NewNetProbe starts a lightweight periodic connectivity check against Spotify endpoints over IPv4/HTTPS.
func NewNetProbe(targets []string) *NetProbe {
	if len(targets) == 0 {
		targets = []string{
			"https://apresolve.spotify.com/",
			"https://open.spotify.com/",
		}
	}

	dialer := &net.Dialer{
		Timeout:   2800 * time.Millisecond,
		KeepAlive: 15 * time.Second,
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Prefer IPv4 first on phone hotspots where carrier IPv6 routes frequently blackhole
			if conn, err := dialer.DialContext(ctx, "tcp4", addr); err == nil {
				return conn, nil
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   2800 * time.Millisecond,
		ResponseHeaderTimeout: 3000 * time.Millisecond,
		DisableKeepAlives:     true,
	}

	p := &NetProbe{
		client:  &http.Client{Transport: transport, Timeout: 3800 * time.Millisecond},
		targets: targets,
		stopCh:  make(chan struct{}),
	}

	go p.loop()
	return p
}

func (p *NetProbe) loop() {
	p.CheckNow()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.CheckNow()
		}
	}
}

// CheckNow executes an immediate upstream Spotify reachability check.
func (p *NetProbe) CheckNow() NetStatus {
	if p == nil {
		return NetStatus{}
	}

	var lastErr error
	for _, target := range p.targets {
		start := time.Now()
		req, err := http.NewRequest("HEAD", target, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "DeckJam-NetCheck/1.0")

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()

		// Any HTTP response (200..499) from Spotify proves DNS + routing + TLS internet works over hotspot!
		if resp.StatusCode > 0 && resp.StatusCode < 500 {
			ms := int(time.Since(start).Milliseconds())
			if ms < 1 {
				ms = 1
			}
			st := NetStatus{
				Checked:   true,
				Online:    true,
				LatencyMS: ms,
				Detail:    fmt.Sprintf("Hotspot Internet OK (%dms)", ms),
				CheckedAt: time.Now(),
			}
			p.mu.Lock()
			p.status = st
			p.mu.Unlock()
			return st
		}
		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	detail := "Hotspot Wi-Fi connected, but NO internet reaching Spotify (check phone Mobile Data / cell signal)"
	if lastErr != nil {
		errLower := strings.ToLower(lastErr.Error())
		if strings.Contains(errLower, "no such host") || strings.Contains(errLower, "lookup") {
			detail = "Hotspot DNS lookup failed — check phone Mobile Data / cellular signal"
		} else if strings.Contains(errLower, "network is unreachable") || strings.Contains(errLower, "no route") {
			detail = "Steam Deck has no internet route via hotspot — check Wi-Fi / Mobile Data"
		} else if strings.Contains(errLower, "timeout") || strings.Contains(errLower, "deadline") {
			detail = "Hotspot internet timed out reaching Spotify — weak event cell signal"
		}
	}

	st := NetStatus{
		Checked:   true,
		Online:    false,
		LatencyMS: 0,
		Detail:    detail,
		CheckedAt: time.Now(),
	}
	p.mu.Lock()
	p.status = st
	p.mu.Unlock()
	return st
}

// Status returns the most recent cached NetStatus.
func (p *NetProbe) Status() NetStatus {
	if p == nil {
		return NetStatus{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status
}

// Close stops background probing.
func (p *NetProbe) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		close(p.stopCh)
	})
}

var (
	journalErrFieldRe = regexp.MustCompile(`error="([^"]+)"`)
	journalMsgFieldRe = regexp.MustCompile(`msg="([^"]+)"`)
)

// ReadLibrespotJournalHint inspects the last 25 user-systemd log lines of go-librespot
// to surface actionable root-cause errors when audio playback stalls or fails.
func ReadLibrespotJournalHint() string {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, "journalctl", "--user", "-u", "go-librespot.service", "-n", "25", "--no-pager", "-o", "cat").CombinedOutput()
	if err != nil || len(out) == 0 {
		return ""
	}

	return ParseLibrespotLogHint(string(out))
}

// ParseLibrespotLogHint extracts a concise, user-friendly explanation from go-librespot logs.
func ParseLibrespotLogHint(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)

		if strings.Contains(lower, "panic:") || strings.Contains(lower, "unsupported context type") {
			return "go-librespot needs update for single-track play — re-run ./go.sh with internet"
		}
		if strings.Contains(lower, "failed to open fifo") || strings.Contains(lower, "no such device or address") {
			return "Audio FIFO reader not attached — re-run ./go.sh to sync audio pipe"
		}
		if strings.Contains(lower, "audio key") && (strings.Contains(lower, "error") || strings.Contains(lower, "refuse") || strings.Contains(lower, "fail")) {
			return "Spotify refused audio key (ensure paired account has Spotify Premium & internet)"
		}
		if strings.Contains(lower, "no such host") || strings.Contains(lower, "network is unreachable") || strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "tls handshake timeout") || strings.Contains(lower, "did not receive last pong") {
			return "Spotify stream stalled over hotspot — phone cellular internet dropped or timed out"
		}
		if strings.Contains(lower, "level=error") || strings.Contains(lower, "level=fatal") || strings.Contains(lower, "level=warning") {
			if m := journalErrFieldRe.FindStringSubmatch(line); len(m) > 1 && m[1] != "" {
				return "Spotify daemon: " + truncateDiag(m[1], 110)
			}
			if m := journalMsgFieldRe.FindStringSubmatch(line); len(m) > 1 && m[1] != "" {
				return "Spotify daemon: " + truncateDiag(m[1], 110)
			}
		}
	}
	return ""
}

func truncateDiag(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
