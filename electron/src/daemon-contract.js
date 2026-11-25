/**
 * Contract between Electron and the Go daemon for the "wait for sign-in,
 * don't open a browser" startup path.
 *
 * CONFIRMED against the Go source (not a placeholder):
 *   - CLI flag:     cmd/reliant/cmd/daemon.go:741
 *   - Stream value: internal/toolexec/daemonstate/state.go:58
 *
 * With --non-interactive, a daemon that has no credentials for its --server
 * origin does NOT exit and does NOT open a browser — it idles as a
 * long-lived process, publishing stream: "awaiting_credentials" in
 * daemon-state.json, and re-checks for credentials appearing on disk. It
 * only proceeds once Electron mints a PAT (ensureDaemonCreds) and respawns
 * it (restartBackendForAuthPrincipalChange in main.js, triggered by
 * sign-in). Real crashes are unaffected — a daemon that dies still exits
 * and still drives the existing handleCrash restart-loop path.
 */

// CLI flag Electron passes at every daemon spawn (buildDaemonArgs). Always
// on: Electron drives auth itself (ensureDaemonCreds pre-mint, then the
// sign-in-triggered respawn), so the daemon's own interactive flow — which
// opens a localhost browser tab and is broken under headless Electron (no
// TTY) — must never run.
const DAEMON_NON_INTERACTIVE_FLAG = '--non-interactive';

// Env-var fallback for the same flag (RELIANT_DAEMON_NON_INTERACTIVE, not
// RELIANT_NON_INTERACTIVE). Electron always passes the CLI flag directly, so
// this constant exists only for anything that spawns the daemon by env
// instead of argv.
const DAEMON_NON_INTERACTIVE_ENV_VAR = 'RELIANT_DAEMON_NON_INTERACTIVE';

// The daemon's global --verbose flag, which selects who its stdout is FOR.
//
// Unset, the daemon assumes a HUMAN is watching: the structured log goes to the
// rotating file only, stdout gets a few short status lines, and the
// `@@RELIANT_STREAM` notices below are suppressed as unreadable noise.
//
// Set, stdout carries the full structured log AND those notices — machine
// output, which is what a supervising parent needs. Electron always passes it
// for that reason; see backend-manager.js buildDaemonArgs.
const DAEMON_VERBOSE_FLAG = '--verbose';

// daemon-state.json's `stream` field name (see internal/toolexec/daemonstate
// State.Stream / json tag "stream").
const DAEMON_STATE_STREAM_FIELD = 'stream';

// The Stream value published while the daemon has no credentials for its
// --server origin and is idling under --non-interactive rather than
// attempting interactive registration.
const DAEMON_STREAM_AWAITING_CREDENTIALS = 'awaiting_credentials';

// Fields that identify WHICH connection the record describes, as opposed to
// what state it is in. `connected_at` is restamped every time the gateway
// stream is re-established, and `pid` changes whenever the daemon respawns.
//
// A watcher must key on these, not on `stream` alone: the post-sign-in restart
// takes the daemon from connected → (briefly) awaiting_credentials → connected,
// and the intermediate state can be shorter than the watcher's stat interval.
// Deduping on the string then sees "connected" both times, concludes nothing
// changed, and never reports the NEW connection. See watchDaemonConnection.
const DAEMON_STATE_CONNECTED_AT_FIELD = 'connected_at';
const DAEMON_STATE_PID_FIELD = 'pid';

// The instance slug the daemon stamps into its own record, e.g.
// "http-localhost-8090-0bf46daa/_default/reliant-62553a34" — the value of
// daemoninstance.Key.Slug() for the (origin, sub, workspace) it resolved.
//
// This is what makes the record SELF-DESCRIBING, and it is the difference
// between "a daemon wrote a file at this path" and "MY daemon wrote this".
// Without it a reader can only trust the path, and the path was the thing that
// turned out not to be trustworthy: two stacks writing `./data` from different
// working directories produced records that each looked authoritative to the
// other's Electron. A record whose instance is absent or different is not
// evidence about our daemon, and readDaemonState discards it.
const DAEMON_STATE_INSTANCE_FIELD = 'instance';

// Prefix the daemon prints on stdout when its stream state changes, e.g.
// `@@RELIANT_STREAM connected`.
//
// MIRRORS daemonstate.StreamNoticePrefix (internal/toolexec/daemonstate/
// state.go), which is the source of truth. backend-manager-daemon-stream-
// notice.test.js asserts the two agree by reading the Go constant, so a rename
// on either side fails the Electron suite rather than silently degrading to the
// slower path.
//
// This is the PUSH path for "the daemon is up". The stat-poll on
// daemon-state.json remains as the fallback — see watchDaemonConnection — and
// stays authoritative: a daemon on another machine has no stdout we can read.
const DAEMON_STREAM_NOTICE_PREFIX = '@@RELIANT_STREAM';

// Who supervises the daemon, set in its environment at every spawn
// (backend-manager.js buildDaemonEnv). MIRRORS daemonstate.SupervisorEnv and
// SupervisorElectron (internal/toolexec/daemonstate/state.go). With it, the
// daemon tells this app of a requested upgrade on stdout (below); without it
// (an older app), the daemon prints nothing new and nothing changes.
const DAEMON_SUPERVISOR_ENV_VAR = 'RELIANT_SUPERVISOR';
const DAEMON_SUPERVISOR_ELECTRON = 'electron';

// Prefix the daemon prints on stdout when the platform asks it to upgrade,
// followed by the request as JSON:
//
//   @@RELIANT_UPGRADE {"target_version":"1.40.0","urgency":"normal",...}
//
// MIRRORS daemonstate.UpgradeNoticePrefix (internal/toolexec/daemonstate/
// state.go), the source of truth; backend-manager-daemon-upgrade-notice.test.js
// reads the Go constant so a rename on either side fails this suite.
//
// The daemon runs the binary inside this app's signed bundle, so it never
// replaces itself: it keeps serving and reports that it awaits its owner, and
// its owner is this app's updater. On the notice the app checks for an
// update, downloads it and asks the user to restart (owner decision D7); it
// never installs without them (main.js, update-check.js
// createDaemonUpgradeResponder). The daemon prints it once per distinct
// request.
const DAEMON_UPGRADE_NOTICE_PREFIX = '@@RELIANT_UPGRADE';

// The urgencies the daemon names (RestartUrgency, lowercased). Anything else,
// including one newer than this app, is read as "normal".
const DAEMON_UPGRADE_URGENCIES = new Set(['routine', 'normal', 'security', 'emergency']);

function noticeString(value) {
  return typeof value === 'string' ? value.trim() : '';
}

function noticeNumber(value) {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0;
}

/**
 * Reads the JSON after DAEMON_UPGRADE_NOTICE_PREFIX (daemonstate.UpgradeNotice)
 * into the request the updater acts on, or null when it is not a JSON object.
 * Missing or ill-typed fields read as empty, so a daemon that adds or drops a
 * field never stops the app from updating.
 *
 * @param {string} payload the text after the prefix
 * @returns {null | {targetVersion: string, targetCommit: string,
 *   commitTimeUnix: number, urgency: string, deadlineUnixMs: number,
 *   rolloutId: string, reason: string, upgradeCommand: string,
 *   upgradeUrl: string}}
 */
function parseDaemonUpgradeNotice(payload) {
  let raw;
  try {
    raw = JSON.parse(payload);
  } catch {
    return null;
  }
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const urgency = noticeString(raw.urgency).toLowerCase();
  return {
    targetVersion: noticeString(raw.target_version),
    targetCommit: noticeString(raw.target_commit),
    commitTimeUnix: noticeNumber(raw.commit_time_unix),
    urgency: DAEMON_UPGRADE_URGENCIES.has(urgency) ? urgency : 'normal',
    deadlineUnixMs: noticeNumber(raw.deadline_unix_ms),
    rolloutId: noticeString(raw.rollout_id),
    reason: noticeString(raw.reason),
    upgradeCommand: noticeString(raw.upgrade_command),
    upgradeUrl: noticeString(raw.upgrade_url),
  };
}

module.exports = {
  DAEMON_NON_INTERACTIVE_FLAG,
  DAEMON_NON_INTERACTIVE_ENV_VAR,
  DAEMON_VERBOSE_FLAG,
  DAEMON_STATE_STREAM_FIELD,
  DAEMON_STREAM_AWAITING_CREDENTIALS,
  DAEMON_STATE_CONNECTED_AT_FIELD,
  DAEMON_STATE_PID_FIELD,
  DAEMON_STATE_INSTANCE_FIELD,
  DAEMON_STREAM_NOTICE_PREFIX,
  DAEMON_SUPERVISOR_ENV_VAR,
  DAEMON_SUPERVISOR_ELECTRON,
  DAEMON_UPGRADE_NOTICE_PREFIX,
  parseDaemonUpgradeNotice,
};
