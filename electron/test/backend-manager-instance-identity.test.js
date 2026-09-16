// "Electron must never pick the wrong daemon."
//
// Two failures, one root cause. `daemonDataDir()` returned the literal
// './data' in dev — resolved against whatever directory Electron happened to
// be launched from — so WHICH daemon this app managed depended on its cwd. On
// one developer machine that produced two live daemons, under `reliant/data`
// and `reliant/electron/data`, each invisible to the other and each answering
// `daemon stop` with "No daemon running".
//
// And because `readDaemonState()` returned whatever JSON sat at that path,
// every caller trusted a pid it had no claim on: checkHealth read a
// neighbour's daemon as its own health, and cleanupFromLockFile treated the
// same record as a process it was entitled to signal.
//
// The fix has two halves and both are pinned here:
//
//   1. the data dir is DERIVED from the instance key (origin, sub, workspace),
//      so the same inputs give the same directory from any cwd; and
//   2. the record is SELF-DESCRIBING — the daemon stamps its instance slug
//      into it — so a record that names another instance is discarded rather
//      than treated as weak evidence about ours.
//
// Nothing here touches the developer's real ~/.reliant: every instance
// directory assertion is computed, not created, and the one place a home
// directory is needed gets a temp one.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const BackendManager = require('../src/backend-manager');
const daemonCreds = require('../src/daemon-creds');
const { DAEMON_STATE_INSTANCE_FIELD } = require('../src/daemon-contract');

const DAEMON_PID = 4242;

/**
 * A BackendManager reading a throwaway data dir, with an explicit instance
 * key. `workspace` is pinned rather than probed so the key is deterministic
 * and no `git rev-parse` subprocess runs.
 */
function harness({ apiUrl = 'http://localhost:8090', sub = '', workspace } = {}) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'instance-identity-'));
  const manager = new BackendManager();

  manager.apiUrl = apiUrl;
  manager.instanceWorkspaceOverride = workspace ?? dataDir;
  manager.authStorage = sub ? { loadStoredAuth: () => ({ user: { id: sub } }) } : null;
  manager.daemonDataDir = () => dataDir;
  manager.process = { pid: DAEMON_PID, killed: false };
  manager.startupTimeout = 200;

  return {
    manager,
    dataDir,
    /** Write a record verbatim — the caller controls the instance stamp. */
    writeRecord: (record) =>
      fs.writeFileSync(path.join(dataDir, 'daemon-state.json'), JSON.stringify(record)),
  };
}

// ─── The slug mirror must match the Go implementation exactly ───────────────

// Captured from the real `daemoninstance.Resolve(origin, sub, ws).Slug()`.
// The Go side is authoritative: if a row fails, fix the JS mirror in
// daemon-creds.js rather than adjusting the expectation here. Split-brain —
// the daemon writing its record under one instance and reading credentials
// from another — is what drift produces, and it presents as "my PAT vanished".
const GO_SLUGS = [
  {
    why: 'a plain dev origin with no account',
    origin: 'http://localhost:8090',
    sub: '',
    workspace: '/Users/seanteeling/src/reliant-labs/reliant',
    slug: 'http-localhost-8090-0bf46daa/_default/reliant-62553a34',
  },
  {
    why: 'a different PORT is a different instance — the hash covers the full origin',
    origin: 'http://localhost:8690',
    sub: '',
    workspace: '/Users/seanteeling/src/reliant-labs/reliant',
    slug: 'http-localhost-8690-553138c4/_default/reliant-62553a34',
  },
  {
    why: 'a nested directory as an EXPLICIT workspace is its own instance',
    origin: 'http://localhost:8090',
    sub: '',
    workspace: '/Users/seanteeling/src/reliant-labs/reliant/electron',
    slug: 'http-localhost-8090-0bf46daa/_default/electron-791e8afa',
  },
  {
    why: 'a 36-char sub truncates to 32 readable chars while the hash still covers all of it',
    origin: 'https://api.reliantapi.com',
    sub: 'a6e15ec0-d1be-40c2-9c17-8d775613c904',
    workspace: '/Users/seanteeling/src/reliant-labs/reliant',
    slug:
      'https-api.reliantapi.com-c9232e15/' +
      'a6e15ec0-d1be-40c2-9c17-8d775613-327a9ca5/reliant-62553a34',
  },
  {
    why: 'the origin is lowercased and its path dropped BEFORE hashing',
    origin: 'HTTPS://API.Example.COM/path',
    sub: '',
    workspace: 'ci',
    slug: 'https-api.example.com-137b9e5e/_default/ci-1b158839',
  },
  {
    why: 'IPv6 brackets and colons collapse in the readable half; the hash carries identity',
    origin: 'http://[::1]:8080',
    sub: '',
    workspace: 'release',
    slug: 'http-1-8080-23f440d8/_default/release-a4d451ec',
  },
  {
    // The 32-char cap lands mid-port here, leaving the readable half saying
    // "844" for port 8443 — deliberately NOT repaired, because the hash is
    // what carries identity and a prefix that lies is still unique.
    why: 'the readable cap can truncate mid-token, and that is fine',
    origin: 'https://staging.reliantapi.com:8443/grpc',
    sub: 'sub-with-UPPER-and-symbols!!@@',
    workspace: '/tmp/some dir/with spaces',
    slug:
      'https-staging.reliantapi.com-844-a938c78c/' +
      'sub-with-upper-and-symbols-96401231/with-spaces-b82283b2',
  },
  {
    // A sub with nothing sanitizable degrades to "x" rather than to an empty
    // segment, and "/" as a workspace becomes "root" rather than "" — both of
    // which would otherwise collapse the path.
    why: 'segments with nothing readable degrade to "x" and "root", never to empty',
    origin: 'http://127.0.0.1:1',
    sub: '\u2026\u2026',
    workspace: '/',
    slug: 'http-127.0.0.1-1-10d8eb39/x-a7f878b8/root-8a5edab2',
  },
];

for (const { why, origin, sub, workspace, slug } of GO_SLUGS) {
  test(`instance slug matches Go: ${why}`, () => {
    const key = daemonCreds.instanceKey({ apiUrl: origin, sub, workspace });
    assert.equal(daemonCreds.instanceSlug(key), slug);
  });
}

test('an unparseable server URL names no instance and is refused, never defaulted', () => {
  // The one thing worse than refusing to start is starting against silently
  // the wrong backend. Same contract as Go's ErrInvalidOrigin.
  assert.throws(
    () => daemonCreds.instanceKey({ apiUrl: 'not a url', workspace: '/tmp/x' }),
    /invalid --server URL/,
  );
});

test('the data dir lives under the instance directory, not the working directory', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'fake-home-'));
  const key = daemonCreds.instanceKey({
    apiUrl: 'http://localhost:8090',
    workspace: '/Users/seanteeling/src/reliant-labs/reliant',
  });

  assert.equal(
    daemonCreds.instanceDataDir(key, { homeDir: home }),
    path.join(
      home,
      '.reliant',
      'instances',
      'http-localhost-8090-0bf46daa',
      '_default',
      'reliant-62553a34',
    ),
  );
});

// ─── A foreign record is not evidence about our daemon ─────────────────────

test('a record stamped with another instance is IGNORED, not trusted', () => {
  // THE regression. Before the instance stamp this record read as our
  // daemon's: same path, plausible pid, `connected`. checkHealth reported a
  // neighbour's daemon as ours being up.
  const { manager, writeRecord } = harness();
  writeRecord({
    [DAEMON_STATE_INSTANCE_FIELD]: 'http-localhost-9999-deadbeef/_default/other-12345678',
    pid: DAEMON_PID,
    stream: 'connected',
  });

  assert.equal(manager.readDaemonState(), null, 'read a foreign record as our own');
});

test('a foreign record does not make checkHealth report ready', async () => {
  const { manager, writeRecord } = harness();
  writeRecord({
    [DAEMON_STATE_INSTANCE_FIELD]: 'http-localhost-9999-deadbeef/_default/other-12345678',
    pid: DAEMON_PID,
    stream: 'connected',
  });

  assert.equal(await manager.checkHealth(), false);
});

test('a foreign record does not make isDaemonConnected or isAwaitingCredentials answer yes', () => {
  const { manager, writeRecord } = harness();
  writeRecord({
    [DAEMON_STATE_INSTANCE_FIELD]: 'http-localhost-9999-deadbeef/_default/other-12345678',
    pid: DAEMON_PID,
    stream: 'connected',
  });
  assert.equal(manager.isDaemonConnected(), false);

  writeRecord({
    [DAEMON_STATE_INSTANCE_FIELD]: 'http-localhost-9999-deadbeef/_default/other-12345678',
    pid: DAEMON_PID,
    stream: 'awaiting_credentials',
  });
  assert.equal(manager.isAwaitingCredentials(), false);
});

test('a record with NO instance field is UNKNOWN, and unknown is never ours', () => {
  // The third case, and a legitimate one rather than a defect: the Go side
  // derives the stamp from the data dir's position under ~/.reliant/instances,
  // so a container running with an explicit DAEMON_DATA_DIR=/data writes none
  // at all. "I cannot tell whose this is" must read as "not mine" — the
  // alternative is trusting a pid on the strength of its path, which is
  // exactly the thing that broke.
  const { manager, writeRecord } = harness();
  writeRecord({ pid: DAEMON_PID, stream: 'connected' });

  assert.equal(manager.readDaemonState(), null);
});

test('an unknown record is neither trusted nor warned about', () => {
  // It is an ordinary steady state for a container deployment, and this path
  // is polled every 50ms by waitForReady — logging it as an error or an
  // upgrade warning would be both wrong and a flood.
  const { manager, writeRecord } = harness();
  writeRecord({ pid: DAEMON_PID, stream: 'connected' });

  const logger = require('../src/logger');
  const noisy = [];
  const realWarn = logger.warn;
  const realError = logger.error;
  logger.warn = (...args) => noisy.push(['warn', ...args]);
  logger.error = (...args) => noisy.push(['error', ...args]);
  try {
    for (let i = 0; i < 5; i++) manager.readDaemonState();
  } finally {
    logger.warn = realWarn;
    logger.error = realError;
  }

  assert.deepEqual(noisy, [], `unknown-instance reads must stay quiet, got: ${JSON.stringify(noisy)}`);
});

test('an unknown record is reported as unidentifiable, distinctly from a foreign one', async () => {
  // Three different failures — no record, someone else's record, and a record
  // that names nobody — must not collapse into one message. Someone reading
  // "the daemon never started" about a daemon that started fine goes looking
  // in the wrong place.
  const unknown = harness();
  unknown.writeRecord({ pid: DAEMON_PID, stream: 'connected' });
  await assert.rejects(() => unknown.manager.waitForReady(), /carries no instance stamp/);

  const foreign = harness();
  foreign.writeRecord({
    [DAEMON_STATE_INSTANCE_FIELD]: 'http-localhost-9999-deadbeef/_default/other-12345678',
    pid: DAEMON_PID,
    stream: 'connected',
  });
  await assert.rejects(
    () => foreign.manager.waitForReady(),
    /belongs to instance "http-localhost-9999-deadbeef/,
  );

  const absent = harness();
  await assert.rejects(() => absent.manager.waitForReady(), /no runtime record written/);
});

test('our OWN stamped record is still read normally', () => {
  // The negative tests above are only meaningful if the positive path works:
  // a check that rejects everything would pass all of them.
  const { manager, writeRecord } = harness();
  writeRecord({
    [DAEMON_STATE_INSTANCE_FIELD]: manager.daemonInstanceSlug(),
    pid: DAEMON_PID,
    stream: 'connected',
  });

  const state = manager.readDaemonState();
  assert.equal(state.pid, DAEMON_PID);
  assert.equal(state.stream, 'connected');
});

test('cleanupFromLockFile will not signal a pid named by a foreign record', async (t) => {
  // The dangerous half of trusting the path. This function SIGTERMs the pid it
  // reads, so a record belonging to another instance is a live process we have
  // no claim on. A real orphan sweep must never reach it.
  const childProcess = require('node:child_process');
  const { manager, dataDir, writeRecord } = harness();

  // A pid that is certainly alive and certainly not ours to kill: this very
  // test process. classifyDaemonProcess would refuse it anyway; the point is
  // that we must not even ASK about a pid from a foreign record.
  writeRecord({
    [DAEMON_STATE_INSTANCE_FIELD]: 'http-localhost-9999-deadbeef/_default/other-12345678',
    pid: process.pid,
    stream: 'connected',
  });

  const real = childProcess.execSync;
  const kills = [];
  childProcess.execSync = (command, options) => {
    if (/^(kill -TERM|kill -9|taskkill)/.test(command)) {
      kills.push(command);
      return '';
    }
    return real(command, options);
  };
  t.after(() => {
    childProcess.execSync = real;
  });

  await manager.cleanupFromLockFile();

  assert.deepEqual(kills, [], `signalled a pid from another instance's record: ${kills.join(', ')}`);
  assert.equal(
    fs.existsSync(path.join(dataDir, 'daemon-state.json')),
    false,
    "a foreign record at OUR path is junk and should be cleared, just never acted on",
  );
});

// ─── Two instances, two daemons, no crosstalk ──────────────────────────────

test('two Electron instances with different keys do not see each other daemons', () => {
  // Different accounts on one origin and one machine — the multi-account case
  // that used to share a single `./data`.
  const a = harness({ sub: 'user-aaaa-1111', workspace: '/w/one' });
  const b = harness({ sub: 'user-bbbb-2222', workspace: '/w/one' });

  assert.notEqual(a.manager.daemonInstanceSlug(), b.manager.daemonInstanceSlug());

  // Point both at ONE directory — the worst case, and what './data' produced.
  // Even sharing a file, each must recognize only its own record.
  const shared = a.dataDir;
  b.manager.daemonDataDir = () => shared;

  fs.writeFileSync(
    path.join(shared, 'daemon-state.json'),
    JSON.stringify({
      [DAEMON_STATE_INSTANCE_FIELD]: a.manager.daemonInstanceSlug(),
      pid: DAEMON_PID,
      stream: 'connected',
    }),
  );

  assert.equal(a.manager.readDaemonState().pid, DAEMON_PID, "A must see its own daemon");
  assert.equal(b.manager.readDaemonState(), null, "B must not see A's daemon");
});

test('different origins are different instances', () => {
  // dev vs staging vs prod on one machine.
  const dev = harness({ apiUrl: 'http://localhost:8090', workspace: '/w/one' });
  const prod = harness({ apiUrl: 'https://api.reliantapi.com', workspace: '/w/one' });

  assert.notEqual(dev.manager.daemonInstanceSlug(), prod.manager.daemonInstanceSlug());
});

// ─── cwd independence: the original bug ────────────────────────────────────

test('launching from different cwds inside one worktree resolves the SAME data dir', () => {
  // The regression guard. `reliant/` and `reliant/electron/` are two
  // directories in ONE git worktree, and launching from each produced two
  // mutually-invisible daemons. The workspace is now the git toplevel, which
  // is the same answer from both, so both must land on one instance.
  const repoRoot = path.join(__dirname, '..', '..');

  const fromRoot = new BackendManager();
  const fromElectron = new BackendManager();
  for (const m of [fromRoot, fromElectron]) {
    m.apiUrl = 'http://localhost:8090';
    m.authStorage = null;
    m.isDevelopment = true;
  }

  // Both processes probe git from their own source location and get the repo
  // root; simulate that by letting the real probe run (it is cwd-independent
  // by construction — see daemonInstanceWorkspace).
  const rootWorkspace = fromRoot.daemonInstanceWorkspace();
  const electronWorkspace = fromElectron.daemonInstanceWorkspace();

  assert.equal(
    rootWorkspace,
    electronWorkspace,
    'the workspace must not depend on where the process started',
  );
  assert.equal(
    fromRoot.daemonDataDir(),
    fromElectron.daemonDataDir(),
    'two launches in one worktree must manage ONE daemon',
  );

  // And it must be the repo root specifically, not some nested directory —
  // that is what collapses reliant/ and reliant/electron/ into one instance.
  assert.equal(
    fs.realpathSync(rootWorkspace),
    fs.realpathSync(repoRoot),
    `expected the git toplevel (${repoRoot}), got ${rootWorkspace}`,
  );

  // Concretely: the slug is the repo's, not electron's.
  const repoKey = daemonCreds.instanceKey({
    apiUrl: 'http://localhost:8090',
    workspace: repoRoot,
  });
  assert.equal(fromRoot.daemonInstanceSlug(), daemonCreds.instanceSlug(repoKey));
});

test('an explicit workspace override still wins over the git probe', () => {
  // Highest precedence, matching the Go CLI: explicit, then
  // RELIANT_INSTANCE_WORKSPACE, then git toplevel, then cwd. This is how a
  // container pins a workspace that has no repository.
  const manager = new BackendManager();
  manager.apiUrl = 'http://localhost:8090';
  manager.authStorage = null;
  manager.instanceWorkspaceOverride = '/Users/seanteeling/src/reliant-labs/reliant/electron';

  assert.equal(
    manager.daemonInstanceSlug(),
    'http-localhost-8090-0bf46daa/_default/electron-791e8afa',
  );
});

test('the env override is honoured when no explicit workspace is set', () => {
  const previous = process.env[daemonCreds.ENV_INSTANCE_WORKSPACE];
  process.env[daemonCreds.ENV_INSTANCE_WORKSPACE] = 'ci';
  try {
    const manager = new BackendManager();
    manager.apiUrl = 'HTTPS://API.Example.COM/path';
    manager.authStorage = null;
    assert.equal(
      manager.daemonInstanceSlug(),
      'https-api.example.com-137b9e5e/_default/ci-1b158839',
    );
  } finally {
    if (previous === undefined) delete process.env[daemonCreds.ENV_INSTANCE_WORKSPACE];
    else process.env[daemonCreds.ENV_INSTANCE_WORKSPACE] = previous;
  }
});

test('the daemon is told the same data dir we read from', () => {
  // buildDaemonArgs and every reader must resolve from ONE value, or the app
  // watches a directory the daemon was never told to write to — which is the
  // failure mode this whole change exists to remove.
  const { manager } = harness();
  const args = manager.buildDaemonArgs();
  const dataDirArg = args[args.indexOf('--data-dir') + 1];

  assert.equal(dataDirArg, manager.daemonDataDir());
});
