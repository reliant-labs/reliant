// Which Reliant window a "bring the app forward" gesture (tray click, dock
// click, the tray's "Show Reliant") surfaces, and whether closing a window on
// macOS hides it or really closes it.
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
// Kept free of Electron imports so the policy is unit-testable; a "window" here
// is anything with isDestroyed / isVisible / isMinimized.

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

module.exports = {
  orderByRecentFocus,
  pickWindowToSurface,
  shouldHideOnClose,
};
