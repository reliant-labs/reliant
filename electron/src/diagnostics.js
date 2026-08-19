/**
 * Diagnostics policy for packaged builds.
 *
 * v1.6.3 shipped a window that never rendered, and the user could not tell us
 * anything about it: DevTools were hard-disabled in packaged builds, so there
 * was no console to read and no way to reach the log file except by knowing
 * the macOS path by heart.
 *
 * Two decisions come out of that, and the order matters.
 *
 * 1. The log file is the primary diagnostic, not DevTools. A renderer that
 *    fails to boot cannot show its own console, and the console would not have
 *    contained the interesting part anyway — the asset 404s and the daemon
 *    spawn both happen in the main process. `main.log` already had everything
 *    needed to diagnose this bug; it was simply unreachable. So diagnostics
 *    are exposed as a menu item that reveals the log in Finder, which works
 *    even when the window is blank.
 *
 * 2. DevTools become available in packaged builds, gated. Restricting them to
 *    prerelease (`-rc`) builds was considered and rejected: the failure we
 *    need to debug is the one a user hits on a STABLE build, and telling them
 *    "install an RC to reproduce it" changes the artifact under test. The
 *    threat model does not support hiding them either — the app ships its
 *    renderer as readable files on disk, so DevTools reveal nothing an
 *    attacker with local access could not already read, while the earlier
 *    lockdown cost real diagnosability.
 *
 * They stay behind a deliberate gesture rather than F12 so an ordinary user
 * cannot open them by accident: an explicit View-menu item, or
 * RELIANT_DEVTOOLS=1 for a user we are actively walking through a bug.
 */

const DEVTOOLS_ENV_VAR = "RELIANT_DEVTOOLS";

/**
 * Whether DevTools may be opened at all.
 *
 * @param {{ isPackaged: boolean, env?: Record<string, string|undefined>, version?: string }} context
 * @returns {boolean}
 */
function isDevToolsAllowed(context) {
  if (!context.isPackaged) {
    return true;
  }
  return isDevToolsEnvEnabled(context.env || {});
}

/**
 * Whether DevTools should open automatically on launch.
 *
 * Only the env var does this. A prerelease build is still a build someone
 * demos; auto-opening DevTools on every RC launch would be noise, and the
 * menu item is one click away.
 *
 * @param {{ isPackaged: boolean, env?: Record<string, string|undefined> }} context
 * @returns {boolean}
 */
function shouldAutoOpenDevTools(context) {
  if (!context.isPackaged) {
    return false;
  }
  return isDevToolsEnvEnabled(context.env || {});
}

/**
 * Whether the View menu should offer DevTools.
 *
 * Always true. The menu item is the discoverable path for a user we are
 * debugging with, and hiding it behind the same env var it enables would make
 * it useless — nobody sets an env var for a GUI app they cannot open.
 *
 * @returns {boolean}
 */
function shouldShowDevToolsMenuItem() {
  return true;
}

function isDevToolsEnvEnabled(env) {
  const raw = env[DEVTOOLS_ENV_VAR];
  if (!raw) return false;
  const normalized = String(raw).trim().toLowerCase();
  return normalized === "1" || normalized === "true" || normalized === "yes";
}

/**
 * Whether a version string denotes a prerelease.
 *
 * Retained because the release channel is worth surfacing in the diagnostics
 * report even though it no longer gates DevTools.
 *
 * @param {string} version
 * @returns {boolean}
 */
function isPrereleaseVersion(version) {
  if (!version || typeof version !== "string") return false;
  return /-(rc|alpha|beta|next|canary)/i.test(version);
}

/**
 * Build the human-readable diagnostics summary shown in the Help menu.
 *
 * Deliberately plain text: a user with a blank window can select it, copy it,
 * and paste it into a bug report without any tooling.
 *
 * @param {{ version: string, electronVersion: string, platform: string, arch: string,
 *           logPath: string, rendererUrl: string, backendReady: boolean,
 *           backendPort: number|null, apiUrl: string }} info
 * @returns {string}
 */
function formatDiagnosticsReport(info) {
  return [
    `Reliant ${info.version}${isPrereleaseVersion(info.version) ? " (prerelease)" : ""}`,
    `Electron ${info.electronVersion} on ${info.platform}/${info.arch}`,
    "",
    `Renderer URL : ${info.rendererUrl || "(not loaded)"}`,
    `Backend ready: ${info.backendReady ? "yes" : "no"}`,
    `Backend port : ${info.backendPort ?? "(none)"}`,
    `API URL      : ${info.apiUrl || "(unset)"}`,
    "",
    `Log file: ${info.logPath}`,
  ].join("\n");
}

module.exports = {
  DEVTOOLS_ENV_VAR,
  formatDiagnosticsReport,
  isDevToolsAllowed,
  isPrereleaseVersion,
  shouldAutoOpenDevTools,
  shouldShowDevToolsMenuItem,
};
