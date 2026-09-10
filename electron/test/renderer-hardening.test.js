// Pins the renderer's security posture in the PACKAGED app.
//
// Everything here is a setting whose wrong value produces a working app. That
// is the whole reason these are tests: nothing at runtime complains, no build
// goes red, and the regression is only visible by reading a config file that
// nobody reads. Both assertions below correspond to a real drift that already
// happened once.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const ELECTRON_DIR = path.join(__dirname, '..');
const common = require(path.join(ELECTRON_DIR, 'electron-builder.common.js'));

// Outside a real Electron process `require('electron')` resolves to a STRING
// (the path to the binary), so window-config's `const { app } = require(...)`
// yields undefined and getPreloadPath() throws on `app.isPackaged`. Seed the
// require cache with the shape the module needs, before requiring it.
require.cache[require.resolve('electron')] = {
  id: require.resolve('electron'),
  filename: require.resolve('electron'),
  loaded: true,
  exports: { app: { isPackaged: true } },
};

// The packaged branch of getPreloadPath() also reads process.resourcesPath,
// which only exists inside Electron. The value is irrelevant here — these
// assertions are about the security flags, not the preload location.
process.resourcesPath = process.resourcesPath || '/tmp/resources';

const windowConfig = require(path.join(ELECTRON_DIR, 'src', 'window-config.js'));

test('file:// gets no extra privileges — the renderer is served over app://', () => {
  // v1.6.3 and earlier loaded the renderer with loadFile(), so this fuse was
  // genuinely required. v1.7.0 moved to app:// (src/app-protocol.js) because
  // the Vite bundle's root-absolute asset paths resolve against the FILESYSTEM
  // root under file://, which opened a blank window.
  //
  // The fuse stayed `true` for a scheme the app no longer loads, still
  // carrying the comment "required for loadFile to work". It widened what a
  // compromised renderer could read from disk in exchange for nothing.
  assert.strictEqual(
    common.electronFuses.grantFileProtocolExtraPrivileges,
    false,
    'grantFileProtocolExtraPrivileges must stay false: nothing loads from file://. ' +
      'If a loadFile() call is ever reintroduced, fix the loader rather than reopening this.'
  );
});

test('no packaged code path loads the renderer from file://', () => {
  // The guard for the assertion above. If someone reintroduces loadFile(),
  // flipping the fuse back would look like the correct fix; this fails first
  // and says which change actually needs reverting.
  const srcDir = path.join(ELECTRON_DIR, 'src');
  const offenders = [];

  for (const entry of fs.readdirSync(srcDir)) {
    if (!entry.endsWith('.js')) continue;
    const body = fs.readFileSync(path.join(srcDir, entry), 'utf8');
    // Strip block comments: app-protocol.js and main.js both DISCUSS the
    // historical loadFile() at length, and the prose is not a call site.
    const code = body.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
    if (/\.loadFile\s*\(/.test(code) || /loadURL\s*\(\s*['"`]file:/.test(code)) {
      offenders.push(entry);
    }
  }

  assert.deepStrictEqual(
    offenders,
    [],
    `these files load the renderer from file://: ${offenders.join(', ')}. ` +
      'The packaged renderer must load over app:// (see src/app-protocol.js).'
  );
});

test('the renderer keeps node out and context isolation on', () => {
  // Standard Electron hardening. Asserted because webviewTag is enabled for
  // the embedded browser, which makes the renderer a richer target than a
  // plain SPA — these three are what keep a compromise inside the sandbox.
  const prefs = windowConfig.getWebPreferences();

  assert.strictEqual(prefs.nodeIntegration, false, 'nodeIntegration must stay off');
  assert.strictEqual(prefs.contextIsolation, true, 'contextIsolation must stay on');
  assert.strictEqual(prefs.enableRemoteModule, false, 'enableRemoteModule must stay off');
});
