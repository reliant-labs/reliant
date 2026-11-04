const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const {
  MENU_SHORTCUT_CHANNEL,
  createMenuShortcutDelivery,
  menuShortcutItems,
} = require("../src/menu-shortcuts");
const {
  MENU_SHORTCUT_IDS,
  defaultAccelerators,
  resolveMenuAccelerators,
} = require("../src/menu-accelerators");
const { createWindowSurfacer } = require("../src/window-surfacing");

const SRC = path.join(__dirname, "..", "src");

// The per-shortcut channels "menu-shortcut" replaced. Nothing may send or
// listen on them any more.
const REMOVED_CHANNELS = [
  "create-new-tab",
  "close-current-tab",
  "reopen-last-tab",
  "toggle-terminal",
  "new-terminal",
  "navigate-next-chat",
  "navigate-prev-chat",
  "navigate-next-sidebar",
  "navigate-prev-sidebar",
  "clear-terminal",
];

const CONTEXT_RESOLVED = [
  "nextChat",
  "prevChat",
  "nextRightSidebarTab",
  "prevRightSidebarTab",
  "clearTerminal",
  "findInPage",
];

// Shortcuts that act on what the user is looking at, so do nothing when no
// Reliant window has focus.
const NEEDS_FOCUSED_WINDOW = ["closeTab", "clearTerminal", "findInPage"];

// A BrowserWindow stand-in. open = on screen, minimized = in the Dock,
// hidden = closed to the tray. Its webContents records what it was sent.
function fakeWindow(name, state, { loading = false } = {}) {
  const contents = new EventEmitter();
  contents.loading = loading;
  contents.sent = [];
  contents.isLoading = () => contents.loading;
  contents.send = (channel, payload) => contents.sent.push([channel, payload]);
  const window = {
    name,
    state,
    calls: [],
    webContents: contents,
    isDestroyed: () => window.state === "destroyed",
    isVisible: () => window.state === "open",
    isMinimized: () => window.state === "minimized",
    restore: () => {
      window.calls.push("restore");
      window.state = "open";
    },
    show: () => {
      window.calls.push("show");
      if (window.state === "hidden") window.state = "open";
    },
    focus: () => window.calls.push("focus"),
  };
  return window;
}

// The menu as main.js wires it: items from menuShortcutItems, delivered through
// the app's one surfacer. createWindow resolves once the new window's page has
// loaded, like main.js's; finishLoads() completes pending ones.
function fakeMenu({ windows = [], focused = false, accelerators = defaultAccelerators() } = {}) {
  const harness = { windows, created: [], loads: [], deliveries: [] };
  harness.surfacer = createWindowSurfacer({
    getWindows: () => harness.windows,
    createWindow: () => {
      const window = fakeWindow(`new-${harness.created.length + 1}`, "hidden");
      harness.created.push(window);
      harness.windows = [window, ...harness.windows];
      return new Promise((resolve) => {
        harness.loads.push(() => {
          window.state = "open";
          resolve(window);
        });
      });
    },
    settle: (fn) => fn(),
  });
  const deliver = createMenuShortcutDelivery({
    send: (channel, payload) => {
      harness.deliveries.push(harness.surfacer.surfaceAndSend(channel, payload));
    },
    hasFocusedWindow: () => focused,
  });
  harness.items = menuShortcutItems({ accelerators, deliver });
  harness.settled = async () => {
    while (harness.loads.length) harness.loads.shift()();
    await Promise.all(harness.deliveries);
  };
  return harness;
}

test("every shortcut menu item delivers its id on the menu-shortcut channel", () => {
  const sent = [];
  const deliver = createMenuShortcutDelivery({
    send: (channel, payload) => sent.push([channel, payload]),
    hasFocusedWindow: () => true,
  });
  const items = menuShortcutItems({ accelerators: defaultAccelerators(), deliver });

  assert.equal(MENU_SHORTCUT_CHANNEL, "menu-shortcut");
  assert.deepEqual(Object.keys(items).sort(), [...MENU_SHORTCUT_IDS].sort());
  for (const id of MENU_SHORTCUT_IDS) {
    sent.length = 0;
    items[id].click();
    assert.deepEqual(sent, [["menu-shortcut", id]], `${items[id].label} must deliver ${id}`);
  }
});

test("menu items carry the resolved accelerators, and none for context-resolved chords", () => {
  const remapped = resolveMenuAccelerators({
    newChat: "Cmd+Alt+N",
    nextChat: "Shift+Cmd+Down",
    selectionDownComposer: "Cmd+Shift+Down",
    selectionUpMonaco: "Cmd+Shift+Up",
    selectionLeftMonaco: "Cmd+Shift+Left",
    selectionRightMonaco: "Cmd+Shift+Right",
    connectSelectedSteps: "Shift+Cmd+L",
    chatSearch: "Cmd+F",
  });

  for (const accelerators of [defaultAccelerators(), remapped]) {
    const items = menuShortcutItems({ accelerators, deliver: () => {} });
    for (const id of MENU_SHORTCUT_IDS) {
      assert.equal(items[id].accelerator, accelerators[id]);
    }
    for (const id of CONTEXT_RESOLVED) {
      assert.equal(items[id].accelerator, undefined, `${items[id].label} must not claim its chord`);
    }
  }
  assert.equal(menuShortcutItems({ accelerators: remapped, deliver: () => {} }).newChat.accelerator, "CmdOrCtrl+Alt+N");
});

test("File › New Tab with the window closed to the tray brings it back instead of opening a second", async () => {
  // The reported bug: with no focused window, New Tab called
  // createWindow({ createInitialTab: true }) even though a window sat hidden.
  const closed = fakeWindow("closed", "hidden");
  const menu = fakeMenu({ windows: [closed] });

  menu.items.newChat.click();
  await menu.settled();

  assert.equal(menu.created.length, 0, "no second window");
  assert.equal(closed.state, "open");
  assert.deepEqual(closed.calls, ["show", "focus"]);
  assert.deepEqual(closed.webContents.sent, [["menu-shortcut", "newChat"]]);
});

test("File › New Tab with the window minimized restores it", async () => {
  const minimized = fakeWindow("minimized", "minimized");
  const menu = fakeMenu({ windows: [minimized] });

  menu.items.newChat.click();
  await menu.settled();

  assert.equal(menu.created.length, 0);
  assert.deepEqual(minimized.calls, ["restore", "show", "focus"]);
  assert.deepEqual(minimized.webContents.sent, [["menu-shortcut", "newChat"]]);
});

test("File › New Tab with no window at all opens exactly one and delivers to it", async () => {
  const menu = fakeMenu();

  menu.items.newChat.click();
  menu.items.newChat.click();
  await menu.settled();

  assert.equal(menu.created.length, 1, "both clicks share the window being created");
  assert.deepEqual(menu.created[0].webContents.sent, [
    ["menu-shortcut", "newChat"],
    ["menu-shortcut", "newChat"],
  ]);
});

test("File › New Tab in a focused window delivers to it without opening another", async () => {
  const open = fakeWindow("open", "open");
  const other = fakeWindow("other", "open");
  const menu = fakeMenu({ windows: [open, other], focused: true });

  menu.items.newChat.click();
  await menu.settled();

  assert.equal(menu.created.length, 0);
  assert.deepEqual(open.webContents.sent, [["menu-shortcut", "newChat"]]);
  assert.deepEqual(other.webContents.sent, [], "only the window the user is in");
});

test("every shortcut but Close Tab and Clear Terminal brings a window back when none is in front", async () => {
  for (const id of MENU_SHORTCUT_IDS.filter((id) => !NEEDS_FOCUSED_WINDOW.includes(id))) {
    const closed = fakeWindow("closed", "hidden");
    const menu = fakeMenu({ windows: [closed] });

    menu.items[id].click();
    await menu.settled();

    assert.equal(menu.created.length, 0, id);
    assert.equal(closed.state, "open", id);
    assert.deepEqual(closed.webContents.sent, [["menu-shortcut", id]], id);
  }
});

for (const id of NEEDS_FOCUSED_WINDOW) {
  test(`${id} with no focused window does nothing rather than resurrect one to act on`, async () => {
    // Closing a tab or clearing a terminal acts on what the user is looking
    // at. With no Reliant window focused there is nothing on screen to act on.
    const closed = fakeWindow("closed", "hidden");
    const menu = fakeMenu({ windows: [closed], focused: false });

    menu.items[id].click();
    await menu.settled();

    assert.equal(closed.state, "hidden");
    assert.deepEqual(closed.calls, []);
    assert.deepEqual(closed.webContents.sent, []);
    assert.equal(menu.created.length, 0);

    const open = fakeWindow("open", "open");
    const focusedMenu = fakeMenu({ windows: [open], focused: true });
    focusedMenu.items[id].click();
    await focusedMenu.settled();
    assert.deepEqual(open.webContents.sent, [["menu-shortcut", id]]);
  });
}

test("main.js puts every shortcut on the menu through menuShortcutItems", () => {
  const src = fs.readFileSync(path.join(SRC, "main.js"), "utf8");

  for (const id of MENU_SHORTCUT_IDS) {
    assert.match(src, new RegExp(`\\bshortcut\\.${id}\\b`), `${id} must be placed on the menu`);
  }
  assert.match(
    src,
    /label: "New Chat",[\s\S]{0,120}deliverMenuShortcut\("newChat"\)/,
    "the tray's New Chat delivers the same shortcut",
  );
  assert.doesNotMatch(src, /createInitialTab/, "a new window no longer carries a pending tab");
  assert.doesNotMatch(src, /createWindow\(\s*\{/, "createWindow takes no options");
});

test("no per-shortcut channel survives in main or preload", () => {
  for (const file of ["main.js", "preload.js"]) {
    const src = fs.readFileSync(path.join(SRC, file), "utf8");
    for (const channel of REMOVED_CHANNELS) {
      assert.ok(!src.includes(`"${channel}"`) && !src.includes(`'${channel}'`), `${file} still uses ${channel}`);
    }
  }
});

// Runs src/preload.js against a stand-in electron and returns the API it
// exposes, a way to deliver an IPC message, and its pending timers. The preload
// is sandboxed and shipped as a single file, so it may require only "electron".
function loadPreload({ now = () => 0 } = {}) {
  const listeners = new Map();
  const timers = [];
  const exposed = {};
  const ipcRenderer = {
    on: (channel, fn) => {
      if (!listeners.has(channel)) listeners.set(channel, []);
      listeners.get(channel).push(fn);
    },
    once: (channel, fn) => ipcRenderer.on(channel, fn),
    removeListener: (channel, fn) => {
      const list = listeners.get(channel) || [];
      const index = list.indexOf(fn);
      if (index !== -1) list.splice(index, 1);
    },
    send: () => {},
    invoke: () => Promise.resolve(undefined),
  };
  const electron = {
    contextBridge: { exposeInMainWorld: (key, api) => (exposed[key] = api) },
    ipcRenderer,
  };
  const context = {
    require: (name) => {
      if (name === "electron") return electron;
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
    setTimeout: (fn) => timers.push(fn),
    Date: { now: () => now() },
  };
  vm.runInNewContext(fs.readFileSync(path.join(SRC, "preload.js"), "utf8"), context, {
    filename: "preload.js",
  });
  return {
    api: exposed.electronAPI,
    receive: (channel, ...args) => {
      for (const fn of (listeners.get(channel) || []).slice()) fn({}, ...args);
    },
    runTimers: () => {
      while (timers.length) timers.shift()();
    },
  };
}

test("preload exposes onMenuShortcut and none of the per-shortcut subscribers", () => {
  const { api } = loadPreload();

  assert.equal(typeof api.onMenuShortcut, "function");
  for (const removed of [
    "onCreateNewTab",
    "onCloseCurrentTab",
    "onReopenLastTab",
    "onToggleTerminal",
    "onNewTerminal",
    "onClearTerminal",
    "createNewTab",
    "closeCurrentTab",
  ]) {
    assert.equal(api[removed], undefined, `${removed} must be gone`);
  }
  // Menu items that are not shortcuts keep their own channels.
  for (const kept of ["onResumeLastChat", "onNavigateTab", "onCloseAllTabs"]) {
    assert.equal(typeof api[kept], "function", `${kept} must remain`);
  }
});

test("onMenuShortcut receives each id until unsubscribed", () => {
  const preload = loadPreload();
  const received = [];

  const unsubscribe = preload.api.onMenuShortcut((id) => received.push(id));
  preload.receive("menu-shortcut", "newChat");
  preload.receive("menu-shortcut", "nextChat");
  preload.receive("menu-shortcut", 42);
  unsubscribe();
  preload.receive("menu-shortcut", "closeTab");
  preload.runTimers();

  assert.deepEqual(received, ["newChat", "nextChat"]);
});

test("a shortcut sent before the renderer subscribes reaches its first subscriber once", () => {
  // The window opened for New Tab has loaded its page, but React has not yet
  // mounted the hook that listens. That delivery used to be lost.
  const preload = loadPreload();
  preload.receive("menu-shortcut", "newChat");
  preload.receive("menu-shortcut", "toggleTerminal");

  // React StrictMode: subscribe, tear down, subscribe again.
  const first = [];
  const second = [];
  const unsubscribeFirst = preload.api.onMenuShortcut((id) => first.push(id));
  unsubscribeFirst();
  preload.api.onMenuShortcut((id) => second.push(id));
  preload.receive("menu-shortcut", "nextChat");
  preload.runTimers();

  assert.deepEqual(first, []);
  assert.deepEqual(second, ["newChat", "toggleTerminal", "nextChat"], "in the order they were invoked");

  preload.runTimers();
  assert.deepEqual(second, ["newChat", "toggleTerminal", "nextChat"], "held ids are handed over once");
});

test("a held shortcut expires instead of firing long after it was asked for", () => {
  let clock = 0;
  const preload = loadPreload({ now: () => clock });
  preload.receive("menu-shortcut", "newChat");

  clock = 60_000;
  const received = [];
  preload.api.onMenuShortcut((id) => received.push(id));
  preload.runTimers();

  assert.deepEqual(received, []);
});
