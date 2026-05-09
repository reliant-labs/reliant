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
  const { development = false, externalBackend = false } = options;
  const change = describeAuthPrincipalChange(previousSession, nextSession);

  if (!change.changed) {
    return false;
  }

  if (development || externalBackend) {
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
