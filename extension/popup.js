'use strict';

// popup.js — reads / writes chrome.storage.local and refreshes the UI.

const DEFAULT_URL = 'http://localhost:8080';

const els = {
  enabled:   document.getElementById('enabled'),
  serverUrl: document.getElementById('server-url'),
  saveBtn:   document.getElementById('save-btn'),
  dot:       document.getElementById('dot'),
  statusTxt: document.getElementById('status-text'),
  lat:       document.getElementById('c-lat'),
  lon:       document.getElementById('c-lon'),
  alt:       document.getElementById('c-alt'),
  speed:     document.getElementById('c-speed'),
  course:    document.getElementById('c-course'),
  accuracy:  document.getElementById('c-accuracy'),
};

function fmt(v, digits) {
  return v != null ? Number(v).toFixed(digits) : '—';
}

function render({ serverUrl, enabled, connected, currentPos, lastError }) {
  els.serverUrl.value = serverUrl || DEFAULT_URL;
  els.enabled.checked = enabled !== false;

  if (!enabled) {
    els.dot.className    = 'status-dot';
    els.statusTxt.textContent = 'Disabled';
  } else if (connected) {
    els.dot.className    = 'status-dot ok';
    els.statusTxt.textContent = 'Connected';
  } else {
    els.dot.className    = 'status-dot err';
    els.statusTxt.textContent = lastError ? `Error: ${lastError}` : 'Disconnected';
  }

  const p = currentPos;
  els.lat.textContent      = p ? fmt(p.lat, 6)      : '—';
  els.lon.textContent      = p ? fmt(p.lon, 6)      : '—';
  els.alt.textContent      = p ? fmt(p.alt, 1) + ' m' : '—';
  els.speed.textContent    = p ? fmt(p.speed, 2) + ' m/s' : '—';
  els.course.textContent   = p ? fmt(p.course, 1) + '°'  : '—';
  els.accuracy.textContent = p ? fmt(p.accuracy, 1) + ' m' : '—';
}

// ── Initial load ──────────────────────────────────────────────────────────────

chrome.storage.local.get(
  ['serverUrl', 'enabled', 'connected', 'currentPos', 'lastError'],
  render,
);

// Refresh the popup while it's open so the position updates live.
chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== 'local') return;
  chrome.storage.local.get(
    ['serverUrl', 'enabled', 'connected', 'currentPos', 'lastError'],
    render,
  );
});

// ── Controls ──────────────────────────────────────────────────────────────────

els.enabled.addEventListener('change', () => {
  chrome.storage.local.set({ enabled: els.enabled.checked });
});

els.saveBtn.addEventListener('click', () => {
  const url = els.serverUrl.value.trim().replace(/\/$/, '');
  if (url) chrome.storage.local.set({ serverUrl: url });
});
