const test = require('node:test');
const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const { waitForPort } = require('../src/wait-for-port');
const { main, parseOptions } = require('../scripts/wait-and-start');
const { forwardSignals } = require('../src/child-signals');

test('forwarding removes parent handlers after spawn error or exit', () => {
  for (const event of ['exit', 'error']) {
    const parent = new EventEmitter();
    const child = new EventEmitter();
    child.kill = () => true;
    forwardSignals(child, { parent });
    assert.equal(parent.listenerCount('SIGTERM'), 1);
    child.emit(event);
    assert.equal(parent.listenerCount('SIGTERM'), 0);
  }
});

function sockets() {
  const created = [];
  const connect = () => {
    const socket = new EventEmitter();
    socket.destroy = () => { socket.destroyed = true; };
    created.push(socket);
    return socket;
  };
  return { created, connect };
}

test('Air excludes both current and obsolete frontend trees', () => {
  const fs = require('node:fs');
  const path = require('node:path');
  for (const name of ['.air.api.toml', '.air.worker.toml']) {
    const config = fs.readFileSync(path.join(__dirname, '../..', name), 'utf8');
    const exclusions = config.match(/exclude_dir\s*=\s*\[([^\]]+)\]/)[1];
    assert.match(exclusions, /"web"/);
    assert.match(exclusions, /"frontends"/);
  }
});

test('standalone keeps 30s default, accepts bounded override, rejects invalid options', () => {
  assert.deepEqual(parseOptions([]), { timeoutMs: 30000 });
  assert.deepEqual(parseOptions(['--wait-timeout-ms', '120000']), { timeoutMs: 120000 });
  for (const value of ['', '0', '-1', 'Infinity', '1.5', '2147483648', '12oops']) {
    assert.throws(() => parseOptions(['--wait-timeout-ms', value]));
  }
  assert.throws(() => parseOptions(['--unknown']));
});

test('silent socket is bounded by overall deadline and destroyed', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const { created, connect } = sockets();
  const result = waitForPort(5173, { connect, timeoutMs: 30 });
  const rejected = assert.rejects(result, /Timeout waiting for port 5173 after 30ms/);
  t.mock.timers.tick(30);
  await rejected;
  assert.equal(created[0].destroyed, true);
  t.mock.timers.tick(10000);
  assert.equal(created.length, 1);
});

test('per-attempt timeout retries black-holed connection and can recover', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const { created, connect } = sockets();
  const result = waitForPort(5173, { connect, timeoutMs: 1000, attemptTimeoutMs: 10, retryMs: 2 });
  t.mock.timers.tick(10);
  assert.equal(created[0].destroyed, true);
  t.mock.timers.tick(2);
  assert.equal(created.length, 2);
  created[1].emit('connect');
  assert.equal(await result, true);
  assert.equal(created[1].destroyed, true);
  t.mock.timers.tick(2000);
  assert.equal(created.length, 2);
});

test('connection refusal retries; cancellation destroys socket and clears retry', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const { created, connect } = sockets();
  const controller = new AbortController();
  const result = waitForPort(5173, { connect, signal: controller.signal, retryMs: 2 });
  const rejected = assert.rejects(result, /stop/);
  created[0].emit('error', new Error('ECONNREFUSED'));
  controller.abort(new Error('stop'));
  await rejected;
  t.mock.timers.tick(40000);
  assert.equal(created.length, 1);
  assert.equal(created[0].destroyed, true);
});

test('already cancelled never connects; cancellation during attempt destroys socket', async () => {
  const { created, connect } = sockets();
  const controller = new AbortController();
  const result = waitForPort(5173, { connect, signal: controller.signal });
  controller.abort(new Error('stop'));
  await assert.rejects(result, /stop/);
  assert.equal(created[0].destroyed, true);
  await assert.rejects(waitForPort(5173, { connect, signal: controller.signal }), /stop/);
  assert.equal(created.length, 1);
});

test('synchronous connection setup errors are reported', async () => {
  await assert.rejects(waitForPort(5173, { connect() { throw new Error('bad port'); } }), /bad port/);
});

function launcher(spawnChild) {
  const parent = new EventEmitter();
  const run = main({ parent, args: [], wait: async () => true, spawnChild });
  return { parent, run };
}

for (const [code, signal, expected] of [[0, null, 0], [7, null, 7], [null, 'SIGTERM', 143]]) {
  test(`child exit ${code}/${signal} propagates ${expected}`, async () => {
    const child = new EventEmitter();
    child.kill = () => true;
    const { parent, run } = launcher(() => {
      queueMicrotask(() => child.emit('exit', code, signal));
      return child;
    });
    assert.equal(await run, expected);
    for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) assert.equal(parent.listenerCount(signal), 0);
  });
}

test('asynchronous and synchronous spawn failures return failure without leaking signal handlers', async () => {
  for (const synchronous of [true, false]) {
    const { parent, run } = launcher(() => {
      if (synchronous) throw new Error('ENOENT');
      const child = new EventEmitter();
      queueMicrotask(() => child.emit('error', new Error('ENOENT')));
      return child;
    });
    assert.equal(await run, 1);
    assert.equal(parent.listenerCount('SIGTERM'), 0);
  }
});

test('waiting cancellation does not spawn; signal exit status propagates', async () => {
  const parent = new EventEmitter();
  let spawned = false;
  const run = main({ parent, args: [], spawnChild() { spawned = true; }, wait: (_port, { signal }) => {
    return new Promise((resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason)));
  } });
  parent.emit('SIGTERM');
  assert.equal(await run, 143);
  assert.equal(spawned, false);
  assert.equal(parent.listenerCount('SIGTERM'), 0);
});

test('running cancellation forwards to actual child; Unix launch does not insert a shell', async () => {
  const parent = new EventEmitter();
  const child = new EventEmitter();
  const signals = [];
  child.kill = signal => { signals.push(signal); child.emit('exit', null, signal); };
  const run = main({ parent, args: [], wait: async () => true, spawnChild: (_path, _args, options) => {
    assert.equal(options.shell, process.platform === 'win32');
    queueMicrotask(() => parent.emit('SIGTERM'));
    return child;
  } });
  assert.equal(await run, 143);
  assert.deepEqual(signals, ['SIGTERM']);
});

test('launcher passes explicit wait bound and does not spawn after readiness failure', async () => {
  const parent = new EventEmitter();
  const result = await main({ parent, args: ['--wait-timeout-ms', '45000'],
    wait: async (_port, options) => { assert.equal(options.timeoutMs, 45000); throw new Error('timeout'); },
    spawnChild() { assert.fail('must not spawn'); },
  });
  assert.equal(result, 1);
});
