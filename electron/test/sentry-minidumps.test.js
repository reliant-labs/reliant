// Pins that native crash minidumps never leave the app. A minidump is a copy of
// process memory, so it can hold prompts, file contents and credentials that
// the event scrubber in sentry-scrub.js cannot see. JS error reports still go
// to Sentry; only the native dump upload is off.
//
// @sentry/electron enables its SentryMinidump integration BY DEFAULT in the
// main process: it starts Electron's crashReporter and uploads every dump it
// finds. So the test resolves the SDK's real default integrations through the
// options main.js passes to Sentry.init, rather than trusting a hand-written
// list of names.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const Module = require('module');
const path = require('path');

const { getIntegrationsToSetup } = require('@sentry/core');
const { sentryMainOptions } = require('../src/sentry-scrub');

const MINIDUMP_INTEGRATIONS = ['SentryMinidump', 'ElectronMinidump'];

// Loads @sentry/electron/main under plain node. The SDK reads
// process.versions.electron and requires 'electron' at load time, so both are
// faked: 'electron' becomes an inert object whose every property and call
// returns itself. Building the default integration list touches no real
// Electron API; only setup() does, and nothing here calls it.
function loadSentryMain() {
  if (!process.versions.electron) {
    Object.defineProperty(process.versions, 'electron', { value: '41.0.0', configurable: true });
  }
  const inert = new Proxy(function () {}, {
    get: (_target, prop) => (prop === 'then' ? undefined : inert),
    apply: () => inert,
  });
  const originalLoad = Module._load;
  Module._load = function (request, ...rest) {
    return request === 'electron' ? inert : originalLoad.call(this, request, ...rest);
  };
  try {
    return require('@sentry/electron/main');
  } finally {
    Module._load = originalLoad;
  }
}

const SentryMain = loadSentryMain();

function mainOptions() {
  return sentryMainOptions({
    dsn: 'https://public@o0.ingest.sentry.io/0',
    environment: 'production',
    release: 'reliant@1.0.0',
    isEnabled: () => true,
  });
}

// The integrations Sentry.init would actually set up, resolved the way the SDK
// resolves them (defaults, then the `integrations` option applied on top).
function resolvedIntegrationNames(defaults) {
  const options = mainOptions();
  return getIntegrationsToSetup({ ...options, defaultIntegrations: defaults }).map((i) => i.name);
}

test('the SDK still ships a minidump integration by default (so removing it is load-bearing)', () => {
  const defaults = SentryMain.getDefaultIntegrations({}).map((i) => i.name);
  assert.ok(
    defaults.includes('SentryMinidump'),
    `expected SentryMinidump among @sentry/electron's defaults, got: ${defaults.join(', ')}. ` +
      `If the SDK renamed or dropped it, update MINIDUMP_INTEGRATIONS in src/sentry-scrub.js and this test.`,
  );
});

test('the main-process Sentry options remove every minidump integration', () => {
  const names = resolvedIntegrationNames(SentryMain.getDefaultIntegrations({}));
  for (const minidump of MINIDUMP_INTEGRATIONS) {
    assert.ok(!names.includes(minidump), `${minidump} is active: ${names.join(', ')}`);
  }
});

test('an explicitly added ElectronMinidump integration is removed too', () => {
  const options = mainOptions();
  assert.equal(typeof options.integrations, 'function', 'sentryMainOptions must filter integrations');
  const names = options
    .integrations([...SentryMain.getDefaultIntegrations({}), SentryMain.electronMinidumpIntegration()])
    .map((i) => i.name);
  for (const minidump of MINIDUMP_INTEGRATIONS) {
    assert.ok(!names.includes(minidump), `${minidump} is active: ${names.join(', ')}`);
  }
});

test('JS error reporting stays on in the main process', () => {
  const names = resolvedIntegrationNames(SentryMain.getDefaultIntegrations({}));
  for (const kept of ['OnUncaughtException', 'OnUnhandledRejection', 'LinkedErrors', 'ChildProcess']) {
    assert.ok(names.includes(kept), `${kept} was removed: ${names.join(', ')}`);
  }
});

test('nothing in the main process starts Electron\'s crash reporter', () => {
  // With the Sentry integration gone, crashpad only runs if our own code starts
  // it. If local dumps are ever wanted, start it with uploadToServer: false and
  // update this test to check for that instead.
  const srcDir = path.join(__dirname, '..', 'src');
  const offenders = fs
    .readdirSync(srcDir)
    .filter((f) => f.endsWith('.js'))
    .filter((f) => /crashReporter\s*\.\s*start\s*\(/.test(fs.readFileSync(path.join(srcDir, f), 'utf8')));
  assert.deepEqual(offenders, []);
});
