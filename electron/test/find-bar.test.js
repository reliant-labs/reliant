// The desktop find bar (src/find-bar.js): one view per window, laid over it,
// searching the window's page on the bar page's behalf.
const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const {
  FIND_BAR_CHANNELS,
  FIND_BAR_SIZE,
  createFindBarController,
  findBarBounds,
  sanitizeFindRequest,
  sanitizeTheme,
} = require("../src/find-bar");

const ELECTRON_DIR = path.join(__dirname, "..");
const SRC = path.join(ELECTRON_DIR, "src");

// ── Fakes ──────────────────────────────────────────────────────────────────

/** A BrowserWindow: its page records searches; its content view, children. */
function fakeWindow({ width = 1400, height = 900 } = {}) {
  const page = new EventEmitter();
  Object.assign(page, {
    destroyed: false,
    finds: [],
    stops: [],
    focusCount: 0,
    lastRequestId: 0,
    isDestroyed() {
      return this.destroyed;
    },
    findInPage(text, options) {
      this.finds.push([text, options]);
      this.lastRequestId += 1;
      return this.lastRequestId;
    },
    stopFindInPage(action) {
      this.stops.push(action);
    },
    focus() {
      this.focusCount += 1;
    },
    /** What Electron emits for a search's progress. */
    report(result) {
      this.emit("found-in-page", {}, { finalUpdate: true, ...result });
    },
  });
  const window = new EventEmitter();
  Object.assign(window, {
    webContents: page,
    bounds: { x: 0, y: 0, width, height },
    contentView: {
      children: [],
      addChildView(view) {
        this.children = this.children.filter((child) => child !== view);
        this.children.push(view);
      },
    },
    getContentBounds() {
      return this.bounds;
    },
    isDestroyed() {
      return false;
    },
  });
  return window;
}

/** A WebContentsView whose page records what main sent it. */
function fakeView() {
  const contents = new EventEmitter();
  Object.assign(contents, {
    sent: [],
    loaded: [],
    focusCount: 0,
    closed: false,
    isDestroyed() {
      return this.closed;
    },
    send(channel, payload) {
      this.sent.push([channel, payload]);
    },
    loadURL(url) {
      this.loaded.push(url);
      return Promise.resolve();
    },
    focus() {
      this.focusCount += 1;
    },
    close() {
      this.closed = true;
    },
    setWindowOpenHandler(handler) {
      this.openHandler = handler;
    },
  });
  return {
    webContents: contents,
    bounds: null,
    visible: null,
    background: null,
    setBounds(bounds) {
      this.bounds = bounds;
    },
    setVisible(visible) {
      this.visible = visible;
    },
    setBackgroundColor(color) {
      this.background = color;
    },
  };
}

const URL = "app://bundle/find-bar.html";
const DARK = { dark: true, colorScheme: "pure-black" };

function setup(options) {
  const window = fakeWindow(options);
  const views = [];
  const bar = createFindBarController({
    window,
    createView: () => {
      const view = fakeView();
      views.push(view);
      return view;
    },
    url: URL,
    log: { error: () => {} },
  });
  return {
    window,
    page: window.webContents,
    bar,
    views,
    view: () => views[0],
    /** What main sent the bar page, by channel. */
    sent: (channel) =>
      views[0].webContents.sent.filter(([name]) => name === channel).map(([, payload]) => payload),
  };
}

const step = (text, forward) => ({ text, forward, findNext: false, matchCase: false });
const search = (text) => ({ text, forward: true, findNext: true, matchCase: false });

// ── Opening ────────────────────────────────────────────────────────────────

test("opening lays the bar over the window's top-right, shows it and gives it the keyboard", () => {
  const { window, bar, view } = setup({ width: 1400 });

  assert.equal(bar.open(DARK), true);

  assert.deepEqual(view().webContents.loaded, [URL]);
  assert.deepEqual(window.contentView.children, [view()]);
  assert.deepEqual(view().bounds, findBarBounds(window.getContentBounds()));
  assert.equal(view().bounds.x + view().bounds.width, 1400 - 12);
  assert.equal(view().visible, true);
  assert.equal(view().background, "#00000000", "transparent around the bar, for its shadow");
  assert.equal(view().webContents.focusCount, 1);
  assert.equal(bar.isOpen(), true);
});

test("the bar page hears it was opened only once it is listening", () => {
  const { bar, sent } = setup();

  bar.open(DARK);
  assert.deepEqual(sent(FIND_BAR_CHANNELS.opened), [], "the page has not said it is ready");

  bar.ready();
  assert.deepEqual(sent(FIND_BAR_CHANNELS.opened), [{ theme: DARK }]);

  bar.open({ dark: false, colorScheme: null });
  assert.deepEqual(sent(FIND_BAR_CHANNELS.opened).at(-1), {
    theme: { dark: false, colorScheme: null },
  });
});

test("one view per window, raised above the others each time it opens", () => {
  const { window, bar, views } = setup();
  bar.open(DARK);
  const other = { name: "embedded browser tab" };
  window.contentView.addChildView(other);

  bar.open(DARK);

  assert.equal(views.length, 1);
  assert.equal(window.contentView.children.at(-1), views[0]);
});

test("a reloaded bar page is not talked to until it is ready again", () => {
  const { page, bar, view, sent } = setup();
  bar.open(DARK);
  bar.ready();

  view().webContents.emit("did-start-loading");
  bar.find(search("alpha"));
  page.report({ requestId: 1, matches: 3, activeMatchOrdinal: 1 });
  assert.deepEqual(sent(FIND_BAR_CHANNELS.result), [], "sent to a page still loading, it is lost");

  bar.ready();
  assert.equal(sent(FIND_BAR_CHANNELS.opened).length, 2, "told again that it is open");
});

test("the bar page never navigates or opens a window", () => {
  const { bar, view } = setup();
  bar.open(DARK);

  let prevented = false;
  view().webContents.emit("will-navigate", { preventDefault: () => (prevented = true) }, "https://example.com");

  assert.equal(prevented, true);
  assert.deepEqual(view().webContents.openHandler(), { action: "deny" });
});

// ── Searching ──────────────────────────────────────────────────────────────

test("searches the window's page and reports back the latest search only", () => {
  const { page, bar, sent } = setup();
  bar.open(DARK);
  bar.ready();

  bar.find(search("al"));
  bar.find(search("alpha"));
  page.report({ requestId: 1, matches: 9, activeMatchOrdinal: 1 });
  page.report({ requestId: 2, matches: 3, activeMatchOrdinal: 1 });

  assert.deepEqual(page.finds, [
    ["al", { forward: true, findNext: true, matchCase: false }],
    ["alpha", { forward: true, findNext: true, matchCase: false }],
  ]);
  assert.deepEqual(sent(FIND_BAR_CHANNELS.result), [
    { matches: 3, activeMatchOrdinal: 1, finalUpdate: true },
  ]);
});

test("steps forward and back through the current search", () => {
  const { page, bar, sent } = setup();
  bar.open(DARK);
  bar.ready();
  bar.find(search("alpha"));

  bar.find(step("alpha", true));
  page.report({ requestId: 2, matches: 3, activeMatchOrdinal: 2 });
  bar.find(step("alpha", false));
  page.report({ requestId: 3, matches: 3, activeMatchOrdinal: 1 });

  assert.deepEqual(page.finds.slice(1), [
    ["alpha", { forward: true, findNext: false, matchCase: false }],
    ["alpha", { forward: false, findNext: false, matchCase: false }],
  ]);
  assert.deepEqual(
    sent(FIND_BAR_CHANNELS.result).map((result) => result.activeMatchOrdinal),
    [2, 1],
  );
});

test("an emptied query ends the search and clears its highlights", () => {
  const { page, bar, sent } = setup();
  bar.open(DARK);
  bar.ready();
  bar.find(search("alpha"));

  bar.find(search(""));
  page.report({ requestId: 1, matches: 3, activeMatchOrdinal: 1 });

  assert.deepEqual(page.stops, ["clearSelection"]);
  assert.deepEqual(sent(FIND_BAR_CHANNELS.result), [
    { matches: 0, activeMatchOrdinal: 0, finalUpdate: true },
  ]);
});

test("ignores malformed requests, and any while the bar is closed", () => {
  const { page, bar } = setup();
  bar.find(search("alpha"));
  bar.open(DARK);
  for (const junk of [null, "alpha", { text: 3 }, {}]) bar.find(junk);

  assert.deepEqual(page.finds, []);
});

// ── Closing ────────────────────────────────────────────────────────────────

test("closing hides the bar, clears the highlights and hands the keyboard back", () => {
  const { page, bar, view, sent } = setup();
  bar.open(DARK);
  bar.ready();
  bar.find(search("alpha"));

  bar.close();
  page.report({ requestId: 1, matches: 3, activeMatchOrdinal: 1 });

  assert.equal(bar.isOpen(), false);
  assert.equal(view().visible, false);
  assert.deepEqual(page.stops, ["clearSelection"]);
  assert.equal(page.focusCount, 1);
  assert.deepEqual(sent(FIND_BAR_CHANNELS.result), [], "a closed bar hears nothing");

  bar.close();
  assert.deepEqual(page.stops, ["clearSelection"], "closing twice is closing once");
});

test("a new document closes the bar", () => {
  const { page, bar, view } = setup();
  bar.open(DARK);

  page.emit("did-navigate");

  assert.equal(bar.isOpen(), false);
  assert.equal(view().visible, false);
});

test("follows the window's size", () => {
  const { window, bar, view } = setup({ width: 1400 });
  bar.open(DARK);

  window.bounds = { x: 0, y: 0, width: 900, height: 700 };
  window.emit("resize");

  assert.deepEqual(view().bounds, findBarBounds(window.bounds));
  assert.equal(view().bounds.x + view().bounds.width, 900 - 12);
});

test("a window narrower than the bar gets a bar no wider than itself", () => {
  const bounds = findBarBounds({ x: 0, y: 0, width: 300, height: 600 });
  assert.equal(bounds.x, 0);
  assert.equal(bounds.width, 300);
  assert.equal(bounds.height, FIND_BAR_SIZE.height);
});

test("destroying it lets go of the window and closes the bar's page", () => {
  const { window, page, bar, view } = setup();
  bar.open(DARK);

  bar.destroy();

  assert.equal(view().webContents.closed, true);
  assert.equal(page.listenerCount("found-in-page"), 0);
  assert.equal(page.listenerCount("did-navigate"), 0);
  assert.equal(window.listenerCount("resize"), 0);
  assert.equal(bar.open(DARK), false);
});

test("owns only its own page", () => {
  const { bar, view, page } = setup();
  assert.equal(bar.owns(page), false);
  bar.open(DARK);
  assert.equal(bar.owns(view().webContents), true);
  assert.equal(bar.owns(page), false);
  assert.equal(bar.owns(null), false);
});

// ── Input from renderers ───────────────────────────────────────────────────

test("a theme is forwarded only as a boolean and a scheme id", () => {
  assert.deepEqual(sanitizeTheme({ dark: true, colorScheme: "professional-blue" }), {
    dark: true,
    colorScheme: "professional-blue",
  });
  assert.deepEqual(sanitizeTheme({ dark: "yes", colorScheme: '"><script>' }), {
    dark: false,
    colorScheme: null,
  });
  assert.deepEqual(sanitizeTheme(undefined), { dark: false, colorScheme: null });
});

test("a search request keeps only the options findInPage takes", () => {
  assert.deepEqual(sanitizeFindRequest({ text: "a", extra: 1 }), {
    text: "a",
    forward: true,
    findNext: false,
    matchCase: false,
  });
  assert.deepEqual(sanitizeFindRequest({ text: "a", forward: false, findNext: true, matchCase: true }), {
    text: "a",
    forward: false,
    findNext: true,
    matchCase: true,
  });
  assert.equal(sanitizeFindRequest({ text: null }), null);
});

// ── The wiring ─────────────────────────────────────────────────────────────

/** Runs a sandboxed preload against a stand-in electron; returns what it exposed and sends. */
function loadPreload(file) {
  const listeners = new Map();
  const sent = [];
  const invoked = [];
  const exposed = {};
  const ipcRenderer = {
    on: (channel, fn) => {
      if (!listeners.has(channel)) listeners.set(channel, []);
      listeners.get(channel).push(fn);
    },
    removeListener: (channel, fn) => {
      const list = listeners.get(channel) || [];
      const index = list.indexOf(fn);
      if (index !== -1) list.splice(index, 1);
    },
    send: (channel, ...args) => sent.push([channel, ...args]),
    invoke: (channel, ...args) => {
      invoked.push([channel, ...args]);
      return Promise.resolve({ success: true });
    },
  };
  const context = {
    require: (name) => {
      if (name === "electron") {
        return { contextBridge: { exposeInMainWorld: (key, api) => (exposed[key] = api) }, ipcRenderer };
      }
      throw new Error(`the sandboxed preload cannot require ${name}`);
    },
    window: {
      addEventListener: () => {},
      postMessage: () => {},
      dispatchEvent: () => {},
      location: { origin: "app://bundle" },
    },
    process: { platform: "darwin" },
    console,
    setTimeout: () => {},
    Date,
  };
  vm.runInNewContext(fs.readFileSync(path.join(SRC, file), "utf8"), context, { filename: file });
  return {
    exposed,
    sent,
    invoked,
    receive: (channel, payload) => {
      for (const fn of (listeners.get(channel) || []).slice()) fn({}, payload);
    },
  };
}

test("the bar's preload speaks find-bar.js's channels, and nothing else", () => {
  const preload = loadPreload("find-bar-preload.js");
  const api = preload.exposed.findBarAPI;
  assert.deepEqual(Object.keys(preload.exposed), ["findBarAPI"]);
  assert.deepEqual(Object.keys(api).sort(), ["close", "find", "onOpened", "onResult", "ready"]);

  api.ready();
  api.find(search("alpha"));
  api.close();
  assert.deepEqual(preload.sent, [
    [FIND_BAR_CHANNELS.ready],
    [FIND_BAR_CHANNELS.find, search("alpha")],
    [FIND_BAR_CHANNELS.close],
  ]);

  const opened = [];
  const results = [];
  const offOpened = api.onOpened((event) => opened.push(event));
  api.onResult((result) => results.push(result));
  preload.receive(FIND_BAR_CHANNELS.opened, { theme: DARK });
  preload.receive(FIND_BAR_CHANNELS.result, { matches: 1 });
  offOpened();
  preload.receive(FIND_BAR_CHANNELS.opened, { theme: DARK });

  assert.deepEqual(opened, [{ theme: DARK }]);
  assert.deepEqual(results, [{ matches: 1 }]);
});

test("the window's preload opens the bar on find-bar.js's channel", async () => {
  const preload = loadPreload("preload.js");
  const request = { theme: DARK };

  await preload.exposed.electronAPI.findInPage.open(request);

  assert.deepEqual(
    preload.invoked.filter(([channel]) => channel.startsWith("find-bar:")),
    [[FIND_BAR_CHANNELS.open, request]],
  );
});

test("main opens a bar only for a window's own page, and routes the bar's messages by sender", () => {
  const src = fs.readFileSync(path.join(SRC, "main.js"), "utf8");
  for (const name of Object.keys(FIND_BAR_CHANNELS).filter((name) => !["opened", "result"].includes(name))) {
    assert.match(src, new RegExp(`FIND_BAR_CHANNELS\\.${name}\\b`), `main.js must handle ${name}`);
  }
  // Not an embedded browser tab, nor the bar itself.
  assert.match(src, /window\.webContents !== event\.sender/);
  assert.match(src, /bar\.owns\(event\.sender\)/);
  assert.match(src, /getFindBarWebPreferences\(\)/);
});

test("the bar's view is sandboxed, with its own preload and no webview tag", () => {
  // Outside Electron, require("electron") is a path string; window-config needs `app`.
  require.cache[require.resolve("electron")] = {
    id: require.resolve("electron"),
    filename: require.resolve("electron"),
    loaded: true,
    exports: { app: { isPackaged: false } },
  };
  const windowConfig = require("../src/window-config");
  const preferences = windowConfig.getFindBarWebPreferences();

  assert.equal(preferences.sandbox, true);
  assert.equal(preferences.contextIsolation, true);
  assert.equal(preferences.nodeIntegration, false);
  assert.equal(preferences.webviewTag, undefined);
  assert.equal(path.basename(preferences.preload), "find-bar-preload.js");
  assert.ok(fs.existsSync(preferences.preload));
});

test("the packaged app ships the bar's preload beside the window's", () => {
  const common = require("../electron-builder.common.js");
  const resources = common.extraResources.map(({ from, to }) => [from, to]);
  assert.deepEqual(
    resources.find(([from]) => from === "src/find-bar-preload.js"),
    ["src/find-bar-preload.js", "find-bar-preload.js"],
  );
});
