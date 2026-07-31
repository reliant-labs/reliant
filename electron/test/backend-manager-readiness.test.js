// Readiness contract between the Electron sidecar and the daemon's runtime
// record (<data-dir>/daemon-state.json, written by internal/toolexec/daemonstate).
//
// This exists because that contract silently broke once: the daemon stopped
// writing daemon.pid when it gained the runtime record, BackendManager kept
// polling for daemon.pid, and every launch spent 30s waiting for a file nobody
// writes and then SIGKILLed a daemon that was already connected and serving.
// Nothing failed loudly — the daemon was healthy, the app just refused to
// believe it. These tests fail if the shape of that record drifts again.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const BackendManager = require('../src/backend-manager');

const DAEMON_PID = 4242;

/**
 * A BackendManager with a throwaway data dir and a stand-in child process,
 * plus a writer for the record the daemon would have written there.
 */
function harness() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'daemon-state-'));
  const manager = new BackendManager();

  manager.daemonDataDir = () => dataDir;
  manager.process = { pid: DAEMON_PID, killed: false };

  return {
    manager,
    writeState: (state) =>
      fs.writeFileSync(path.join(dataDir, 'daemon-state.json'), JSON.stringify(state)),
  };
}

test('a daemon with an established stream is ready', async () => {
  const { manager, writeState } = harness();
  writeState({ pid: DAEMON_PID, stream: 'connected' });

  assert.equal(await manager.checkHealth(), true);
});

test('a daemon still registering is NOT ready', async () => {
  // The regression this file is named for. The record exists from startup
  // onward carrying "connecting", so presence-of-file is not readiness — a
  // daemon stuck in interactive OAuth looks exactly like this one.
  const { manager, writeState } = harness();
  writeState({ pid: DAEMON_PID, stream: 'connecting' });

  assert.equal(await manager.checkHealth(), false);
});

test('a dropped stream is NOT ready', async () => {
  const { manager, writeState } = harness();
  writeState({ pid: DAEMON_PID, stream: 'disconnected', stream_detail: 'gateway closed' });

  assert.equal(await manager.checkHealth(), false);
});

test('a record left behind by an earlier daemon is NOT ready', async () => {
  const { manager, writeState } = harness();
  writeState({ pid: DAEMON_PID + 1, stream: 'connected' });

  assert.equal(await manager.checkHealth(), false);
});

test('a missing or unparseable record is NOT ready', async () => {
  const { manager } = harness();
  assert.equal(await manager.checkHealth(), false);

  fs.writeFileSync(path.join(manager.daemonDataDir(), 'daemon-state.json'), '{ trunc');
  assert.equal(await manager.checkHealth(), false);
});

test('in server mode, listening is the healthy steady state', async () => {
  // Server mode inverts who dials: the daemon accepts gateway connections
  // rather than making one, so waiting for "connected" would never return.
  const { manager, writeState } = harness();
  writeState({ pid: DAEMON_PID, stream: 'listening', server_mode: true });

  assert.equal(await manager.checkHealth(), true);

  writeState({ pid: DAEMON_PID, stream: 'listening', server_mode: false });
  assert.equal(await manager.checkHealth(), false);
});

test('no live child process is NOT ready, whatever the record says', async () => {
  const { manager, writeState } = harness();
  writeState({ pid: DAEMON_PID, stream: 'connected' });

  manager.process = null;
  assert.equal(await manager.checkHealth(), false);

  manager.process = { pid: DAEMON_PID, killed: true };
  assert.equal(await manager.checkHealth(), false);
});

test('the timeout message names why the daemon never came up', async () => {
  const { manager, writeState } = harness();

  // Never wrote a record at all.
  await manager.checkHealth();
  assert.match(manager.describeDaemonState(), /no runtime record/);

  // Wrote one, but never got past registration — and says what stopped it.
  writeState({ pid: DAEMON_PID, stream: 'disconnected', stream_detail: 'unauthenticated' });
  await manager.checkHealth();
  assert.match(manager.describeDaemonState(), /disconnected.*unauthenticated/);
});
