// Ownership contract for startup orphan cleanup.
//
// This exists because cleanup used to decide what to kill by pattern-matching
// `ps aux` output against the daemon's binary path. That path is identical for
// two Electron stacks launched from the SAME worktree, so a starting Electron
// SIGTERMed — then SIGKILLed — the healthy, connected daemon of a stack that
// was running right then, stranding its in-flight tool calls. The doc comment
// claimed the binary path made this "per-worktree", which is true only across
// worktrees and is exactly why the bug survived review.
//
// A command line cannot say who owns a process, so these tests are written in
// terms of real lineage: a daemon whose parent is alive is somebody's, and a
// daemon reparented to init is nobody's. Every process below is one this test
// started itself.

const test = require('node:test');
const assert = require('node:assert/strict');
const childProcess = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const BackendManager = require('../src/backend-manager');

const BINARY = '/tmp/reliant-orphan-test/dist/reliant';

/** A ps-aux-shaped line for a daemon at `pid`, as the real scan would see it. */
function psLine(pid, command = `${BINARY} daemon start --port 9190 --data-dir ./data`) {
  return `seanteeling ${pid}  0.5  0.2 410659120 265792 s003  S  9:22PM  1:56.30 ${command}`;
}

/**
 * Replace child_process.execSync with a spy that RECORDS kill commands instead
 * of running them, serves canned output for the `ps aux | grep` scan, and
 * delegates everything else — notably the `ps -o ppid=` lineage probe — to the
 * real implementation. The liveness logic under test therefore runs against
 * real processes while no signal is ever actually delivered.
 */
function spyExecSync(psOutput) {
  const real = childProcess.execSync;
  const kills = [];

  childProcess.execSync = (command, options) => {
    if (/^ps aux \| grep/.test(command)) {
      if (!psOutput) {
        const error = new Error('no match');
        error.status = 1;
        throw error;
      }
      return psOutput;
    }
    if (/^(kill -TERM|kill -9|taskkill)/.test(command)) {
      kills.push(command);
      return '';
    }
    return real(command, options);
  };

  return {
    kills,
    restore: () => {
      childProcess.execSync = real;
    },
  };
}

/** A BackendManager in dev mode with a throwaway data dir and no daemon yet. */
function manager() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'orphan-cleanup-'));
  const instance = new BackendManager();

  instance.isDevelopment = true;
  instance.devBinaryPath = BINARY;
  instance.devProcessSearchPattern = BINARY;
  instance.instanceId = 'reliant';
  instance.daemonDataDir = () => dataDir;
  // The bug's headline condition: a second Electron has not spawned its own
  // daemon yet, so the old `pid === this.process.pid` guard matches nothing.
  instance.process = null;

  return { instance, dataDir };
}

/**
 * Spawn a supervisor that spawns a long-lived grandchild and stays alive,
 * mimicking "another dev stack's Electron owning its daemon". Resolves with
 * the grandchild's pid.
 */
function spawnOwnedGrandchild(started) {
  const supervisor = childProcess.spawn(
    process.execPath,
    [
      '-e',
      // process.stdout.write, not console.log: console.log inspects a number
      // and wraps it in ANSI colour codes, which parseInt reads as NaN.
      `const { spawn } = require("child_process");
       const child = spawn("sleep", ["45"], { stdio: "ignore" });
       process.stdout.write(String(child.pid) + "\\n");
       setInterval(() => {}, 1000);`,
    ],
    { stdio: ['ignore', 'pipe', 'ignore'] }
  );
  started.push(supervisor.pid);

  return new Promise((resolve) => {
    supervisor.stdout.once('data', (chunk) => {
      const pid = parseInt(chunk.toString().trim(), 10);
      started.push(pid);
      resolve({ pid, supervisor });
    });
  });
}

/**
 * Spawn a grandchild whose parent exits immediately, so the kernel reparents
 * it to init. This is what a crashed run actually leaves behind.
 */
function spawnRealOrphan(started) {
  const out = childProcess.execSync(
    `${process.execPath} -e 'const { spawn } = require("child_process"); ` +
      `const c = spawn("sleep", ["45"], { detached: true, stdio: "ignore" }); ` +
      `c.unref(); process.stdout.write(String(c.pid));'`,
    { encoding: 'utf8' }
  );
  const pid = parseInt(out.trim(), 10);
  started.push(pid);

  // Wait for the intermediate node to exit and the kernel to reparent.
  return new Promise((resolve) => {
    const check = () => {
      const ppid = parseInt(
        childProcess.execSync(`ps -o ppid= -p ${pid}`, { encoding: 'utf8' }).trim(),
        10
      );
      if (ppid <= 1) {
        resolve(pid);
      } else {
        setTimeout(check, 50);
      }
    };
    check();
  });
}

/** Kill only the pids this test started, and only if still alive. */
function reap(started) {
  for (const pid of started) {
    try {
      process.kill(pid, 'SIGKILL');
    } catch (e) {
      // Already gone.
    }
  }
}

test('a second Electron does NOT kill a live daemon owned by another stack', async (t) => {
  // The regression. Two stacks in one worktree share RELIANT_BACKEND_BIN, the
  // data dir and the `--data-dir ./data` argument, so the ps line below is
  // byte-for-byte what the *starting* instance sees for the *running* one.
  const started = [];
  t.after(() => reap(started));

  const { pid } = await spawnOwnedGrandchild(started);
  const { instance } = manager();
  const spy = spyExecSync(psLine(pid));
  t.after(() => spy.restore());

  await instance.cleanupOrphanedProcesses();

  assert.deepEqual(
    spy.kills,
    [],
    `signalled a daemon owned by a live parent: ${spy.kills.join(', ')}`
  );
  assert.ok(instance.isProcessAlive(pid), 'the other stack\'s daemon should still be running');
});

test('a genuinely orphaned daemon is still cleaned up', async (t) => {
  // The purpose the module exists for — the fix must not be "stop cleaning up".
  const started = [];
  t.after(() => reap(started));

  const pid = await spawnRealOrphan(started);
  const { instance } = manager();
  const spy = spyExecSync(psLine(pid));
  t.after(() => spy.restore());

  await instance.cleanupOrphanedProcesses();

  assert.deepEqual(spy.kills, [`kill -TERM ${pid}`]);
});

test('an orphan is signalled, never SIGKILLed', async (t) => {
  // SIGKILL is what strands in-flight tool calls: the daemon drains on
  // SIGTERM, and a daemon slow to drain is one doing the work we would destroy.
  const started = [];
  t.after(() => reap(started));

  const pid = await spawnRealOrphan(started);
  const { instance } = manager();
  const spy = spyExecSync(psLine(pid));
  t.after(() => spy.restore());

  await instance.cleanupOrphanedProcesses();

  assert.ok(
    !spy.kills.some((command) => command.includes('kill -9')),
    `escalated to SIGKILL: ${spy.kills.join(', ')}`
  );
});

test('server subcommands stay out of the kill set even when orphaned', async (t) => {
  // Guards the earlier regression: matching the bare binary path reaped
  // cloud-dev's air-managed `reliant server api|gateway|worker`, and the
  // auto-mint that runs ~2s later 502ed against a dead downstream.
  const started = [];
  t.after(() => reap(started));

  const pid = await spawnRealOrphan(started);
  const { instance } = manager();
  const spy = spyExecSync(psLine(pid, `${BINARY} server api --port 8090`));
  t.after(() => spy.restore());

  await instance.cleanupOrphanedProcesses();

  assert.deepEqual(spy.kills, []);
});

test('classifyDaemonProcess refuses to convict when lineage is unreadable', (t) => {
  // "I could not tell" must mean "leave it alone". If a lineage probe failure
  // fell through to killing, the fix would be one flaky `ps` away from the bug.
  const { instance } = manager();
  instance.readProcessParent = () => ({ status: 'unknown' });

  const { orphaned } = instance.classifyDaemonProcess(4242);
  assert.equal(orphaned, false);
});

test('the lock-file path does not kill or forget a live stack\'s daemon', async (t) => {
  // Same-worktree stacks share ./data, so daemon-state.json names whichever
  // daemon started last — routinely a healthy one. Killing it is the bug; but
  // deleting its record is nearly as bad, because that record is how its own
  // Electron decides the daemon is ready.
  const started = [];
  t.after(() => reap(started));

  const { pid } = await spawnOwnedGrandchild(started);
  const { instance, dataDir } = manager();
  const statePath = path.join(dataDir, 'daemon-state.json');
  fs.writeFileSync(statePath, JSON.stringify({ pid, stream: 'connected' }));

  const spy = spyExecSync(null);
  t.after(() => spy.restore());

  await instance.cleanupFromLockFile();

  assert.deepEqual(spy.kills, [], `signalled a live stack's daemon: ${spy.kills.join(', ')}`);
  assert.ok(fs.existsSync(statePath), 'deleted a live daemon\'s runtime record');
});

test('the lock-file path still clears a record whose process is gone', async (t) => {
  const { instance, dataDir } = manager();
  const statePath = path.join(dataDir, 'daemon-state.json');

  // A pid that has certainly exited: spawn and wait for it.
  const dead = childProcess.spawnSync(process.execPath, ['-e', 'process.exit(0)']);
  fs.writeFileSync(statePath, JSON.stringify({ pid: dead.pid, stream: 'connected' }));

  const spy = spyExecSync(null);
  t.after(() => spy.restore());

  await instance.cleanupFromLockFile();

  assert.deepEqual(spy.kills, []);
  assert.ok(!fs.existsSync(statePath), 'stale record should be removed');
});
