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

// daemon-state.json's `stream` field name (see internal/toolexec/daemonstate
// State.Stream / json tag "stream").
const DAEMON_STATE_STREAM_FIELD = 'stream';

// The Stream value published while the daemon has no credentials for its
// --server origin and is idling under --non-interactive rather than
// attempting interactive registration.
const DAEMON_STREAM_AWAITING_CREDENTIALS = 'awaiting_credentials';

module.exports = {
  DAEMON_NON_INTERACTIVE_FLAG,
  DAEMON_NON_INTERACTIVE_ENV_VAR,
  DAEMON_STATE_STREAM_FIELD,
  DAEMON_STREAM_AWAITING_CREDENTIALS,
};
