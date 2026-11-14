/**
 * DeckJam Frontend Client - Mobile-First Guest & Host Interface
 * Zero external libraries, zero build step.
 */

(function () {
  'use strict';

  // --- Guest Profile & LocalStorage State ---
  const STORAGE_KEYS = {
    GUEST_ID: 'deckjam_guest_id',
    GUEST_NAME: 'deckjam_guest_name',
    GUEST_COLOR: 'deckjam_guest_color',
    HOST_PIN: 'deckjam_host_pin',
    OUTBOX: 'deckjam_outbox',
    GITHUB_SHIM_URL: 'deckjam_github_shim_url'
  };

  const AVATAR_COLORS = [
    '#1ed760', '#24b47e', '#3b82f6', '#8b5cf6',
    '#ec4899', '#f59e0b', '#06b6d4', '#10b981'
  ];

  const FUN_NAMES = [
    'Neon Otter', 'Steam Pilot', 'Sonic Badger', 'Groovy Fox',
    'Laser Lynx', 'Pixel Panda', 'Echo Falcon', 'Retro Koala',
    'Velvet Hawk', 'Analog Bear', 'Solar Gecko', 'Cosmic Owl'
  ];

  function getOrGenerateGuest() {
    let id = localStorage.getItem(STORAGE_KEYS.GUEST_ID);
    if (!id) {
      id = 'guest-' + Math.random().toString(36).substring(2, 9) + Date.now().toString(36);
      localStorage.setItem(STORAGE_KEYS.GUEST_ID, id);
    }
    let name = localStorage.getItem(STORAGE_KEYS.GUEST_NAME);
    if (!name) {
      name = FUN_NAMES[Math.floor(Math.random() * FUN_NAMES.length)];
      localStorage.setItem(STORAGE_KEYS.GUEST_NAME, name);
    }
    let color = localStorage.getItem(STORAGE_KEYS.GUEST_COLOR);
    if (!color) {
      color = AVATAR_COLORS[Math.floor(Math.random() * AVATAR_COLORS.length)];
      localStorage.setItem(STORAGE_KEYS.GUEST_COLOR, color);
    }
    return { id, name, color };
  }

  let guest = getOrGenerateGuest();
  let state = {
    now_playing: null,
    queue: [],
    history: [],
    fallback_count: 0,
    player: {},
    settings: {},
    is_host: false,
    host_pin: '',
    join_url: window.location.origin,
    lan_urls: [],
    online_guests: 0,
    server_time_ms: Date.now()
  };

  let sseSource = null;
  let isOffline = false;
  let interpolatorInterval = null;
  let interpolatedPositionMs = 0;
  let lastProgressReceivedAt = Date.now();
  let isPlayingLocal = false;
  let currentDurationMs = 0;
  let deferredPrompt = null;

  // --- DOM Elements ---
  const el = {
    roomNameDisplay: document.getElementById('roomNameDisplay'),
    connStatus: document.getElementById('connStatus'),
    connLed: document.getElementById('connLed'),
    connLabel: document.getElementById('connLabel'),
    onlineCountBadge: document.getElementById('onlineCountBadge'),
    btnEditProfile: document.getElementById('btnEditProfile'),
    profileDot: document.getElementById('profileDot'),
    profileName: document.getElementById('profileName'),
    outboxBanner: document.getElementById('outboxBanner'),
    outboxMessage: document.getElementById('outboxMessage'),
    btnFlushOutbox: document.getElementById('btnFlushOutbox'),
    btnShareSetup: document.getElementById('btnShareSetup'),
    btnShowQr: document.getElementById('btnShowQr'),
    btnHostToggle: document.getElementById('btnHostToggle'),
    hostLockIcon: document.getElementById('hostLockIcon'),
    hostStatusLabel: document.getElementById('hostStatusLabel'),

    // Now Playing Hero
    nowPlayingCard: document.getElementById('nowPlayingCard'),
    npArtwork: document.getElementById('npArtwork'),
    npStatusTag: document.getElementById('npStatusTag'),
    npStatusText: document.getElementById('npStatusText'),
    npTitle: document.getElementById('npTitle'),
    npArtist: document.getElementById('npArtist'),
    npQueuedBy: document.getElementById('npQueuedBy'),
    npQueuedName: document.getElementById('npQueuedName'),
    progressBarFill: document.getElementById('progressBarFill'),
    timeElapsed: document.getElementById('timeElapsed'),
    timeDuration: document.getElementById('timeDuration'),
    btnVoteSkip: document.getElementById('btnVoteSkip'),
    btnVoteSkipLabel: document.getElementById('btnVoteSkipLabel'),

    // Host Transport
    hostTransportBar: document.getElementById('hostTransportBar'),
    btnHostPrev: document.getElementById('btnHostPrev'),
    btnHostPlayPause: document.getElementById('btnHostPlayPause'),
    btnHostNext: document.getElementById('btnHostNext'),
    playPauseIcon: document.getElementById('playPauseIcon'),
    hostVolumeSlider: document.getElementById('hostVolumeSlider'),

    // Add to Queue
    queueForm: document.getElementById('queueForm'),
    spotifyInput: document.getElementById('spotifyInput'),
    btnPasteClipboard: document.getElementById('btnPasteClipboard'),
    btnSubmitQueue: document.getElementById('btnSubmitQueue'),
    playlistModeBar: document.getElementById('playlistModeBar'),
    playlistDetectedTag: document.getElementById('playlistDetectedTag'),
    btnTargetQueue: document.getElementById('btnTargetQueue'),
    btnTargetFallback: document.getElementById('btnTargetFallback'),

    // Tabs & Lists
    tabQueueBtn: document.getElementById('tabQueueBtn'),
    tabHistoryBtn: document.getElementById('tabHistoryBtn'),
    queueTabContent: document.getElementById('queueTabContent'),
    historyTabContent: document.getElementById('historyTabContent'),
    queueCountBadge: document.getElementById('queueCountBadge'),
    historyCountBadge: document.getElementById('historyCountBadge'),
    queueEmptyState: document.getElementById('queueEmptyState'),
    historyEmptyState: document.getElementById('historyEmptyState'),
    queueList: document.getElementById('queueList'),
    historyList: document.getElementById('historyList'),

    // Modals
    toastContainer: document.getElementById('toastContainer'),
    profileModal: document.getElementById('profileModal'),
    btnCloseProfileModal: document.getElementById('btnCloseProfileModal'),
    profileNameInput: document.getElementById('profileNameInput'),
    colorSwatches: document.getElementById('colorSwatches'),
    btnSaveProfile: document.getElementById('btnSaveProfile'),

    qrModal: document.getElementById('qrModal'),
    btnCloseQrModal: document.getElementById('btnCloseQrModal'),
    qrModalImage: document.getElementById('qrModalImage'),
    qrJoinUrlText: document.getElementById('qrJoinUrlText'),

    shareModal: document.getElementById('shareModal'),
    btnCloseShareModal: document.getElementById('btnCloseShareModal'),
    tabIosBtn: document.getElementById('tabIosBtn'),
    tabAndroidBtn: document.getElementById('tabAndroidBtn'),
    shareIosTab: document.getElementById('shareIosTab'),
    shareAndroidTab: document.getElementById('shareAndroidTab'),
    iosShareUrlDisplay: document.getElementById('iosShareUrlDisplay'),
    btnCopyIosShareUrl: document.getElementById('btnCopyIosShareUrl'),
    androidShimUrlInput: document.getElementById('androidShimUrlInput'),
    btnOpenAndroidShim: document.getElementById('btnOpenAndroidShim'),
    btnInstallPwa: document.getElementById('btnInstallPwa'),
    btnDownloadHttpShortcuts: document.getElementById('btnDownloadHttpShortcuts'),

    hostModal: document.getElementById('hostModal'),
    btnCloseHostModal: document.getElementById('btnCloseHostModal'),
    hostPinSection: document.getElementById('hostPinSection'),
    hostPinInput: document.getElementById('hostPinInput'),
    btnVerifyPin: document.getElementById('btnVerifyPin'),
    hostSettingsSection: document.getElementById('hostSettingsSection'),
    hostRoomNameInput: document.getElementById('hostRoomNameInput'),
    hostQueueModeSelect: document.getElementById('hostQueueModeSelect'),
    hostSkipThresholdInput: document.getElementById('hostSkipThresholdInput'),
    hostBackendSelect: document.getElementById('hostBackendSelect'),
    hostAutoplayFallbackCheck: document.getElementById('hostAutoplayFallbackCheck'),
    hostFallbackCount: document.getElementById('hostFallbackCount'),
    btnClearFallbackBtn: document.getElementById('btnClearFallbackBtn'),
    btnClearQueueBtn: document.getElementById('btnClearQueueBtn'),
    hostPinDisplay: document.getElementById('hostPinDisplay'),
    btnSaveHostSettings: document.getElementById('btnSaveHostSettings')
  };

  let targetMode = 'queue'; // 'queue' or 'fallback'

  // --- Toast Notifications ---
  function showToast(message, type = 'info', durationMs = 3800) {
    const toast = document.createElement('div');
    toast.className = `toast toast-${type === 'success' ? 'success' : type === 'amber' ? 'amber' : type === 'error' ? 'error' : ''}`;
    toast.textContent = message;
    el.toastContainer.appendChild(toast);
    setTimeout(() => {
      toast.style.transition = 'opacity 250ms ease, transform 250ms ease';
      toast.style.opacity = '0';
      toast.style.transform = 'translateY(10px)';
      setTimeout(() => toast.remove(), 260);
    }, durationMs);
  }

  // --- Time Formatting ---
  function formatMs(ms) {
    if (!ms || ms < 0) return '0:00';
    const totalSeconds = Math.floor(ms / 1000);
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = totalSeconds % 60;
    return `${minutes}:${seconds < 10 ? '0' : ''}${seconds}`;
  }

  // --- SVG Vinyl Cover Fallback Generator ---
  function getSvgFallbackCover(title = 'DeckJam', artist = 'Spotify Jam') {
    const safeTitle = (title || 'DeckJam').replace(/[<>&"]/g, '');
    const safeArtist = (artist || 'Spotify Jam').replace(/[<>&"]/g, '');
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 300 300" width="100%" height="100%">
      <rect width="300" height="300" fill="#14171a"/>
      <circle cx="150" cy="150" r="130" fill="#1c2025" stroke="#2c323b" stroke-width="4"/>
      <circle cx="150" cy="150" r="95" fill="none" stroke="#22272e" stroke-width="2"/>
      <circle cx="150" cy="150" r="60" fill="#111315" stroke="#363c46" stroke-width="3"/>
      <circle cx="150" cy="150" r="38" fill="#1ed760"/>
      <circle cx="150" cy="150" r="10" fill="#111315"/>
      <text x="150" y="278" font-family="'Inter', sans-serif" font-size="12" fill="#9aa1ac" text-anchor="middle">${safeArtist} - ${safeTitle}</text>
    </svg>`;
    return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
  }

  // --- Outbox (Offline Queue) Management ---
  function getOutbox() {
    try {
      const stored = localStorage.getItem(STORAGE_KEYS.OUTBOX);
      return stored ? JSON.parse(stored) : [];
    } catch {
      return [];
    }
  }

  function saveOutbox(items) {
    localStorage.setItem(STORAGE_KEYS.OUTBOX, JSON.stringify(items));
    renderOutboxBanner();
  }

  function addToOutbox(input, target = 'queue') {
    const outbox = getOutbox();
    outbox.push({
      input,
      target,
      guest_id: guest.id,
      guest_name: guest.name,
      timestamp: Date.now()
    });
    saveOutbox(outbox);
    showToast('Saved offline. Will queue automatically when Wi-Fi connects.', 'amber');
  }

  function renderOutboxBanner() {
    const outbox = getOutbox();
    if (outbox.length > 0) {
      el.outboxMessage.textContent = `${outbox.length} song${outbox.length > 1 ? 's' : ''} saved offline — will auto-queue when Wi-Fi connects`;
      el.outboxBanner.classList.remove('hidden');
    } else {
      el.outboxBanner.classList.add('hidden');
    }
  }

  async function flushOutbox() {
    const outbox = getOutbox();
    if (outbox.length === 0) return;

    const remaining = [];
    let flushedCount = 0;

    for (const item of outbox) {
      try {
        const res = await fetch('/api/queue', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            input: item.input,
            guest_id: item.guest_id || guest.id,
            guest_name: item.guest_name || guest.name,
            target: item.target || 'queue'
          })
        });
        if (res.ok) {
          flushedCount++;
        } else {
          remaining.push(item);
        }
      } catch {
        remaining.push(item);
      }
    }

    saveOutbox(remaining);
    if (flushedCount > 0) {
      showToast(`Auto-queued ${flushedCount} offline song${flushedCount > 1 ? 's' : ''}!`, 'success');
      fetchState();
    }
  }

  // --- Network & SSE Connection ---
  function updateConnectionUI(online) {
    isOffline = !online;
    if (online) {
      el.connStatus.classList.remove('offline');
      el.connLabel.textContent = 'Live on LAN';
      renderOutboxBanner();
      flushOutbox();
    } else {
      el.connStatus.classList.add('offline');
      el.connLabel.textContent = 'Reconnecting — Offline Outbox Active';
      renderOutboxBanner();
    }
  }

  function connectSSE() {
    if (sseSource) {
      sseSource.close();
    }

    const hostPin = localStorage.getItem(STORAGE_KEYS.HOST_PIN) || '';
    const sseUrl = `/api/events?guest_id=${encodeURIComponent(guest.id)}`;
    sseSource = new EventSource(sseUrl);

    sseSource.addEventListener('state', (e) => {
      try {
        const data = JSON.parse(e.data);
        handleStateUpdate(data);
        updateConnectionUI(true);
      } catch (err) {
        console.error('Failed to parse state snapshot:', err);
      }
    });

    sseSource.addEventListener('progress', (e) => {
      try {
        const prog = JSON.parse(e.data);
        handleProgressUpdate(prog);
        updateConnectionUI(true);
      } catch (err) {
        console.error('Failed to parse progress event:', err);
      }
    });

    sseSource.onopen = () => {
      updateConnectionUI(true);
      flushOutbox();
    };

    sseSource.onerror = () => {
      updateConnectionUI(false);
    };
  }

  async function fetchState() {
    try {
      const headers = {};
      const hostPin = localStorage.getItem(STORAGE_KEYS.HOST_PIN);
      if (hostPin) {
        headers['X-Host-Pin'] = hostPin;
      }

      const res = await fetch(`/api/state?guest_id=${encodeURIComponent(guest.id)}`, { headers });
      if (res.ok) {
        const data = await res.json();
        handleStateUpdate(data);
        updateConnectionUI(true);
      }
    } catch {
      updateConnectionUI(false);
    }
  }

  // --- State Handlers ---
  function handleStateUpdate(newState) {
    state = { ...state, ...newState };

    // Update Room Title
    if (state.settings && state.settings.room_name) {
      el.roomNameDisplay.textContent = state.settings.room_name;
      document.title = `${state.settings.room_name} — DeckJam`;
    }

    // Online Guests
    if (typeof state.online_guests === 'number') {
      el.onlineCountBadge.textContent = `(${state.online_guests} online)`;
    }

    // Host status
    updateHostBadge(state.is_host);

    // Update Now Playing Hero
    renderNowPlaying(state.now_playing, state.player);

    // Render Queue & History
    renderQueue(state.queue || []);
    renderHistory(state.history || []);

    // Update Fallback count
    if (el.hostFallbackCount) {
      el.hostFallbackCount.textContent = state.fallback_count || 0;
    }

    // Update QR Code Image & URL
    const joinUrl = state.join_url || window.location.origin;
    el.qrModalImage.src = `/api/qr.svg?data=${encodeURIComponent(joinUrl)}`;
    el.qrJoinUrlText.textContent = joinUrl;

    // Update iOS share display url
    const iosUrl = `${joinUrl}/api/quick-share?guest=${encodeURIComponent(guest.id)}&guest_name=${encodeURIComponent(guest.name)}&text=`;
    el.iosShareUrlDisplay.textContent = iosUrl;

    // Update HTTP Shortcuts download link
    el.btnDownloadHttpShortcuts.href = `/api/shortcuts/http-shortcuts.json?guest_id=${encodeURIComponent(guest.id)}&guest_name=${encodeURIComponent(guest.name)}`;
  }

  function handleProgressUpdate(prog) {
    if (typeof prog.online_guests === 'number') {
      el.onlineCountBadge.textContent = `(${prog.online_guests} online)`;
    }

    isPlayingLocal = !!(prog.playing && !prog.paused);
    interpolatedPositionMs = prog.position_ms || 0;
    currentDurationMs = prog.duration_ms || (state.now_playing ? state.now_playing.duration_ms : 0);
    lastProgressReceivedAt = Date.now();

    updateProgressUI();

    if (typeof prog.skip_votes === 'number') {
      updateSkipVoteButton(prog.skip_votes, prog.skip_vote_threshold || (state.settings ? state.settings.skip_vote_threshold : 3));
    }
  }

  // --- Live Progress Interpolator ---
  function startProgressInterpolator() {
    if (interpolatorInterval) clearInterval(interpolatorInterval);
    interpolatorInterval = setInterval(() => {
      if (isPlayingLocal && currentDurationMs > 0) {
        const delta = Date.now() - lastProgressReceivedAt;
        const currentPos = Math.min(interpolatedPositionMs + delta, currentDurationMs);
        const percent = Math.min(100, Math.max(0, (currentPos / currentDurationMs) * 100));
        el.progressBarFill.style.width = `${percent}%`;
        el.timeElapsed.textContent = formatMs(currentPos);
      }
    }, 250);
  }

  function updateProgressUI() {
    if (!currentDurationMs || currentDurationMs <= 0) {
      el.progressBarFill.style.width = '0%';
      el.timeElapsed.textContent = '0:00';
      el.timeDuration.textContent = '0:00';
      return;
    }
    const percent = Math.min(100, Math.max(0, (interpolatedPositionMs / currentDurationMs) * 100));
    el.progressBarFill.style.width = `${percent}%`;
    el.timeElapsed.textContent = formatMs(interpolatedPositionMs);
    el.timeDuration.textContent = formatMs(currentDurationMs);
  }

  // --- Render Now Playing Hero ---
  function renderNowPlaying(track, player) {
    if (!track) {
      el.npArtwork.src = '/icon.svg';
      el.npTitle.textContent = 'Party queue is empty';
      el.npArtist.textContent = 'Add your favorite Spotify tracks below!';
      el.npStatusText.textContent = 'Waiting';
      el.npStatusTag.style.color = 'var(--text-tertiary)';
      el.npQueuedBy.style.display = 'none';
      el.btnVoteSkip.disabled = true;
      el.btnVoteSkipLabel.textContent = 'Vote to Skip';
      isPlayingLocal = false;
      currentDurationMs = 0;
      updateProgressUI();
      return;
    }

    el.npTitle.textContent = track.title || 'Unknown Title';
    el.npArtist.textContent = track.artist || 'Unknown Artist';
    el.npArtwork.src = `/api/artwork/${encodeURIComponent(track.track_id || track.id || 'default')}`;
    el.npArtwork.onerror = () => {
      el.npArtwork.src = getSvgFallbackCover(track.title, track.artist);
    };

    if (track.added_by_name) {
      el.npQueuedBy.style.display = 'inline-flex';
      el.npQueuedName.textContent = track.added_by_name;
      if (track.added_by_color) {
        el.npQueuedName.style.borderLeft = `3px solid ${track.added_by_color}`;
      }
    } else {
      el.npQueuedBy.style.display = 'none';
    }

    const isPlaying = player && player.playing && !player.paused;
    isPlayingLocal = isPlaying;
    el.npStatusText.textContent = isPlaying ? 'Now Playing' : 'Paused';
    el.npStatusTag.style.color = isPlaying ? 'var(--accent-emerald)' : 'var(--accent-amber)';

    currentDurationMs = track.duration_ms || (player ? player.duration_ms : 0) || 0;
    interpolatedPositionMs = (player ? player.position_ms : 0) || 0;
    lastProgressReceivedAt = Date.now();
    updateProgressUI();

    // Skip vote button logic
    const isMyTrack = track.added_by_id === guest.id;
    const votes = (player && player.skip_votes) || 0;
    const threshold = (player && player.skip_vote_threshold) || (state.settings && state.settings.skip_vote_threshold) || 3;
    const votedByMe = !!(player && player.skip_voted_by_me);

    el.btnVoteSkip.disabled = false;
    if (isMyTrack) {
      el.btnVoteSkipLabel.textContent = 'Instant Skip (Your Track)';
      el.btnVoteSkip.classList.remove('voted');
    } else {
      el.btnVoteSkipLabel.textContent = `Vote to Skip (${votes}/${threshold})`;
      if (votedByMe) {
        el.btnVoteSkip.classList.add('voted');
      } else {
        el.btnVoteSkip.classList.remove('voted');
      }
    }

    // Host Play/Pause icon update
    if (el.playPauseIcon) {
      el.playPauseIcon.innerHTML = isPlaying
        ? '<path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/>'
        : '<path d="M8 5v14l11-7z"/>';
    }
  }

  function updateSkipVoteButton(votes, threshold) {
    if (!state.now_playing) return;
    const isMyTrack = state.now_playing.added_by_id === guest.id;
    if (!isMyTrack) {
      el.btnVoteSkipLabel.textContent = `Vote to Skip (${votes}/${threshold})`;
    }
  }

  // --- Render Queue & History ---
  function renderQueue(queue) {
    el.queueCountBadge.textContent = queue.length;
    if (queue.length === 0) {
      el.queueEmptyState.style.display = 'flex';
      el.queueList.innerHTML = '';
      return;
    }

    el.queueEmptyState.style.display = 'none';
    const isRoundRobin = state.settings && state.settings.queue_mode === 'round_robin';
    let currentRound = -1;

    let html = '';
    queue.forEach((item, index) => {
      // Group dividers in Fair Round-Robin mode
      if (isRoundRobin && item.round_number && item.round_number !== currentRound) {
        currentRound = item.round_number;
        html += `<div class="round-divider">Round <span>#${currentRound}</span></div>`;
      }

      const isMyTrack = item.added_by_id === guest.id;
      const canDelete = isMyTrack || state.is_host;
      const isResolving = item.status === 'resolving';

      html += `
        <div class="queue-row" data-id="${item.id}">
          <div class="item-thumb-wrapper">
            <img class="item-thumb" src="/api/artwork/${encodeURIComponent(item.track_id || item.id || 'default')}" alt="Art" onerror="this.src='${getSvgFallbackCover(item.title, item.artist)}'">
          </div>
          <div class="item-info">
            <div class="item-title">${escapeHtml(item.title || 'Resolving track...')}</div>
            <div class="item-artist">${escapeHtml(item.artist || 'Waiting for metadata')}</div>
            <div class="item-meta">
              <span class="guest-tag">
                <span class="guest-avatar-dot" style="background:${escapeHtml(item.added_by_color || '#1ed760')}"></span>
                <span>${escapeHtml(item.added_by_name || 'Guest')}</span>
              </span>
              ${isResolving ? '<span class="badge-resolving">Resolving</span>' : ''}
              ${item.duration_ms ? `<span>&bull; ${formatMs(item.duration_ms)}</span>` : ''}
            </div>
          </div>
          <div class="item-actions">
            <button class="btn-upvote ${item.voted_by_me ? 'voted' : ''}" data-action="vote" data-id="${item.id}" title="Upvote">
              ▲ ${item.votes || 0}
            </button>
            ${state.is_host ? `
              <button class="icon-btn" data-action="host-play-now" data-id="${item.id}" title="Play Now" style="padding:4px 6px; height:28px;">
                ▶
              </button>
            ` : ''}
            ${canDelete ? `
              <button class="btn-remove" data-action="remove" data-id="${item.id}" title="Remove track">&times;</button>
            ` : ''}
          </div>
        </div>
      `;
    });

    el.queueList.innerHTML = html;
  }

  function renderHistory(history) {
    el.historyCountBadge.textContent = history.length;
    if (history.length === 0) {
      el.historyEmptyState.style.display = 'flex';
      el.historyList.innerHTML = '';
      return;
    }

    el.historyEmptyState.style.display = 'none';
    let html = '';
    history.forEach((item) => {
      html += `
        <div class="queue-row" data-id="${item.id}">
          <div class="item-thumb-wrapper">
            <img class="item-thumb" src="/api/artwork/${encodeURIComponent(item.track_id || item.id || 'default')}" alt="Art" onerror="this.src='${getSvgFallbackCover(item.title, item.artist)}'">
          </div>
          <div class="item-info">
            <div class="item-title">${escapeHtml(item.title || 'Track')}</div>
            <div class="item-artist">${escapeHtml(item.artist || 'Artist')}</div>
            <div class="item-meta">
              <span>Played &bull; ${item.added_by_name ? escapeHtml(item.added_by_name) : 'Party'}</span>
            </div>
          </div>
          <div class="item-actions">
            <button class="btn-requeue" data-action="requeue" data-uri="${escapeHtml(item.raw_input || item.spotify_uri || '')}" title="Re-queue track">
              + Re-queue
            </button>
          </div>
        </div>
      `;
    });

    el.historyList.innerHTML = html;
  }

  function escapeHtml(str) {
    if (!str) return '';
    return String(str).replace(/[&<>"']/g, (m) => {
      return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[m];
    });
  }

  // --- Submitting Songs / Albums / Playlists ---
  async function submitQueueInput(rawInput, explicitTarget = null) {
    const trimmed = (rawInput || '').trim();
    if (!trimmed) {
      showToast('Please paste a Spotify track, album, or playlist link', 'amber');
      return;
    }

    const finalTarget = explicitTarget || targetMode || 'queue';

    // If client is completely offline or fetch errors, stash into Outbox
    if (!navigator.onLine || isOffline) {
      addToOutbox(trimmed, finalTarget);
      el.spotifyInput.value = '';
      hidePlaylistModeBar();
      return;
    }

    try {
      const res = await fetch('/api/queue', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          input: trimmed,
          guest_id: guest.id,
          guest_name: guest.name,
          target: finalTarget
        })
      });

      if (res.ok) {
        const data = await res.json();
        showToast(data.message || `Queued ${data.added_count || 1} song(s)!`, 'success');
        el.spotifyInput.value = '';
        hidePlaylistModeBar();
        fetchState();
      } else {
        const err = await res.json().catch(() => ({}));
        showToast(err.error || 'Failed to queue track', 'error');
      }
    } catch {
      // Network drop during request: save to outbox
      addToOutbox(trimmed, finalTarget);
      el.spotifyInput.value = '';
      hidePlaylistModeBar();
    }
  }

  // Detect playlist/album in input
  function checkInputForPlaylist(val) {
    if (/playlist|album/i.test(val)) {
      el.playlistModeBar.classList.remove('hidden');
      if (/album/i.test(val)) {
        el.playlistDetectedTag.textContent = 'Spotify Album Detected';
      } else {
        el.playlistDetectedTag.textContent = 'Spotify Playlist Detected';
      }
    } else {
      hidePlaylistModeBar();
    }
  }

  function hidePlaylistModeBar() {
    el.playlistModeBar.classList.add('hidden');
    targetMode = 'queue';
    el.btnTargetQueue.classList.add('active');
    el.btnTargetFallback.classList.remove('active');
  }

  // --- Skip Voting & Upvoting ---
  async function voteSkip() {
    try {
      const res = await fetch('/api/skip-vote', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ guest_id: guest.id })
      });
      if (res.ok) {
        const data = await res.json();
        if (data.skipped) {
          showToast('Track skipped!', 'success');
        } else {
          showToast(`Skip vote recorded (${data.skip_votes}/${data.threshold})`, 'info');
        }
        fetchState();
      }
    } catch {
      showToast('Network error voting to skip', 'error');
    }
  }

  async function voteItem(itemId) {
    try {
      const res = await fetch(`/api/queue/${itemId}/vote`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ guest_id: guest.id })
      });
      if (res.ok) {
        fetchState();
      }
    } catch (err) {
      console.error('Vote error:', err);
    }
  }

  async function deleteQueueItem(itemId) {
    try {
      const headers = {};
      const hostPin = localStorage.getItem(STORAGE_KEYS.HOST_PIN);
      if (hostPin) headers['X-Host-Pin'] = hostPin;

      const res = await fetch(`/api/queue/${itemId}?guest_id=${encodeURIComponent(guest.id)}`, {
        method: 'DELETE',
        headers
      });
      if (res.ok) {
        showToast('Removed from queue', 'info');
        fetchState();
      }
    } catch {
      showToast('Failed to remove item', 'error');
    }
  }

  // --- Host Commands ---
  async function sendHostCommand(action, payload = {}) {
    try {
      const hostPin = localStorage.getItem(STORAGE_KEYS.HOST_PIN) || '';
      const headers = {
        'Content-Type': 'application/json',
        'X-Host-Pin': hostPin
      };

      const res = await fetch('/api/host/control', {
        method: 'POST',
        headers,
        body: JSON.stringify({
          action,
          ...payload
        })
      });

      if (res.ok) {
        fetchState();
      } else {
        showToast('Host action failed (check PIN)', 'error');
      }
    } catch {
      showToast('Network error executing host action', 'error');
    }
  }

  function updateHostBadge(isHost) {
    if (isHost) {
      el.btnHostToggle.classList.add('active');
      el.hostStatusLabel.textContent = 'Host';
      el.hostTransportBar.classList.remove('hidden');
    } else {
      el.btnHostToggle.classList.remove('active');
      el.hostStatusLabel.textContent = 'Host';
      el.hostTransportBar.classList.add('hidden');
    }
  }

  // --- Guest Profile Management ---
  function updateProfileUI() {
    el.profileName.textContent = guest.name;
    el.profileDot.style.background = guest.color;
    el.profileNameInput.value = guest.name;
  }

  async function saveProfile() {
    const newName = el.profileNameInput.value.trim() || guest.name;
    guest.name = newName;
    localStorage.setItem(STORAGE_KEYS.GUEST_NAME, newName);
    localStorage.setItem(STORAGE_KEYS.GUEST_COLOR, guest.color);
    updateProfileUI();
    el.profileModal.classList.add('hidden');

    try {
      await fetch('/api/guest', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          guest_id: guest.id,
          name: guest.name,
          color: guest.color
        })
      });
    } catch {
      // Best effort update
    }
    showToast(`Profile updated: ${guest.name}`, 'info');
  }

  function setupColorSwatches() {
    el.colorSwatches.innerHTML = '';
    AVATAR_COLORS.forEach((color) => {
      const swatch = document.createElement('div');
      swatch.style.width = '28px';
      swatch.style.height = '28px';
      swatch.style.borderRadius = '50%';
      swatch.style.background = color;
      swatch.style.cursor = 'pointer';
      swatch.style.border = (color === guest.color) ? '2px solid #ffffff' : '2px solid transparent';
      swatch.addEventListener('click', () => {
        guest.color = color;
        setupColorSwatches();
      });
      el.colorSwatches.appendChild(swatch);
    });
  }

  // --- Event Listeners Initialization ---
  function bindEvents() {
    // Online / Offline window listeners
    window.addEventListener('online', () => updateConnectionUI(true));
    window.addEventListener('offline', () => updateConnectionUI(false));

    // Profile modal
    el.btnEditProfile.addEventListener('click', () => {
      setupColorSwatches();
      el.profileNameInput.value = guest.name;
      el.profileModal.classList.remove('hidden');
    });
    el.btnCloseProfileModal.addEventListener('click', () => el.profileModal.classList.add('hidden'));
    el.btnSaveProfile.addEventListener('click', saveProfile);

    // QR modal
    el.btnShowQr.addEventListener('click', () => {
      const joinUrl = state.join_url || window.location.origin;
      el.qrModalImage.src = `/api/qr.svg?data=${encodeURIComponent(joinUrl)}`;
      el.qrJoinUrlText.textContent = joinUrl;
      el.qrModal.classList.remove('hidden');
    });
    el.btnCloseQrModal.addEventListener('click', () => el.qrModal.classList.add('hidden'));

    // Share Sheet Setup modal
    el.btnShareSetup.addEventListener('click', () => {
      const joinUrl = state.join_url || window.location.origin;
      const iosUrl = `${joinUrl}/api/quick-share?guest=${encodeURIComponent(guest.id)}&guest_name=${encodeURIComponent(guest.name)}&text=`;
      el.iosShareUrlDisplay.textContent = iosUrl;
      const savedShim = localStorage.getItem(STORAGE_KEYS.GITHUB_SHIM_URL) || '';
      el.androidShimUrlInput.value = savedShim;
      el.shareModal.classList.remove('hidden');
    });
    el.btnCloseShareModal.addEventListener('click', () => el.shareModal.classList.add('hidden'));

    // Segmented tab switch in Share Modal
    el.tabIosBtn.addEventListener('click', () => {
      el.tabIosBtn.classList.add('active');
      el.tabAndroidBtn.classList.remove('active');
      el.shareIosTab.style.display = 'block';
      el.shareAndroidTab.style.display = 'none';
    });
    el.tabAndroidBtn.addEventListener('click', () => {
      el.tabAndroidBtn.classList.add('active');
      el.tabIosBtn.classList.remove('active');
      el.shareIosTab.style.display = 'none';
      el.shareAndroidTab.style.display = 'block';
    });

    // Copy iOS share URL
    el.btnCopyIosShareUrl.addEventListener('click', () => {
      const text = el.iosShareUrlDisplay.textContent;
      navigator.clipboard.writeText(text).then(() => {
        showToast('iOS Quick-Share URL copied!', 'success');
      }).catch(() => {
        showToast('Failed to copy. Please manually select and copy.', 'amber');
      });
    });

    // Android GitHub shim launch
    el.btnOpenAndroidShim.addEventListener('click', () => {
      let shim = el.androidShimUrlInput.value.trim();
      if (!shim) {
        showToast('Enter your GitHub Pages URL (e.g. https://marcelkornblum.github.io/deckjam)', 'amber');
        return;
      }
      if (!shim.startsWith('http')) shim = 'https://' + shim;
      localStorage.setItem(STORAGE_KEYS.GITHUB_SHIM_URL, shim);

      const joinUrl = state.join_url || window.location.origin;
      const launchUrl = `${shim}?server=${encodeURIComponent(joinUrl)}&guest=${encodeURIComponent(guest.id)}&name=${encodeURIComponent(guest.name)}`;
      window.open(launchUrl, '_blank');
    });

    // PWA Install prompt handling
    window.addEventListener('beforeinstallprompt', (e) => {
      e.preventDefault();
      deferredPrompt = e;
      el.btnInstallPwa.style.display = 'inline-flex';
    });

    el.btnInstallPwa.addEventListener('click', async () => {
      if (deferredPrompt) {
        deferredPrompt.prompt();
        const choice = await deferredPrompt.userChoice;
        if (choice.outcome === 'accepted') {
          showToast('DeckJam installed as OS Share Target!', 'success');
        }
        deferredPrompt = null;
        el.btnInstallPwa.style.display = 'none';
      }
    });

    // Outbox manual flush
    el.btnFlushOutbox.addEventListener('click', flushOutbox);

    // Vote to Skip
    el.btnVoteSkip.addEventListener('click', voteSkip);

    // Host Transport buttons
    el.btnHostPlayPause.addEventListener('click', () => sendHostCommand('toggle'));
    el.btnHostNext.addEventListener('click', () => sendHostCommand('skip'));
    el.btnHostPrev.addEventListener('click', () => sendHostCommand('restart'));
    el.hostVolumeSlider.addEventListener('input', (e) => {
      sendHostCommand('volume', { value: parseInt(e.target.value, 10) });
    });

    // Frictionless Spotify Link Input & Paste
    el.spotifyInput.addEventListener('input', (e) => {
      checkInputForPlaylist(e.target.value);
    });

    el.btnPasteClipboard.addEventListener('click', async () => {
      try {
        if (navigator.clipboard && navigator.clipboard.readText) {
          const clip = await navigator.clipboard.readText();
          if (clip) {
            el.spotifyInput.value = clip;
            checkInputForPlaylist(clip);
            showToast('Pasted from clipboard!', 'info', 1800);
            return;
          }
        }
      } catch {
        // Clipboard read permission denied or unavailable
      }
      el.spotifyInput.focus();
      showToast('Tap inside the box and paste your link', 'info', 2000);
    });

    el.btnSubmitQueue.addEventListener('click', () => {
      submitQueueInput(el.spotifyInput.value);
    });

    el.btnTargetQueue.addEventListener('click', () => {
      targetMode = 'queue';
      el.btnTargetQueue.classList.add('active');
      el.btnTargetFallback.classList.remove('active');
    });

    el.btnTargetFallback.addEventListener('click', () => {
      targetMode = 'fallback';
      el.btnTargetFallback.classList.add('active');
      el.btnTargetQueue.classList.remove('active');
    });

    // Tab navigation (Queue vs History)
    el.tabQueueBtn.addEventListener('click', () => {
      el.tabQueueBtn.classList.add('active');
      el.tabHistoryBtn.classList.remove('active');
      el.queueTabContent.style.display = 'flex';
      el.historyTabContent.style.display = 'none';
    });

    el.tabHistoryBtn.addEventListener('click', () => {
      el.tabHistoryBtn.classList.add('active');
      el.tabQueueBtn.classList.remove('active');
      el.queueTabContent.style.display = 'none';
      el.historyTabContent.style.display = 'flex';
    });

    // Queue & History Delegated Click Actions
    el.queueList.addEventListener('click', (e) => {
      const btn = e.target.closest('button');
      if (!btn) return;
      const action = btn.dataset.action;
      const itemId = btn.dataset.id;
      if (action === 'vote') {
        voteItem(itemId);
      } else if (action === 'remove') {
        deleteQueueItem(itemId);
      } else if (action === 'host-play-now') {
        sendHostCommand('play_now', { item_id: parseInt(itemId, 10) });
      }
    });

    el.historyList.addEventListener('click', (e) => {
      const btn = e.target.closest('button');
      if (!btn) return;
      if (btn.dataset.action === 'requeue') {
        const uri = btn.dataset.uri;
        if (uri) submitQueueInput(uri, 'queue');
      }
    });

    // Host modal trigger & PIN verification
    el.btnHostToggle.addEventListener('click', () => {
      if (state.is_host) {
        showHostSettings();
      } else {
        el.hostPinSection.style.display = 'block';
        el.hostSettingsSection.style.display = 'none';
        el.hostPinInput.value = '';
      }
      el.hostModal.classList.remove('hidden');
    });
    el.btnCloseHostModal.addEventListener('click', () => el.hostModal.classList.add('hidden'));

    el.btnVerifyPin.addEventListener('click', async () => {
      const enteredPin = el.hostPinInput.value.trim();
      if (!enteredPin) return;

      try {
        const res = await fetch('/api/host/verify', {
          headers: { 'X-Host-Pin': enteredPin }
        });
        if (res.ok) {
          localStorage.setItem(STORAGE_KEYS.HOST_PIN, enteredPin);
          showToast('Host PIN Verified!', 'success');
          fetchState();
          showHostSettings();
        } else {
          showToast('Invalid Host PIN', 'error');
        }
      } catch {
        showToast('Verification failed', 'error');
      }
    });

    function showHostSettings() {
      el.hostPinSection.style.display = 'none';
      el.hostSettingsSection.style.display = 'flex';
      if (state.settings) {
        el.hostRoomNameInput.value = state.settings.room_name || 'DeckJam';
        el.hostQueueModeSelect.value = state.settings.queue_mode || 'round_robin';
        el.hostSkipThresholdInput.value = state.settings.skip_vote_threshold || 3;
        el.hostBackendSelect.value = state.settings.player_backend || 'auto';
        el.hostAutoplayFallbackCheck.checked = !!state.settings.autoplay_fallback;
      }
      el.hostFallbackCount.textContent = state.fallback_count || 0;
      el.hostPinDisplay.textContent = state.host_pin || localStorage.getItem(STORAGE_KEYS.HOST_PIN) || '----';
    }

    el.btnSaveHostSettings.addEventListener('click', () => {
      const updated = {
        room_name: el.hostRoomNameInput.value.trim(),
        queue_mode: el.hostQueueModeSelect.value,
        skip_vote_threshold: parseInt(el.hostSkipThresholdInput.value, 10) || 3,
        autoplay_fallback: el.hostAutoplayFallbackCheck.checked,
        player_backend: el.hostBackendSelect.value
      };
      sendHostCommand('update_settings', { settings: updated });
      el.hostModal.classList.add('hidden');
      showToast('Settings saved!', 'success');
    });

    el.btnClearQueueBtn.addEventListener('click', () => {
      if (confirm('Clear the entire party queue?')) {
        sendHostCommand('clear_queue');
      }
    });

    el.btnClearFallbackBtn.addEventListener('click', () => {
      if (confirm('Clear the fallback pool tracks?')) {
        sendHostCommand('clear_fallback');
      }
    });
  }

  // --- Handle Shared Redirect Query Params ---
  function handleUrlParams() {
    const params = new URLSearchParams(window.location.search);
    if (params.get('shared') === '1') {
      const msg = params.get('msg') || 'Track queued from Share Sheet!';
      showToast(msg, 'success', 5000);
      window.history.replaceState({}, document.title, window.location.pathname);
    }
  }

  // --- Register Service Worker ---
  function registerServiceWorker() {
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.register('/sw.js').catch((err) => {
        console.warn('ServiceWorker registration error:', err);
      });
    }
  }

  // --- Bootstrap ---
  function init() {
    updateProfileUI();
    bindEvents();
    handleUrlParams();
    registerServiceWorker();
    renderOutboxBanner();
    fetchState();
    connectSSE();
    startProgressInterpolator();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
