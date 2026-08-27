// `isDaemonConnected()` — the synchronous answer to "is the daemon up RIGHT
// NOW?", used by a renderer that mounts too late to have received the event.
//
// The event alone is not sufficient. watchDaemonConnection de-duplicates on
// the stream value, and the renderer RELOADS as part of the post-sign-in
// daemon restart — so "connected" is published to the OUTGOING renderer and
// the freshly-mounted one, the one actually showing the onboarding compute
// step, receives nothing. Measured on a real prod sign-in: the daemon was
// listable at 22:20:11.233, the UI learned at 22:20:15.289 on the next 5s poll
// tick, and zero events were delivered in between.
//
// These tests pin the states that must and must not count as connected.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const BackendManager = require('../src/backend-manager');
const {
  DAEMON_STATE_STREAM_FIELD,
  DAEMON_STREAM_AWAITING_CREDENTIALS,
} = require('../src/daemon-contract');

/** A BackendManager pointed at a throwaway data dir we can write records into. */
function harness() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'daemon-connected-'));
  const manager = new BackendManager();
  manager.daemonDataDir = () => dataDir;

  return {
    manager,
    writeState(state) {
      fs.writeFileSync(
        path.join(dataDir, 'daemon-state.json'),
        JSON.stringify(state),
      );
    },
    writeRaw(raw) {
      fs.writeFileSync(path.join(dataDir, 'daemon-state.json'), raw);
    },
  };
}

test('a connected stream reports connected', () => {
  const { manager, writeState } = harness();
  writeState({ [DAEMON_STATE_STREAM_FIELD]: 'connected' });
  assert.equal(manager.isDaemonConnected(), true);
});

test('awaiting_credentials is NOT connected', () => {
  // The daemon spawns and idles under --non-interactive before sign-in. It is
  // running, but it has no credential and has not reached the gateway, so it
  // cannot serve. Treating this as connected would let onboarding advance past
  // the compute step before there is anything to compute on.
  const { manager, writeState } = harness();
  writeState({
    [DAEMON_STATE_STREAM_FIELD]: DAEMON_STREAM_AWAITING_CREDENTIALS,
  });
  assert.equal(manager.isDaemonConnected(), false);
});

test('a missing record is not evidence of a running daemon', () => {
  const { manager } = harness();
  assert.equal(manager.isDaemonConnected(), false);
});

test('an unparseable record is not evidence of a running daemon', () => {
  // The daemon rewrites this file from another process; a reader can catch it
  // mid-write. A truncated record must read as "unknown", never as connected.
  const { manager, writeRaw } = harness();
  writeRaw('{"stream": "conn');
  assert.equal(manager.isDaemonConnected(), false);
});

test('a record with no stream field is not connected', () => {
  const { manager, writeState } = harness();
  writeState({ pid: 4242 });
  assert.equal(manager.isDaemonConnected(), false);
});

test('an unrecognised future stream state counts as connected', () => {
  // Matching on the negative — anything that is not awaiting_credentials — is
  // deliberate, so a daemon that gains further live stream states keeps
  // working without a lockstep Electron change.
  const { manager, writeState } = harness();
  writeState({ [DAEMON_STATE_STREAM_FIELD]: 'reconnected' });
  assert.equal(manager.isDaemonConnected(), true);
});
