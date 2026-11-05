// The preload exposes the desktop app's versions as window.forgeDesktop, which
// the web runtime reads once at startup and reports as app.desktop.* on every
// span (forge web-runtime client-context.ts). Only version-shaped values pass.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const SRC = path.join(__dirname, "..", "src");

// Runs src/preload.js against a stand-in electron and the given process, and
// returns what it exposed.
function exposedWith(proc) {
  const exposed = {};
  const ipcRenderer = {
    on: () => {},
    once: () => {},
    removeListener: () => {},
    send: () => {},
    invoke: () => Promise.resolve(undefined),
  };
  const context = {
    require: (name) => {
      if (name === "electron") {
        return { contextBridge: { exposeInMainWorld: (key, api) => (exposed[key] = api) }, ipcRenderer };
      }
      throw new Error(`the sandboxed preload cannot require ${name}`);
    },
    window: { addEventListener: () => {}, postMessage: () => {}, dispatchEvent: () => {}, location: { origin: "app://bundle" } },
    process: proc,
    console: { log() {}, info() {}, warn() {}, error() {}, debug() {} },
    setTimeout: () => {},
    Date,
  };
  vm.runInNewContext(fs.readFileSync(path.join(SRC, "preload.js"), "utf8"), context, { filename: "preload.js" });
  return exposed;
}

test("exposes the app, Electron and Chromium versions", () => {
  const { forgeDesktop } = exposedWith({
    platform: "darwin",
    argv: ["/Applications/Reliant.app/Contents/MacOS/Reliant", "--type=renderer", "--reliant-app-version=1.8.16-dev+7d787d80"],
    versions: { electron: "41.10.7", chrome: "146.0.7680.216", node: "24.1.0" },
  });
  assert.deepStrictEqual({ ...forgeDesktop }, {
    appVersion: "1.8.16-dev+7d787d80",
    electronVersion: "41.10.7",
    chromiumVersion: "146.0.7680.216",
  });
  assert.ok(Object.isFrozen(forgeDesktop), "the page cannot rewrite what it reports");
});

test("leaves out anything that is not a version, and survives a bare process", () => {
  const { forgeDesktop } = exposedWith({
    platform: "linux",
    argv: ["--reliant-app-version=1.8.16\n<script>"],
    versions: { electron: 41, chrome: "" },
  });
  assert.deepStrictEqual({ ...forgeDesktop }, { appVersion: undefined, electronVersion: undefined, chromiumVersion: undefined });
  assert.doesNotThrow(() => exposedWith({ platform: "darwin" }));
});
