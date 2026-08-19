const test = require('node:test');
const assert = require('node:assert');

const {
  PROBE_SCRIPT,
  assessRendererHealth,
  watchRendererHealth,
} = require('../src/renderer-health');

test('an empty #root after load is reported as unhealthy', () => {
  // This is exactly the v1.6.3 state: the document loaded, the preload ran,
  // did-finish-load fired — and nothing mounted.
  const result = assessRendererHealth({ rootChildCount: 0, hasBundleError: false });
  assert.strictEqual(result.healthy, false);
  assert.match(result.reason, /#root is empty/);
});

test('a mounted app is healthy', () => {
  assert.deepStrictEqual(
    assessRendererHealth({ rootChildCount: 3, hasBundleError: false }),
    { healthy: true }
  );
});

test('an explicit bundle error is unhealthy even if something rendered', () => {
  const result = assessRendererHealth({ rootChildCount: 5, hasBundleError: true });
  assert.strictEqual(result.healthy, false);
  assert.match(result.reason, /script\/module load error/);
});

test('the probe inspects #root and cannot throw', () => {
  assert.match(PROBE_SCRIPT, /getElementById\('root'\)/);
  assert.match(PROBE_SCRIPT, /try/);
  assert.match(PROBE_SCRIPT, /catch/);
});

/** Minimal BrowserWindow stand-in driving the real listener wiring. */
function fakeWindow(probeResult) {
  const listeners = new Map();
  return {
    isDestroyed: () => false,
    webContents: {
      on(event, fn) {
        if (!listeners.has(event)) listeners.set(event, []);
        listeners.get(event).push(fn);
      },
      off(event, fn) {
        const fns = listeners.get(event) || [];
        const i = fns.indexOf(fn);
        if (i >= 0) fns.splice(i, 1);
      },
      getURL: () => 'app://bundle/',
      executeJavaScript: async () => probeResult,
    },
    emit(event, ...args) {
      for (const fn of listeners.get(event) || []) fn(...args);
    },
  };
}

function captureLog() {
  const lines = [];
  const record = (...args) => lines.push(args.join(' '));
  return { lines, info: record, warn: record, error: record };
}

test('a blank renderer triggers the unhealthy callback and names the failed asset', async () => {
  const window = fakeWindow({ rootChildCount: 0, hasBundleError: false });
  const log = captureLog();
  let reported = null;

  watchRendererHealth(window, log, {
    gracePeriodMs: 5,
    onUnhealthy: (reason) => {
      reported = reason;
    },
  });

  // A subresource failure, which is the only place the missing asset appears.
  window.emit('did-fail-load', {}, -6, 'ERR_FILE_NOT_FOUND', 'file:///assets/index.js', false);
  window.emit('did-finish-load');

  await new Promise((r) => setTimeout(r, 40));

  assert.ok(reported, 'expected the blank renderer to be reported');
  const output = log.lines.join('\n');
  assert.match(output, /BLANK WINDOW DETECTED/);
  assert.match(
    output,
    /assets\/index\.js/,
    'the report must name the asset that failed, or it is not actionable'
  );
});

test('a healthy renderer reports nothing', async () => {
  const window = fakeWindow({ rootChildCount: 4, hasBundleError: false });
  const log = captureLog();
  let reported = null;

  watchRendererHealth(window, log, {
    gracePeriodMs: 5,
    onUnhealthy: (reason) => {
      reported = reason;
    },
  });

  window.emit('did-finish-load');
  await new Promise((r) => setTimeout(r, 40));

  assert.strictEqual(reported, null);
  assert.match(log.lines.join('\n'), /mounted successfully/);
});

test('cancelling before the grace period suppresses the check', async () => {
  const window = fakeWindow({ rootChildCount: 0, hasBundleError: false });
  const log = captureLog();
  let reported = null;

  const cancel = watchRendererHealth(window, log, {
    gracePeriodMs: 20,
    onUnhealthy: (reason) => {
      reported = reason;
    },
  });

  window.emit('did-finish-load');
  cancel();
  await new Promise((r) => setTimeout(r, 50));

  assert.strictEqual(reported, null, 'a cancelled watcher must not fire');
});
