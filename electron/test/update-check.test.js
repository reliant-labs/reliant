const test = require("node:test");
const assert = require("node:assert/strict");
const {
  canCheckForUpdates,
  classifyCheckResult,
  createDownloadRunner,
  createMenuUpdateCheck,
  isUpdatePending,
  nextUpdateState,
} = require("../src/update-check");

// electron-updater 6's checkForUpdates() result when the running version is
// the latest: non-null, with updateInfo describing the running release. The
// menu used to read any non-null result as "an update", so it said nothing.
const UP_TO_DATE = {
  isUpdateAvailable: false,
  updateInfo: { version: "1.13.0" },
  versionInfo: { version: "1.13.0" },
};
const AVAILABLE = {
  isUpdateAvailable: true,
  updateInfo: { version: "1.14.0", releaseNotes: "notes" },
  versionInfo: { version: "1.14.0" },
};

test("classifyCheckResult reads isUpdateAvailable, not the presence of a result", () => {
  assert.equal(classifyCheckResult(null), "inactive");
  assert.equal(classifyCheckResult(UP_TO_DATE), "up-to-date");
  assert.equal(classifyCheckResult(AVAILABLE), "available");
});

test("canCheckForUpdates stands down while a download is in flight or done", () => {
  assert.equal(canCheckForUpdates(null), true);
  assert.equal(canCheckForUpdates({ type: "available" }), true);
  assert.equal(canCheckForUpdates({ type: "not-available" }), true);
  assert.equal(canCheckForUpdates({ type: "download-progress" }), false);
  assert.equal(canCheckForUpdates({ type: "downloaded" }), false);
  // Asked for, no progress reported yet: the status still says "available".
  assert.equal(canCheckForUpdates({ type: "available" }, true), false);
});

// Drives the state the way main.js does: every status goes through
// nextUpdateState, and the runner's onFailure broadcasts an "error".
function downloadHarness(download) {
  let state = { type: "available", version: "1.14.0" };
  const apply = (status) => (state = nextUpdateState(state, status));
  const runner = createDownloadRunner({
    download: () => download(apply),
    onFailure: (error) => apply({ type: "error", error }),
  });
  return { runner, state: () => state };
}

test("a download that fails after progress, reported only by its return value, can be retried", async () => {
  const { runner, state } = downloadHarness(async (apply) => {
    apply({ type: "download-progress", version: "1.14.0", progress: { percent: 40 } });
    return { error: "Chunked download failed" };
  });

  assert.deepEqual(await runner.run(), { error: "Chunked download failed" });
  assert.deepEqual(state(), { type: "available", version: "1.14.0" });
  assert.equal(canCheckForUpdates(state(), runner.isInFlight()), true);
});

test("a download that throws after progress is reported and can be retried", async () => {
  const { runner, state } = downloadHarness(async (apply) => {
    apply({ type: "download-progress", version: "1.14.0", progress: { percent: 40 } });
    throw new Error("socket hang up");
  });

  assert.deepEqual(await runner.run(), { error: "socket hang up" });
  assert.equal(state().type, "available");
  assert.equal(runner.isInFlight(), false);
});

test("a download in flight holds off checks, and a second request joins it", async () => {
  let finish;
  let calls = 0;
  const { runner, state } = downloadHarness((apply) => {
    calls += 1;
    return new Promise((resolve) => {
      finish = () => {
        apply({ type: "downloaded", version: "1.14.0" });
        resolve({ success: true });
      };
    });
  });

  const first = runner.run();
  assert.equal(runner.run(), first);
  assert.equal(runner.isInFlight(), true);
  assert.equal(canCheckForUpdates(state(), runner.isInFlight()), false);

  finish();
  assert.deepEqual(await first, { success: true });
  assert.equal(calls, 1);
  assert.equal(state().type, "downloaded");
});

test("isUpdatePending covers found, downloading and downloaded", () => {
  assert.equal(isUpdatePending(null), false);
  assert.equal(isUpdatePending({ type: "checking" }), false);
  assert.equal(isUpdatePending({ type: "not-available" }), false);
  assert.equal(isUpdatePending({ type: "error" }), false);
  assert.equal(isUpdatePending({ type: "available" }), true);
  assert.equal(isUpdatePending({ type: "download-progress" }), true);
  assert.equal(isUpdatePending({ type: "downloaded" }), true);
});

test("nextUpdateState keeps what was found through a check and a failed download", () => {
  const available = { type: "available", version: "1.14.0" };
  assert.equal(nextUpdateState(available, { type: "checking" }), available);
  assert.equal(nextUpdateState(available, { type: "error", error: "offline" }), available);
  assert.equal(nextUpdateState(available, { type: "not-available" }).type, "not-available");

  const downloading = { type: "download-progress", version: "1.14.0", progress: { percent: 40 } };
  assert.deepEqual(nextUpdateState(downloading, { type: "error", error: "stalled" }), {
    type: "available",
    version: "1.14.0",
  });
  assert.equal(nextUpdateState(downloading, { type: "downloaded", version: "1.14.0" }).type, "downloaded");
  assert.equal(nextUpdateState(null, { type: "error", error: "offline" }), null);
});

function menuCheck({ status = null, downloading = false, check }) {
  const calls = { reveal: [], messages: [], checks: 0 };
  let current = status;
  const run = createMenuUpdateCheck({
    getStatus: () => current,
    isDownloading: () => downloading,
    check: async () => {
      calls.checks += 1;
      const result = await check();
      if (result && result.isUpdateAvailable) {
        // electron-updater emits update-available before the check resolves.
        current = { type: "available", version: result.updateInfo.version };
      }
      return result;
    },
    reveal: (s) => calls.reveal.push(s),
    showMessage: async (options) => calls.messages.push(options),
    currentVersion: () => "1.13.0",
  });
  return { run, calls };
}

test("menu check says the app is up to date", async () => {
  const { run, calls } = menuCheck({ check: async () => UP_TO_DATE });
  assert.equal(await run(), "up-to-date");
  assert.equal(calls.reveal.length, 0);
  assert.equal(calls.messages.length, 1);
  assert.equal(calls.messages[0].message, "You're up to date.");
  assert.match(calls.messages[0].detail, /1\.13\.0/);
});

test("menu check reveals the update dialog when an update exists", async () => {
  const { run, calls } = menuCheck({ check: async () => AVAILABLE });
  assert.equal(await run(), "available");
  assert.deepEqual(calls.reveal, [{ type: "available", version: "1.14.0" }]);
  assert.equal(calls.messages.length, 0);
});

test("menu check reports a failed check", async () => {
  const { run, calls } = menuCheck({
    check: async () => {
      throw new Error("net::ERR_INTERNET_DISCONNECTED");
    },
  });
  assert.equal(await run(), "error");
  assert.equal(calls.messages[0].type, "error");
  assert.match(calls.messages[0].detail, /ERR_INTERNET_DISCONNECTED/);
});

test("menu check explains an inactive updater", async () => {
  const { run, calls } = menuCheck({ check: async () => null });
  assert.equal(await run(), "inactive");
  assert.equal(calls.messages.length, 1);
});

test("menu check shows a downloaded update instead of re-checking", async () => {
  const downloaded = { type: "downloaded", version: "1.14.0" };
  const { run, calls } = menuCheck({ status: downloaded, check: async () => AVAILABLE });
  assert.equal(await run(), "revealed");
  assert.equal(calls.checks, 0);
  assert.deepEqual(calls.reveal, [downloaded]);
});

test("menu check does not check over a download that has not reported progress yet", async () => {
  const available = { type: "available", version: "1.14.0" };
  const { run, calls } = menuCheck({ status: available, downloading: true, check: async () => AVAILABLE });
  assert.equal(await run(), "revealed");
  assert.equal(calls.checks, 0);
  assert.deepEqual(calls.reveal, [available]);
});

test("a second menu click joins the running check", async () => {
  let resolve;
  const { run, calls } = menuCheck({ check: () => new Promise((r) => (resolve = r)) });
  const first = run();
  const second = run();
  assert.equal(first, second);
  resolve(UP_TO_DATE);
  await first;
  assert.equal(calls.checks, 1);
  assert.equal(calls.messages.length, 1);
});
