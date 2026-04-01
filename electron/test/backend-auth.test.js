const test = require('node:test');
const assert = require('node:assert/strict');

const {
  applyDaemonIdentityEnv,
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

test('shouldRestartBackendForAuthChange only restarts for production principal changes', () => {
  const previousSession = null;
  const nextSession = { user: { id: 'user-1' } };

  assert.equal(
    shouldRestartBackendForAuthChange(previousSession, nextSession),
    true
  );
  assert.equal(
    shouldRestartBackendForAuthChange(previousSession, nextSession, { development: true }),
    false
  );
  assert.equal(
    shouldRestartBackendForAuthChange(previousSession, nextSession, { externalBackend: true }),
    false
  );
  assert.equal(
    shouldRestartBackendForAuthChange(nextSession, { user: { id: 'user-1' } }),
    false
  );
});

test('applyDaemonIdentityEnv injects and clears RELIANT_DAEMON_USER_ID deterministically', () => {
  const baseEnv = {
    PATH: '/usr/bin',
    RELIANT_DAEMON_USER_ID: 'stale-user',
  };

  const authedEnv = applyDaemonIdentityEnv(baseEnv, { user: { id: 'user-42' } });
  assert.equal(authedEnv.RELIANT_DAEMON_USER_ID, 'user-42');
  assert.equal(authedEnv.PATH, '/usr/bin');

  const anonymousEnv = applyDaemonIdentityEnv(baseEnv, null);
  assert.equal(Object.prototype.hasOwnProperty.call(anonymousEnv, 'RELIANT_DAEMON_USER_ID'), false);
  assert.equal(anonymousEnv.PATH, '/usr/bin');
});

test('getAuthPrincipal falls back to anonymous without a user id', () => {
  assert.equal(getAuthPrincipal(null), 'anonymous');
  assert.equal(getAuthPrincipal({ user: { id: 'user-9' } }), 'user-9');
});
