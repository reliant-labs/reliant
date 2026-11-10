const test = require("node:test");
const assert = require("node:assert/strict");
const {
  canCheckForUpdates,
  classifyCheckResult,
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

function menuCheck({ status = null, check }) {
  const calls = { reveal: [], messages: [], checks: 0 };
  let current = status;
  const run = createMenuUpdateCheck({
    getStatus: () => current,
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
