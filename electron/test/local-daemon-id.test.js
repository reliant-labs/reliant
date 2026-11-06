const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');
const daemonCreds = require('../src/daemon-creds');

const SRC = path.join(__dirname, '..', 'src');

// Load the real manager without Electron, logging or environment-file side
// effects. Construct only the state its instance resolution actually consumes.
function managerFor(t) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'local-daemon-id-'));
  t.after(() => fs.rmSync(home, { recursive: true, force: true }));
  const filename = path.join(SRC, 'backend-manager.js');
  const realRequire = createRequire(filename);
  const context = {
    module: { exports: {} },
    require(name) {
      if (name === 'electron') return {};
      if (name === './logger' || name === 'dotenv') return {};
      if (name === './daemon-creds') return {
        ...daemonCreds,
        instanceDataDir: (key) => daemonCreds.instanceDataDir(key, { homeDir: home }),
      };
      return realRequire(name);
    },
  };
  vm.runInNewContext(fs.readFileSync(filename, 'utf8'), context, { filename });
  const manager = Object.create(context.module.exports.prototype);
  manager.apiUrl = 'https://one.example';
  manager._instanceWorkspace = '/workspace/one';
  let sub = 'account-one';
  manager.authStorage = { loadStoredAuth: () => ({ user: { id: sub } }) };
  return {
    manager,
    setAccount: (next) => { sub = next; },
    write(id) {
      const dir = manager.daemonDataDir();
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(path.join(dir, 'daemon-id'), id);
    },
  };
}

test('reads only the current instance daemon-id, freshly, with no credentials', (t) => {
  const { manager, write } = managerFor(t);
  write('  local-id\n');
  // Neither runtime state nor the credential store is an identity source.
  fs.writeFileSync(path.join(manager.daemonDataDir(), 'daemon.json'), '{"pat":"secret","daemon_id":"remote"}');
  manager.readDaemonState = () => { throw new Error('must not read runtime state'); };
  assert.equal(manager.getLocalDaemonId(), 'local-id');
  write('replacement-id\n');
  assert.equal(manager.getLocalDaemonId(), 'replacement-id');
  fs.unlinkSync(path.join(manager.daemonDataDir(), 'daemon-id'));
  assert.equal(manager.getLocalDaemonId(), null);
});

test('missing, empty, unreadable and unresolved identities safely return null', (t) => {
  const { manager, write } = managerFor(t);
  assert.equal(manager.getLocalDaemonId(), null);
  write(' \n\t');
  assert.equal(manager.getLocalDaemonId(), null);
  const file = path.join(manager.daemonDataDir(), 'daemon-id');
  fs.unlinkSync(file);
  fs.mkdirSync(file); // A deterministic read error, even when tests run as root.
  assert.equal(manager.getLocalDaemonId(), null);
  manager.apiUrl = 'not a server URL';
  assert.equal(manager.getLocalDaemonId(), null);
  manager.authStorage.loadStoredAuth = () => { throw new Error('store unavailable'); };
  assert.equal(manager.getLocalDaemonId(), null);
});

test('account, server and workspace changes never reuse another instance identity', (t) => {
  const { manager, write, setAccount } = managerFor(t);
  write('account-one-id');
  setAccount('account-two');
  assert.equal(manager.getLocalDaemonId(), null);
  write('account-two-id');
  assert.equal(manager.getLocalDaemonId(), 'account-two-id');
  setAccount(''); // Signed out: must not fall back to the previous account.
  assert.equal(manager.getLocalDaemonId(), null);
  setAccount('account-one');
  assert.equal(manager.getLocalDaemonId(), 'account-one-id');
  manager.apiUrl = 'https://two.example';
  assert.equal(manager.getLocalDaemonId(), null);
  write('other-server-id');
  assert.equal(manager.getLocalDaemonId(), 'other-server-id');
  manager._instanceWorkspace = '/workspace/two';
  assert.equal(manager.getLocalDaemonId(), null);
});

// Execute the actual IPC registration, not a duplicate implementation. The
// main module boots the entire app; isolate just this synchronous handler.
function ipcHarness({ packaged = true, manager = { getLocalDaemonId: () => 'local-id' } } = {}) {
  const handlers = new Map();
  let window;
  const main = fs.readFileSync(path.join(SRC, 'main.js'), 'utf8');
  const start = main.indexOf('ipcMain.handle("daemon:get-local-id",');
  assert.notEqual(start, -1, 'local identity IPC must be registered');
  const end = main.indexOf('\nipcMain.handle(', start + 1);
  vm.runInNewContext(main.slice(start, end), {
    ipcMain: { handle: (channel, fn) => handlers.set(channel, fn) },
    BrowserWindow: { fromWebContents: () => window },
    app: { isPackaged: packaged }, APP_ORIGIN: 'app://bundle',
    DEV_URL: 'http://localhost:5173', URL, backendManager: manager,
  });
  return (url, { subframe = false, guest = false, noWindow = false, destroyed = false } = {}) => {
    const frame = { url };
    const sender = { mainFrame: frame };
    window = noWindow ? null : { isDestroyed: () => destroyed, webContents: guest ? {} : sender };
    return handlers.get('daemon:get-local-id')({ sender, senderFrame: subframe ? { url } : frame });
  };
}

test('identity IPC accepts the app main frame only, including the configured dev origin', () => {
  const invoke = ipcHarness();
  assert.equal(invoke('app://bundle/projects'), 'local-id');
  for (const url of ['https://evil.example', 'app://other', 'file:///tmp/page', 'invalid']) {
    assert.equal(invoke(url), null, url);
  }
  for (const option of ['subframe', 'guest', 'noWindow', 'destroyed']) {
    assert.equal(invoke('app://bundle', { [option]: true }), null, option);
  }
  const dev = ipcHarness({ packaged: false });
  assert.equal(dev('http://localhost:5173/projects'), 'local-id');
  assert.equal(dev('http://localhost:5174/projects'), null);
  assert.equal(dev('app://bundle'), null);
  assert.equal(ipcHarness({ manager: null })('app://bundle'), null);
  assert.equal(ipcHarness({ manager: { getLocalDaemonId() { throw new Error('unavailable'); } } })('app://bundle'), null);
});

test('the sandboxed preload exposes a no-argument identity bridge without caching', async () => {
  const exposed = {};
  const calls = [];
  let id = 'local-id';
  vm.runInNewContext(fs.readFileSync(path.join(SRC, 'preload.js'), 'utf8'), {
    require(name) {
      assert.equal(name, 'electron');
      return {
        contextBridge: { exposeInMainWorld: (key, value) => { exposed[key] = value; } },
        ipcRenderer: {
          on() {}, once() {}, send() {},
          invoke: async (...args) => { calls.push(args); return id; },
        },
      };
    },
    process: { platform: 'linux', argv: [], versions: {} },
    window: { addEventListener() {}, location: { origin: 'app://bundle' } },
    console: { log() {}, info() {}, warn() {}, error() {}, debug() {} },
  });
  calls.length = 0; // Ignore preload startup probes unrelated to this bridge.
  assert.equal(await exposed.electronAPI.getLocalDaemonId('ignored-path'), 'local-id');
  id = null;
  assert.equal(await exposed.electronAPI.getLocalDaemonId(), null);
  assert.deepEqual(calls, [['daemon:get-local-id'], ['daemon:get-local-id']]);
});
