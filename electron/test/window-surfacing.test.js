const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const fs = require("node:fs");
const path = require("node:path");
const {
  createWindowSurfacer,
  hasWindowOnScreen,
  orderByRecentFocus,
  pickWindowToSurface,
  registerAppActivation,
  shouldHideOnClose,
} = require("../src/window-surfacing");

// A stand-in for a BrowserWindow in one of the states the policy cares about.
//   open      — on screen
//   minimized — in the Dock
//   hidden    — closed to the tray (macOS), or still loading behind show: false
//   destroyed — gone
// Bringing it forward moves it on screen, and `calls` records how.
function fakeWindow(name, state) {
  const window = {
    name,
    state,
    calls: [],
    isDestroyed: () => window.state === "destroyed",
    isVisible: () => window.state === "open",
    isMinimized: () => window.state === "minimized",
    restore: () => {
      window.calls.push("restore");
      window.state = "open";
    },
    show: () => {
      window.calls.push("show");
      if (window.state === "hidden") {
        window.state = "open";
      }
    },
    focus: () => {
      window.calls.push("focus");
    },
  };
  return window;
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

test("a window is on screen only when shown and not minimized", () => {
  assert.equal(hasWindowOnScreen([fakeWindow("open", "open")]), true);
  assert.equal(hasWindowOnScreen([fakeWindow("hidden", "hidden"), fakeWindow("open", "open")]), true);
  assert.equal(hasWindowOnScreen([]), false);
  assert.equal(hasWindowOnScreen([fakeWindow("hidden", "hidden")]), false);
  assert.equal(hasWindowOnScreen([fakeWindow("minimized", "minimized")]), false);
  assert.equal(hasWindowOnScreen([fakeWindow("gone", "destroyed")]), false);
});

// The app as the surfacer sees it, with its activation events wired the way
// main.js wires them. createWindow resolves only once the new window has
// loaded, like the real one. `registerAt` is when that window joins the list:
//   "construct" — main.js's createWindow registers it on construction
//   "load"      — WindowManager.createWindow registers it after loading, the
//                 worst case for a second gesture arriving mid-creation
// did-become-active's deferred check runs only on runDeferred().
function fakeApp({ windows = [], registerAt = "construct", quitting = false, failCreates = 0 } = {}) {
  const harness = {
    windows,
    created: [],
    quitting,
    errors: [],
    loads: [],
    deferred: [],
  };
  harness.surfacer = createWindowSurfacer({
    getWindows: () => harness.windows,
    createWindow: () => {
      if (failCreates > 0) {
        failCreates -= 1;
        return Promise.reject(new Error("load failed"));
      }
      const window = fakeWindow(`new-${harness.created.length + 1}`, "hidden");
      harness.created.push(window);
      if (registerAt === "construct") {
        harness.windows = [window, ...harness.windows];
      }
      return new Promise((resolve) => {
        harness.loads.push(() => {
          if (registerAt === "load") {
            harness.windows = [window, ...harness.windows];
          }
          window.state = "open"; // ready-to-show
          resolve(window);
        });
      });
    },
    isSuppressed: () => harness.quitting,
  });
  harness.app = new EventEmitter();
  registerAppActivation(harness.app, harness.surfacer, {
    defer: (fn) => harness.deferred.push(fn),
    onError: (source, error) => harness.errors.push({ source, error }),
  });
  harness.runDeferred = () => {
    while (harness.deferred.length) {
      harness.deferred.shift()();
    }
  };
  harness.finishLoads = async () => {
    while (harness.loads.length) {
      harness.loads.shift()();
    }
    await new Promise((resolve) => setImmediate(resolve));
  };
  // Cmd+Tab to the app: did-become-active alone.
  harness.switchTo = () => {
    harness.app.emit("did-become-active");
    harness.runDeferred();
  };
  return harness;
}

test("switching to the app with no window at all opens one", async () => {
  // Cmd+Tab raises did-become-active and NOT activate, so a handler on
  // activate alone left the app active with nothing on screen.
  const app = fakeApp();

  app.switchTo();
  await app.finishLoads();

  assert.equal(app.created.length, 1);
  assert.equal(app.created[0].state, "open");
});

test("switching to the app brings back the window that was closed to the tray", () => {
  // The reported bug: close the window, Cmd+Tab away and back, nothing shows.
  const closed = fakeWindow("closed", "hidden");
  const app = fakeApp({ windows: [closed] });

  app.switchTo();

  assert.equal(closed.state, "open");
  assert.deepEqual(closed.calls, ["show", "focus"]);
  assert.equal(app.created.length, 0, "the closed window is reused, not replaced");
});

test("switching to the app with only a minimized window restores it", () => {
  const minimized = fakeWindow("minimized", "minimized");
  const app = fakeApp({ windows: [minimized] });

  app.switchTo();

  assert.equal(minimized.state, "open");
  assert.deepEqual(minimized.calls, ["restore", "show", "focus"]);
  assert.equal(app.created.length, 0);
});

test("switching to the app while a window is on screen changes nothing", () => {
  // macOS has already brought the app forward. Surfacing here would restore
  // windows the user minimized and move focus they did not ask to move.
  const minimized = fakeWindow("minimized", "minimized");
  const open = fakeWindow("open", "open");
  const app = fakeApp({ windows: [minimized, open] });

  app.switchTo();

  assert.deepEqual(minimized.calls, []);
  assert.deepEqual(open.calls, []);
  assert.equal(minimized.state, "minimized");
  assert.equal(app.created.length, 0);
});

for (const registerAt of ["construct", "load"]) {
  test(`a dock click's activate and did-become-active open exactly one window (registered on ${registerAt})`, async () => {
    // One dock click on an inactive app raises both events, in either order.
    for (const order of [
      ["activate", "did-become-active"],
      ["did-become-active", "activate"],
    ]) {
      const app = fakeApp({ registerAt });

      for (const event of order) {
        app.app.emit(event);
      }
      app.runDeferred();
      await app.finishLoads();

      assert.equal(app.created.length, 1, `order: ${order.join(" then ")}`);
    }
  });
}

test("did-become-active's check running first still lets activate share its window", async () => {
  // The deferred check can run before activate arrives; activate must then
  // join the window being created, not start a second one.
  const app = fakeApp({ registerAt: "load" });

  app.app.emit("did-become-active");
  app.runDeferred();
  app.app.emit("activate");
  await app.finishLoads();

  assert.equal(app.created.length, 1);
});

test("a gesture while a window is still loading neither shows it early nor opens another", async () => {
  // The startup window: created hidden, shown by itself once it has loaded.
  // Launching raises activation events while it loads, and the user may click
  // the dock or the tray before it appears.
  const app = fakeApp();
  const startup = app.surfacer.surface();
  const [window] = app.created;

  app.app.emit("activate");
  app.app.emit("did-become-active");
  app.runDeferred();
  const fromTray = app.surfacer.surface();

  assert.deepEqual(window.calls, [], "not shown half-loaded");
  await app.finishLoads();
  assert.equal(await startup, window);
  assert.equal(await fromTray, window, "the tray gets the window being created");
  assert.equal(app.created.length, 1);
});

test("the dock and the tray bring the window in use forward even when it is on screen", () => {
  // A dock click is an explicit "bring Reliant forward", same as the tray: it
  // does not stop at "something is already on screen".
  const open = fakeWindow("open", "open");
  const app = fakeApp({ windows: [open] });

  app.app.emit("activate");

  assert.deepEqual(open.calls, ["show", "focus"]);
  assert.equal(app.created.length, 0);
});

test("nothing is surfaced while the app is quitting or installing an update", async () => {
  const closed = fakeWindow("closed", "hidden");
  const app = fakeApp({ windows: [closed], quitting: true });

  app.app.emit("activate");
  app.app.emit("did-become-active");
  app.runDeferred();

  assert.equal(await app.surfacer.surface(), null);
  assert.equal(await app.surfacer.surfaceIfNothingOnScreen(), null);
  assert.deepEqual(closed.calls, []);
  assert.equal(closed.state, "hidden");

  const empty = fakeApp({ quitting: true });
  empty.app.emit("activate");
  empty.switchTo();
  assert.equal(empty.created.length, 0, "no window is opened during a quit");
});

test("a window that failed to open does not block the next activation", async () => {
  const app = fakeApp({ failCreates: 1 });

  app.app.emit("activate");
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(app.errors.length, 1);
  assert.equal(app.errors[0].source, "activate");
  assert.equal(app.created.length, 0);

  app.switchTo();
  await app.finishLoads();

  assert.equal(app.created.length, 1, "the next activation tries again");
});

test("main.js listens for activation before startup's first await", () => {
  // The other half of the bug. `activate` used to be registered at the very
  // end of startup, after awaiting the first window's load and the backend
  // start. When either rejected, the listener was never added and the dock
  // did nothing for the rest of the session while the tray (registered
  // earlier) still worked.
  const src = fs.readFileSync(path.join(__dirname, "..", "src", "main.js"), "utf8");
  const ready = src.indexOf("app.whenReady().then(async () => {");
  assert.notEqual(ready, -1, "startup handler not found");

  const register = src.indexOf("registerAppActivation(app,", ready);
  assert.notEqual(register, -1, "registerAppActivation(app, ...) must be called in startup");

  let offset = ready;
  let firstAwait = -1;
  for (const line of src.slice(ready).split("\n")) {
    if (offset > ready && !line.trim().startsWith("//") && /\bawait\b/.test(line)) {
      firstAwait = offset;
      break;
    }
    offset += line.length + 1;
  }
  assert.notEqual(firstAwait, -1, "startup has no await to order against");
  assert.ok(register < firstAwait, "activation listeners must be registered before startup awaits");

  assert.equal(
    src.match(/app\.on\(\s*["'](activate|did-become-active)["']/g),
    null,
    "activation is wired only through registerAppActivation",
  );
});
