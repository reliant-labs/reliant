// The environment Electron spawns the daemon with, as far as the daemon's
// guard settings go (reliant's DaemonConfig: RELIANT_BACKGROUND_IDLE_REAP,
// RELIANT_HEAVY_SHARE, RELIANT_COMMAND_THREADS, ...).
//
// Electron sets none of them: a desktop daemon runs each at the daemon's
// default unless the user exported a value, and then it runs the user's.
// What each value becomes once loaded — unset is the 4h idle window, a
// duration is that window, 0 is off — is pinned on the Go side by
// cmd/reliant/cmd/daemon_guards_test.go (TestDaemonGuards_BackgroundIdleReap).
// This file pins the other half: which value reaches the daemon at all.

const test = require('node:test');
const assert = require('node:assert/strict');

const BackendManager = require('../src/backend-manager');

const GUARD_SETTINGS = [
  'RELIANT_BACKGROUND_IDLE_REAP',
  'RELIANT_CHILD_PRIORITY',
  'RELIANT_COMMAND_THREADS',
  'RELIANT_DISK_RECLAIM_AT',
  'RELIANT_DISK_REFUSE_AT',
  'RELIANT_HEAVY_COMMANDS',
  'RELIANT_HEAVY_SHARE',
];

const fakeAppPath = (name) => `/tmp/reliant-test-app/${name}`;

function daemonEnv() {
  const manager = new BackendManager();
  manager.daemonPort = 9191;
  manager.apiUrl = 'http://localhost:8090';
  return manager.buildDaemonEnv(fakeAppPath);
}

// Sets the variables for the duration of one test, restoring what was there.
function withEnv(t, vars) {
  for (const [name, value] of Object.entries(vars)) {
    const previous = process.env[name];
    if (value === undefined) delete process.env[name];
    else process.env[name] = value;
    t.after(() => {
      if (previous === undefined) delete process.env[name];
      else process.env[name] = previous;
    });
  }
}

test('Electron sets none of the daemon\'s guard settings: unset, each runs its default', (t) => {
  withEnv(t, Object.fromEntries(GUARD_SETTINGS.map((name) => [name, undefined])));
  const env = daemonEnv();
  for (const name of GUARD_SETTINGS) {
    assert.equal(env[name], undefined, `${name} is set on the daemon Electron spawns: ${env[name]}`);
  }
  // The env is the real spawn env, not an empty stand-in.
  assert.equal(env.TOOLS_DAEMON_PORT, '9191');
  assert.equal(env.RELIANT_SERVER_URL, 'http://localhost:8090');
});

test('a value the user exported reaches the daemon unchanged, including 0 (off)', (t) => {
  for (const value of ['90m', '0']) {
    withEnv(t, { RELIANT_BACKGROUND_IDLE_REAP: value, RELIANT_HEAVY_SHARE: '6' });
    const env = daemonEnv();
    assert.equal(env.RELIANT_BACKGROUND_IDLE_REAP, value);
    assert.equal(env.RELIANT_HEAVY_SHARE, '6');
  }
});
