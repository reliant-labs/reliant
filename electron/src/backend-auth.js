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

  return true;
}

module.exports = {
  describeAuthPrincipalChange,
  getAuthPrincipal,
  getSessionUserId,
  shouldRestartBackendForAuthChange,
};
