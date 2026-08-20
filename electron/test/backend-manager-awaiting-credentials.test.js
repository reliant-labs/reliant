// A daemon spawned under --non-interactive with no credentials on disk does
// NOT open a browser and does NOT exit — it idles, publishing
// stream: "awaiting_credentials" in daemon-state.json, until Electron mints
// a PAT and respawns it after sign-in (see daemon-contract.js).
//
// Before this fix, BackendManager had no notion of that state: waitForReady()
// only knew "connected" (ready) or "not connected yet" (keep polling until
// startupTimeout, then reject). A credential-less daemon idling in
// "awaiting_credentials" looked identical to one stuck mid-crash, so it got
// the full 30s timeout, a rejected promise, an app.whenReady "Backend Error"
// dialog / IPC "Backend Connection Error" dialog, and — for the
// process-exit-driven path — handleCrash's 5-attempt browser-tab-opening
// restart loop. These tests fail on that pre-fix behavior and pass after it.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const BackendManager = require('../src/backend-manager');
const {
  DAEMON_NON_INTERACTIVE_FLAG,
  DAEMON_STREAM_AWAITING_CREDENTIALS,
} = require('../src/daemon-contract');

const DAEMON_PID = 4242;

function harness() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'daemon-state-'));
  const manager = new BackendManager();

  manager.daemonDataDir = () => dataDir;
  manager.process = { pid: DAEMON_PID, killed: false };
  // Keep these tests fast — the timeout path is exercised explicitly below
  // with its own short value, not the production 30s default.
  manager.startupTimeout = 300;

  return {
    manager,
    writeState: (state) =>
      fs.writeFileSync(path.join(dataDir, 'daemon-state.json'), JSON.stringify(state)),
  };
}

test('every daemon spawn passes the non-interactive flag', () => {
  const manager = new BackendManager();
  const args = manager.buildDaemonArgs();
  assert.ok(
    args.includes(DAEMON_NON_INTERACTIVE_FLAG),
    `expected ${DAEMON_NON_INTERACTIVE_FLAG} in daemon args, got: ${args.join(' ')}`
  );
});

test('isAwaitingCredentials is true only for our own pid publishing the awaiting-credentials stream', async () => {
  const { manager, writeState } = harness();

  writeState({ pid: DAEMON_PID, stream: DAEMON_STREAM_AWAITING_CREDENTIALS });
  assert.equal(manager.isAwaitingCredentials(), true);

  // A record from a different (earlier) daemon must not count.
  writeState({ pid: DAEMON_PID + 1, stream: DAEMON_STREAM_AWAITING_CREDENTIALS });
  assert.equal(manager.isAwaitingCredentials(), false);

  // Any other stream value is not "awaiting credentials".
  writeState({ pid: DAEMON_PID, stream: 'connecting' });
  assert.equal(manager.isAwaitingCredentials(), false);

  writeState({ pid: DAEMON_PID, stream: 'connected' });
  assert.equal(manager.isAwaitingCredentials(), false);

  // No live process — never awaiting credentials, whatever the file says.
  writeState({ pid: DAEMON_PID, stream: DAEMON_STREAM_AWAITING_CREDENTIALS });
  manager.process = null;
  assert.equal(manager.isAwaitingCredentials(), false);
});

test('waitForReady resolves false promptly when awaiting credentials — it does not reject or hang for the full startup timeout', async () => {
  const { manager, writeState } = harness();
  writeState({ pid: DAEMON_PID, stream: DAEMON_STREAM_AWAITING_CREDENTIALS });

  const start = Date.now();
  const ready = await manager.waitForReady();
  const elapsed = Date.now() - start;

  assert.equal(ready, false, 'awaiting-credentials must resolve false, not throw');
  assert.ok(
    elapsed < manager.startupTimeout,
    `expected an early settle well under the ${manager.startupTimeout}ms startup timeout, took ${elapsed}ms`
  );
});

test('waitForReady still rejects on timeout for a daemon that is NOT awaiting credentials (real crash/stall path preserved)', async () => {
  const { manager, writeState } = harness();
  // Stuck "connecting" forever is the genuine stall/crash case this promise
  // must still catch — distinct from "awaiting_credentials".
  writeState({ pid: DAEMON_PID, stream: 'connecting' });

  await assert.rejects(() => manager.waitForReady(), /failed to become ready/);
});

test('waitForReady resolves true once the daemon moves from awaiting_credentials to connected', async () => {
  const { manager, writeState } = harness();
  writeState({ pid: DAEMON_PID, stream: DAEMON_STREAM_AWAITING_CREDENTIALS });

  // Flip to connected shortly after the wait starts (simulating the
  // post-sign-in respawn reaching the gateway).
  setTimeout(() => writeState({ pid: DAEMON_PID, stream: 'connected' }), 20);

  // Poll manually since resolving false is instantaneous in the harness above;
  // here we want the *eventual* ready path, so call again after the flip.
  const firstPass = await manager.waitForReady();
  assert.equal(firstPass, false);

  await new Promise((resolve) => setTimeout(resolve, 40));
  const secondPass = await manager.waitForReady();
  assert.equal(secondPass, true);
});
