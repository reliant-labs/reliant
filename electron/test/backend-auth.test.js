const test = require('node:test');
const assert = require('node:assert/strict');

const {
  buildAuthLoadReply,
  describeAuthPrincipalChange,
  getAuthPrincipal,
  getSessionUserId,
  shouldRestartBackendForAuthChange,
  startBackendRestart,
  runAuthWrite,
} = require('../src/backend-auth');

// ---------------------------------------------------------------------------
// buildAuthLoadReply — the auth:load payload the renderer sees
// ---------------------------------------------------------------------------
//
// This mapping is where "getToken() returned null while signed in" was born.
// The handler used to reply `{ success, session }` and nothing more, so an
// undecryptable blob arrived as a bare null — the same value a user who never
// signed in gets. Supabase's storage adapter reads that as "no session",
// getToken() resolves null, and every RPC goes out with no Authorization
// header, while the in-memory auth store still holds a user from an earlier
// onAuthStateChange. Signed in on screen, tokenless on the wire.

test('buildAuthLoadReply: a readable session is returned as-is', () => {
  const session = { access_token: 'a', refresh_token: 'r' };

  assert.deepEqual(buildAuthLoadReply({ status: 'ok', session }), {
    success: true,
    session,
  });
});

test('buildAuthLoadReply: not signed in carries no failure', () => {
  const reply = buildAuthLoadReply({ status: 'empty', session: null });

  assert.deepEqual(reply, { success: true, session: null });
  // The absence of `failure` is the contract: a signed-out user must never be
  // told their saved sign-in was destroyed.
  assert.equal('failure' in reply, false);
});

test('buildAuthLoadReply: an unreadable blob is reported as a distinct failure', () => {
  const reply = buildAuthLoadReply({
    status: 'unreadable',
    session: null,
    reason: 'decrypt_failed',
    cleared: true,
    recoverable: false,
  });

  assert.deepEqual(reply, {
    success: true,
    session: null,
    failure: { reason: 'decrypt_failed', cleared: true, recoverable: false },
  });
});

test('buildAuthLoadReply: an unreadable read still reports success', () => {
  const reply = buildAuthLoadReply({
    status: 'unreadable',
    session: null,
    reason: 'decrypt_failed',
    cleared: true,
    recoverable: false,
  });

  // `success` describes whether the READ completed, not whether a session came
  // back. The renderer's adapter guards on `result.success && result.session`,
  // so flipping this to false would swallow the very failure it must surface.
  assert.equal(reply.success, true);
});

test('buildAuthLoadReply: a failure found by an earlier read is still reported', () => {
  // The main process reads auth before the renderer does (Statsig user id,
  // daemon PAT mint). That read discovers and clears the dead blob, so the
  // renderer's own read is a clean `empty` — the latched failure is the only
  // remaining evidence, and dropping it loses the incident entirely.
  const reply = buildAuthLoadReply(
    { status: 'empty', session: null },
    { reason: 'decrypt_failed', cleared: true, recoverable: false }
  );

  assert.deepEqual(reply.failure, {
    reason: 'decrypt_failed',
    cleared: true,
    recoverable: false,
  });
});

test('buildAuthLoadReply: a working session outranks a stale latched failure', () => {
  // The user re-authenticated after the incident (or a refresh rewrote the
  // blob under the current key), so the session reads fine. Reporting the old
  // failure here would sign out a user whose session demonstrably works.
  const session = { access_token: 'a', refresh_token: 'r' };

  const reply = buildAuthLoadReply({ status: 'ok', session }, {
    reason: 'decrypt_failed',
    cleared: true,
    recoverable: false,
  });

  assert.deepEqual(reply, { success: true, session });
  assert.equal('failure' in reply, false);
});

test('buildAuthLoadReply: a recoverable failure is marked recoverable', () => {
  // A locked Linux keyring. The ciphertext is intact and a later read
  // succeeds, so the renderer must NOT force a re-auth over this.
  const reply = buildAuthLoadReply({
    status: 'unreadable',
    session: null,
    reason: 'encryption_unavailable',
    cleared: false,
    recoverable: true,
  });

  assert.equal(reply.failure.recoverable, true);
  assert.equal(reply.failure.cleared, false);
});

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

// --- startBackendRestart -----------------------------------------------
//
// These pin the property the post-signup stall violated: the auth IPC path
// must not be held open by the daemon's process lifecycle. Awaiting the
// restart inline cost 11.1s of dead time on sign-in (10.0s of it the outgoing
// daemon missing its SIGTERM deadline), during which the Supabase session was
// uncommitted and every RPC went out unauthenticated.

test('startBackendRestart returns before a slow restart finishes', async () => {
  let releaseRestart;
  const restartFinished = new Promise((resolve) => { releaseRestart = resolve; });
  let restartSettled = false;

  const settled = startBackendRestart({
    restart: () => restartFinished.then(() => { restartSettled = true; }),
    reason: 'auth:save',
  });

  // The whole point: control is back here while the restart is still running.
  // Yield generously — if this ever starts awaiting the restart, `restartSettled`
  // stays false only because the restart is blocked, and the assert below fires.
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(restartSettled, false, 'restart must still be in flight');

  releaseRestart();
  await settled;
  assert.equal(restartSettled, true, 'restart still runs to completion');
});

test('startBackendRestart swallows an async restart failure', async () => {
  const logged = [];

  await startBackendRestart({
    restart: () => Promise.reject(new Error('daemon refused to start')),
    reason: 'auth:save',
    logger: { error: (...args) => logged.push(args) },
  });

  // A failed restart must never reject: the renderer experiences this path as
  // "I just signed in", and an unhandled rejection surfaces there as a crash.
  assert.equal(logged.length, 1);
  assert.match(logged[0][0], /Background backend restart failed/);
  assert.equal(logged[0][1].reason, 'auth:save');
  assert.match(logged[0][1].error, /daemon refused to start/);
});

test('startBackendRestart swallows a synchronous restart failure', async () => {
  const logged = [];

  await startBackendRestart({
    restart: () => { throw new Error('backend manager exploded'); },
    reason: 'auth:clear',
    logger: { error: (...args) => logged.push(args) },
  });

  assert.equal(logged.length, 1);
  assert.match(logged[0][1].error, /backend manager exploded/);
});

test('startBackendRestart tolerates a missing logger', async () => {
  await startBackendRestart({
    restart: () => Promise.reject(new Error('boom')),
    reason: 'auth:save',
  });
});

// --- runAuthWrite: the ordering guard ----------------------------------
//
// This is the test that actually catches the post-signup stall. It asserts a
// property of the CALL SITE — that the handler returns to the renderer while
// the daemon restart is still in flight. A test of startBackendRestart alone
// cannot catch this: it passes just as happily against a handler that awaits.

test('runAuthWrite returns to the renderer without awaiting the restart', () => {
  let restartStarted = false;
  let releaseRestart;
  const restartFinished = new Promise((resolve) => { releaseRestart = resolve; });

  // Note: NOT awaited. If runAuthWrite ever becomes async / awaits the restart,
  // `result` is a pending Promise rather than the object asserted below and
  // this test fails — which is precisely the regression we are guarding.
  const result = runAuthWrite({
    persist: () => true,
    restart: () => {
      restartStarted = true;
      return restartFinished;
    },
    reason: 'auth:save',
  });

  assert.deepEqual(result, { success: true, restarting: true });
  assert.equal(restartStarted, true, 'restart should have been kicked off');

  releaseRestart();
});

test('runAuthWrite reports failure and skips the restart when persist fails', () => {
  let restartStarted = false;

  const result = runAuthWrite({
    persist: () => false,
    restart: () => { restartStarted = true; return Promise.resolve(); },
    reason: 'auth:save',
  });

  // Nothing changed on disk, so there is no new principal to restart for.
  assert.deepEqual(result, { success: false, restarting: false });
  assert.equal(restartStarted, false, 'must not restart when the write failed');
});

test('runAuthWrite runs beforeRestart after a successful persist', () => {
  const order = [];

  const result = runAuthWrite({
    persist: () => { order.push('persist'); return true; },
    // auth:clear drops the daemon.json entry here. It must land BEFORE the
    // restart so the next mint cannot race a half-cleared credentials file.
    beforeRestart: () => { order.push('beforeRestart'); },
    restart: () => { order.push('restart'); return Promise.resolve(); },
    reason: 'auth:clear',
  });

  assert.deepEqual(order, ['persist', 'beforeRestart', 'restart']);
  assert.equal(result.success, true);
});

test('runAuthWrite does not run beforeRestart when persist fails', () => {
  let ran = false;

  runAuthWrite({
    persist: () => false,
    beforeRestart: () => { ran = true; },
    restart: () => Promise.resolve(),
    reason: 'auth:clear',
  });

  assert.equal(ran, false);
});

test('runAuthWrite still returns success when the restart fails', () => {
  const logged = [];

  // A daemon that cannot restart must not be reported as a failed sign-in:
  // the session IS saved, and the app recovers when the daemon comes back.
  const result = runAuthWrite({
    persist: () => true,
    restart: () => Promise.reject(new Error('spawn failed')),
    reason: 'auth:save',
    logger: { error: (...args) => logged.push(args) },
  });

  assert.deepEqual(result, { success: true, restarting: true });
});
