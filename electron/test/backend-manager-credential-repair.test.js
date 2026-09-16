// A daemon that idles in "awaiting_credentials" while Electron holds a valid
// session is a REPAIRABLE FAULT, not a steady state — and repairing it must
// survive the window in which the mint cannot succeed yet.
//
// THE BUG. On a cold `forge up`, Electron's pre-spawn mint raced
// control-plane's boot and lost: ECONNREFUSED on :8090, three attempts inside
// ~1s, while the API only began listening ~3.2s later. The daemon then idled
// forever under --non-interactive, because the mint only ever ran pre-spawn.
// Symptom: "no daemon connected", cause 80,000 log lines deep.
//
// The near-miss these tests are really guarding. An earlier fix re-minted
// ONCE, from waitForReady, the moment the daemon reported
// awaiting_credentials — which is ~1.4s BEFORE the API starts listening in the
// measured timeline. It burned its attempt into the same refused port and then
// settled false, so the race survived the fix. "Retries at least once" is
// therefore not the property worth asserting; "keeps trying across a failure
// window longer than one mint budget" is, and it is what the first two tests
// pin down.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const BackendManager = require('../src/backend-manager');
const daemonCreds = require('../src/daemon-creds');
const { ENV_INSTANCE_WORKSPACE } = require('../src/daemon-creds');
const { DAEMON_STREAM_AWAITING_CREDENTIALS } = require('../src/daemon-contract');

const DAEMON_PID = 4242;

function harness(t, { outcomes = [], authStorage = {} } = {}) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'cred-repair-'));

  // Name the instance workspace explicitly: daemonInstanceWorkspace's
  // packaged branch calls app.getPath('userData') and there is no Electron
  // `app` under `node --test`, while its dev branch shells out to git and
  // would make results depend on the checkout path.
  const prev = process.env[ENV_INSTANCE_WORKSPACE];
  process.env[ENV_INSTANCE_WORKSPACE] = dataDir;
  t.after(() => {
    if (prev === undefined) delete process.env[ENV_INSTANCE_WORKSPACE];
    else process.env[ENV_INSTANCE_WORKSPACE] = prev;
  });

  const manager = new BackendManager({ authStorage });
  manager.daemonDataDir = () => dataDir;
  manager.process = { pid: DAEMON_PID, killed: false };
  // Collapse the backoff so the policy is exercised without wall-clock waits.
  manager.credRepairBackoffsMs = [0, 0, 0];
  manager.credRepairTickMs = 5;

  // Stand in for the mint. The queue is the control-plane boot timeline:
  // each entry is what a mint attempted at that moment would report.
  const calls = [];
  const queue = [...outcomes];
  manager.ensureDaemonCreds = async () => {
    const outcome = queue.length > 1 ? queue.shift() : queue[0];
    calls.push(outcome);
    return outcome;
  };

  const writeState = (state) =>
    fs.writeFileSync(
      path.join(dataDir, 'daemon-state.json'),
      JSON.stringify({ instance: manager.daemonInstanceSlug(), ...state }),
    );

  t.after(() => manager.stopWatchingDaemonConnection());

  return { manager, calls, writeState, dataDir };
}

const idling = { pid: DAEMON_PID, stream: DAEMON_STREAM_AWAITING_CREDENTIALS };

test('keeps minting across a failure window longer than one mint budget, and succeeds when the API finally answers', async (t) => {
  // Two failures then success: the shape of losing a startup race. A repair
  // that fires once — however promptly — never reaches the third call.
  const { manager, calls } = harness(t, {
    outcomes: [
      daemonCreds.ENSURE_FAILED,
      daemonCreds.ENSURE_FAILED,
      daemonCreds.ENSURE_MINTED,
    ],
  });

  for (let i = 0; i < 3; i++) await manager.maybeRepairDaemonCredentials();

  assert.deepEqual(calls, [
    daemonCreds.ENSURE_FAILED,
    daemonCreds.ENSURE_FAILED,
    daemonCreds.ENSURE_MINTED,
  ]);
});

test('retries on its own clock, with the daemon-state record never changing once', async (t) => {
  // THE TRAP THIS PINS DOWN, which cost this fix two attempts to get right.
  //
  // It is tempting to hang the repair off watchDaemonConnection, which is
  // already stat-polling this very file every 250ms. It does not work, and it
  // fails silently. fs.watchFile is a CHANGE notifier: the listener runs only
  // when the file's stat moves. And a daemon parked in awaiting_credentials
  // never touches its record — Go calls SetStream once, before entering
  // waitForCredentialsNonInteractive, and that loop only READS the credentials
  // file. So the record below is written exactly once, deliberately, and the
  // repair must still make multiple attempts against it.
  //
  // A change-driven repair scores exactly one attempt here and the assertion
  // fails — which is the same single-shot behaviour as the original bug.
  const { manager, calls, writeState } = harness(t, {
    outcomes: [
      daemonCreds.ENSURE_FAILED,
      daemonCreds.ENSURE_FAILED,
      daemonCreds.ENSURE_MINTED,
    ],
  });
  writeState(idling); // ...and never again.

  manager.watchDaemonConnection(() => {});
  await new Promise((r) => setTimeout(r, 60));

  assert.ok(
    calls.length >= 3,
    `expected repeated attempts against an unchanging record, got ${calls.length}: ${calls}`
  );
  assert.ok(
    calls.includes(daemonCreds.ENSURE_MINTED),
    'expected the repair to eventually mint once the API answered'
  );
});

test('the repair loop stops with the watcher, and cannot hold the process alive', async (t) => {
  const { manager, calls, writeState } = harness(t, {
    outcomes: [daemonCreds.ENSURE_FAILED],
  });
  writeState(idling);

  manager.watchDaemonConnection(() => {});
  await new Promise((r) => setTimeout(r, 30));
  assert.ok(calls.length >= 1, 'precondition: the loop was running');

  manager.stopWatchingDaemonConnection();
  const after = calls.length;
  await new Promise((r) => setTimeout(r, 30));

  assert.equal(calls.length, after, 'expected no further attempts after teardown');
});

test('stops — and says why — when the store already holds a current credential', async (t) => {
  // Minting cannot fix this: ensureDaemonPATForOrigin short-circuits before
  // the RPC when the cached PAT matches the signed-in user, so the fault is
  // elsewhere (origin/account mismatch, revoked PAT). Retrying would spin
  // forever against a function that is guaranteed not to change anything.
  const { manager, calls } = harness(t, {
    outcomes: [daemonCreds.ENSURE_ALREADY_CURRENT],
  });

  for (let i = 0; i < 5; i++) await manager.maybeRepairDaemonCredentials();

  assert.equal(calls.length, 1, `expected exactly one attempt, got ${calls.length}`);
});

test('never mints when there is no session, but stays armed for one appearing later', async (t) => {
  // A signed-out user is the state the awaiting-credentials branch was
  // originally written for, and must stay quiet — no RPC storm, no giving up.
  const { manager, calls } = harness(t, {
    outcomes: [daemonCreds.ENSURE_NO_SESSION],
  });

  for (let i = 0; i < 3; i++) await manager.maybeRepairDaemonCredentials();

  assert.ok(calls.length >= 1, 'expected the cheap no-session check to still run');
  assert.equal(manager._credRepairStopped, false, 'no-session must not disarm the repair');
  assert.equal(
    manager._credRepairAttempt,
    0,
    'a signed-out spell must not consume the attempts a later race needs'
  );
});

test('does nothing at all without auth storage — an OSS build has no mint to make', async (t) => {
  const { manager, calls } = harness(t, {
    outcomes: [daemonCreds.ENSURE_MINTED],
    authStorage: null,
  });

  await manager.maybeRepairDaemonCredentials();

  assert.deepEqual(calls, []);
});

test('only one repair runs at a time', async (t) => {
  // The watcher fires every 250ms while the mint can take seconds (its
  // per-attempt timeout alone is 5s). Overlapping mints would race on
  // daemon.json and multiply PATs.
  const { manager } = harness(t);
  let concurrent = 0;
  let maxConcurrent = 0;
  manager.ensureDaemonCreds = async () => {
    concurrent += 1;
    maxConcurrent = Math.max(maxConcurrent, concurrent);
    await new Promise((r) => setTimeout(r, 20));
    concurrent -= 1;
    return daemonCreds.ENSURE_FAILED;
  };

  await Promise.all([
    manager.maybeRepairDaemonCredentials(),
    manager.maybeRepairDaemonCredentials(),
    manager.maybeRepairDaemonCredentials(),
  ]);

  assert.equal(maxConcurrent, 1, 'expected the in-flight guard to serialize repairs');
});

test('re-arms per episode: a daemon that connects and later idles again is repaired again', async (t) => {
  // The budget is per-episode on purpose. A lifetime cap would silently
  // disarm the repair for the rest of the app's run, so the same fault an
  // hour later — a control-plane restart, say — would go unrepaired.
  const { manager, calls, writeState } = harness(t, {
    outcomes: [daemonCreds.ENSURE_ALREADY_CURRENT],
  });

  await manager.maybeRepairDaemonCredentials();
  assert.equal(manager._credRepairStopped, true, 'precondition: the first episode stopped');

  // The repair loop observes a connected daemon and clears the episode.
  writeState({ pid: DAEMON_PID, stream: 'connected' });
  manager.startDaemonCredentialRepairLoop();
  manager.stopDaemonCredentialRepairLoop();

  assert.equal(manager._credRepairStopped, false, 'connecting must re-arm the repair');

  await manager.maybeRepairDaemonCredentials();
  assert.equal(calls.length, 2, 'expected a second episode to attempt afresh');
});
