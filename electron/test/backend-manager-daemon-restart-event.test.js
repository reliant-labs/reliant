// watchDaemonConnection must announce the daemon that exists NOW, including
// after a restart that returns to the same stream value.
//
// This is the bug that made onboarding's compute step hang. Signing in
// restarts the daemon, taking it connected → awaiting_credentials → connected.
// The watcher stats the record every 250ms, and that intermediate state is
// routinely shorter than one interval — so a watcher deduping on the `stream`
// string sees "connected" before and "connected" after, concludes nothing
// changed, and never reports the new daemon. The only event it ever emitted
// described the daemon that had already gone away.
//
// Dedupe therefore keys on the connection's IDENTITY (`connected_at` + `pid`),
// which changes on every re-establishment and respawn.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const BackendManager = require('../src/backend-manager');
const { DAEMON_STREAM_AWAITING_CREDENTIALS } = require('../src/daemon-contract');

const STAT_INTERVAL_MS = 250;
/** Long enough for the stat-poll watcher to have observed a write. */
const SETTLE_MS = 900;

function harness() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'daemon-restart-'));
  const manager = new BackendManager();
  manager.daemonDataDir = () => dataDir;
  const statePath = path.join(dataDir, 'daemon-state.json');

  return {
    manager,
    /** Write a record the way the daemon would, with a fresh connection stamp. */
    write({ stream, connectedAt, pid = 4242 }) {
      fs.writeFileSync(
        statePath,
        JSON.stringify({
          pid,
          stream,
          connected_at: connectedAt,
          stream_changed_at: connectedAt,
        }),
      );
    },
    remove() {
      try {
        fs.unlinkSync(statePath);
      } catch {
        /* already gone */
      }
    },
  };
}

const settle = () => new Promise((r) => setTimeout(r, SETTLE_MS));

test('a restart back to connected reports the NEW daemon', async () => {
  const { manager, write } = harness();
  write({ stream: 'connected', connectedAt: '2026-08-26T02:00:00Z', pid: 100 });

  const fired = [];
  manager.watchDaemonConnection((stream) => fired.push(stream));
  assert.equal(fired.length, 1, 'arming reports the daemon already connected');

  // Sign-in restarts the daemon. Both writes land in the SAME tick, so the
  // watcher cannot observe the intermediate state — it stats a file that says
  // "connected" before and "connected" after. That is the case that used to
  // swallow the event, and it is not a contrived one: a sweep of this window
  // against the old stream-only dedupe lost the event at every gap below
  // ~62ms and kept it above ~150ms. Writing in one tick pins the failure
  // deterministically instead of racing the 250ms stat interval.
  await settle();
  write({
    stream: DAEMON_STREAM_AWAITING_CREDENTIALS,
    connectedAt: '',
    pid: 200,
  });
  write({ stream: 'connected', connectedAt: '2026-08-26T02:05:00Z', pid: 200 });
  await settle();

  manager.stopWatchingDaemonConnection();
  assert.equal(
    fired.length,
    2,
    'the daemon that came up after sign-in must be announced',
  );
});

test('repeated writes of the SAME connection do not re-fire', async () => {
  // The daemon rewrites this record for reasons unrelated to connectivity —
  // session counts, heartbeats. Those must not each look like a new daemon, or
  // the renderer refetches ListDaemons on every heartbeat.
  const { manager } = harness();
  const dataDir = manager.daemonDataDir();
  const statePath = path.join(dataDir, 'daemon-state.json');
  const record = {
    pid: 100,
    stream: 'connected',
    connected_at: '2026-08-26T02:00:00Z',
  };
  fs.writeFileSync(statePath, JSON.stringify(record));

  const fired = [];
  manager.watchDaemonConnection((stream) => fired.push(stream));
  assert.equal(fired.length, 1);

  for (let i = 1; i <= 3; i++) {
    await new Promise((r) => setTimeout(r, 300));
    fs.writeFileSync(statePath, JSON.stringify({ ...record, sessions: i }));
  }
  await settle();

  manager.stopWatchingDaemonConnection();
  assert.equal(fired.length, 1, 'same connection, so still one event');
});

test('a restart that only changes pid still reports', async () => {
  // A daemon can respawn and reconnect fast enough to reuse a connection
  // timestamp at second granularity; the pid still distinguishes it.
  const { manager, write } = harness();
  write({ stream: 'connected', connectedAt: '2026-08-26T02:00:00Z', pid: 100 });

  const fired = [];
  manager.watchDaemonConnection((stream) => fired.push(stream));
  await settle();

  write({ stream: 'connected', connectedAt: '2026-08-26T02:00:00Z', pid: 300 });
  await settle();

  manager.stopWatchingDaemonConnection();
  assert.equal(fired.length, 2);
});

test('awaiting_credentials is never announced as connected', async () => {
  const { manager, write } = harness();
  write({
    stream: DAEMON_STREAM_AWAITING_CREDENTIALS,
    connectedAt: '',
    pid: 100,
  });

  const fired = [];
  manager.watchDaemonConnection((stream) => fired.push(stream));
  await settle();

  manager.stopWatchingDaemonConnection();
  assert.equal(fired.length, 0, 'no credential, no gateway, nothing to report');
});
