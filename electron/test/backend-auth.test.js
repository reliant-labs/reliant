const test = require('node:test');
const assert = require('node:assert/strict');

const {
  describeAuthPrincipalChange,
  getAuthPrincipal,
  getSessionUserId,
  shouldRestartBackendForAuthChange,
} = require('../src/backend-auth');

test('getSessionUserId extracts trimmed authenticated user id', () => {
  assert.equal(getSessionUserId({ user: { id: ' user-123 ' } }), 'user-123');
  assert.equal(getSessionUserId({ user: { id: '' } }), '');
  assert.equal(getSessionUserId(null), '');
});

test('describeAuthPrincipalChange compares authenticated and anonymous principals', () => {
  assert.deepEqual(
    describeAuthPrincipalChange(null, { user: { id: 'user-1' } }),
    {
      previousPrincipal: 'anonymous',
      nextPrincipal: 'user-1',
      changed: true,
    }
  );

  assert.deepEqual(
    describeAuthPrincipalChange({ user: { id: 'user-1' } }, { user: { id: 'user-1' } }),
    {
      previousPrincipal: 'user-1',
      nextPrincipal: 'user-1',
      changed: false,
    }
  );
});

test('shouldRestartBackendForAuthChange restarts on principal change in dev and prod', () => {
  const previousSession = null;
  const nextSession = { user: { id: 'user-1' } };

  assert.equal(
    shouldRestartBackendForAuthChange(previousSession, nextSession),
    true
  );
  // The daemon's PAT is per-user — restart is required even in dev mode so
  // ensureDaemonCreds re-mints. Suppressing it here was a footgun that left
  // the daemon registered under the prior user's identity.
  assert.equal(
    shouldRestartBackendForAuthChange(previousSession, nextSession, { development: true }),
    true
  );
  // externalBackend = user is managing their own backend out-of-band; we
  // must not stop/start it.
  assert.equal(
    shouldRestartBackendForAuthChange(previousSession, nextSession, { externalBackend: true }),
    false
  );
  // No principal change → no restart, regardless of env.
  assert.equal(
    shouldRestartBackendForAuthChange(nextSession, { user: { id: 'user-1' } }),
    false
  );
});

test('shouldRestartBackendForAuthChange keeps the daemon warm on logout', () => {
  // LOGOUT (user -> anonymous): do NOT restart. Restarting on the way out is
  // pure latency on the logout path and there is no new principal to mint a
  // PAT for. The next login (anonymous -> user) still restarts, so the daemon
  // re-mints for the incoming user before their RPCs land.
  assert.equal(
    shouldRestartBackendForAuthChange({ user: { id: 'user-1' } }, null),
    false
  );
  assert.equal(
    shouldRestartBackendForAuthChange({ user: { id: 'user-1' } }, { user: { id: '' } }),
    false
  );
  // User switch (user-1 -> user-2) MUST still restart to re-mint + evict the
  // previous user's PAT.
  assert.equal(
    shouldRestartBackendForAuthChange({ user: { id: 'user-1' } }, { user: { id: 'user-2' } }),
    true
  );
});

test('getAuthPrincipal falls back to anonymous without a user id', () => {
  assert.equal(getAuthPrincipal(null), 'anonymous');
  assert.equal(getAuthPrincipal({ user: { id: 'user-9' } }), 'user-9');
});
