'use strict';

// inject.js — runs in the page's MAIN world (loaded via a <script> tag by
// content.js).  It overrides navigator.geolocation before page scripts execute
// so every call to getCurrentPosition / watchPosition returns the fake GPS
// coordinates provided by the multigps server.

(function () {
  // Save a reference to the real geolocation object in case we need to restore
  // it when the extension is disabled.
  const realGeolocation = navigator.geolocation;

  let fakePos  = null; // most recent position from the server
  let active   = true; // false after a __multigps:disable event
  let watchSeq = 0;    // monotonically increasing watch-ID counter
  const watchers = new Map(); // id → { success, error }

  // ── Helpers ─────────────────────────────────────────────────────────────────

  function buildGeolocationPosition(data) {
    // The Geolocation API exposes a GeolocationPosition-like object.  We build
    // a plain object that satisfies everything web apps commonly inspect.
    return {
      coords: {
        latitude:         data.lat,
        longitude:        data.lon,
        altitude:         data.alt  != null ? data.alt   : null,
        accuracy:         data.accuracy != null ? data.accuracy : 10,
        altitudeAccuracy: data.alt  != null ? 5            : null,
        heading:          data.course != null ? data.course : null,
        speed:            data.speed  != null ? data.speed  : null,
      },
      timestamp: Date.now(),
    };
  }

  function notifyWatchers() {
    if (!fakePos) return;
    const position = buildGeolocationPosition(fakePos);
    for (const { success } of watchers.values()) {
      try { success(position); } catch (_) {}
    }
  }

  // ── Fake Geolocation API ─────────────────────────────────────────────────────

  const fakeGeolocation = {
    getCurrentPosition(success, error /*, options */) {
      if (!active) { realGeolocation.getCurrentPosition(success, error); return; }
      if (fakePos) {
        try { success(buildGeolocationPosition(fakePos)); } catch (_) {}
      } else if (typeof error === 'function') {
        error({ code: 2, message: 'MultiGPS: waiting for server position' });
      }
    },

    watchPosition(success, error /*, options */) {
      if (!active) return realGeolocation.watchPosition(success, error);
      const id = ++watchSeq;
      watchers.set(id, { success, error: error || null });
      if (fakePos) {
        try { success(buildGeolocationPosition(fakePos)); } catch (_) {}
      }
      return id;
    },

    clearWatch(id) {
      if (watchers.has(id)) {
        watchers.delete(id);
      } else {
        // May be a real watch ID if the extension was disabled mid-session.
        realGeolocation.clearWatch(id);
      }
    },
  };

  // Override navigator.geolocation via the prototype so pages that cache the
  // geolocation reference (const geo = navigator.geolocation) also get the
  // fake object.
  try {
    Object.defineProperty(Navigator.prototype, 'geolocation', {
      get: () => active ? fakeGeolocation : realGeolocation,
      configurable: true,
    });
  } catch (_) {
    // Fall back to direct property override if the prototype descriptor is
    // non-configurable in this browser version.
    try {
      Object.defineProperty(navigator, 'geolocation', {
        get: () => active ? fakeGeolocation : realGeolocation,
        configurable: true,
      });
    } catch (_) {}
  }

  // ── Event listeners (content.js → inject.js bridge) ──────────────────────────

  window.addEventListener('__multigps:position', (e) => {
    fakePos  = e.detail;
    active   = true;
    notifyWatchers();
  });

  window.addEventListener('__multigps:disable', () => {
    active = false;
    watchers.clear();
  });
})();
