function getSessionUserId(session) {
  const userId = session?.user?.id;
  if (typeof userId !== 'string') {
    return '';
  }

  const trimmed = userId.trim();
  return trimmed.length > 0 ? trimmed : '';
}

function getAuthPrincipal(session) {
  return getSessionUserId(session) || 'anonymous';
}

function describeAuthPrincipalChange(previousSession, nextSession) {
  const previousPrincipal = getAuthPrincipal(previousSession);
  const nextPrincipal = getAuthPrincipal(nextSession);

  return {
    previousPrincipal,
    nextPrincipal,
    changed: previousPrincipal !== nextPrincipal,
  };
}

function shouldRestartBackendForAuthChange(previousSession, nextSession, options = {}) {
  const { externalBackend = false } = options;
  // The `development` option used to suppress restart here (the comment
  // implied dev-mode iteration would be disrupted), but BackendManager's
  // only job today is supervising the daemon. The daemon's PAT is bound to
  // the signed-in Supabase subject — when the principal changes, the
  // daemon MUST respawn so `ensureDaemonCreds` re-mints. Without the
  // restart, the daemon keeps using the old user's PAT (or no PAT, if
  // it spawned before sign-in), registers under the wrong owner, and
  // the new user's ListDaemons returns empty.
  const change = describeAuthPrincipalChange(previousSession, nextSession);

  if (!change.changed) {
    return false;
  }

  // externalBackend = user is pointing the renderer at a backend they're
  // managing themselves (RELIANT_EXTERNAL_BACKEND env). Don't touch it.
  if (externalBackend) {
    return false;
  }

  // LOGOUT (real user -> anonymous): keep the daemon warm.
  //
  // Restarting the daemon on the way OUT is pure latency on the logout path
  // (renderer's signOut awaits supabase.signOut -> storage removeItem ->
  // auth:clear IPC -> stop()+start()) and buys nothing: there is no new
  // principal to (re)mint a PAT for. The daemon has no user-facing work to
  // do while logged out, so we leave the supervised process running instead
  // of paying a SIGTERM + respawn + ensureDaemonCreds + waitForReady round
  // trip. On the NEXT login the anonymous->user (or user->user) branch below
  // still returns true, so the daemon respawns and re-mints for the incoming
  // user before any of their RPCs land — correctness is unchanged.
  //
  // Trade-off: the previous user's daemon (holding their PAT) stays resident
  // until the next login (which stops+respawns it, evicting the old PAT) or
  // until app quit (which stops it). Acceptable for a local single-user
  // desktop daemon; revisit if the daemon ever becomes multi-tenant.
  if (change.nextPrincipal === 'anonymous') {
    return false;
  }

  return true;
}

/**
 * Build the `auth:load` IPC reply from a stored-auth read.
 *
 * Extracted from the handler so the mapping is testable: `main.js` cannot be
 * required outside an Electron runtime, and this mapping is exactly where the
 * bug lived. The old handler returned `{ success, session }` and nothing else,
 * so "the stored session cannot be decrypted" reached the renderer as a plain
 * null — identical to "not signed in". Supabase's storage adapter therefore
 * reported no session and `getToken()` resolved null, while the in-memory auth
 * store still held a user: signed in on screen, tokenless on the wire.
 *
 * `success` stays true for an unreadable blob. It describes whether the READ
 * completed, not whether a session came back; flipping it would make the
 * adapter's existing `result?.success && result?.session` guard swallow the
 * failure it now needs to see.
 *
 * @param {{ status: string, session: object|null, reason?: string,
 *           cleared?: boolean, recoverable?: boolean }} result
 *   from `authStorage.readStoredAuth()`.
 * @param {{ reason: string, cleared: boolean, recoverable: boolean }|null} [pending]
 *   a failure latched by an EARLIER main-process read (Statsig user id, daemon
 *   PAT mint). That read is usually the one that finds and clears the dead
 *   blob, so by the time the renderer asks, `result` is a clean `empty` and
 *   this is the only remaining trace of the incident.
 * @returns {{ success: boolean, session: object|null,
 *             failure?: { reason: string, cleared: boolean, recoverable: boolean } }}
 */
function buildAuthLoadReply(result, pending = null) {
  const session = (result && result.session) || null;

  // A session that read back successfully outranks any latched failure: the
  // user re-authenticated (or a refresh rewrote the blob under the current
  // key), so the incident is already resolved. Reporting it anyway would sign
  // out a user whose session demonstrably works — the exact damage this whole
  // change exists to prevent, just from the opposite direction.
  if (session) {
    return { success: true, session };
  }

  const failure = result && result.status === 'unreadable' ? result : pending;
  if (!failure) {
    return { success: true, session: null };
  }

  return {
    success: true,
    session: null,
    failure: {
      reason: failure.reason,
      cleared: failure.cleared === true,
      recoverable: failure.recoverable === true,
    },
  };
}

/**
 * Start a backend restart WITHOUT waiting for it, swallowing any failure.
 *
 * The auth IPC handlers (`auth:save` / `auth:clear`) are awaited by the
 * renderer's Supabase storage adapter on every session write, so anything left
 * on that path delays the session commit itself. Restarting inline cost 11.1s
 * measured — 10.0s of it the outgoing daemon missing its SIGTERM deadline — and
 * for that whole window `supabase.auth.getSession()` returned null, so every
 * startup RPC went out with no Authorization header and came back
 * "missing authorization token".
 *
 * Returning immediately is safe because the restart's only output the renderer
 * needs is the new port, which is pushed separately via the 'backend-port'
 * event once the daemon is up.
 *
 * Takes the restart as a function so the policy is testable without Electron.
 *
 * @param {{ restart: () => Promise<unknown>, reason: string,
 *           logger?: { error: Function } }} options
 * @returns {Promise<void>} settles when the background restart finishes.
 *   Returned for TESTS and callers that want to observe completion — the IPC
 *   handlers deliberately ignore it. Never rejects.
 */
function startBackendRestart({ restart, reason, logger }) {
  let started;
  try {
    started = Promise.resolve(restart());
  } catch (error) {
    // A restart that throws synchronously must not take down the auth path.
    started = Promise.reject(error);
  }

  return started.then(
    () => undefined,
    (error) => {
      logger?.error?.('[Auth] Background backend restart failed', {
        reason,
        error: error?.message || String(error),
      });
    }
  );
}

/**
 * The shared body of the `auth:save` / `auth:clear` IPC handlers: persist the
 * session change, then kick off the daemon restart WITHOUT waiting for it.
 *
 * This function exists to make the ordering testable. The ordering IS the bug:
 * whether the restart is awaited is a property of the call site, so a test of
 * `startBackendRestart` alone cannot catch a regression here — it passes just
 * as happily against an implementation that awaits. Routing both handlers
 * through one function gives that decision a single home with a test on it.
 *
 * `persist` runs first and synchronously, because the renderer is blocked on
 * this IPC precisely to learn that the session hit disk.
 *
 * @param {{ persist: () => boolean,
 *           beforeRestart?: () => void,
 *           restart: () => Promise<unknown>,
 *           reason: string,
 *           logger?: { error: Function } }} options
 * @returns {{ success: boolean, restarting: boolean }}
 */
function runAuthWrite({ persist, beforeRestart, restart, reason, logger }) {
  const success = persist() === true;
  if (!success) {
    return { success: false, restarting: false };
  }

  if (beforeRestart) beforeRestart();

  // Deliberately not awaited, and deliberately not returned: awaiting here is
  // exactly the regression this function guards against.
  startBackendRestart({ restart, reason, logger });

  return { success: true, restarting: true };
}

module.exports = {
  buildAuthLoadReply,
  describeAuthPrincipalChange,
  getAuthPrincipal,
  getSessionUserId,
  shouldRestartBackendForAuthChange,
  startBackendRestart,
  runAuthWrite,
};
