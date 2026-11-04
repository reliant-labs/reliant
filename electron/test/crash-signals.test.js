// Pins the native crash signal path (src/crash-signals.js): one telemetry record
// per process death carrying reason and exit code, rate-limited per process
// type, and never a minidump.

const test = require('node:test');
const assert = require('node:assert/strict');
const EventEmitter = require('events');
const fs = require('fs');
const path = require('path');

const {
  RATE_LIMIT_MAX_EVENTS,
  RATE_LIMIT_WINDOW_MS,
  mapReason,
  mapChildProcessType,
  safeRouteName,
  buildCrashEvent,
  createCrashSignalReporter,
  installCrashSignals,
} = require('../src/crash-signals');

const ENV = {
  appVersion: '1.7.0',
  platform: 'darwin',
  arch: 'arm64',
  osVersion: '23.3.0',
  uptimeSeconds: 42.4,
};
const silentLog = { warn() {}, error() {}, info() {} };

function makeReporter(overrides = {}) {
  const sent = [];
  let clock = 1_000_000;
  const report = createCrashSignalReporter({
    capture: (e) => sent.push(e),
    log: silentLog,
    env: () => ENV,
    now: () => clock,
    ...overrides,
  });
  return { report, sent, advance: (ms) => (clock += ms) };
}

test('reason mapping keeps Electron\'s vocabulary and collapses anything else to unknown', () => {
  for (const reason of ['crashed', 'oom', 'killed', 'launch-failed', 'integrity-failure', 'abnormal-exit']) {
    assert.equal(mapReason(reason), reason);
  }
  assert.equal(mapReason('clean-exit'), null);
  assert.equal(mapReason('some new electron reason'), 'unknown');
  assert.equal(mapReason(undefined), 'unknown');
});

test('child process types map to a closed set', () => {
  assert.equal(mapChildProcessType('GPU'), 'gpu');
  assert.equal(mapChildProcessType('Utility'), 'utility');
  assert.equal(mapChildProcessType('Sandbox helper'), 'sandbox-helper');
  assert.equal(mapChildProcessType('/Users/jane/secret-plugin'), 'unknown');
  assert.equal(mapChildProcessType(undefined), 'unknown');
});

test('event shape: process type, reason, exit code, version, os, arch and uptime', () => {
  const event = buildCrashEvent({ processType: 'renderer', reason: 'oom', exitCode: 137, route: 'chats' }, ENV);
  assert.deepEqual(event, {
    processType: 'renderer',
    reason: 'oom',
    exitCode: 137,
    appVersion: '1.7.0',
    platform: 'darwin',
    arch: 'arm64',
    osVersion: '23.3.0',
    uptimeSeconds: 42,
    route: 'chats',
  });
});

test('a missing exit code is -1 and a clean exit is not an event', () => {
  assert.equal(buildCrashEvent({ processType: 'gpu', reason: 'crashed' }, ENV).exitCode, -1);
  assert.equal(buildCrashEvent({ processType: 'renderer', reason: 'clean-exit', exitCode: 0 }, ENV), null);
});

test('the route is only a first path segment shaped like a route name', () => {
  assert.equal(safeRouteName('app://bundle/chats/3f2a8c1e-9b7d-4e2f-8a6b-1c2d3e4f5a6b?q=secret#frag'), 'chats');
  assert.equal(safeRouteName('app://bundle/settings'), 'settings');
  assert.equal(safeRouteName('app://bundle/'), '');
  assert.equal(safeRouteName('app://bundle/3f2a8c1e-9b7d'), '');
  assert.equal(safeRouteName('app://bundle/Refactor%20the%20payment%20module'), '');
  assert.equal(safeRouteName('not a url'), '');
  assert.equal(safeRouteName(undefined), '');
});

test('one telemetry record per crash, one issue per process type and reason', () => {
  const { report, sent } = makeReporter();
  report({ processType: 'gpu', reason: 'crashed', exitCode: 139 });
  report({ processType: 'gpu', reason: 'crashed', exitCode: 11 });
  assert.equal(sent.length, 2);
  assert.equal(sent[0].severity, 'ERROR');
  assert.equal(sent[0].body, 'native-crash gpu crashed');
  // error.group pins forge's issue grouping, whatever else differs.
  assert.equal(sent[0].attributes['error.group'], 'native-crash/gpu/crashed');
  assert.equal(sent[0].attributes['crash.exit_code'], 139);
  assert.equal(sent[1].attributes['error.group'], sent[0].attributes['error.group']);
  assert.equal(sent[1].body, sent[0].body);
});

test('a killed process is a warning, not an error', () => {
  const { report, sent } = makeReporter();
  report({ processType: 'utility', reason: 'killed', exitCode: 9 });
  assert.equal(sent[0].severity, 'WARN');
});

test('at most 5 events per 10 minutes per process type, then it recovers', () => {
  assert.equal(RATE_LIMIT_MAX_EVENTS, 5);
  assert.equal(RATE_LIMIT_WINDOW_MS, 10 * 60 * 1000);
  const { report, sent, advance } = makeReporter();

  for (let i = 0; i < 20; i++) report({ processType: 'renderer', reason: 'crashed', exitCode: 1 });
  assert.equal(sent.length, 5);

  // Another process type has its own budget.
  report({ processType: 'gpu', reason: 'crashed', exitCode: 1 });
  assert.equal(sent.length, 6);

  advance(RATE_LIMIT_WINDOW_MS + 1);
  report({ processType: 'renderer', reason: 'crashed', exitCode: 1 });
  assert.equal(sent.length, 7);
  assert.equal(sent[6].attributes['crash.suppressed_since_last_report'], 15);
  assert.equal(sent[5].attributes['crash.suppressed_since_last_report'], undefined);
});

test('a throwing capture never propagates into the crash handler', () => {
  const { report } = makeReporter({
    capture: () => {
      throw new Error('telemetry down');
    },
  });
  assert.doesNotThrow(() => report({ processType: 'gpu', reason: 'crashed', exitCode: 1 }));
});

test('render-process-gone and child-process-gone are both hooked', () => {
  const app = new EventEmitter();
  const reports = [];
  installCrashSignals({
    app,
    report: (c) => reports.push(c),
    getActiveRoute: () => 'settings',
  });

  app.emit(
    'render-process-gone',
    {},
    { getURL: () => 'app://bundle/chats/3f2a8c1e-9b7d' },
    { reason: 'oom', exitCode: 137 },
  );
  app.emit('child-process-gone', {}, { type: 'GPU', reason: 'launch-failed', exitCode: 18, name: 'GPU Helper' });

  assert.deepEqual(reports, [
    { processType: 'renderer', reason: 'oom', exitCode: 137, route: 'chats' },
    { processType: 'gpu', reason: 'launch-failed', exitCode: 18, serviceName: undefined, route: 'settings' },
  ]);
});

test('a destroyed web contents does not break the renderer crash report', () => {
  const app = new EventEmitter();
  const reports = [];
  installCrashSignals({ app, report: (c) => reports.push(c) });
  const destroyed = {
    getURL() {
      throw new Error('Object has been destroyed');
    },
  };
  app.emit('render-process-gone', {}, destroyed, { reason: 'crashed', exitCode: 1 });
  assert.equal(reports[0].route, '');
});

test('the record carries the event\'s enums, numbers and versions and nothing else', () => {
  const { report, sent } = makeReporter();
  report({ processType: 'renderer', reason: 'oom', exitCode: 137, route: 'chats' });
  assert.deepEqual(sent[0], {
    severity: 'ERROR',
    body: 'native-crash renderer oom',
    attributes: {
      'error.group': 'native-crash/renderer/oom',
      'crash.signal': true,
      'crash.process_type': 'renderer',
      'crash.reason': 'oom',
      'crash.exit_code': 137,
      'app.version': '1.7.0',
      'os.platform': 'darwin',
      'os.arch': 'arm64',
      'os.version': '23.3.0',
      'process.uptime_s': 42,
      'crash.route': 'chats',
    },
  });
});

test('Electron details beyond reason, exit code and type are never forwarded', () => {
  const app = new EventEmitter();
  const { report, sent } = makeReporter();
  installCrashSignals({ app, report });
  app.emit(
    'child-process-gone',
    {},
    {
      type: 'Utility',
      reason: 'crashed',
      exitCode: 3,
      name: 'user prompt: refactor the payment module',
      serviceName: 'network.mojom.NetworkService',
      minidump: 'AAAA',
    },
  );
  const wire = JSON.stringify(sent[0]);
  assert.ok(!wire.includes('refactor'));
  assert.ok(!wire.includes('AAAA'));
  assert.equal(sent[0].attributes['crash.service_name'], 'network.mojom.NetworkService');
});

// Minidumps are copies of process memory; crashReporter uploads stay off.
test('crash signal code never touches crashReporter or minidumps', () => {
  const source = fs.readFileSync(path.join(__dirname, '..', 'src', 'crash-signals.js'), 'utf8');
  const code = source.replace(/^\s*\/\/.*$/gm, '');
  assert.ok(!/crashReporter/.test(code));
  assert.ok(!/minidump/i.test(code));
});

test('crashReporter is never started with uploads, anywhere in the main process', () => {
  const srcDir = path.join(__dirname, '..', 'src');
  for (const file of fs.readdirSync(srcDir).filter((f) => f.endsWith('.js'))) {
    const source = fs.readFileSync(path.join(srcDir, file), 'utf8');
    for (const match of source.matchAll(/crashReporter\s*\.\s*start\s*\(([\s\S]{0,400}?)\)/g)) {
      assert.match(match[1], /uploadToServer\s*:\s*false/, `${file}: crashReporter.start must set uploadToServer: false`);
    }
    assert.ok(!/uploadToServer\s*:\s*true/.test(source), `${file}: uploadToServer: true`);
  }
});
