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
 */
function nextUpdateState(previous, event) {
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
  createDownloadRunner,
  createMenuUpdateCheck,
  isUpdatePending,
  nextUpdateState,
};
