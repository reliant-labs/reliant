/**
 * The decisions behind the desktop updater, kept out of main.js so they are
 * testable without Electron: what a check's result means, when a check may
 * run, which state a newly opened window should show, and what the app menu's
 * "Check for Updates…" tells the user. main.js owns electron-updater, the IPC
 * and the windows; it calls into this file for every one of those choices.
 */

// Statuses that mean an update is waiting for the user: found, downloading,
// or downloaded and ready to install.
const PENDING_TYPES = new Set(["available", "download-progress", "downloaded"]);

function isUpdatePending(status) {
  return Boolean(status) && PENDING_TYPES.has(status.type);
}

// The update feed every install reads: `latest` (latest-mac.yml, latest.yml,
// latest-linux*.yml on downloads.reliantlabs.io). A prerelease version once
// chose the `alpha` channel, but nothing has published alpha since RC tags
// became `rc/vX.Y.Z-rc.N`, so an install that picked it stopped updating. The
// alpha feed files stay in the bucket for installs built before this change.
function updateFeedFor(_version) {
  return { channel: "latest", allowPrerelease: false };
}

// Once a download has started, a check would lose it: `checking-for-update`
// resets main.js's download bookkeeping (including the path of a chunked
// download that installUpdate needs), and electron-updater re-announces a
// downloaded version as merely "available". So the startup, periodic and
// manual checks all stand down while a download is in flight or done.
// `downloadInFlight` covers the stretch the status cannot: a download that has
// been asked for but has not reported progress yet.
function canCheckForUpdates(status, downloadInFlight = false) {
  if (downloadInFlight) return false;
  return !status || (status.type !== "download-progress" && status.type !== "downloaded");
}

/**
 * Runs the download-update IPC's work so that its outcome always reaches the
 * update state, and so a second request joins the first instead of starting
 * a second download.
 *
 * The download can fail after progress events without electron-updater
 * emitting `error` (the chunked fallback failing, or retries running out); it
 * only returns `{ error }` to the caller. Unreported, that left the state at
 * "download-progress" for the rest of the session: every check stood down,
 * and the sidebar showed a dead download with no way to retry. `onFailure`
 * reports it (main.js broadcasts an "error" status, which nextUpdateState
 * turns back into a retryable "available").
 *
 * @param {object} deps
 * @param {() => Promise<{success?: boolean, error?: string}>} deps.download
 * @param {(message: string) => void} deps.onFailure
 */
function createDownloadRunner({ download, onFailure }) {
  let inFlight = null;

  function run() {
    if (!inFlight) {
      inFlight = (async () => {
        try {
          return await download();
        } catch (error) {
          return { error: errorMessage(error) };
        }
      })()
        .then((result) => {
          if (result && result.error) onFailure(result.error);
          return result;
        })
        .finally(() => {
          inFlight = null;
        });
    }
    return inFlight;
  }

  return { run, isInFlight: () => inFlight !== null };
}

/**
 * Reads electron-updater's checkForUpdates() result. It resolves null only
 * when the updater is inactive (an unpackaged build). Otherwise it ALWAYS
 * resolves an object whose `updateInfo` describes the latest release, even
 * when that release is the one running, so `isUpdateAvailable` is the only
 * signal that an update exists. Treating a non-null result as "an update" is
 * what made the menu item silent when the app was already up to date.
 */
function classifyCheckResult(result) {
  if (!result) return "inactive";
  return result.isUpdateAvailable ? "available" : "up-to-date";
}

/**
 * The state a window should show after `event`, given the state before it.
 * main.js keeps this so a window that opens or reloads after a check still
 * knows an update is waiting, rather than learning of it at the next check.
 *
 * - "checking" changes nothing: what was found before is still true until
 *   the check answers.
 * - An "error" leaves the previous state in place, except that a failed
 *   download falls back to "available", from where the user can retry it.
 * - "forced" (createForcedUpdate) marks the state `forced` with the server's
 *   `minVersion`, and every later state keeps the mark: the update is no
 *   longer optional for the rest of the session.
 */
function nextUpdateState(previous, event) {
  if (event.type === "forced") {
    // A found or downloading update stays what the window shows; it is now
    // the forced one. Otherwise the forced update has not found one yet.
    const base = isUpdatePending(previous) ? previous : { type: "forced" };
    return { ...base, forced: true, minVersion: event.minVersion };
  }
  const next = unforcedNextState(previous, event);
  // Forced is sticky for the session: the server will not serve this version
  // again, so no later event makes the update optional.
  if (previous && previous.forced && next && !next.forced) {
    return { ...next, forced: true, minVersion: previous.minVersion };
  }
  return next;
}

function unforcedNextState(previous, event) {
  switch (event.type) {
    case "checking":
      return previous;
    case "error":
      if (previous && previous.type === "download-progress") {
        return { type: "available", version: previous.version };
      }
      return previous;
    default:
      return event;
  }
}

/**
 * The forced update: the server requires a newer version than this one
 * (the renderer's forceUpdate(minVersion) IPC, after an `upgrade_required`
 * refusal or a served minimum above this version). Unlike every other update
 * it does not wait for the user: it checks, downloads and installs, and the
 * renderer's update dialog cannot be dismissed while it runs.
 *
 * Resolves the outcome: "installing" (the app is quitting to install),
 * "up-to-date" (the update feed has nothing newer, so the user must install
 * from the download page), "inactive" (an unpackaged build), or "error" with
 * the message (the dialog offers to retry, which calls this again).
 *
 * @param {object} deps
 * @param {() => object|null} deps.getStatus   the updater's state (nextUpdateState)
 * @param {() => boolean} deps.isDownloading   a download has been asked for and not finished
 * @param {() => Promise<object|null>} deps.check   autoUpdater.checkForUpdates
 * @param {() => Promise<{success?: boolean, error?: string}>} deps.download   the download runner; joins one in flight
 * @param {() => Promise<{success?: boolean, error?: string}>} deps.install   quits and installs
 * @param {(minVersion: string) => void} deps.announce   broadcasts the forced state to every window
 */
function createForcedUpdate({ getStatus, isDownloading, check, download, install, announce }) {
  let inFlight = null;

  async function run(minVersion) {
    announce(minVersion);
    const fetched = await checkAndDownload({ getStatus, isDownloading, check, download });
    if (fetched.outcome !== "downloaded") return fetched;
    let installed;
    try {
      installed = await install();
    } catch (error) {
      return { outcome: "error", error: errorMessage(error) };
    }
    if (installed && installed.error) return { outcome: "error", error: installed.error };
    return { outcome: "installing" };
  }

  // A second request (another window, a retry while it runs) joins the first.
  return function forceUpdate(minVersion) {
    if (!inFlight) {
      inFlight = run(minVersion).finally(() => {
        inFlight = null;
      });
    }
    return inFlight;
  };
}

/**
 * Brings the update to "downloaded": checks when nothing has been found yet
 * (a found or downloading update needs no check, and a check would discard a
 * download in flight), then downloads, joining a download in flight. Never
 * installs. Shared by the forced update and the daemon's upgrade request.
 *
 * Resolves "downloaded", "up-to-date" (the feed has nothing newer),
 * "inactive" (an unpackaged build), or "error" with the message.
 */
async function checkAndDownload({ getStatus, isDownloading, check, download }) {
  const status = getStatus();
  if (status && status.type === "downloaded") return { outcome: "downloaded" };
  if (canCheckForUpdates(status, isDownloading()) && !(status && status.type === "available")) {
    let result;
    try {
      result = await check();
    } catch (error) {
      return { outcome: "error", error: errorMessage(error) };
    }
    const found = classifyCheckResult(result);
    if (found !== "available") return { outcome: found };
  }
  let downloaded;
  try {
    downloaded = await download();
  } catch (error) {
    return { outcome: "error", error: errorMessage(error) };
  }
  if (downloaded && downloaded.error) return { outcome: "error", error: downloaded.error };
  return { outcome: "downloaded" };
}

/**
 * The daemon's upgrade request (phase E1 of forge
 * docs/proposals/self-upgrade.md): the platform asked the daemon this app
 * runs to upgrade, and the daemon printed it on stdout
 * (daemon-contract.js DAEMON_UPGRADE_NOTICE_PREFIX). The daemon is inside
 * this app's bundle, so this app's updater is what upgrades it.
 *
 * Owner decision D7: the app downloads the update by itself and asks the
 * user to restart; it NEVER installs. There is no install dependency here to
 * call. The windows show the ready update with the request attached
 * (withDaemonUpgrade), which opens the update dialog when the download
 * finishes. Owner decision D6: nothing blocks — the daemon keeps serving, and
 * the user may say Later, even when the request is overdue (isUpgradeOverdue
 * only strengthens the dialog's wording). The forced update's undismissable
 * install (createForcedUpdate) is for a server that refuses this app, which
 * is not the case here.
 *
 * Resolves the outcome of checkAndDownload. A notice while one is being acted
 * on joins it, after `announce` has recorded the newer request. Failed checks,
 * missing releases and failed downloads retry after one minute with exponential
 * backoff capped at one hour; success stops retries without installing.
 *
 * @param {object} deps
 * @param {() => object|null} deps.getStatus   the updater's state (nextUpdateState)
 * @param {() => boolean} deps.isDownloading   a download has been asked for and not finished
 * @param {() => Promise<object|null>} deps.check   autoUpdater.checkForUpdates
 * @param {() => Promise<{success?: boolean, error?: string}>} deps.download   the download runner; joins one in flight
 * @param {(request: object) => void} deps.announce   records the request and shows it with the update the windows have
 */
function createDaemonUpgradeResponder({
  getStatus, isDownloading, check, download, announce,
  schedule = setTimeout, cancel = clearTimeout,
}) {
  let inFlight = null;
  let pendingRequest = null;
  let retryTimer = null;
  let retryDelay = 60 * 1000;
  const maxRetryDelay = 60 * 60 * 1000;

  // The daemon emits each request only once. Electron owns retries until the
  // download succeeds, including when the feed has not published it yet.
  function respond(request) {
    pendingRequest = request;
    announce(request);
    if (!inFlight) {
      if (retryTimer !== null) cancel(retryTimer);
      retryTimer = null;
      inFlight = checkAndDownload({ getStatus, isDownloading, check, download }).then((result) => {
        if (result.outcome === "error" || result.outcome === "up-to-date") {
          retryTimer = schedule(() => {
            retryTimer = null;
            return respond(pendingRequest);
          }, retryDelay);
          retryTimer.unref?.();
          retryDelay = Math.min(retryDelay * 2, maxRetryDelay);
        } else {
          retryDelay = 60 * 1000;
        }
        return result;
      }).finally(() => {
        inFlight = null;
      });
    }
    return inFlight;
  }
  return respond;
}

/**
 * `status` as the windows should see it while the daemon has asked for an
 * upgrade: with the request attached as `daemonUpgrade`, so the renderer
 * opens the update dialog when it is downloaded and words it by urgency. A
 * renderer that predates the field ignores it.
 */
function withDaemonUpgrade(status, request) {
  if (!status || !request) return status;
  return { ...status, daemonUpgrade: request };
}

/**
 * Whether a daemon upgrade request is overdue: a security or emergency
 * request whose deadline has passed at `nowMs`. The update dialog then says
 * so plainly; it is still the user's choice when to restart.
 */
function isUpgradeOverdue(request, nowMs) {
  if (!request) return false;
  if (request.urgency !== "security" && request.urgency !== "emergency") return false;
  return request.deadlineUnixMs > 0 && nowMs >= request.deadlineUnixMs;
}

/** A minimum version from the renderer, or "" when it is not a version token. */
function cleanMinVersion(value) {
  return typeof value === "string" && /^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$/.test(value) ? value : "";
}

function errorMessage(error) {
  if (error && typeof error.message === "string" && error.message) return error.message;
  return String(error);
}

/**
 * The app menu's "Check for Updates…". It always answers: an update opens the
 * in-app update dialog (`reveal`, which brings a window forward first), and
 * every other outcome gets a native dialog, so there is an answer even with no
 * window open.
 *
 * @param {object} deps
 * @param {() => object|null} deps.getStatus   the updater's current state (nextUpdateState)
 * @param {() => boolean} [deps.isDownloading]   a download has been asked for and not finished
 * @param {() => Promise<object|null>} deps.check   autoUpdater.checkForUpdates
 * @param {(status: object) => void} deps.reveal   shows the update dialog with `status`
 * @param {(options: object) => Promise<unknown>} deps.showMessage   a native message box
 * @param {() => string} deps.currentVersion
 */
function createMenuUpdateCheck({ getStatus, isDownloading = () => false, check, reveal, showMessage, currentVersion }) {
  let inFlight = null;

  async function run() {
    // A download in flight or done: show it rather than re-check.
    const before = getStatus();
    if (!canCheckForUpdates(before, isDownloading())) {
      reveal(before);
      return "revealed";
    }

    let result;
    try {
      result = await check();
    } catch (error) {
      await showMessage({
        type: "error",
        title: "Update check failed",
        message: "Reliant couldn't check for updates.",
        detail: errorMessage(error),
      });
      return "error";
    }

    switch (classifyCheckResult(result)) {
      case "available": {
        const info = result.updateInfo || {};
        reveal(getStatus() || { type: "available", version: info.version, releaseNotes: info.releaseNotes });
        return "available";
      }
      case "up-to-date":
        await showMessage({
          type: "info",
          title: "No updates",
          message: "You're up to date.",
          detail: `Reliant ${currentVersion()} is the latest version.`,
        });
        return "up-to-date";
      default:
        await showMessage({
          type: "info",
          title: "Updates unavailable",
          message: "This build of Reliant doesn't update itself.",
          detail: "Install Reliant from the download page to get automatic updates.",
        });
        return "inactive";
    }
  }

  // A second click while the first check is still running joins it rather
  // than stacking a second dialog.
  return function checkFromMenu() {
    if (!inFlight) {
      inFlight = run().finally(() => {
        inFlight = null;
      });
    }
    return inFlight;
  };
}

module.exports = {
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
};
