/**
 * Contract between Electron and the Go daemon for the "wait for sign-in,
 * don't open a browser" startup path.
 *
 * CONFIRMED against the Go source (not a placeholder):
 *   - CLI flag:     cmd/reliant/commands/daemon.go:741
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

module.exports = {
  DAEMON_NON_INTERACTIVE_FLAG,
  DAEMON_NON_INTERACTIVE_ENV_VAR,
  DAEMON_VERBOSE_FLAG,
  DAEMON_STATE_STREAM_FIELD,
  DAEMON_STREAM_AWAITING_CREDENTIALS,
  DAEMON_STATE_CONNECTED_AT_FIELD,
  DAEMON_STATE_PID_FIELD,
  DAEMON_STREAM_NOTICE_PREFIX,
};
