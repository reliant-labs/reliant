/**
 * Regression tests for the reported sign-in failure.
 *
 * ── What actually happened ────────────────────────────────────────────
 *
 * From ~/Library/Logs/reliant-local/main.log, 2026-08-26:
 *
 *   15:09:45.232 WARN  daemon command still running    commandType=auth.start_oauth
 *   15:09:50.218 ERROR [gRPC Client] Request failed:   method: 'StartOAuthFlow'
 *                                                      baseUrl: api.reliantapi.com
 *   15:09:50.229 WARN  daemon command completed slowly elapsed=14.998s failed=true
 *
 * `auth.start_oauth` → `oauthcallback.Run`, which binds a port, opens a
 * browser AND BLOCKS UNTIL THE HUMAN FINISHES. That was carried by a single
 * request/response RPC across the network. At ~15s the request died — the user
 * saw "Failed to fetch (api.reliantapi.com)" — and because the daemon's
 * listener was tied to that request's context it was torn down too, so the
 * browser's eventual redirect hit a closed port: ERR_CONNECTION_REFUSED.
 *
 * The two reported symptoms were one bug. Both tests below fail against the
 * old design for the same reason: it had no way to bind a port without also
 * blocking on the human.
 *
 * The 15s bound is NOT ours to raise — the renderer's timeout table and the
 * server's both register StartOAuthFlow at "no timeout" (and the log contains
 * no "timed out after" line, so the client interceptor provably never fired),
 * and NATS is given an hour. It comes from the network path. Which is the
 * point: a request/response RPC that must outlive a consent screen is the
 * wrong shape at any deadline, so these tests assert the SHAPE — start returns
 * without waiting, and the listener outlives the start call.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');

const providerLogin = require('../src/oauth-provider-login');

const CLAUDE_AUTHORIZE = 'https://claude.ai/oauth/authorize?redirect_uri={redirect_uri}';

function httpGet(url) {
  return new Promise((resolve, reject) => {
    http
      .get(url, (res) => {
        res.resume();
        res.on('end', () => resolve(res.statusCode));
      })
      .on('error', reject);
  });
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * Node surfaces a refused loopback connect as a bare ECONNREFUSED or, when the
 * host resolves to several addresses, as an AggregateError wrapping one per
 * address. Both mean "nothing is listening", which is what these tests assert.
 */
const isConnectionRefused = (error) =>
  /ECONNREFUSED|socket hang up/.test(
    [error.code, error.message, ...(error.errors || []).map((e) => e.code)].join(' '),
  );

test('starting the flow does NOT wait for the user', async (t) => {
  // The core defect. The old path could not hand back a redirect URI without
  // blocking on the whole browser round trip, so the caller's deadline had to
  // cover human time. Here start resolves with nothing having happened yet.
  const started = Date.now();
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  assert.ok(
    Date.now() - started < 1000,
    'start must return immediately, not block on the sign-in',
  );
  assert.ok(redirectUri, 'start must hand back the redirect URI up front');
});

test('the listener SURVIVES far longer than the RPC deadline that killed it', async (t) => {
  // The ERR_CONNECTION_REFUSED half. The daemon's listener lived and died with
  // the RPC's context, so once the request was cancelled at ~15s the port was
  // closed and the browser's redirect had nowhere to land.
  //
  // Scaled down to keep the suite fast: what is being asserted is that the
  // listener's lifetime is INDEPENDENT of the caller, not the specific number.
  // Under the old design nothing was listening at this point at all.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  await sleep(300);

  const status = await httpGet(`${redirectUri}?code=late-arrival&state=s`);
  assert.equal(status, 200, 'the port must still be bound when the user finally lands');

  const result = await providerLogin.waitForProviderLogin(flowId);
  assert.equal(result.code, 'late-arrival');
});

test('waiting has no deadline of its own', async (t) => {
  // The wait is plain IPC to the same machine. Nothing between the renderer
  // and the main process can time it out, so a slow user is not an error.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  const pending = providerLogin.waitForProviderLogin(flowId);

  let settled = false;
  void pending.then(
    () => {
      settled = true;
    },
    () => {
      settled = true;
    },
  );

  await sleep(250);
  assert.equal(settled, false, 'the wait must not resolve or reject on its own');

  await httpGet(`${redirectUri}?code=eventually&state=s`);
  assert.equal((await pending).code, 'eventually');
});

test('a provider error reaches the caller instead of hanging', async (t) => {
  // A denied consent must surface. Dropping it would leave the UI spinning
  // until the flow timeout — indistinguishable, to the user, from the bug
  // being fixed here.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  await httpGet(`${redirectUri}?error=access_denied&error_description=User%20cancelled`);

  await assert.rejects(
    providerLogin.waitForProviderLogin(flowId),
    /access_denied.*User cancelled/,
  );
});

test('a completed flow releases its port', async (t) => {
  // Codex's port 1455 is FIXED by OpenAI, so a leaked listener blocks every
  // later attempt for the whole session.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  await httpGet(`${redirectUri}?code=done&state=s`);
  await providerLogin.waitForProviderLogin(flowId);
  await sleep(80);

  await assert.rejects(httpGet(`${redirectUri}?code=second`), isConnectionRefused);
  assert.equal(providerLogin.__activeFlowCount(), 0, 'no flow may be left registered');
});

test('cancelling releases the port without waiting for the timeout', async () => {
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);

  const pending = providerLogin.waitForProviderLogin(flowId);
  providerLogin.cancelProviderLogin(flowId);

  await assert.rejects(pending, /cancelled/);
  await sleep(80);
  await assert.rejects(httpGet(`${redirectUri}?code=x`), isConnectionRefused);
});

test('a code that arrives BEFORE the wait is still collected', async (t) => {
  // Start and wait are separate calls, so the callback can land in between —
  // which is exactly what happens for a user already signed in to the provider,
  // i.e. the FASTEST sign-ins. If the outcome were discarded when the listener
  // closed, those would fail with "unknown flow": a nastier version of the bug
  // being fixed, because it would look intermittent.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  await httpGet(`${redirectUri}?code=arrived-early&state=s`);
  await sleep(50);

  assert.equal((await providerLogin.waitForProviderLogin(flowId)).code, 'arrived-early');
});

test('a second callback cannot change a settled outcome', async (t) => {
  // A provider that redirects twice, or a timeout racing a real callback, must
  // not resolve the promise twice or double-close the server.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  await httpGet(`${redirectUri}?code=first&state=s`);
  assert.equal((await providerLogin.waitForProviderLogin(flowId)).code, 'first');

  // Asking again returns the same outcome rather than hanging or re-running.
  assert.equal((await providerLogin.waitForProviderLogin(flowId)).code, 'first');
});

test('concurrent flows get independent ports and do not cross-deliver', async (t) => {
  // Claude and Codex can both be started; each must get its own listener and
  // its own code. A shared/global listener would deliver the first code to
  // whichever caller happened to be waiting.
  const a = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  const b = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => {
    providerLogin.cancelProviderLogin(a.flowId, 'test cleanup');
    providerLogin.cancelProviderLogin(b.flowId, 'test cleanup');
  });

  assert.notEqual(new URL(a.redirectUri).port, new URL(b.redirectUri).port);

  await httpGet(`${a.redirectUri}?code=for-a&state=s`);
  await httpGet(`${b.redirectUri}?code=for-b&state=s`);

  assert.equal((await providerLogin.waitForProviderLogin(a.flowId)).code, 'for-a');
  assert.equal((await providerLogin.waitForProviderLogin(b.flowId)).code, 'for-b');
});
