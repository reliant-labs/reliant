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

module.exports = {
  describeAuthPrincipalChange,
  getAuthPrincipal,
  getSessionUserId,
  shouldRestartBackendForAuthChange,
};
