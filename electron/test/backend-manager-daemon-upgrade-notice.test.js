// The daemon tells this app when the platform asks it to upgrade.
//
// The daemon Electron spawns runs the binary inside this app's signed bundle,
// so it never replaces itself: it keeps serving, reports that it awaits its
// owner, and prints one `@@RELIANT_UPGRADE {json}` line per request on stdout.
// This app's updater is its owner. These tests pin the contract between the
// two: the spawn declares the supervisor, the prefix and JSON match the Go
// side, and a notice reaches the updater (main.js handleDaemonUpgradeNotice)
// while every other line is still logged.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const BackendManager = require('../src/backend-manager');
const {
  DAEMON_STREAM_NOTICE_PREFIX,
  DAEMON_SUPERVISOR_ENV_VAR,
  DAEMON_SUPERVISOR_ELECTRON,
  DAEMON_UPGRADE_NOTICE_PREFIX,
  parseDaemonUpgradeNotice,
} = require('../src/daemon-contract');

const goStateSource = () =>
  fs.readFileSync(path.join(__dirname, '../../internal/toolexec/daemonstate/state.go'), 'utf8');

// The exact line the Go daemon prints for a security request, copied from
// TestDesktopDaemonPrintsTheUpgradeNoticeShape
// (internal/toolexec/daemonruntime/upgrade_supervisor_test.go). If the Go
// side changes the shape, that test fails; if this side stops reading it,
// this one does.
const GO_NOTICE_LINE =
  '@@RELIANT_UPGRADE {"target_version":"1.40.0","target_commit":"abc123","commit_time_unix":1790000000,' +
  '"urgency":"security","deadline_unix_ms":1790000600000,"rollout_id":"roll-7","reason":"a security fix",' +
  '"upgrade_command":"brew upgrade --cask reliant","upgrade_url":"https://reliantlabs.io/download"}';

const PARSED_GO_NOTICE = {
  targetVersion: '1.40.0',
  targetCommit: 'abc123',
  commitTimeUnix: 1790000000,
  urgency: 'security',
  deadlineUnixMs: 1790000600000,
  rolloutId: 'roll-7',
  reason: 'a security fix',
  upgradeCommand: 'brew upgrade --cask reliant',
  upgradeUrl: 'https://reliantlabs.io/download',
};

/** A manager whose upgrade notices and stream notices are recorded. */
function harness() {
  const instance = new BackendManager();
  instance.process = { pid: 4242, killed: false };
  const notices = [];
  const streams = [];
  instance.onDaemonUpgradeNotice((notice) => notices.push(notice));
  instance._onDaemonConnected = (stream) => streams.push(stream);
  instance.readDaemonState = () => null;
  return { instance, notices, streams };
}

test('the upgrade prefix and supervisor match the Go constants they mirror', () => {
  const source = goStateSource();
  const prefix = source.match(/UpgradeNoticePrefix\s*=\s*"([^"]+)"/);
  assert.ok(prefix, 'UpgradeNoticePrefix not found in daemonstate/state.go');
  assert.equal(prefix[1], DAEMON_UPGRADE_NOTICE_PREFIX, 'Go and Electron disagree on the upgrade-notice prefix');

  const env = source.match(/SupervisorEnv\s*=\s*"([^"]+)"/);
  assert.ok(env, 'SupervisorEnv not found in daemonstate/state.go');
  assert.equal(env[1], DAEMON_SUPERVISOR_ENV_VAR);

  const electron = source.match(/SupervisorElectron\s*=\s*"([^"]+)"/);
  assert.ok(electron, 'SupervisorElectron not found in daemonstate/state.go');
  assert.equal(electron[1], DAEMON_SUPERVISOR_ELECTRON);
});

test('every daemon spawn declares this app as its supervisor, or the daemon prints no notice', () => {
  const manager = new BackendManager();
  manager.daemonPort = 9191;
  manager.apiUrl = 'http://localhost:8090';
  const env = manager.buildDaemonEnv((name) => `/tmp/reliant-test-app/${name}`);
  assert.equal(env[DAEMON_SUPERVISOR_ENV_VAR], DAEMON_SUPERVISOR_ELECTRON);
});

test('parses the line the Go daemon prints', () => {
  const payload = GO_NOTICE_LINE.slice(DAEMON_UPGRADE_NOTICE_PREFIX.length).trim();
  assert.deepEqual(parseDaemonUpgradeNotice(payload), PARSED_GO_NOTICE);
});

test('a notice with missing, ill-typed or unknown fields still parses', () => {
  assert.deepEqual(parseDaemonUpgradeNotice('{"urgency":"tectonic","deadline_unix_ms":"soon","extra":1}'), {
    targetVersion: '',
    targetCommit: '',
    commitTimeUnix: 0,
    urgency: 'normal',
    deadlineUnixMs: 0,
    rolloutId: '',
    reason: '',
    upgradeCommand: '',
    upgradeUrl: '',
  });
  assert.equal(parseDaemonUpgradeNotice('{"urgency":"EMERGENCY"}').urgency, 'emergency');
});

test('a payload that is not a JSON object is not a notice', () => {
  for (const payload of ['', 'not json', '[1,2]', 'null', '"text"', '42']) {
    assert.equal(parseDaemonUpgradeNotice(payload), null, payload);
  }
});

test('an upgrade notice on stdout reaches the updater', () => {
  const { instance, notices, streams } = harness();
  instance.consumeDaemonStdout(`${GO_NOTICE_LINE}\n`);
  assert.deepEqual(notices, [PARSED_GO_NOTICE]);
  assert.deepEqual(streams, [], 'an upgrade notice is not a stream transition');
});

test('a notice split across chunks, or among log lines, still reaches the updater once', () => {
  const { instance, notices } = harness();
  const split = Math.floor(GO_NOTICE_LINE.length / 2);
  instance.consumeDaemonStdout(`time=2026-11-24 level=INFO msg="serving"\n${GO_NOTICE_LINE.slice(0, split)}`);
  assert.deepEqual(notices, [], 'a half-line must not fire');
  instance.consumeDaemonStdout(`${GO_NOTICE_LINE.slice(split)}\n${DAEMON_STREAM_NOTICE_PREFIX} connected\n`);
  assert.deepEqual(notices, [PARSED_GO_NOTICE]);
});

test('an unreadable upgrade line, or one with no listener, is only logged', () => {
  const { instance, notices } = harness();
  instance.consumeDaemonStdout(`${DAEMON_UPGRADE_NOTICE_PREFIX} {broken\n`);
  assert.deepEqual(notices, []);

  // Before main.js wires the updater (or in a manager nobody wired), a notice
  // is logged and dropped, never thrown.
  const bare = new BackendManager();
  bare.readDaemonState = () => null;
  assert.doesNotThrow(() => bare.consumeDaemonStdout(`${GO_NOTICE_LINE}\n`));
});

test('a throwing updater does not break reading the daemon output', () => {
  const { instance, streams } = harness();
  instance.onDaemonUpgradeNotice(() => {
    throw new Error('updater exploded');
  });
  assert.doesNotThrow(() =>
    instance.consumeDaemonStdout(`${GO_NOTICE_LINE}\n${DAEMON_STREAM_NOTICE_PREFIX} connected\n`),
  );
  assert.deepEqual(streams, ['connected'], 'the next line is still read');
});
