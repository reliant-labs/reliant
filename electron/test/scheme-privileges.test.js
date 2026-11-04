const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const { APP_SCHEME, registerSchemePrivileges } = require('../src/app-protocol');

const MAIN_SRC = fs.readFileSync(path.join(__dirname, '..', 'src', 'main.js'), 'utf8');

/**
 * Capture the privilege list handed to protocol.registerSchemesAsPrivileged.
 */
function captureRegistration() {
  const calls = [];
  return {
    calls,
    protocol: {
      registerSchemesAsPrivileged(schemes) {
        calls.push(schemes);
      },
    },
  };
}

test('app:// is declared secure, so crypto.subtle exists in the renderer', () => {
  // PKCE sign-in (frontends/web/src/lib/pkce.ts) needs SHA-256, and crypto.subtle is
  // exposed ONLY in a secure context. A custom scheme is not trustworthy by
  // default: without `secure` the renderer gets window.isSecureContext === false
  // and Claude/Codex sign-in fails in the S256 step with InsecureContextError.
  const { calls, protocol } = captureRegistration();
  registerSchemePrivileges(protocol);

  assert.strictEqual(calls.length, 1, 'must register exactly once');
  const entry = calls[0].find((s) => s.scheme === APP_SCHEME);
  assert.ok(entry, `${APP_SCHEME} must be registered`);
  assert.strictEqual(entry.privileges.secure, true, 'app:// must be a secure context');
  // `standard` is what makes it a real origin (localStorage / IndexedDB).
  assert.strictEqual(entry.privileges.standard, true, 'app:// must be a standard origin');
});

test('the scheme privileges are registered exactly once, and nothing else registers schemes', () => {
  // Electron does not merge the per-privilege scheme lists across separate
  // registerSchemesAsPrivileged calls: the last call wins for every privilege
  // it names. That is how @sentry/electron's own `sentry-ipc` registration once
  // dropped app:// from the secure list (shipped 1.7.8: crypto.subtle gone,
  // PKCE sign-in broken). So the process makes exactly one such call, ours.
  const countOf = (needle) => MAIN_SRC.split(needle).length - 1;
  assert.strictEqual(
    countOf('\nregisterSchemePrivileges(protocol);'),
    1,
    'exactly one registerSchemePrivileges(protocol) call'
  );
  const srcDir = path.join(__dirname, '..', 'src');
  for (const file of fs.readdirSync(srcDir).filter((f) => f.endsWith('.js'))) {
    if (file === 'app-protocol.js') continue;
    const code = fs
      .readFileSync(path.join(srcDir, file), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/^\s*\/\/.*$/gm, '');
    assert.ok(
      !/registerSchemesAsPrivileged/.test(code),
      `${file} calls registerSchemesAsPrivileged; app-protocol.js's call must be the only one`
    );
  }
});

test('both privileged registrations happen before app ready', () => {
  // registerSchemesAsPrivileged is ignored once the app is ready.
  const schemeInit = MAIN_SRC.indexOf('\nregisterSchemePrivileges(protocol);');
  const appReady = MAIN_SRC.indexOf('app.whenReady().then(async () =>');

  assert.ok(appReady !== -1, 'expected the main app.whenReady() handler');
  assert.ok(
    schemeInit < appReady,
    'scheme privileges must be declared before app.whenReady()'
  );
});
