// The daemon PUSHES its stream state on stdout; Electron no longer waits to
// notice it.
//
// The fallback is a stat-poll of daemon-state.json every 250ms, so a connect is
// discovered up to a full interval late — measured at 146ms on a real dev
// sign-in, purely from landing mid-interval. Nothing was slow; the supervisor
// was asking instead of being told. A locally spawned daemon already has an
// ordered pipe to its parent, so it announces on it.
//
// The poll STAYS. A daemon on another machine has no stdout its supervisor can
// read, and daemon-state.json remains the source of truth. This is a fast path,
// never the only path — which is what the dedupe tests below protect: whichever
// path sees a connection first reports it, and the other must stay quiet.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const BackendManager = require('../src/backend-manager');
const {
  DAEMON_STREAM_NOTICE_PREFIX,
  DAEMON_STREAM_AWAITING_CREDENTIALS,
  DAEMON_STATE_STREAM_FIELD,
  DAEMON_VERBOSE_FLAG,
} = require('../src/daemon-contract');

// The push path only exists when we ASK for machine output.
//
// The daemon suppresses `@@RELIANT_STREAM` notices unless --verbose is set,
// because without it stdout belongs to a human reading short status lines. If
// this flag is ever dropped from buildDaemonArgs, every test below still passes
// — they feed the notice text in directly — while the real app silently falls
// back to the 250ms stat-poll for every connect. That is precisely the kind of
// regression that shows up as "the app feels slower after sign-in" months later
// and is nearly impossible to attribute, so it gets its own assertion here,
// beside the parsing tests it protects.
test('every daemon spawn asks for machine output, or the push path is dead', () => {
  const args = new BackendManager().buildDaemonArgs();
  assert.ok(
    args.includes(DAEMON_VERBOSE_FLAG),
    `expected ${DAEMON_VERBOSE_FLAG} in daemon args — without it the daemon prints no ` +
      `${DAEMON_STREAM_NOTICE_PREFIX} notices and connects are only seen by the slower ` +
      `stat-poll. Got: ${args.join(' ')}`
  );
});

/** A manager with a throwaway data dir and a stand-in daemon process. */
function harness({ pid = 4242 } = {}) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'stream-notice-'));
  const instance = new BackendManager();
  instance.daemonDataDir = () => dataDir;
  instance.process = { pid, killed: false };

  const events = [];
  instance._onDaemonConnected = (stream) => events.push(stream);

  return {
    instance,
    events,
    /** Write the record the daemon would have written alongside its notice. */
    writeState({ stream, connectedAt = '2026-08-26T00:00:00Z' }) {
      fs.writeFileSync(
        path.join(dataDir, 'daemon-state.json'),
        JSON.stringify({ pid, stream, connected_at: connectedAt }),
      );
    },
  };
}

const notice = (stream) => `${DAEMON_STREAM_NOTICE_PREFIX} ${stream}\n`;

test('a connect notice on stdout is reported immediately', () => {
  const { instance, events } = harness();
  instance.consumeDaemonStdout(notice('connected'));
  assert.deepEqual(events, ['connected']);
});

test('a notice split across two chunks is still reported', () => {
  // Chunks are not lines. A pipe delivers whatever has been written when the
  // reader wakes, so a notice can straddle a chunk boundary — and matching on
  // raw chunks would drop it, producing an occasional unreproducible fallback
  // to the slow path.
  const { instance, events } = harness();
  const line = notice('connected');
  const split = Math.floor(line.length / 2);

  instance.consumeDaemonStdout(line.slice(0, split));
  assert.deepEqual(events, [], 'a half-line must not fire');

  instance.consumeDaemonStdout(line.slice(split));
  assert.deepEqual(events, ['connected']);
});

test('a notice sharing a chunk with log lines is still reported', () => {
  const { instance, events } = harness();
  instance.consumeDaemonStdout(
    `time=2026-08-26 level=INFO msg="starting"\n${notice('connected')}time=2026-08-26 level=INFO msg="ready"\n`,
  );
  assert.deepEqual(events, ['connected']);
});

test('awaiting_credentials is never reported as connected', () => {
  // The daemon idles in this state before sign-in. It is running but holds no
  // credential and has not reached the gateway, so it cannot serve.
  const { instance, events } = harness();
  instance.consumeDaemonStdout(notice(DAEMON_STREAM_AWAITING_CREDENTIALS));
  assert.deepEqual(events, []);
});

test('ordinary log output produces no event', () => {
  const { instance, events } = harness();
  instance.consumeDaemonStdout(
    'time=2026-08-26 level=INFO msg="Connecting to gateway"\n',
  );
  assert.deepEqual(events, []);
});

test('the same connection is reported ONCE across both paths', () => {
  // The push and poll paths must dedupe against each other. Without comparable
  // identities every connection fires twice — harmless, since the renderer
  // treats the event as a refetch trigger, but it doubles the RPCs and makes
  // the log misreport what happened.
  const { instance, events, writeState } = harness();
  writeState({ stream: 'connected' });

  instance.consumeDaemonStdout(notice('connected'));
  assert.deepEqual(events, ['connected'], 'stdout reports first');

  // The watcher's stat lands moments later and sees the same record.
  instance.watchDaemonConnection((stream) => events.push(stream));
  instance.stopWatchingDaemonConnection();

  assert.deepEqual(events, ['connected'], 'the poll must not re-report it');
});

test('a genuine reconnect IS reported again', () => {
  // Dedupe must not swallow the next real connection. The post-sign-in restart
  // produces a NEW connection under a new pid, which is the event onboarding
  // depends on.
  const { instance, events, writeState } = harness();
  writeState({ stream: 'connected', connectedAt: '2026-08-26T00:00:00Z' });
  instance.consumeDaemonStdout(notice('connected'));
  assert.deepEqual(events, ['connected']);

  // Daemon restarts: new process, new connection.
  instance.process = { pid: 9999, killed: false };
  fs.writeFileSync(
    path.join(instance.daemonDataDir(), 'daemon-state.json'),
    JSON.stringify({
      pid: 9999,
      [DAEMON_STATE_STREAM_FIELD]: 'connected',
      connected_at: '2026-08-26T00:05:00Z',
    }),
  );
  instance.consumeDaemonStdout(notice('connected'));

  assert.deepEqual(events, ['connected', 'connected']);
});

test('the notice prefix matches the Go constant it mirrors', () => {
  // The daemon writes this string; Electron matches on it. A rename on either
  // side degrades the app to the slow poll SILENTLY — everything still works,
  // just measurably worse — which is the kind of regression no other test would
  // catch. Read the Go source and compare.
  const goSource = fs.readFileSync(
    path.join(__dirname, '../../internal/toolexec/daemonstate/state.go'),
    'utf8',
  );
  const match = goSource.match(/StreamNoticePrefix\s*=\s*"([^"]+)"/);

  assert.ok(match, 'StreamNoticePrefix not found in daemonstate/state.go');
  assert.equal(
    match[1],
    DAEMON_STREAM_NOTICE_PREFIX,
    'Go and Electron disagree on the stream-notice prefix',
  );
});
