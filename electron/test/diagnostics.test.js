const test = require('node:test');
const assert = require('node:assert');

const {
  DEVTOOLS_ENV_VAR,
  formatDiagnosticsReport,
  isDevToolsAllowed,
  isPrereleaseVersion,
  shouldAutoOpenDevTools,
  shouldShowDevToolsMenuItem,
} = require('../src/diagnostics');

test('DevTools are reachable in a packaged build', () => {
  // The regression that made v1.6.3 undiagnosable: packaged builds forced
  // DevTools closed, so a blank window could not be inspected at all.
  assert.strictEqual(
    shouldShowDevToolsMenuItem(),
    true,
    'the menu item is the only way in when the window is blank'
  );
});

test('the env var opens DevTools automatically in a packaged build', () => {
  for (const value of ['1', 'true', 'TRUE', 'yes']) {
    const env = { [DEVTOOLS_ENV_VAR]: value };
    assert.strictEqual(isDevToolsAllowed({ isPackaged: true, env }), true, value);
    assert.strictEqual(shouldAutoOpenDevTools({ isPackaged: true, env }), true, value);
  }
});

test('DevTools do not auto-open for an ordinary packaged launch', () => {
  assert.strictEqual(shouldAutoOpenDevTools({ isPackaged: true, env: {} }), false);
  for (const value of ['0', 'false', '', 'no']) {
    assert.strictEqual(
      shouldAutoOpenDevTools({ isPackaged: true, env: { [DEVTOOLS_ENV_VAR]: value } }),
      false,
      value
    );
  }
});

test('development always allows DevTools and never force-opens them', () => {
  assert.strictEqual(isDevToolsAllowed({ isPackaged: false, env: {} }), true);
  assert.strictEqual(shouldAutoOpenDevTools({ isPackaged: false, env: {} }), false);
});

test('prerelease versions are recognised for the diagnostics header', () => {
  for (const version of ['1.6.4-rc1', '1.7.0-beta.2', '2.0.0-alpha']) {
    assert.strictEqual(isPrereleaseVersion(version), true, version);
  }
  for (const version of ['1.6.3', '1.6.4', '']) {
    assert.strictEqual(isPrereleaseVersion(version), false, String(version));
  }
});

test('the diagnostics report carries what a bug report needs', () => {
  const report = formatDiagnosticsReport({
    version: '1.6.3',
    electronVersion: '39.2.7',
    platform: 'darwin',
    arch: 'arm64',
    logPath: '/Users/x/Library/Logs/reliant/main.log',
    rendererUrl: 'app://bundle/',
    backendReady: false,
    backendPort: 9190,
    apiUrl: 'https://api.reliant.so/api',
  });

  // The renderer URL and the log path are the two facts that would have
  // identified this bug from a user's paste.
  assert.match(report, /app:\/\/bundle\//);
  assert.match(report, /Library\/Logs\/reliant\/main\.log/);
  assert.match(report, /Backend ready: no/);
  assert.match(report, /9190/);
  assert.match(report, /api\.reliant\.so/);
});
