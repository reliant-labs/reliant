// Pins main-process telemetry (src/telemetry.js): forge's Node runtime, aimed at
// the renderer's keyed ingest, sending the renderer's origin, and quiet when it
// has nowhere to go.
//
// The last two tests run the REAL runtime (@reliantlabs/forge-web-runtime/node)
// against a fixture of control-plane's keyed ingest route, which admits a
// request only for its key and an exact allowlisted Origin, as prod does.

const test = require('node:test');
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const http = require('node:http');
const path = require('node:path');

const {
  BATCH_DELAY_MS,
  createMainTelemetry,
  telemetryConfig,
  telemetryEndpoint,
} = require('../src/telemetry');
const { APP_ORIGIN } = require('../src/app-protocol');

const silentLog = { info() {}, warn() {} };

function fakeRuntime() {
  const calls = { init: [], logs: [], users: [], flushes: 0, shutdowns: 0 };
  const instance = {
    logger: {
      error: (body, attributes) => calls.logs.push({ level: 'error', body, attributes }),
      warn: (body, attributes) => calls.logs.push({ level: 'warn', body, attributes }),
    },
    setUser: (id) => calls.users.push(id),
    flush: async () => void calls.flushes++,
    shutdown: async () => void calls.shutdowns++,
  };
  return {
    calls,
    load: () => ({
      frontendServiceName: (p, f) => `${p}-${f}`,
      initNodeTelemetry: (config) => {
        calls.init.push(config);
        return config.endpoint ? instance : null;
      },
    }),
  };
}

test('the endpoint is the renderer\'s OTEL_ENDPOINT, overridable for a test ingest', () => {
  const release = { OTEL_ENDPOINT: 'https://ingest.reliantlabs.dev/k/pk_desktop' };
  assert.equal(telemetryEndpoint({ env: {}, release }), 'https://ingest.reliantlabs.dev/k/pk_desktop');
  assert.equal(
    telemetryEndpoint({ env: { RELIANT_OTEL_ENDPOINT: ' http://127.0.0.1:4318/k/pk_test ' }, release }),
    'http://127.0.0.1:4318/k/pk_test',
  );
  assert.equal(telemetryEndpoint({ env: {}, release: {} }), '');
});

test('the config names the desktop app the forge way and sends the renderer\'s origin', () => {
  const config = telemetryConfig({
    endpoint: 'https://ingest.example/k/pk',
    version: '1.8.2',
    isPrerelease: true,
    appRoot: '/Applications/Reliant.app/Contents/Resources/app.asar',
    serviceName: 'reliant-desktop',
  });
  assert.equal(config.serviceName, 'reliant-desktop');
  assert.equal(config.serviceVersion, '1.8.2');
  assert.equal(config.appClient, 'electron');
  // The keyed ingest admits only an allowlisted Origin; the key's allowlist
  // holds the renderer's, and the main process shares the renderer's key.
  assert.equal(config.origin, APP_ORIGIN);
  assert.equal(config.origin, 'app://bundle');
  assert.equal(config.batchDelayMs, BATCH_DELAY_MS);
  assert.deepEqual(config.resourceAttributes, { 'app.channel': 'prerelease' });
});

test('start, record, user, flush and stop delegate to the runtime; stopped is silent', async () => {
  const { calls, load } = fakeRuntime();
  const t = createMainTelemetry({ log: silentLog, load });
  assert.equal(t.start({ endpoint: 'https://i/k/pk', version: '1', isPrerelease: false, appRoot: '/a' }), true);
  assert.equal(t.running, true);
  assert.equal(calls.init[0].serviceName, 'reliant-desktop');

  t.record({ severity: 'ERROR', body: 'native-crash gpu crashed', attributes: { 'crash.reason': 'crashed' } });
  t.record({ severity: 'WARN', body: 'native-crash utility killed', attributes: {} });
  t.setUser('user_1');
  t.setUser('x'.repeat(129)); // not an id
  t.setUser(42);
  t.setUser(null);
  await t.flush();
  assert.deepEqual(calls.logs.map((l) => [l.level, l.body]), [
    ['error', 'native-crash gpu crashed'],
    ['warn', 'native-crash utility killed'],
  ]);
  assert.deepEqual(calls.users, ['user_1', null]);
  assert.equal(calls.flushes, 1);

  await t.stop();
  assert.equal(calls.shutdowns, 1);
  assert.equal(t.running, false);
  t.record({ severity: 'ERROR', body: 'after stop', attributes: {} });
  t.setUser('user_2');
  await t.flush();
  assert.equal(calls.logs.length, 2);
  assert.equal(calls.flushes, 1);
});

test('no endpoint: off, and nothing throws', async () => {
  const { load } = fakeRuntime();
  const t = createMainTelemetry({ log: silentLog, load });
  assert.equal(t.start({ endpoint: '', version: '1', isPrerelease: false, appRoot: '/a' }), false);
  assert.equal(t.running, false);
  await t.flush();
  await t.stop();
});

test('a runtime that fails to load leaves the app running, telemetry off', () => {
  const warnings = [];
  const t = createMainTelemetry({
    log: { info() {}, warn: (...a) => warnings.push(a.join(' ')) },
    load: () => {
      throw new Error('Cannot find module');
    },
  });
  assert.equal(t.start({ endpoint: 'https://i/k/pk', version: '1', isPrerelease: false, appRoot: '/a' }), false);
  assert.match(warnings[0], /could not start/);
});

// ── The real runtime against a keyed-ingest fixture ─────────────────────

const KEY = 'pk_desktop_test';

async function keyedIngest() {
  const received = [];
  const refused = [];
  const server = http.createServer((req, res) => {
    const m = /^\/k\/([^/]+)\/v1\/(logs|traces)$/.exec(req.url || '');
    if (!m || m[1] !== KEY) {
      refused.push(404);
      return res.writeHead(404).end();
    }
    if (req.headers.origin !== APP_ORIGIN) {
      refused.push(403);
      return res.writeHead(403).end();
    }
    let body = '';
    req.on('data', (d) => (body += d));
    req.on('end', () => {
      received.push({ signal: m[2], body: JSON.parse(body) });
      res.writeHead(200).end('{}');
    });
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  return {
    endpoint: `http://127.0.0.1:${server.address().port}/k/${KEY}`,
    received,
    refused,
    close: () => new Promise((r) => server.close(r)),
  };
}

function records(fixture) {
  return fixture.received
    .filter((r) => r.signal === 'logs')
    .flatMap((r) =>
      r.body.resourceLogs.flatMap((rl) =>
        rl.scopeLogs.flatMap((sl) =>
          sl.logRecords.map((lr) => ({
            resource: Object.fromEntries(rl.resource.attributes.map((kv) => [kv.key, kv.value.stringValue])),
            body: lr.body.stringValue,
            severity: lr.severityNumber,
            attributes: Object.fromEntries(
              lr.attributes.map((kv) => [kv.key, kv.value.stringValue ?? kv.value.intValue ?? kv.value.boolValue]),
            ),
          })),
        ),
      ),
    );
}

test('a crash signal reaches the keyed ingest as reliant-desktop, with the renderer\'s origin', async () => {
  const fixture = await keyedIngest();
  const t = createMainTelemetry({ log: silentLog });
  try {
    assert.equal(
      t.start({ endpoint: fixture.endpoint, version: '1.8.2', isPrerelease: false, appRoot: path.join(__dirname, '..') }),
      true,
    );
    const { createCrashSignalReporter } = require('../src/crash-signals');
    const report = createCrashSignalReporter({
      capture: (record) => t.record(record),
      log: silentLog,
      env: () => ({ appVersion: '1.8.2', platform: 'darwin', arch: 'arm64', osVersion: '23.3.0', uptimeSeconds: 5 }),
    });
    report({ processType: 'gpu', reason: 'crashed', exitCode: 139 });
    await t.flush();
  } finally {
    await t.stop();
    await fixture.close();
  }
  assert.deepEqual(fixture.refused, []);
  const [crash] = records(fixture);
  assert.equal(crash.resource['service.name'], 'reliant-desktop');
  assert.equal(crash.resource['service.version'], '1.8.2');
  assert.equal(crash.body, 'native-crash gpu crashed');
  assert.equal(crash.severity, 17);
  assert.equal(crash.attributes['error.group'], 'native-crash/gpu/crashed');
  assert.equal(crash.attributes['app.client'], 'electron');
});

test('an uncaught main-process exception reaches the keyed ingest, grouped by where it was thrown', async () => {
  const fixture = await keyedIngest();
  try {
    const script = `
      const { createMainTelemetry } = require(${JSON.stringify(path.join(__dirname, '..', 'src', 'telemetry.js'))});
      const t = createMainTelemetry({ log: console });
      t.start({ endpoint: ${JSON.stringify(fixture.endpoint)}, version: '1.8.2', isPrerelease: false, appRoot: ${JSON.stringify(path.join(__dirname, '..'))} });
      function startBackend() { throw new TypeError('spawn reliant ENOENT'); }
      setTimeout(startBackend, 0);
    `;
    const child = spawn(process.execPath, ['-e', script], { stdio: ['ignore', 'ignore', 'pipe'] });
    let stderr = '';
    child.stderr.on('data', (d) => (stderr += d));
    const code = await new Promise((r) => child.on('close', r));
    // Plain Node with no handler of its own: recorded, then Node's exit.
    assert.equal(code, 1, stderr);
  } finally {
    await fixture.close();
  }
  const [uncaught] = records(fixture);
  assert.equal(uncaught.body, 'uncaught exception');
  assert.equal(uncaught.severity, 17);
  assert.equal(uncaught.attributes['exception.type'], 'TypeError');
  assert.equal(uncaught.attributes['exception.message'], 'spawn reliant ENOENT');
  assert.match(String(uncaught.attributes['code.function.name']), /startBackend/);
});
