// The orphan sweep is a COLD-START concern, and it is not free.
//
// cleanupOrphanedProcesses shells out to a blocking `ps aux | grep`. Measured
// on a dev machine: ~100ms per call, and it blocks the Electron main process's
// event loop while it runs. The dev-stack log shows it costing 93ms
// (28.964 → 29.057) on the post-sign-in daemon restart.
//
// That cost buys nothing on a restart. The sweep looks for daemons abandoned by
// a PREVIOUS app run — a crash, a force-quit, a failed update. On a restart we
// have just watched our own daemon exit, in this process, and no previous run
// can have materialised since we last looked. Skipping it also REMOVES risk:
// every sweep is another chance to misclassify a sibling dev stack's healthy
// daemon, which is the regression backend-manager-orphan-cleanup.test.js exists
// to prevent.
//
// The exception is a CRASH. A daemon that died badly may have left children, so
// the crash path re-arms the sweep.

const test = require('node:test');
const assert = require('node:assert/strict');
const childProcess = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const BackendManager = require('../src/backend-manager');

/**
 * Count `ps aux` scans while suppressing every real shell-out.
 *
 * The scan reports "no match" (grep's exit status 1), which cleanupOrphaned-
 * Processes treats as "nothing to clean" — so these tests measure how often the
 * sweep RUNS, with no process ever signalled.
 */
function countScans() {
  const real = childProcess.execSync;
  const scans = [];

  childProcess.execSync = (command, options) => {
    if (/^ps aux \| grep/.test(command)) {
      scans.push(command);
      const error = new Error('no match');
      error.status = 1;
      throw error;
    }
    if (/^(kill -TERM|kill -9|taskkill|pgrep)/.test(command)) {
      return '';
    }
    return real(command, options);
  };

  return {
    scans,
    restore: () => {
      childProcess.execSync = real;
    },
  };
}

/**
 * A BackendManager wired so start() reaches the sweep and then stops, without
 * spawning a real daemon: RELIANT_EXTERNAL_BACKEND makes start() adopt an
 * "already running" backend and return immediately after the sweep.
 */
function manager() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'orphan-sweep-once-'));
  const instance = new BackendManager();
  instance.isDevelopment = true;
  instance.devProcessSearchPattern = '/tmp/reliant-sweep-test/dist/reliant';
  instance.instanceId = 'reliant';
  instance.daemonDataDir = () => dataDir;
  instance.process = null;
  return instance;
}

/** Run start() in external-backend mode so it returns without spawning. */
async function startOnce(instance) {
  const prevExternal = process.env.RELIANT_EXTERNAL_BACKEND;
  const prevPort = process.env.TOOLS_DAEMON_PORT;
  process.env.RELIANT_EXTERNAL_BACKEND = '1';
  process.env.TOOLS_DAEMON_PORT = '9190';
  try {
    await instance.start();
  } finally {
    if (prevExternal === undefined) delete process.env.RELIANT_EXTERNAL_BACKEND;
    else process.env.RELIANT_EXTERNAL_BACKEND = prevExternal;
    if (prevPort === undefined) delete process.env.TOOLS_DAEMON_PORT;
    else process.env.TOOLS_DAEMON_PORT = prevPort;
  }
}

test('the first start sweeps for orphans', async () => {
  // Cold start: a previous run really may have left a daemon behind.
  const spy = countScans();
  try {
    await startOnce(manager());
    assert.equal(spy.scans.length, 1, 'cold start must sweep');
  } finally {
    spy.restore();
  }
});

test('a restart does NOT sweep again', async () => {
  // The sign-in path. ~93ms of blocking ps aux, to rediscover nothing.
  const spy = countScans();
  try {
    const instance = manager();
    await startOnce(instance);
    await startOnce(instance);
    await startOnce(instance);
    assert.equal(
      spy.scans.length,
      1,
      'only the cold start should sweep; restarts must not pay for it again',
    );
  } finally {
    spy.restore();
  }
});

test('a crashed daemon re-arms the sweep', async () => {
  // A daemon that died badly may have left children — exactly what the sweep
  // is for — so the restart that follows a crash must sweep again.
  const spy = countScans();
  try {
    const instance = manager();
    await startOnce(instance);
    assert.equal(spy.scans.length, 1);

    // What the exit handler does on an unclean exit before handleCrash().
    instance.hasSweptOrphans = false;

    await startOnce(instance);
    assert.equal(spy.scans.length, 2, 'the post-crash restart must sweep');
  } finally {
    spy.restore();
  }
});

test('each BackendManager sweeps on its own first start', async () => {
  // The flag is per-instance, not module state: a second manager in the same
  // process (tests, a re-created backend) still gets its cold-start sweep.
  const spy = countScans();
  try {
    await startOnce(manager());
    await startOnce(manager());
    assert.equal(spy.scans.length, 2);
  } finally {
    spy.restore();
  }
});
