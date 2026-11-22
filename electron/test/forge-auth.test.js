// Desktop forge sign-in (src/forge-auth.js): the loopback handoff, where the
// session lives, and the app:// API proxy. The security properties under test:
//
//   - the loopback listener settles only for the state this process minted
//     (any local process can reach it), and only once;
//   - the PKCE verifier goes to the broker's redeem and nowhere else, and it
//     is the one whose S256 the start carried;
//   - a one-time code is redeemed once: a failed redeem is never retried;
//   - the session id never reaches the renderer: not in an IPC answer, not in
//     a proxied response, and a renderer-supplied Cookie is never forwarded.

const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const http = require('node:http');

const {
  AUTH_PARTITION,
  DEV_COOKIE_NAME,
  ForgeAuth,
  SECURE_COOKIE_NAME,
  authProviderOf,
  createApiProxy,
  isFromApp,
  isSessionId,
  newPkcePair,
  parseSetCookie,
  sessionCookieName,
  startLoopback,
  upstreamFor,
} = require('../src/forge-auth');

const APP = 'app://bundle';
const API = 'https://api.example.test';
const CP = 'https://cp.example.test';
const s256 = (v) => crypto.createHash('sha256').update(v).digest('base64url');
const newSessionId = () => `fs_${crypto.randomBytes(32).toString('base64url')}`;
const quiet = { debug() {}, info() {}, warn() {}, error() {} };

/** An in-memory stand-in for Electron's Cookies API. */
function fakeCookies() {
  const jar = new Map();
  const key = (url, name) => `${new URL(url).origin}|${name}`;
  return {
    jar,
    async get({ url, name }) {
      const c = jar.get(key(url, name));
      return c ? [c] : [];
    },
    async set(c) {
      jar.set(key(c.url, c.name), { ...c });
    },
    async remove(url, name) {
      jar.delete(key(url, name));
    },
    async flushStore() {},
  };
}

/** The GET the system browser makes when consent redirects to the loopback. */
function browserGet(url) {
  return new Promise((resolve, reject) => {
    const req = http.get(url, { agent: false }, (res) => {
      let body = '';
      res.on('data', (c) => (body += c));
      res.on('end', () => resolve({ status: res.statusCode, body }));
    });
    req.on('error', reject);
  });
}

function json(status, body, headers = {}) {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...headers } });
}

/**
 * A fake reliant-api broker: /auth/handoff/{start,redeem}, /auth/session and
 * /auth/logout, with forge's rules (S256 only, the code bound to the
 * challenge and redirect, taken by the FIRST redeem).
 */
function fakeBroker({ subject = 'user-1', email = 'p@example.com', consentUrl = 'https://app.example.test/auth/handoff' } = {}) {
  const calls = [];
  const pending = new Map(); // req id -> {challenge, redirect, state}
  const codes = new Map(); // code -> {challenge, redirect}
  const sessions = new Set();
  const b = {
    calls,
    sessions,
    // What the consent page's Continue does: mint a code for req and send
    // the browser to the loopback.
    consent(req) {
      const p = pending.get(req);
      const code = crypto.randomBytes(16).toString('hex');
      codes.set(code, { challenge: p.challenge, redirect: p.redirect });
      return `${p.redirect}?code=${code}&state=${encodeURIComponent(p.state)}`;
    },
    fetch: async (url, init = {}) => {
      const u = new URL(url);
      const body = init.body ? JSON.parse(init.body) : undefined;
      const cookie = (init.headers && init.headers.Cookie) || '';
      calls.push({ path: u.pathname, method: init.method || 'GET', body, cookie });
      if (u.pathname === '/auth/handoff/start') {
        assert.equal(body.code_challenge_method, 'S256');
        const req = crypto.randomBytes(8).toString('hex');
        pending.set(req, { challenge: body.code_challenge, redirect: body.redirect_uri, state: body.state });
        return json(200, { authorize_url: `${consentUrl}?req=${req}`, expires_in: 300 });
      }
      if (u.pathname === '/auth/handoff/redeem') {
        const c = codes.get(body.code);
        codes.delete(body.code); // taken by the first attempt, right or wrong
        if (!c || c.redirect !== body.redirect_uri || s256(body.code_verifier) !== c.challenge) {
          return json(400, { code: 'handoff_invalid', error: 'The code is not valid. Start again from the app.' });
        }
        const id = newSessionId();
        sessions.add(id);
        return json(200, { kind: 'native', session: id, token_type: 'Bearer', expires_at: '2100-01-01T00:00:00Z' });
      }
      const presented = cookie.split('=')[1] || '';
      if (u.pathname === '/auth/session') {
        return sessions.has(presented)
          ? json(200, { authenticated: true, subject, email })
          : json(200, { authenticated: false });
      }
      if (u.pathname === '/auth/logout') {
        sessions.delete(presented);
        return json(200, { authenticated: false });
      }
      return json(404, {});
    },
  };
  return b;
}

function newAuth(broker, overrides = {}) {
  const cookies = fakeCookies();
  const changes = [];
  const opened = [];
  const auth = new ForgeAuth({
    apiUrl: API,
    cookies,
    fetch: broker.fetch,
    logger: quiet,
    onPrincipalChange: (prev, next, reason) => changes.push({ prev, next, reason }),
    // The person clicks Continue: the browser follows the redirect.
    openExternal: async (url) => {
      opened.push(url);
      const req = new URL(url).searchParams.get('req');
      setImmediate(() => browserGet(broker.consent(req)).catch(() => {}));
    },
    ...overrides,
  });
  return { auth, cookies, changes, opened };
}

// ── Flag ─────────────────────────────────────────────────────────────────

test('only AUTH_PROVIDER "forge" selects forge sign-in; anything else keeps Supabase', () => {
  assert.equal(authProviderOf({ AUTH_PROVIDER: 'forge' }), 'forge');
  assert.equal(authProviderOf({ AUTH_PROVIDER: ' Forge ' }), 'forge');
  for (const v of [undefined, '', 'supabase', 'forgee', 1]) {
    assert.equal(authProviderOf({ AUTH_PROVIDER: v }), 'supabase', String(v));
  }
  assert.equal(authProviderOf(null), 'supabase');
});

test('the session cookie is __Host- prefixed on https and the plain name only on http', () => {
  assert.equal(sessionCookieName('https://api.reliantlabs.io'), SECURE_COOKIE_NAME);
  assert.equal(sessionCookieName('http://127.0.0.1:21710'), DEV_COOKIE_NAME);
  assert.equal(AUTH_PARTITION.startsWith('persist:'), true, 'the session survives a restart');
});

test('a PKCE pair is a 256-bit verifier and its S256 challenge', () => {
  const { verifier, challenge } = newPkcePair();
  assert.match(verifier, /^[A-Za-z0-9_-]{43}$/);
  assert.equal(challenge, s256(verifier));
  assert.notEqual(newPkcePair().verifier, verifier);
});

// ── The loopback listener ────────────────────────────────────────────────

test('the loopback ignores a wrong or missing state and settles only for the expected one', async () => {
  const loop = await startLoopback({ state: 'the-state' });
  try {
    assert.match(loop.redirectUri, /^http:\/\/127\.0\.0\.1:\d+\/callback$/);
    // A local process guessing: refused, and the flow keeps waiting.
    assert.equal((await browserGet(`${loop.redirectUri}?code=evil&state=guess`)).status, 400);
    assert.equal((await browserGet(`${loop.redirectUri}?code=evil`)).status, 400);
    assert.equal((await browserGet(`${loop.redirectUri.replace('/callback', '/other')}?code=c&state=the-state`)).status, 404);
    const ok = await browserGet(`${loop.redirectUri}?code=real-code&state=the-state`);
    assert.equal(ok.status, 200);
    assert.equal(await loop.code, 'real-code');
  } finally {
    loop.close();
  }
});

test('the loopback accepts one callback: a replayed redirect finds the flow finished', async () => {
  const loop = await startLoopback({ state: 's' });
  const url = `${loop.redirectUri}?code=c1&state=s`;
  assert.equal((await browserGet(url)).status, 200);
  assert.equal(await loop.code, 'c1');
  const again = await browserGet(url).then((r) => r.status, (e) => e.code);
  assert.ok(again === 410 || again === 'ECONNREFUSED', `a second delivery was ${again}`);
});

test('an error from the consent page with the right state ends the flow; with a wrong state it is ignored', async () => {
  const loop = await startLoopback({ state: 's' });
  assert.equal((await browserGet(`${loop.redirectUri}?error=access_denied&state=nope`)).status, 400);
  await browserGet(`${loop.redirectUri}?error=access_denied&state=s`);
  await assert.rejects(loop.code, /access_denied/);
});

// ── Sign-in ──────────────────────────────────────────────────────────────

test('sign-in redeems the code with the verifier of the start, keeps the session in the auth jar, and returns no credential', async () => {
  const broker = fakeBroker();
  const { auth, cookies, changes, opened } = newAuth(broker);
  const who = await auth.signIn();

  assert.deepEqual(who, { subject: 'user-1', email: 'p@example.com', name: '' });
  assert.doesNotMatch(JSON.stringify(who), /fs_/, 'the answer to the renderer carries no session id');

  const start = broker.calls.find((c) => c.path === '/auth/handoff/start');
  const redeem = broker.calls.filter((c) => c.path === '/auth/handoff/redeem');
  assert.equal(redeem.length, 1);
  assert.equal(s256(redeem[0].body.code_verifier), start.body.code_challenge, 'the verifier matches the challenge');
  assert.equal(redeem[0].body.redirect_uri, start.body.redirect_uri);
  assert.ok(!JSON.stringify(start.body).includes(redeem[0].body.code_verifier), 'the verifier is not in the start');
  assert.ok(!opened[0].includes(redeem[0].body.code_verifier), 'the verifier is not in the browser URL');
  assert.equal(opened.length, 1);

  const stored = [...cookies.jar.values()];
  assert.equal(stored.length, 1);
  assert.equal(stored[0].name, SECURE_COOKIE_NAME);
  assert.equal(stored[0].httpOnly, true);
  assert.equal(stored[0].secure, true);
  assert.equal(stored[0].url, `${API}/`);
  assert.ok(isSessionId(stored[0].value));

  assert.deepEqual(changes.map((c) => [c.prev, c.next && c.next.subject, c.reason]), [[null, 'user-1', 'sign-in']]);
  const forDaemon = auth.storage().loadStoredAuth();
  assert.equal(forDaemon.user.id, 'user-1');
  assert.equal(forDaemon.cookie, `${SECURE_COOKIE_NAME}=${stored[0].value}`);
});

test('a refused redeem is not retried (the code is single-use) and stores nothing', async () => {
  const broker = fakeBroker();
  const { auth, cookies } = newAuth(broker, {
    // The browser is sent with a code the broker never minted.
    openExternal: async (url) => {
      const req = new URL(url).searchParams.get('req');
      const real = new URL(broker.consent(req));
      real.searchParams.set('code', 'forged');
      setImmediate(() => browserGet(real.toString()).catch(() => {}));
    },
  });
  await assert.rejects(auth.signIn(), /code is not valid/);
  assert.equal(broker.calls.filter((c) => c.path === '/auth/handoff/redeem').length, 1);
  assert.equal(cookies.jar.size, 0);
  assert.equal(auth.storage().loadStoredAuth(), null);
});

test('a redeemed code cannot be redeemed again', async () => {
  const broker = fakeBroker();
  let codeUrl;
  const { auth } = newAuth(broker, {
    openExternal: async (url) => {
      codeUrl = broker.consent(new URL(url).searchParams.get('req'));
      setImmediate(() => browserGet(codeUrl).catch(() => {}));
    },
  });
  await auth.signIn();
  const redeem = broker.calls.find((c) => c.path === '/auth/handoff/redeem');
  const replay = await broker.fetch(`${API}/auth/handoff/redeem`, { method: 'POST', body: JSON.stringify(redeem.body) });
  assert.equal(replay.status, 400);
});

test('a consent page that is not a web page is never opened', async () => {
  for (const consentUrl of ['file:///etc/passwd', 'javascript:alert(1)', 'reliant-evil:/x']) {
    const broker = fakeBroker({ consentUrl });
    const { auth, opened } = newAuth(broker);
    await assert.rejects(auth.signIn(), /not a web page|invalid/);
    assert.equal(opened.length, 0, consentUrl);
  }
  const broker = fakeBroker();
  const { auth, opened } = newAuth(broker, { allowedConsentOrigins: ['https://app.reliantlabs.io'] });
  await assert.rejects(auth.signIn(), /does not trust/);
  assert.equal(opened.length, 0);
});

test('a redeem answer that is not a native session id stores nothing', async () => {
  const broker = fakeBroker();
  const inner = broker.fetch;
  broker.fetch = async (url, init) => {
    if (new URL(url).pathname === '/auth/handoff/redeem') {
      await inner(url, init);
      return json(200, { kind: 'native', session: 'eyJhbGciOi.jwt.token' });
    }
    return inner(url, init);
  };
  const { auth, cookies } = newAuth(broker);
  await assert.rejects(auth.signIn(), /finish the sign-in/);
  assert.equal(cookies.jar.size, 0);
});

test('sign-out ends the session at the server and here, and the daemon follows', async () => {
  const broker = fakeBroker();
  const { auth, cookies, changes } = newAuth(broker);
  await auth.signIn();
  const id = [...cookies.jar.values()][0].value;
  await auth.signOut();
  const logout = broker.calls.find((c) => c.path === '/auth/logout');
  assert.equal(logout.cookie, `${SECURE_COOKIE_NAME}=${id}`);
  assert.equal(broker.sessions.has(id), false);
  assert.equal(cookies.jar.size, 0);
  assert.equal(auth.storage().loadStoredAuth(), null);
  assert.deepEqual(changes.at(-1).next, null);
  assert.equal(changes.at(-1).prev.subject, 'user-1');
});

test('startup reads the stored session; one the server no longer knows is dropped, an outage keeps it', async () => {
  const broker = fakeBroker();
  const cookies = fakeCookies();
  const id = newSessionId();
  broker.sessions.add(id);
  await cookies.set({ url: `${API}/`, name: SECURE_COOKIE_NAME, value: id });
  const auth = new ForgeAuth({ apiUrl: API, cookies, fetch: broker.fetch, logger: quiet, openExternal() {} });
  assert.equal((await auth.init()).subject, 'user-1');

  broker.sessions.delete(id);
  await auth.refreshPrincipal('test');
  assert.equal(cookies.jar.size, 0, 'an ended session is forgotten');

  await cookies.set({ url: `${API}/`, name: SECURE_COOKIE_NAME, value: id });
  const down = new ForgeAuth({
    apiUrl: API, cookies, logger: quiet, openExternal() {},
    fetch: async () => json(503, { error: 'unavailable' }),
  });
  await down.init();
  assert.equal(down.principal, null);
  assert.equal(cookies.jar.size, 1, 'an unreachable server does not sign the person out');
});

// ── The API proxy ────────────────────────────────────────────────────────

test('upstreamFor maps the named APIs and never leaves the upstream', () => {
  const up = { reliant: API, cp: CP };
  assert.equal(upstreamFor(`${APP}/_api/@reliant/reliant.v1.ChatService/ListChats`, up), `${API}/reliant.v1.ChatService/ListChats`);
  assert.equal(upstreamFor(`${APP}/_api/@cp/auth/session?x=1`, up), `${CP}/auth/session?x=1`);
  assert.equal(upstreamFor(`${APP}/_api/@reliant`, up), `${API}/`);
  assert.equal(upstreamFor(`${APP}/_api/@reliant//evil.example/x`, up), `${API}/evil.example/x`);
  for (const bad of [
    `${APP}/_api/@nope/x`,
    `${APP}/_api/x`,
    `${APP}/_api/@reliant/a%2f..%2fb`,
    `${APP}/_api/@constructor/x`,
    `${APP}/assets/x.js`,
  ]) {
    assert.equal(upstreamFor(bad, up), null, bad);
  }
  assert.equal(upstreamFor(`${APP}/_api/@cp/x`, { reliant: API, cp: '' }), null, 'an unset upstream is not served');
});

test('isFromApp admits the app origin only, and no navigation', () => {
  const h = (o) => new Headers(o);
  assert.equal(isFromApp(h({ origin: APP, 'sec-fetch-site': 'same-origin', 'sec-fetch-mode': 'cors' }), APP), true);
  assert.equal(isFromApp(h({}), APP), true);
  assert.equal(isFromApp(h({ origin: 'https://evil.example' }), APP), false);
  assert.equal(isFromApp(h({ origin: 'null' }), APP), false);
  assert.equal(isFromApp(h({ 'sec-fetch-site': 'cross-site' }), APP), false);
  assert.equal(isFromApp(h({ 'sec-fetch-site': 'same-site' }), APP), false);
  assert.equal(isFromApp(h({ 'sec-fetch-mode': 'navigate' }), APP), false);
});

async function signedInProxy(upstreamFetch) {
  const broker = fakeBroker();
  const { auth, cookies, changes } = newAuth(broker);
  await auth.signIn();
  const id = [...cookies.jar.values()][0].value;
  auth.fetch = broker.fetch; // /auth/session for applySetCookies
  const seen = [];
  const proxy = createApiProxy({
    upstreams: { reliant: API, cp: CP },
    auth,
    appOrigin: APP,
    logger: quiet,
    fetch: async (url, init) => {
      seen.push({ url, init });
      return upstreamFetch(url, init, broker);
    },
  });
  return { proxy, seen, id, cookies, changes, broker };
}

test('the proxy attaches the session cookie, and drops what the renderer claims', async () => {
  const { proxy, seen, id } = await signedInProxy(async () => json(200, { ok: true }));
  const res = await proxy(new Request(`${APP}/_api/@reliant/reliant.v1.ChatService/ListChats`, {
    method: 'POST',
    headers: {
      origin: APP,
      'content-type': 'application/json',
      'connect-protocol-version': '1',
      cookie: `${SECURE_COOKIE_NAME}=fs_planted`,
      authorization: 'Bearer stolen',
      'x-forwarded-for': '1.2.3.4',
    },
    body: '{}',
  }));
  assert.equal(res.status, 200);
  assert.equal(seen.length, 1);
  assert.equal(seen[0].url, `${API}/reliant.v1.ChatService/ListChats`);
  const sent = seen[0].init.headers;
  assert.equal(sent.get('cookie'), `${SECURE_COOKIE_NAME}=${id}`);
  assert.equal(sent.get('authorization'), null);
  assert.equal(sent.get('x-forwarded-for'), null);
  assert.equal(sent.get('origin'), null);
  assert.equal(sent.get('connect-protocol-version'), '1');
  assert.equal(Buffer.from(seen[0].init.body).toString(), '{}');
});

test('the renderer never receives a Set-Cookie; a cleared session cookie signs the app out', async () => {
  const { proxy, cookies, changes, id, broker } = await signedInProxy(async (_url, _init, b) => {
    for (const s of b.sessions) b.sessions.delete(s);
    return json(200, { authenticated: false }, {
      'set-cookie': `${SECURE_COOKIE_NAME}=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax`,
    });
  });
  assert.ok(broker.sessions.size === 1 && id);
  const res = await proxy(new Request(`${APP}/_api/@reliant/auth/logout`, { method: 'POST', headers: { origin: APP }, body: '{}' }));
  assert.equal(res.headers.get('set-cookie'), null);
  assert.doesNotMatch(await res.text(), /fs_/);
  assert.equal(cookies.jar.size, 0);
  assert.equal(changes.at(-1).next, null);
});

test('a new session cookie from an in-window sign-in is kept here, not given to the renderer', async () => {
  const fresh = newSessionId();
  const { proxy, cookies } = await signedInProxy(async (_url, _init, b) => {
    b.sessions.add(fresh);
    return json(200, { authenticated: true }, { 'set-cookie': `${SECURE_COOKIE_NAME}=${fresh}; Path=/; Max-Age=3600; HttpOnly; Secure` });
  });
  const res = await proxy(new Request(`${APP}/_api/@reliant/auth/login`, { method: 'POST', headers: { origin: APP }, body: '{}' }));
  assert.equal(res.headers.get('set-cookie'), null);
  assert.equal([...cookies.jar.values()][0].value, fresh);
});

test('a request from another origin is refused before it reaches the API', async () => {
  const { proxy, seen } = await signedInProxy(async () => json(200, {}));
  const res = await proxy(new Request(`${APP}/_api/@reliant/reliant.v1.ApprovalService/Decide`, {
    method: 'POST', headers: { origin: 'https://evil.example', 'content-type': 'text/plain' }, body: 'x',
  }));
  assert.equal(res.status, 403);
  assert.equal(seen.length, 0);
});

test('parseSetCookie reads clearing and lifetimes', () => {
  const now = Date.UTC(2030, 0, 1);
  assert.equal(parseSetCookie('a=; Max-Age=0', now).cleared, true);
  assert.equal(parseSetCookie('a=b; Expires=Thu, 01 Jan 1970 00:00:00 GMT', now).cleared, true);
  const kept = parseSetCookie('a=b; Max-Age=60; HttpOnly', now);
  assert.equal(kept.cleared, false);
  assert.equal(kept.expiresAt.getTime(), now + 60000);
  assert.equal(parseSetCookie('garbage', now), null);
});

test('signed out, the proxy forwards no cookie at all, even one the renderer planted', async () => {
  const seen = [];
  const auth = new ForgeAuth({ apiUrl: API, cookies: fakeCookies(), logger: quiet, openExternal() {}, fetch: async () => json(200, {}) });
  const proxy = createApiProxy({
    upstreams: { reliant: API }, auth, appOrigin: APP, logger: quiet,
    fetch: async (url, init) => { seen.push(init.headers); return json(401, { code: 'unauthenticated' }); },
  });
  const res = await proxy(new Request(`${APP}/_api/@reliant/x`, { headers: { cookie: `${SECURE_COOKIE_NAME}=fs_planted` } }));
  assert.equal(res.status, 401);
  assert.equal(seen[0].get('cookie'), null);
});

test('originOf names an app:// sender, which URL.origin cannot', () => {
  const { originOf } = require('../src/forge-auth');
  assert.equal(new URL('app://bundle/chat/1').origin, 'null', 'the trap originOf exists for');
  assert.equal(originOf('app://bundle/chat/1'), APP);
  assert.equal(originOf('http://127.0.0.1:5173/x'), 'http://127.0.0.1:5173');
  assert.equal(originOf('https://evil.example/app://bundle'), 'https://evil.example');
  assert.equal(originOf('not a url'), '');
  assert.equal(originOf(undefined), '');
});
