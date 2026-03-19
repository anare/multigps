'use strict';

// content.js — isolated world, runs in every page frame at document_start.
//
// Responsibilities:
//  1. Inject inject.js into the page's MAIN world so it can override
//     navigator.geolocation before any page code runs.
//  2. Forward position updates (from chrome.storage) to inject.js via a
//     CustomEvent on window.  CustomEvents cross the isolated/main world
//     boundary because both share the same DOM.

(function () {
  // ── 1. Inject the geolocation override into the MAIN world ───────────────
  const script = document.createElement('script');
  script.src = chrome.runtime.getURL('inject.js');
  script.addEventListener('load', () => {
    // inject.js is now executing in the MAIN world; send the current position
    // (if we already have one) so geolocation is spoofed from the first call.
    chrome.storage.local.get(['currentPos', 'enabled'], ({ currentPos, enabled }) => {
      if (enabled && currentPos) sendPosition(currentPos);
    });
  });
  // Append to <html> (always present at document_start) and remove after load
  // so the element doesn't linger in the DOM.
  document.documentElement.appendChild(script);

  // ── 2. Forward storage changes to inject.js ───────────────────────────────
  chrome.storage.onChanged.addListener((changes, area) => {
    if (area !== 'local') return;

    // Extension was disabled — tell inject.js to restore real geolocation.
    if (changes.enabled && changes.enabled.newValue === false) {
      window.dispatchEvent(new CustomEvent('__multigps:disable'));
      return;
    }
    // Extension was re-enabled — send the current position immediately.
    if (changes.enabled && changes.enabled.newValue === true) {
      chrome.storage.local.get(['currentPos'], ({ currentPos }) => {
        if (currentPos) sendPosition(currentPos);
      });
      return;
    }
    // New position arrived from the background poller.
    if (changes.currentPos) {
      const pos = changes.currentPos.newValue;
      if (pos) sendPosition(pos);
    }
  });

  function sendPosition(pos) {
    window.dispatchEvent(new CustomEvent('__multigps:position', { detail: pos }));
  }
})();
