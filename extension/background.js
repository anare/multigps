'use strict';

// background.js — service worker
// Polls the multigps server's /api/position endpoint and stores the result in
// chrome.storage.local so content scripts can relay it to the injected
// geolocation override without each page making its own HTTP requests.

const DEFAULT_SERVER_URL = 'http://localhost:8080';
const POLL_INTERVAL_MS   = 1000; // 1 second
const FETCH_TIMEOUT_MS   = 5000;
const ALARM_NAME         = 'multigps-keepalive';

// ── Setup ────────────────────────────────────────────────────────────────────

chrome.runtime.onInstalled.addListener(() => {
  chrome.storage.local.get(['serverUrl', 'enabled'], (items) => {
    const defaults = {};
    if (items.serverUrl === undefined) defaults.serverUrl = DEFAULT_SERVER_URL;
    if (items.enabled   === undefined) defaults.enabled   = true;
    if (Object.keys(defaults).length) chrome.storage.local.set(defaults);
  });
  // 1-minute alarm acts as a keepalive to restart polling if the service
  // worker is terminated between polls.
  chrome.alarms.create(ALARM_NAME, { periodInMinutes: 1 });
  startPolling();
});

chrome.runtime.onStartup.addListener(() => {
  chrome.alarms.create(ALARM_NAME, { periodInMinutes: 1 });
  startPolling();
});

// Restart polling whenever the keepalive alarm fires.
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === ALARM_NAME) startPolling();
});

// ── Polling ──────────────────────────────────────────────────────────────────

let pollIntervalId = null;

function startPolling() {
  if (pollIntervalId !== null) return; // already running
  pollIntervalId = setInterval(poll, POLL_INTERVAL_MS);
  poll(); // immediate first tick
}

async function poll() {
  let serverUrl, enabled;
  try {
    ({ serverUrl = DEFAULT_SERVER_URL, enabled = true } =
      await chrome.storage.local.get(['serverUrl', 'enabled']));
  } catch (_) {
    return; // storage not yet available
  }

  if (!enabled) {
    await chrome.storage.local.set({ connected: false, currentPos: null }).catch(() => {});
    return;
  }

  const controller = new AbortController();
  const timeoutId  = setTimeout(() => controller.abort(), FETCH_TIMEOUT_MS);

  try {
    const resp = await fetch(`${serverUrl}/api/position`, { signal: controller.signal });
    clearTimeout(timeoutId);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const pos = await resp.json();
    if (pos.error) throw new Error(pos.error);
    await chrome.storage.local.set({ currentPos: pos, connected: true, lastError: null });
  } catch (e) {
    clearTimeout(timeoutId);
    if (e.name !== 'AbortError') {
      await chrome.storage.local.set({ connected: false, lastError: e.message }).catch(() => {});
    }
  }
}
