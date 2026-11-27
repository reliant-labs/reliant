const test = require("node:test");
const assert = require("node:assert/strict");
const {
  canCheckForUpdates,
  classifyCheckResult,
  cleanMinVersion,
  createDaemonUpgradeResponder,
  createDownloadRunner,
  createForcedUpdate,
  createMenuUpdateCheck,
  isUpdatePending,
  isUpgradeOverdue,
  nextUpdateState,
  updateFeedFor,
  withDaemonUpgrade,
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

test("nextUpdateState: forced marks the state, keeps a found update, and stays forced", () => {
  // Nothing found yet: the forced state itself.
  assert.deepEqual(nextUpdateState(null, { type: "forced", minVersion: "1.9.0" }), {
    type: "forced",
    forced: true,
    minVersion: "1.9.0",
  });
  // A found or downloading update becomes the forced one.
  const downloading = { type: "download-progress", version: "1.14.0", progress: { percent: 40 } };
  const forcedDownload = nextUpdateState(downloading, { type: "forced", minVersion: "1.9.0" });
  assert.equal(forcedDownload.type, "download-progress");
  assert.equal(forcedDownload.forced, true);

  // Every later state keeps the mark: the update is no longer optional.
  let state = nextUpdateState(null, { type: "forced", minVersion: "1.9.0" });
  for (const event of [
    { type: "checking" },
    { type: "available", version: "1.14.0" },
    { type: "download-progress", version: "1.14.0", progress: { percent: 10 } },
    { type: "error", error: "stalled" },
    { type: "not-available" },
    { type: "downloaded", version: "1.14.0" },
  ]) {
    state = nextUpdateState(state, event);
    assert.equal(state.forced, true, `after ${event.type}`);
    assert.equal(state.minVersion, "1.9.0", `after ${event.type}`);
  }
  assert.equal(state.type, "downloaded");
  // A failed forced download still falls back to a retryable "available".
  assert.equal(nextUpdateState(forcedDownload, { type: "error", error: "x" }).type, "available");
  // Without "forced", nothing is marked.
  assert.equal(nextUpdateState(null, { type: "available", version: "1.14.0" }).forced, undefined);
});

function forcedUpdate({ status = null, downloading = false, check, download, install } = {}) {
  const calls = { announced: [], checks: 0, downloads: 0, installs: 0 };
  let current = status;
  const run = createForcedUpdate({
    getStatus: () => current,
    isDownloading: () => downloading,
    check: async () => {
      calls.checks += 1;
      const result = await (check ? check() : AVAILABLE);
      if (result && result.isUpdateAvailable) current = { type: "available", version: result.updateInfo.version };
      return result;
    },
    download: async () => {
      calls.downloads += 1;
      const result = await (download ? download() : { success: true });
      if (!result.error) current = { type: "downloaded", version: "1.14.0" };
      return result;
    },
    install: async () => {
      calls.installs += 1;
      return install ? install() : { success: true };
    },
    announce: (minVersion) => calls.announced.push(minVersion),
  });
  return { run, calls };
}

test("forced update: checks, downloads and installs without waiting for the user", async () => {
  const { run, calls } = forcedUpdate();
  assert.deepEqual(await run("1.9.0"), { outcome: "installing" });
  assert.deepEqual(calls, { announced: ["1.9.0"], checks: 1, downloads: 1, installs: 1 });
});

test("forced update: installs a downloaded update at once, and joins a download in flight", async () => {
  const ready = forcedUpdate({ status: { type: "downloaded", version: "1.14.0" } });
  assert.deepEqual(await ready.run("1.9.0"), { outcome: "installing" });
  assert.equal(ready.calls.checks, 0);
  assert.equal(ready.calls.downloads, 0);
  assert.equal(ready.calls.installs, 1);

  // A download already running: no check (it would discard the download),
  // the runner's download() joins it.
  const inFlight = forcedUpdate({ status: { type: "available", version: "1.14.0" }, downloading: true });
  assert.deepEqual(await inFlight.run("1.9.0"), { outcome: "installing" });
  assert.equal(inFlight.calls.checks, 0);
  assert.equal(inFlight.calls.downloads, 1);

  // An update already found needs no second check either.
  const found = forcedUpdate({ status: { type: "available", version: "1.14.0" } });
  await found.run("1.9.0");
  assert.equal(found.calls.checks, 0);
});

test("forced update: reports a feed with nothing newer, and an inactive updater, without installing", async () => {
  const upToDate = forcedUpdate({ check: async () => UP_TO_DATE });
  assert.deepEqual(await upToDate.run("1.9.0"), { outcome: "up-to-date" });
  assert.equal(upToDate.calls.installs, 0);

  const inactive = forcedUpdate({ check: async () => null });
  assert.deepEqual(await inactive.run("1.9.0"), { outcome: "inactive" });
});

test("forced update: a failed check, download or install is an error the dialog can retry", async () => {
  const badCheck = forcedUpdate({
    check: async () => {
      throw new Error("offline");
    },
  });
  assert.deepEqual(await badCheck.run("1.9.0"), { outcome: "error", error: "offline" });

  const badDownload = forcedUpdate({ download: async () => ({ error: "stalled" }) });
  assert.deepEqual(await badDownload.run("1.9.0"), { outcome: "error", error: "stalled" });
  assert.equal(badDownload.calls.installs, 0);
  // The retry runs again rather than replaying the failure.
  assert.deepEqual(await badDownload.run("1.9.0"), { outcome: "error", error: "stalled" });
  assert.equal(badDownload.calls.downloads, 2);

  const badInstall = forcedUpdate({ install: async () => ({ error: "locked" }) });
  assert.deepEqual(await badInstall.run("1.9.0"), { outcome: "error", error: "locked" });
});

test("forced update: a second request while it runs joins the first", async () => {
  let release;
  const gate = new Promise((resolve) => (release = resolve));
  const { run, calls } = forcedUpdate({
    check: async () => {
      await gate;
      return AVAILABLE;
    },
  });
  const first = run("1.9.0");
  const second = run("1.9.0");
  release();
  assert.equal(await first, await second);
  assert.equal(calls.checks, 1);
  assert.equal(calls.installs, 1);
});

// main.js keeps the one updater state every window reads (get-update-status)
// by folding each broadcast through nextUpdateState, the forced announcement
// included. So the forced mark lives in the main process, not only in the
// window that asked: a "not-available" from the feed, and a background
// check's events after it, leave it forced, and a window that opens later
// reads it forced. A retry checks the feed again rather than settling on the
// previous check's "not-available".
test("forced update: the main process's state stays forced, and a retry checks the feed again", async () => {
  let state = null;
  const broadcast = (event) => (state = nextUpdateState(state, event));
  const feed = [UP_TO_DATE, AVAILABLE];
  const calls = { checks: 0, downloads: 0, installs: 0 };
  const run = createForcedUpdate({
    getStatus: () => state,
    isDownloading: () => false,
    // What main.js's autoUpdater listeners broadcast for each check.
    check: async () => {
      calls.checks += 1;
      broadcast({ type: "checking" });
      const result = feed.shift();
      broadcast(
        result.isUpdateAvailable
          ? { type: "available", version: result.updateInfo.version }
          : { type: "not-available", version: result.updateInfo.version },
      );
      return result;
    },
    download: async () => {
      calls.downloads += 1;
      broadcast({ type: "downloaded", version: "1.14.0" });
      return { success: true };
    },
    install: async () => {
      calls.installs += 1;
      return { success: true };
    },
    announce: (minVersion) => broadcast({ type: "forced", minVersion }),
  });

  assert.deepEqual(await run("1.9.0"), { outcome: "up-to-date" });
  // The feed said nothing newer; the state a new window reads is still forced.
  assert.equal(state.type, "not-available");
  assert.equal(state.forced, true);
  assert.equal(state.minVersion, "1.9.0");
  // A background check's events after it do not unforce it either.
  broadcast({ type: "checking" });
  broadcast({ type: "not-available", version: "1.13.0" });
  assert.equal(state.forced, true);

  // Try again: the feed is asked again, and now has the release.
  assert.deepEqual(await run("1.9.0"), { outcome: "installing" });
  assert.deepEqual(calls, { checks: 2, downloads: 1, installs: 1 });
  assert.equal(state.type, "downloaded");
  assert.equal(state.forced, true);
});

test("cleanMinVersion passes a version token and nothing else", () => {
  assert.equal(cleanMinVersion("1.9.0"), "1.9.0");
  assert.equal(cleanMinVersion("1.9.0-dev+abc"), "1.9.0-dev+abc");
  assert.equal(cleanMinVersion(""), "");
  assert.equal(cleanMinVersion(undefined), "");
  assert.equal(cleanMinVersion("1.9.0; rm -rf /"), "");
  assert.equal(cleanMinVersion({}), "");
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

// The daemon's upgrade request (createDaemonUpgradeResponder). These model
// main.js: every status is folded through nextUpdateState after
// withDaemonUpgrade attaches the newest request, and `announce` re-shows a
// pending update with the request attached. There is no install dependency at
// all: the responder cannot install, which is the point (D7).
const SECURITY_REQUEST = {
  targetVersion: "1.14.0",
  urgency: "security",
  deadlineUnixMs: 1_790_000_600_000,
  rolloutId: "roll-7",
  reason: "a security fix",
};

function daemonUpgrade({ status = null, downloading = false, check, download } = {}) {
  const calls = { checks: 0, downloads: 0, sent: [] };
  let request = null;
  let state = status;
  const broadcast = (event) => {
    const sent = withDaemonUpgrade(event, request);
    calls.sent.push(sent);
    state = nextUpdateState(state, sent);
  };
  const respond = createDaemonUpgradeResponder({
    getStatus: () => state,
    isDownloading: () => downloading,
    check: async () => {
      calls.checks += 1;
      const result = await (check ? check() : AVAILABLE);
      broadcast(
        result && result.isUpdateAvailable
          ? { type: "available", version: result.updateInfo.version }
          : { type: "not-available" },
      );
      return result;
    },
    download: async () => {
      calls.downloads += 1;
      const result = await (download ? download() : { success: true });
      if (!result.error) broadcast({ type: "downloaded", version: "1.14.0" });
      return result;
    },
    announce: (next) => {
      request = next;
      if (isUpdatePending(state)) broadcast(state);
    },
  });
  return { respond, calls, state: () => state };
}

test("daemon upgrade: checks and downloads by itself, and the ready update carries the request", async () => {
  const { respond, calls, state } = daemonUpgrade();
  assert.deepEqual(await respond(SECURITY_REQUEST), { outcome: "downloaded" });
  assert.equal(calls.checks, 1);
  assert.equal(calls.downloads, 1);
  // What every window hears, and what a window that opens later reads: the
  // downloaded update with the request, which opens the restart prompt.
  assert.equal(state().type, "downloaded");
  assert.deepEqual(state().daemonUpgrade, SECURITY_REQUEST);
  assert.ok(calls.sent.every((event) => event.daemonUpgrade === SECURITY_REQUEST));
});

test("daemon upgrade: an update already downloaded is shown again with the request, not fetched again", async () => {
  const { respond, calls, state } = daemonUpgrade({ status: { type: "downloaded", version: "1.14.0" } });
  assert.deepEqual(await respond(SECURITY_REQUEST), { outcome: "downloaded" });
  assert.equal(calls.checks, 0);
  assert.equal(calls.downloads, 0);
  assert.equal(calls.sent.length, 1, "the downloaded update is re-sent once, with the request");
  assert.equal(calls.sent[0].type, "downloaded");
  assert.deepEqual(state().daemonUpgrade, SECURITY_REQUEST);
});

test("daemon upgrade: a found or downloading update is downloaded without a second check", async () => {
  const found = daemonUpgrade({ status: { type: "available", version: "1.14.0" } });
  assert.deepEqual(await found.respond(SECURITY_REQUEST), { outcome: "downloaded" });
  assert.equal(found.calls.checks, 0);
  assert.equal(found.calls.downloads, 1);

  // A check now would discard the download in flight; the runner joins it.
  const inFlight = daemonUpgrade({ status: { type: "download-progress", version: "1.14.0" }, downloading: true });
  assert.deepEqual(await inFlight.respond(SECURITY_REQUEST), { outcome: "downloaded" });
  assert.equal(inFlight.calls.checks, 0);
  assert.equal(inFlight.calls.downloads, 1);
});

test("daemon upgrade: nothing newer, an inactive updater, or a failure downloads nothing more", async () => {
  const upToDate = daemonUpgrade({ check: async () => UP_TO_DATE });
  assert.deepEqual(await upToDate.respond(SECURITY_REQUEST), { outcome: "up-to-date" });
  assert.equal(upToDate.calls.downloads, 0);

  const inactive = daemonUpgrade({ check: async () => null });
  assert.deepEqual(await inactive.respond(SECURITY_REQUEST), { outcome: "inactive" });

  const offline = daemonUpgrade({
    check: async () => {
      throw new Error("offline");
    },
  });
  assert.deepEqual(await offline.respond(SECURITY_REQUEST), { outcome: "error", error: "offline" });

  const stalled = daemonUpgrade({ download: async () => ({ error: "stalled" }) });
  assert.deepEqual(await stalled.respond(SECURITY_REQUEST), { outcome: "error", error: "stalled" });
  // The next notice tries again rather than replaying the failure.
  await stalled.respond(SECURITY_REQUEST);
  assert.equal(stalled.calls.downloads, 2);
});

test("daemon upgrade: a notice while one is acted on joins it, and the newer request is the one shown", async () => {
  let release;
  const gate = new Promise((resolve) => (release = resolve));
  const { respond, calls, state } = daemonUpgrade({
    check: async () => {
      await gate;
      return AVAILABLE;
    },
  });
  const sooner = { ...SECURITY_REQUEST, deadlineUnixMs: SECURITY_REQUEST.deadlineUnixMs - 60_000 };
  const first = respond(SECURITY_REQUEST);
  const second = respond(sooner);
  assert.equal(first, second);
  release();
  await first;
  assert.equal(calls.checks, 1);
  assert.equal(calls.downloads, 1);
  assert.deepEqual(state().daemonUpgrade, sooner);
});

test("daemon upgrade: the forced mark and the request coexist in the main process's state", () => {
  let state = nextUpdateState(null, { type: "forced", minVersion: "1.9.0" });
  state = nextUpdateState(state, withDaemonUpgrade({ type: "downloaded", version: "1.14.0" }, SECURITY_REQUEST));
  assert.equal(state.forced, true);
  assert.deepEqual(state.daemonUpgrade, SECURITY_REQUEST);
});

test("withDaemonUpgrade attaches the request only when there is one", () => {
  const status = { type: "available", version: "1.14.0" };
  assert.equal(withDaemonUpgrade(status, null), status);
  assert.equal(withDaemonUpgrade(null, SECURITY_REQUEST), null);
  assert.deepEqual(withDaemonUpgrade(status, SECURITY_REQUEST), { ...status, daemonUpgrade: SECURITY_REQUEST });
  assert.equal(status.daemonUpgrade, undefined, "the status passed in is not changed");
});

test("isUpgradeOverdue: security and emergency requests past their deadline", () => {
  const deadline = SECURITY_REQUEST.deadlineUnixMs;
  assert.equal(isUpgradeOverdue(SECURITY_REQUEST, deadline - 1), false, "before the deadline");
  assert.equal(isUpgradeOverdue(SECURITY_REQUEST, deadline), true, "at the deadline");
  assert.equal(isUpgradeOverdue({ ...SECURITY_REQUEST, urgency: "emergency" }, deadline + 1), true);
  assert.equal(isUpgradeOverdue({ ...SECURITY_REQUEST, urgency: "normal" }, deadline + 1), false, "normal is never overdue");
  assert.equal(isUpgradeOverdue({ ...SECURITY_REQUEST, deadlineUnixMs: 0 }, deadline + 1), false, "no deadline");
  assert.equal(isUpgradeOverdue(null, deadline), false);
});

// The alpha channel is retired: nothing publishes alpha*.yml since RC tags
// became rc/vX.Y.Z-rc.N, so an RC install that chose it was stuck on
// 1.6.4-rc1. Every version, prerelease or not, reads the latest feed.
test("every version, a prerelease included, follows the latest feed", () => {
  for (const version of ["1.32.0", "1.6.4-rc1", "1.33.0-rc.2", "1.7.0-beta.2", "2.0.0-alpha", ""]) {
    assert.deepEqual(updateFeedFor(version), { channel: "latest", allowPrerelease: false }, version);
  }
});
