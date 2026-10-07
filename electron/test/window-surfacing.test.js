const test = require("node:test");
const assert = require("node:assert/strict");
const {
  orderByRecentFocus,
  pickWindowToSurface,
  shouldHideOnClose,
} = require("../src/window-surfacing");

// A stand-in for a BrowserWindow in one of the states the policy cares about.
//   open      — on screen
//   minimized — in the Dock
//   hidden    — closed to the tray (macOS)
//   destroyed — gone
function fakeWindow(name, state) {
  return {
    name,
    isDestroyed: () => state === "destroyed",
    isVisible: () => state === "open",
    isMinimized: () => state === "minimized",
  };
}

const MAC = { platform: "darwin", isQuitting: false };

test("the tray focuses the open window, not a newer one that was closed to the tray", () => {
  // The reported bug: window A is in use; window B was opened later and then
  // closed. B is the newest, so the old code (which surfaced the most recently
  // created window) brought B back.
  const inUse = fakeWindow("A", "open");
  const closed = fakeWindow("B", "hidden");

  assert.equal(pickWindowToSurface([closed, inUse]), inUse);
  assert.equal(pickWindowToSurface([inUse, closed]), inUse);
});

test("with several open windows, the most recently focused one is surfaced", () => {
  const recent = fakeWindow("recent", "open");
  const older = fakeWindow("older", "open");

  assert.equal(pickWindowToSurface([recent, older]), recent);
});

test("a minimized window is restored before a closed one is resurrected", () => {
  const closed = fakeWindow("closed", "hidden");
  const minimized = fakeWindow("minimized", "minimized");

  assert.equal(pickWindowToSurface([closed, minimized]), minimized);
});

test("an open window wins over a minimized one even if the minimized one was focused later", () => {
  const minimized = fakeWindow("minimized", "minimized");
  const open = fakeWindow("open", "open");

  assert.equal(pickWindowToSurface([minimized, open]), open);
});

test("when every window was closed to the tray, the most recent one comes back", () => {
  const recent = fakeWindow("recent", "hidden");
  const older = fakeWindow("older", "hidden");

  assert.equal(pickWindowToSurface([recent, older]), recent);
});

test("nothing to surface means a new window is needed", () => {
  assert.equal(pickWindowToSurface([]), null);
  assert.equal(pickWindowToSurface([fakeWindow("gone", "destroyed")]), null);
});

test("windows are ordered by last focus, newest first, and destroyed ones are dropped", () => {
  const a = fakeWindow("a", "open");
  const b = fakeWindow("b", "open");
  const c = fakeWindow("c", "open");
  const gone = fakeWindow("gone", "destroyed");

  const ordered = orderByRecentFocus([
    { window: a, lastFocusedAt: 1 },
    { window: gone, lastFocusedAt: 9 },
    { window: b, lastFocusedAt: 3 },
    { window: c },
  ]);

  assert.deepEqual(
    ordered.map((window) => window.name),
    ["b", "a", "c"],
  );
});

test("on macOS, closing the last open window hides it so the tray can bring it back", () => {
  const last = fakeWindow("last", "open");

  assert.equal(shouldHideOnClose(last, [last], MAC), true);
});

test("on macOS, closing a window while another is open really closes it", () => {
  // Hiding here is what left closed windows alive for the tray to resurrect.
  const closing = fakeWindow("closing", "open");
  const other = fakeWindow("other", "open");

  assert.equal(shouldHideOnClose(closing, [closing, other], MAC), false);
});

test("a minimized window counts as still open", () => {
  const closing = fakeWindow("closing", "open");
  const minimized = fakeWindow("minimized", "minimized");

  assert.equal(shouldHideOnClose(closing, [closing, minimized], MAC), false);
});

test("a window already closed to the tray does not count as open", () => {
  const closing = fakeWindow("closing", "open");
  const closedEarlier = fakeWindow("closed earlier", "hidden");
  const gone = fakeWindow("gone", "destroyed");

  assert.equal(shouldHideOnClose(closing, [closing, closedEarlier, gone], MAC), true);
});

test("windows are never hidden on close off macOS, or while quitting", () => {
  const last = fakeWindow("last", "open");

  assert.equal(shouldHideOnClose(last, [last], { platform: "win32", isQuitting: false }), false);
  assert.equal(shouldHideOnClose(last, [last], { platform: "linux", isQuitting: false }), false);
  assert.equal(shouldHideOnClose(last, [last], { platform: "darwin", isQuitting: true }), false);
});
