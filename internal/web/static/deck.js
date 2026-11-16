/**
 * DeckJam Steam Deck 1280x800 & TV Stage View (deck.js)
 * Tactile touch transport, client interpolation, live SSE updates.
 */

(function () {
  'use strict';

  let state = {
    now_playing: null,
    queue: [],
    player: {
      volume: 80,
      playing: false,
      paused: false,
      position_ms: 0,
      duration_ms: 0
    },
    settings: {
      room_name: 'Steam Deck Jam',
      queue_mode: 'round_robin'
    },
    is_host: true,
    host_pin: '4821',
    join_url: window.location.origin,
    online_guests: 1
  };

  let sseSource = null;
  let interpolator = null;
  let interpolatedPositionMs = 0;
  let lastProgressReceivedAt = Date.now();
  let isPlayingLocal = false;
  let currentDurationMs = 0;
  let isPinVisible = false;

  const el = {
    stageRoomName: document.getElementById('stageRoomName'),
    backendDot: document.getElementById('backendDot'),
    backendName: document.getElementById('backendName'),
    deckPairBanner: document.getElementById('deckPairBanner'),
    deckPairCode: document.getElementById('deckPairCode'),
    deckPairLink: document.getElementById('deckPairLink'),
    stageArtwork: document.getElementById('stageArtwork'),
    stageEq: document.getElementById('stageEq'),
    stageStatusText: document.getElementById('stageStatusText'),
    stageQueueModeBadge: document.getElementById('stageQueueModeBadge'),
    stageTrackTitle: document.getElementById('stageTrackTitle'),
    stageArtistName: document.getElementById('stageArtistName'),
    stageAlbumName: document.getElementById('stageAlbumName'),
    stageGuestCredit: document.getElementById('stageGuestCredit'),
    stageGuestName: document.getElementById('stageGuestName'),
    stageProgressFill: document.getElementById('stageProgressFill'),
    stageElapsed: document.getElementById('stageElapsed'),
    stageDuration: document.getElementById('stageDuration'),
    stageBtnRestart: document.getElementById('stageBtnRestart'),
    stageBtnPlayPause: document.getElementById('stageBtnPlayPause'),
    stagePlayPauseIcon: document.getElementById('stagePlayPauseIcon'),
    stagePlayPauseLabel: document.getElementById('stagePlayPauseLabel'),
    stageBtnSkip: document.getElementById('stageBtnSkip'),
    stageBtnVolDown: document.getElementById('stageBtnVolDown'),
    stageVolReadout: document.getElementById('stageVolReadout'),
    stageBtnVolUp: document.getElementById('stageBtnVolUp'),
    stageQrImg: document.getElementById('stageQrImg'),
    stageJoinUrlText: document.getElementById('stageJoinUrlText'),
    stagePinPill: document.getElementById('stagePinPill'),
    stagePinDisplay: document.getElementById('stagePinDisplay'),
    stageQueueCount: document.getElementById('stageQueueCount'),
    stageOnlineGuests: document.getElementById('stageOnlineGuests'),
    stageQueueList: document.getElementById('stageQueueList')
  };

  function formatMs(ms) {
    if (!ms || ms < 0) return '0:00';
    const totalSeconds = Math.floor(ms / 1000);
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = totalSeconds % 60;
    return `${minutes}:${seconds < 10 ? '0' : ''}${seconds}`;
  }

  function getSvgFallbackCover(title = 'DeckJam', artist = 'Spotify Jam') {
    const safeTitle = (title || 'DeckJam').replace(/[<>&"]/g, '');
    const safeArtist = (artist || 'Spotify Jam').replace(/[<>&"]/g, '');
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 320" width="100%" height="100%">
      <rect width="320" height="320" fill="#14171a"/>
      <circle cx="160" cy="160" r="140" fill="#1a1e24" stroke="#2c323b" stroke-width="4"/>
      <circle cx="160" cy="160" r="105" fill="none" stroke="#22272e" stroke-width="2"/>
      <circle cx="160" cy="160" r="70" fill="#111315" stroke="#363c46" stroke-width="3"/>
      <circle cx="160" cy="160" r="44" fill="#1ed760"/>
      <circle cx="160" cy="160" r="12" fill="#111315"/>
      <text x="160" y="295" font-family="'Inter', sans-serif" font-size="13" fill="#9aa1ac" text-anchor="middle">${safeArtist} - ${safeTitle}</text>
    </svg>`;
    return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
  }

  function connectSSE() {
    if (sseSource) sseSource.close();
    sseSource = new EventSource('/api/events?guest_id=deck-screen');

    sseSource.addEventListener('state', (e) => {
      try {
        const data = JSON.parse(e.data);
        handleStateUpdate(data);
      } catch (err) {
        console.error('Deck state parse err:', err);
      }
    });

    sseSource.addEventListener('progress', (e) => {
      try {
        const prog = JSON.parse(e.data);
        handleProgressUpdate(prog);
      } catch (err) {
        console.error('Deck progress parse err:', err);
      }
    });

    sseSource.onerror = () => {
      el.backendDot.style.background = 'var(--deck-amber)';
      el.backendName.textContent = 'Reconnecting to DeckJam...';
    };
  }

  async function fetchState() {
    try {
      const res = await fetch('/api/state?guest_id=deck-screen');
      if (res.ok) {
        const data = await res.json();
        handleStateUpdate(data);
      }
    } catch (err) {
      console.warn('Failed to fetch initial state:', err);
    }
  }

  function handleStateUpdate(newState) {
    state = { ...state, ...newState };

    if (state.settings && state.settings.room_name) {
      el.stageRoomName.textContent = state.settings.room_name;
      document.title = `${state.settings.room_name} — Stage View`;
    }

    if (state.settings && state.settings.queue_mode) {
      el.stageQueueModeBadge.textContent = state.settings.queue_mode === 'round_robin' ? 'Fair Jam' : 'FIFO';
    }

    if (typeof state.online_guests === 'number') {
      el.stageOnlineGuests.textContent = `${state.online_guests} guest${state.online_guests === 1 ? '' : 's'} online`;
    }

    // Backend & Verified Audio Out pill
    renderStageBackendPill(state.player);

    // Spotify Pairing Banner
    if (el.deckPairBanner) {
      const pairCode = state.player && state.player.librespot_pair_code;
      const pairUrl = (state.player && state.player.librespot_pair_url) || (pairCode ? `https://spotify.com/pair?code=${encodeURIComponent(pairCode)}` : 'https://spotify.com/pair');
      if (pairCode) {
        if (el.deckPairCode) el.deckPairCode.textContent = pairCode;
        if (el.deckPairLink) el.deckPairLink.href = pairUrl;
        el.deckPairBanner.classList.remove('hidden');
      } else {
        el.deckPairBanner.classList.add('hidden');
      }
    }

    // Join QR code and URL
    const joinUrl = state.join_url || window.location.origin;
    el.stageQrImg.src = `/api/qr.svg?data=${encodeURIComponent(joinUrl)}`;
    el.stageJoinUrlText.textContent = joinUrl;

    // PIN Display
    updatePinDisplay();

    // Volume readout
    if (state.player && typeof state.player.volume === 'number') {
      el.stageVolReadout.textContent = `${state.player.volume}%`;
    }

    // Render Now Playing & Queue
    renderNowPlaying(state.now_playing, state.player);
    renderQueue(state.queue || []);
  }

  function renderStageBackendPill(player) {
    if (!player) return;
    const sink = player.audio_sink_name || 'Steam Deck Out';
    const isPlaying = !!(player.playing && !player.paused);
    const noInternet = !!(player.internet_checked && !player.internet_ok);

    if (isPlaying && player.audio_verified) {
      el.backendDot.style.background = 'var(--deck-emerald)';
      const sig = player.audio_peak_pct > 0 ? ` (${player.audio_peak_pct}%)` : '';
      el.backendName.textContent = `🔊 ${sink}${sig}`;
    } else if (player.buffering && noInternet) {
      el.backendDot.style.background = 'var(--deck-amber)';
      el.backendName.textContent = '❌ Hotspot Has No Phone Internet!';
    } else if (player.buffering) {
      el.backendDot.style.background = 'var(--deck-amber)';
      el.backendName.textContent = player.last_error ? `Buffering (${player.last_error})` : `Buffering → ${sink}`;
    } else if (noInternet) {
      el.backendDot.style.background = 'var(--deck-amber)';
      el.backendName.textContent = '⚠️ Hotspot Only • Check Mobile Data';
    } else if (player.active_backend === 'waiting') {
      el.backendDot.style.background = 'var(--deck-amber)';
      el.backendName.textContent = player.librespot_pair_code ? 'Pair Spotify Above' : 'Connecting Spotify...';
    } else if (player.last_error) {
      el.backendDot.style.background = 'var(--deck-amber)';
      el.backendName.textContent = player.last_error;
    } else if (player.active_backend === 'librespot') {
      el.backendDot.style.background = 'var(--deck-emerald)';
      el.backendName.textContent = `🔈 ${sink} Ready`;
    } else if (player.active_backend === 'mpris') {
      el.backendDot.style.background = 'var(--deck-emerald)';
      el.backendName.textContent = 'Spotify App (MPRIS)';
    } else {
      el.backendDot.style.background = 'var(--deck-amber)';
      el.backendName.textContent = 'Preview Mode (No Audio)';
    }
  }

  function handleProgressUpdate(prog) {
    if (typeof prog.online_guests === 'number') {
      el.stageOnlineGuests.textContent = `${prog.online_guests} guest${prog.online_guests === 1 ? '' : 's'} online`;
    }

    isPlayingLocal = !!(prog.playing && !prog.paused);
    interpolatedPositionMs = prog.position_ms || 0;
    currentDurationMs = prog.duration_ms || (state.now_playing ? state.now_playing.duration_ms : 0);
    lastProgressReceivedAt = Date.now();

    if (state.player) {
      state.player.playing = !!prog.playing;
      state.player.paused = !!prog.paused;
      state.player.buffering = !!prog.buffering;
      state.player.audio_verified = !!prog.audio_verified;
      if (prog.audio_sink_name) state.player.audio_sink_name = prog.audio_sink_name;
      if (typeof prog.audio_peak_pct === 'number') state.player.audio_peak_pct = prog.audio_peak_pct;
      if (typeof prog.last_error === 'string') state.player.last_error = prog.last_error;
      if (typeof prog.internet_checked === 'boolean') state.player.internet_checked = prog.internet_checked;
      if (typeof prog.internet_ok === 'boolean') state.player.internet_ok = prog.internet_ok;
      if (typeof prog.internet_latency_ms === 'number') state.player.internet_latency_ms = prog.internet_latency_ms;
      if (typeof prog.internet_detail === 'string') state.player.internet_detail = prog.internet_detail;
      renderStageBackendPill(state.player);
    }

    updateProgressUI();

    if (isPlayingLocal) {
      el.stageEq.classList.add('playing');
    } else {
      el.stageEq.classList.remove('playing');
    }
  }

  function startInterpolator() {
    if (interpolator) clearInterval(interpolator);
    interpolator = setInterval(() => {
      if (isPlayingLocal && currentDurationMs > 0) {
        const delta = Date.now() - lastProgressReceivedAt;
        const currentPos = Math.min(interpolatedPositionMs + delta, currentDurationMs);
        const percent = Math.min(100, Math.max(0, (currentPos / currentDurationMs) * 100));
        el.stageProgressFill.style.width = `${percent}%`;
        el.stageElapsed.textContent = formatMs(currentPos);
      }
    }, 200);
  }

  function updateProgressUI() {
    if (!currentDurationMs || currentDurationMs <= 0) {
      el.stageProgressFill.style.width = '0%';
      el.stageElapsed.textContent = '0:00';
      el.stageDuration.textContent = '0:00';
      return;
    }
    const percent = Math.min(100, Math.max(0, (interpolatedPositionMs / currentDurationMs) * 100));
    el.stageProgressFill.style.width = `${percent}%`;
    el.stageElapsed.textContent = formatMs(interpolatedPositionMs);
    el.stageDuration.textContent = formatMs(currentDurationMs);
  }

  function renderNowPlaying(track, player) {
    if (!track) {
      el.stageArtwork.src = '/icon.svg';
      el.stageTrackTitle.textContent = 'Waiting for Party Queue...';
      el.stageArtistName.textContent = 'Scan QR code to queue your tracks';
      el.stageAlbumName.textContent = (player && player.audio_sink_name) ? `Audio Out: ${player.audio_sink_name}` : 'Local-first party jukebox';
      el.stageStatusText.textContent = (player && player.active_backend === 'waiting') ? 'Connect Spotify' : 'Ready for Tunes';
      el.stageGuestCredit.style.display = 'none';
      el.stageEq.classList.remove('playing');
      isPlayingLocal = false;
      currentDurationMs = 0;
      updateProgressUI();
      updatePlayPauseButton(false);
      return;
    }

    el.stageTrackTitle.textContent = track.title || 'Unknown Title';
    el.stageArtistName.textContent = track.artist || 'Unknown Artist';
    el.stageAlbumName.textContent = track.album || '';
    el.stageArtwork.src = `/api/artwork/${encodeURIComponent(track.track_id || track.id || 'default')}`;
    el.stageArtwork.onerror = () => {
      el.stageArtwork.src = getSvgFallbackCover(track.title, track.artist);
    };

    if (track.added_by_name) {
      el.stageGuestCredit.style.display = 'inline-flex';
      el.stageGuestName.textContent = track.added_by_name;
      if (track.added_by_color) {
        el.stageGuestName.style.borderLeft = `3px solid ${track.added_by_color}`;
      }
    } else {
      el.stageGuestCredit.style.display = 'none';
    }

    const isPlaying = !!(player && player.playing && !player.paused);
    isPlayingLocal = isPlaying;
    if (isPlaying) {
      el.stageStatusText.textContent = (player && player.audio_verified) ? 'Now Playing • Verified' : 'Now Playing';
      el.stageEq.classList.add('playing');
    } else if (player && player.paused) {
      el.stageStatusText.textContent = 'Paused';
      el.stageEq.classList.remove('playing');
    } else if (player && player.active_backend === 'waiting') {
      el.stageStatusText.textContent = 'Waiting for Spotify';
      el.stageEq.classList.remove('playing');
    } else if (player && player.buffering) {
      el.stageStatusText.textContent = 'Connecting Audio...';
      el.stageEq.classList.remove('playing');
    } else {
      el.stageStatusText.textContent = 'Loading Track...';
      el.stageEq.classList.remove('playing');
    }

    currentDurationMs = track.duration_ms || (player ? player.duration_ms : 0) || 0;
    interpolatedPositionMs = (player ? player.position_ms : 0) || 0;
    lastProgressReceivedAt = Date.now();
    updateProgressUI();
    updatePlayPauseButton(isPlaying);
  }

  function updatePlayPauseButton(isPlaying) {
    if (isPlaying) {
      el.stagePlayPauseIcon.innerHTML = '<path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/>';
      el.stagePlayPauseLabel.textContent = 'Pause';
    } else {
      el.stagePlayPauseIcon.innerHTML = '<path d="M8 5v14l11-7z"/>';
      el.stagePlayPauseLabel.textContent = 'Play';
    }
  }

  function updatePinDisplay() {
    if (isPinVisible) {
      el.stagePinDisplay.textContent = state.host_pin || '4821';
    } else {
      el.stagePinDisplay.textContent = '••••';
    }
  }

  function renderQueue(queue) {
    el.stageQueueCount.textContent = queue.length;
    if (queue.length === 0) {
      el.stageQueueList.innerHTML = `
        <div style="padding:40px 10px; text-align:center; color:var(--deck-text-tertiary); font-size:13px;">
          Queue is empty. Scan QR on your phone to add songs!
        </div>
      `;
      return;
    }

    let html = '';
    queue.forEach((item, index) => {
      html += `
        <div class="stage-queue-item" data-id="${item.id}">
          <img class="stage-queue-thumb" src="/api/artwork/${encodeURIComponent(item.track_id || item.id || 'default')}" alt="Art" onerror="this.src='${getSvgFallbackCover(item.title, item.artist)}'">
          <div class="stage-item-meta">
            <div class="stage-item-title">${escapeHtml(item.title || 'Resolving track...')}</div>
            <div class="stage-item-artist">${escapeHtml(item.artist || 'Waiting for metadata')}</div>
            <div class="stage-item-badge">
              <span style="color:${escapeHtml(item.added_by_color || '#1ed760')}">●</span>
              <span>${escapeHtml(item.added_by_name || 'Guest')}</span>
              ${item.duration_ms ? `<span>&bull; ${formatMs(item.duration_ms)}</span>` : ''}
            </div>
          </div>
          <div class="stage-item-vote">▲ ${item.votes || 0}</div>
        </div>
      `;
    });

    el.stageQueueList.innerHTML = html;
  }

  function escapeHtml(str) {
    if (!str) return '';
    return String(str).replace(/[&<>"']/g, (m) => {
      return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[m];
    });
  }

  async function sendHostCommand(action, payload = {}) {
    try {
      await fetch('/api/host/control', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action, ...payload })
      });
      fetchState();
    } catch (err) {
      console.error('Host control error:', err);
    }
  }

  function bindEvents() {
    el.stageBtnPlayPause.addEventListener('click', () => sendHostCommand('toggle'));
    el.stageBtnSkip.addEventListener('click', () => sendHostCommand('skip'));
    el.stageBtnRestart.addEventListener('click', () => sendHostCommand('restart'));

    el.stageBtnVolDown.addEventListener('click', () => {
      const current = (state.player && state.player.volume) || 80;
      const next = Math.max(0, current - 5);
      sendHostCommand('volume', { value: next });
    });

    el.stageBtnVolUp.addEventListener('click', () => {
      const current = (state.player && state.player.volume) || 80;
      const next = Math.min(100, current + 5);
      sendHostCommand('volume', { value: next });
    });

    el.stagePinPill.addEventListener('click', () => {
      isPinVisible = !isPinVisible;
      updatePinDisplay();
    });

    const btnTestSpeaker = document.getElementById('stageBtnTestSpeaker');
    if (btnTestSpeaker) {
      btnTestSpeaker.addEventListener('click', async () => {
        btnTestSpeaker.disabled = true;
        const orig = btnTestSpeaker.innerHTML;
        btnTestSpeaker.innerHTML = '<span>🔊 Playing...</span>';
        try {
          const res = await fetch('/api/audio-test', { method: 'POST' });
          const data = await res.json().catch(() => ({}));
          if (res.ok && data.ok) {
            el.backendDot.style.background = 'var(--deck-emerald)';
            el.backendName.textContent = `✅ Verified: ${data.audio_sink || 'Audio Out'}`;
          } else {
            el.backendDot.style.background = 'var(--deck-amber)';
            el.backendName.textContent = `⚠️ ${data.error || 'Speaker test failed'}`;
          }
          setTimeout(fetchState, 1200);
        } catch {
          el.backendName.textContent = '⚠️ Speaker test request failed';
        } finally {
          btnTestSpeaker.disabled = false;
          btnTestSpeaker.innerHTML = orig;
        }
      });
    }
  }

  function init() {
    bindEvents();
    fetchState();
    connectSSE();
    startInterpolator();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
