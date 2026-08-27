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
  // PKCE sign-in (web/src/lib/pkce.ts) needs SHA-256, and crypto.subtle is
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

test('Sentry initializes BEFORE scheme privileges are registered', () => {
  // THE REGRESSION THIS FILE EXISTS FOR — an ordering bug, not a flag bug.
  //
  // Both this app and @sentry/electron register privileged schemes, and
  // Electron does not merge the per-privilege scheme lists across calls. Sentry
  // guards against that by registering `sentry-ipc` during init and then
  // PROXYING protocol.registerSchemesAsPrivileged so every LATER call has its
  // scheme appended (node_modules/@sentry/electron/main/ipc.js). A call made
  // BEFORE init never passes through that proxy, and Sentry's own later
  // registration then wins.
  //
  // Observed in the shipped 1.7.8 renderer while registration ran first:
  //   --standard-schemes=app          (survived: Sentry declares no `standard`)
  //   --streaming-schemes=app         (survived: Sentry declares no `stream`)
  //   --secure-schemes=sentry-ipc     (CLOBBERED: `app` is gone)
  //   --cors-schemes=sentry-ipc       (CLOBBERED)
  //   --fetch-schemes=sentry-ipc      (CLOBBERED)
  //
  // app:// therefore loaded as a non-secure context and sign-in broke. Ordering
  // Sentry first puts our registration through the proxy, so both survive.
  const sentryInit = MAIN_SRC.indexOf('\ninitializeSentry();');
  const schemeInit = MAIN_SRC.indexOf('\nregisterSchemePrivileges(protocol);');

  assert.ok(sentryInit !== -1, 'initializeSentry() must be called at top level');
  assert.ok(schemeInit !== -1, 'registerSchemePrivileges(protocol) must be called at top level');
  assert.ok(
    sentryInit < schemeInit,
    'initializeSentry() must run BEFORE registerSchemePrivileges(protocol), or ' +
      "Sentry's later scheme registration drops app:// from the secure list and " +
      'crypto.subtle disappears from the renderer'
  );
});

test('Sentry and the scheme privileges are each initialized exactly once', () => {
  // A second initializeSentry() would install a second proxy layer, and a
  // second registerSchemePrivileges() would re-run the clobber-prone path.
  const countOf = (needle) => MAIN_SRC.split(needle).length - 1;

  assert.strictEqual(countOf('\ninitializeSentry();'), 1, 'exactly one initializeSentry() call');
  assert.strictEqual(
    countOf('\nregisterSchemePrivileges(protocol);'),
    1,
    'exactly one registerSchemePrivileges(protocol) call'
  );
});

test('both privileged registrations happen before app ready', () => {
  // registerSchemesAsPrivileged is ignored once the app is ready, and the
  // Sentry SDK throws outright if initialized after ready.
  const schemeInit = MAIN_SRC.indexOf('\nregisterSchemePrivileges(protocol);');
  const appReady = MAIN_SRC.indexOf('app.whenReady().then(async () =>');

  assert.ok(appReady !== -1, 'expected the main app.whenReady() handler');
  assert.ok(
    schemeInit < appReady,
    'scheme privileges must be declared before app.whenReady()'
  );
});
