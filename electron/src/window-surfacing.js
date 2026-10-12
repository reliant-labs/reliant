// Which Reliant window a "bring the app forward" gesture (tray click, dock
// click, the tray's "Show Reliant", switching to the app) surfaces, and whether
// closing a window on macOS hides it or really closes it.
//
// Those two decisions have to agree, and they used to disagree. macOS keeps a
// closed window alive and hidden so the tray can bring the app back without a
// cold start. That applied to EVERY window, so each window the user closed
// lived on invisibly — and the tray surfaced `mainWindow`, which is simply the
// most recently CREATED window. Open a second window, close it, click the tray:
// the closed window came back instead of the one in use.
//
// The rule now:
//   - Closing a window really closes it while another window is still open.
//     Only the last open window is hidden, which is the case the hide exists for.
//   - A surface gesture prefers the most recently focused window that is open,
//     then one that is minimized, and only then one that was closed to the tray.
//
// Switching to the app is the third gesture, and the one that used to do
// nothing. macOS reports an activation through two events:
//   activate           — applicationShouldHandleReopen: a dock click, or
//                        launching the app again from Finder or Spotlight.
//   did-become-active  — applicationDidBecomeActive: EVERY activation,
//                        including the Cmd+Tab app switcher and a notification.
// Only `activate` was handled, so Cmd+Tab to Reliant with its window closed to
// the tray made it the active app with nothing on screen, and only the tray
// brought the window back. Now:
//   - The tray and the dock always bring a window forward (surface).
//   - Any other activation brings one forward only when no Reliant window is
//     on screen (surfaceIfNothingOnScreen). Switching back to an app that is
//     already showing a window must not restore minimized ones or move focus.
//   - Creating a window is single-flight. One dock click raises both events,
//     and a window being created is not in the list until it registers.
//   - Nothing is surfaced while the app is quitting or installing an update.
//
// Kept free of Electron imports so the policy is unit-testable; a "window" here
// is anything with isDestroyed / isVisible / isMinimized (and restore / show /
// focus, to be brought forward; webContents, to be sent to), and `app` is
// anything with on().

// Orders tracked windows most recently focused first, dropping destroyed ones.
// Each entry is { window, lastFocusedAt }; a missing lastFocusedAt sorts last.
function orderByRecentFocus(entries) {
  return entries
    .filter((entry) => entry.window && !entry.window.isDestroyed())
    .sort((a, b) => (b.lastFocusedAt || 0) - (a.lastFocusedAt || 0))
    .map((entry) => entry.window);
}

// Open means the user can see it or get it back from the Dock. A window hidden
// by a close is not open.
function isOpen(window) {
  return !window.isDestroyed() && (window.isVisible() || window.isMinimized());
}

// `windows` must be ordered most recently focused first (orderByRecentFocus).
// Returns null when there is nothing to surface and a new window is needed.
function pickWindowToSurface(windows) {
  const live = windows.filter((window) => !window.isDestroyed());
  return (
    live.find((window) => window.isVisible() && !window.isMinimized()) ||
    live.find((window) => window.isMinimized()) ||
    live[0] ||
    null
  );
}

// Whether closing `window` should hide it instead: only on macOS, never during
// a quit, and only when no other window would be left open.
function shouldHideOnClose(window, windows, { platform, isQuitting }) {
  if (isQuitting || platform !== "darwin") {
    return false;
  }
  return !windows.some((other) => other !== window && isOpen(other));
}

// On screen means shown and not in the Dock. A window closed to the tray, a
// minimized one, and one still loading behind `show: false` are not.
function hasWindowOnScreen(windows) {
  return windows.some(
    (window) => !window.isDestroyed() && window.isVisible() && !window.isMinimized(),
  );
}

// The one path from a gesture to a window on screen.
//   getWindows()   — app windows, most recently focused first (orderByRecentFocus).
//   createWindow() — opens a new app window; may resolve before or after the
//                    window appears in getWindows().
//   isSuppressed() — true while quitting or installing an update.
//   settle(fn)     — runs fn a moment after a still-loading page finishes, so
//                    its app has started before surfaceAndSend's message lands.
function createWindowSurfacer({
  getWindows,
  createWindow,
  isSuppressed = () => false,
  settle = (fn) => setTimeout(fn, 150),
}) {
  // The pending createWindow() while one is in flight. Every gesture that
  // arrives meanwhile shares it: that window shows itself once it has loaded,
  // so the gesture neither opens a second one nor shows it half-loaded.
  let creating = null;

  function create() {
    let pending;
    try {
      pending = Promise.resolve(createWindow());
    } catch (error) {
      pending = Promise.reject(error);
    }
    // Cleared on failure too, so the next gesture can try again.
    creating = pending.finally(() => {
      creating = null;
    });
    return creating;
  }

  // Tray click, dock click, "Show Reliant", the startup window: the window in
  // use, else one to bring back, else a new one.
  function surface() {
    if (isSuppressed()) {
      return Promise.resolve(null);
    }
    if (creating) {
      return creating;
    }
    const window = pickWindowToSurface(getWindows());
    if (!window) {
      return create();
    }
    if (window.isMinimized()) {
      window.restore();
    }
    window.show();
    window.focus();
    return Promise.resolve(window);
  }

  // Any other activation (Cmd+Tab, Spotlight, a notification): the same, but
  // only when no window is on screen. Resolves null when it leaves things be.
  function surfaceIfNothingOnScreen() {
    if (isSuppressed()) {
      return Promise.resolve(null);
    }
    if (creating) {
      return creating;
    }
    if (hasWindowOnScreen(getWindows())) {
      return Promise.resolve(null);
    }
    return surface();
  }

  // A gesture that acts inside the app (the tray's "New Chat", a menu
  // shortcut): surface() a window, then send it `channel` — after its page has
  // loaded if it is still loading. Resolves the window it sent to, or null when
  // nothing was surfaced.
  function surfaceAndSend(channel, payload) {
    return surface().then((window) => {
      if (!window || window.isDestroyed()) {
        return null;
      }
      const { webContents } = window;
      if (!webContents.isLoading()) {
        webContents.send(channel, payload);
        return window;
      }
      return new Promise((resolve) => {
        webContents.once("did-finish-load", () => {
          settle(() => {
            if (window.isDestroyed()) {
              resolve(null);
              return;
            }
            webContents.send(channel, payload);
            resolve(window);
          });
        });
      });
    });
  }

  return { surface, surfaceIfNothingOnScreen, surfaceAndSend };
}

// Listens for every way macOS activates the app. Register it before anything
// at startup can await: a listener added after a step that can reject is never
// added when that step does, and dock clicks then do nothing all session.
//   defer   — runs did-become-active's check a turn later, so macOS has finished
//             its own part of the activation (unhiding after Cmd+H, the same
//             dock click's `activate`) and a window it is bringing back counts
//             as on screen.
//   onError — (eventName, error) when surfacing fails.
function registerAppActivation(app, surfacer, { defer = setImmediate, onError = () => {} } = {}) {
  app.on("activate", () => {
    surfacer.surface().catch((error) => onError("activate", error));
  });
  app.on("did-become-active", () => {
    defer(() => {
      surfacer.surfaceIfNothingOnScreen().catch((error) => onError("did-become-active", error));
    });
  });
}

module.exports = {
  createWindowSurfacer,
  hasWindowOnScreen,
  orderByRecentFocus,
  pickWindowToSurface,
  registerAppActivation,
  shouldHideOnClose,
};
