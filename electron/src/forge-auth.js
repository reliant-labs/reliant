/**
 * Desktop sign-in with the managed identity (forge sign-in, `AUTH_PROVIDER`
 * "forge" in the renderer's config.js). With "supabase", the default, none of
 * this runs and the app behaves exactly as before.
 *
 * ── The shape ─────────────────────────────────────────────────────────
 *
 * The web app's session is an HttpOnly cookie holding an opaque server-side
 * session id (`fs_…`, forge/pkg/session), set by reliant-api's login broker.
 * The desktop renderer is `app://bundle`, which no API host can set a cookie
 * for, and Google refuses sign-in inside an embedded webview. So:
 *
 *   1. SIGN-IN is forge's sign-in handoff (devidp.HandoffRoutes, RFC 8252):
 *      this process listens on http://127.0.0.1:<port>/callback, starts a
 *      handoff with a PKCE S256 challenge, and opens the web app's consent page
 *      in the SYSTEM BROWSER. The person, signed in there (or signing in
 *      there, with any method the web offers), clicks Continue; the browser
 *      lands on the loopback with a one-time code; this process redeems it
 *      with the verifier for a NEW session of the app's own (`fs_…`).
 *   2. The session id LIVES IN THIS PROCESS: a cookie in a dedicated
 *      persistent Electron session partition (AUTH_PARTITION) that no window
 *      or browser tab ever loads, so no renderer script can read it and no
 *      page — not the app's own, not a site in a browser tab — can send it.
 *   3. The app:// protocol handler PROXIES `/_api/@reliant/*` and
 *      `/_api/@cp/*` (the same named same-origin APIs the hosted web app
 *      uses) to reliant-api and the control plane, attaching the session as
 *      the broker's cookie. To the renderer it is a first-party, same-origin
 *      API; it never sees the credential. Set-Cookie answers are applied to
 *      the partition, never passed on.
 *   4. The daemon's PAT is minted with that cookie, and the daemon respawns
 *      when the signed-in principal changes, both from GET /auth/session.
 *
 * The session is a browser session to the API (the broker's cookie, resolved
 * through the session store), so the person can decide approvals from the
 * desktop app as from the web app.
 *
 * Everything here takes its Electron and network dependencies as arguments,
 * so it is unit-tested without Electron (test/forge-auth.test.js).
 */

const crypto = require('crypto');
const http = require('http');

const AUTH_PROVIDER_FORGE = 'forge';
const AUTH_PROVIDER_SUPABASE = 'supabase';

/** The Electron session partition the sign-in cookie lives in. No webContents uses it. */
const AUTH_PARTITION = 'persist:reliant-auth';

/** The loopback path the handoff redirects to; reliant-api's handoff_redirects lists it. */
const CALLBACK_PATH = '/callback';

/** How long a handoff waits for the person in the browser (forge's request TTL). */
const HANDOFF_TIMEOUT_MS = 5 * 60 * 1000;

/** How long one call to the broker may take. */
const BROKER_TIMEOUT_MS = 30 * 1000;

/** forge/pkg/session IsID: "fs_" and 32 bytes of unpadded base64url. */
const SESSION_ID_RE = /^fs_[A-Za-z0-9_-]{43}$/;

/** The broker's cookie names (internal/auth/cookie.go). */
const SECURE_COOKIE_NAME = '__Host-reliant_session';
const DEV_COOKIE_NAME = 'reliant_session';

/** The named same-origin APIs the renderer calls, as the hosted origin serves them. */
const API_PREFIX = '/_api/@';

/**
 * Which sign-in the renderer config selects. Anything but exactly "forge" is
 * Supabase, so a missing or misspelled value keeps today's behaviour.
 *
 * @param {Record<string, unknown>} doc the renderer config document
 * @returns {'forge' | 'supabase'}
 */
function authProviderOf(doc) {
  // Exactly "forge", as the renderer reads it (frontends/web
  // lib/desktop-forge-sign-in.ts authProvider, and the config schema's enum):
  // a value one side normalizes and the other does not would put the main
  // process on forge and the renderer on Supabase.
  return doc && doc.AUTH_PROVIDER === AUTH_PROVIDER_FORGE ? AUTH_PROVIDER_FORGE : AUTH_PROVIDER_SUPABASE;
}

/**
 * scheme://host of a URL, or '' when it does not parse. Not URL.origin, which
 * is "null" for a non-special scheme like app:// — so an `app://bundle` sender
 * would never match.
 */
function originOf(raw) {
  try {
    const u = new URL(raw);
    return `${u.protocol}//${u.host}`;
  } catch {
    return '';
  }
}

function isSessionId(value) {
  return typeof value === 'string' && SESSION_ID_RE.test(value);
}

/**
 * The cookie name reliant-api reads for this upstream: __Host- wherever it is
 * Secure (https), the plain name only on a plain-http dev stack.
 */
function sessionCookieName(apiUrl) {
  return String(apiUrl || '').toLowerCase().startsWith('https://') ? SECURE_COOKIE_NAME : DEV_COOKIE_NAME;
}

function base64url(buf) {
  return Buffer.from(buf).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** A PKCE pair (RFC 7636): a 256-bit verifier and its S256 challenge. */
function newPkcePair(randomBytes = crypto.randomBytes) {
  const verifier = base64url(randomBytes(32));
  const challenge = base64url(crypto.createHash('sha256').update(verifier).digest());
  return { verifier, challenge };
}

/** Constant-time string equality; false for different lengths. */
function safeEqual(a, b) {
  const x = Buffer.from(String(a));
  const y = Buffer.from(String(b));
  return x.length === y.length && crypto.timingSafeEqual(x, y);
}

const CALLBACK_PAGE =
  '<!doctype html><meta charset="utf-8"><title>Signed in</title>' +
  '<style>body{font:14px -apple-system,system-ui,sans-serif;display:grid;place-items:center;' +
  'height:100vh;margin:0;color:#333}</style>' +
  '<p>Signed in. You can close this tab and return to Reliant.</p>';

/**
 * Listen on 127.0.0.1:<any port> for the ONE redirect of the handoff this
 * process started.
 *
 * Any local process can reach a loopback port, so the listener trusts nothing
 * before the state matches: a request with another state (or none) is refused
 * and the flow keeps waiting — it can neither inject a code nor end the
 * sign-in. The first request with the right state settles the flow (a code,
 * or the error the consent page sent), and the listener closes: a second
 * delivery of the same code finds nothing listening.
 *
 * @param {{ state: string, timeoutMs?: number, createServer?: typeof http.createServer }} opts
 * @returns {Promise<{ redirectUri: string, code: Promise<string>, close: () => void }>}
 */
function startLoopback({ state, timeoutMs = HANDOFF_TIMEOUT_MS, createServer = http.createServer }) {
  if (!state) throw new Error('startLoopback: a state is required');
  return new Promise((resolve, reject) => {
    let settle;
    const code = new Promise((ok, fail) => {
      settle = { ok, fail };
    });
    // Nobody may be awaiting yet; an unhandled rejection must not crash main.
    code.catch(() => {});
    let done = false;
    let timer = null;

    const finish = (err, value) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      try {
        server.close();
      } catch {
        // already closed
      }
      if (err) settle.fail(err);
      else settle.ok(value);
    };

    const server = createServer((req, res) => {
      res.setHeader('Cache-Control', 'no-store');
      let url;
      try {
        url = new URL(req.url, 'http://127.0.0.1');
      } catch {
        res.writeHead(400).end('Bad request');
        return;
      }
      if (req.method !== 'GET' || url.pathname !== CALLBACK_PATH) {
        res.writeHead(404).end('Not found');
        return;
      }
      if (done) {
        res.writeHead(410).end('This sign-in has already finished.');
        return;
      }
      // State FIRST, and in constant time: until it matches, this request
      // is not from the flow we started.
      if (!safeEqual(url.searchParams.get('state') || '', state)) {
        res.writeHead(400).end('state mismatch');
        return;
      }
      const error = url.searchParams.get('error');
      if (error) {
        res.writeHead(400, { 'Content-Type': 'text/plain; charset=utf-8' }).end('Sign-in was not completed.');
        finish(new Error(`sign-in was not completed (${error})`));
        return;
      }
      const value = url.searchParams.get('code') || '';
      if (!value) {
        res.writeHead(400).end('no code');
        finish(new Error('the browser returned no sign-in code'));
        return;
      }
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }).end(CALLBACK_PAGE);
      finish(null, value);
    });

    server.on('error', (err) => {
      if (!done) {
        done = true;
        clearTimeout(timer);
        settle.fail(err);
      }
      reject(err);
    });

    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      timer = setTimeout(() => finish(new Error('timed out waiting for the browser to finish signing in')), timeoutMs);
      if (typeof timer.unref === 'function') timer.unref();
      resolve({
        redirectUri: `http://127.0.0.1:${port}${CALLBACK_PATH}`,
        code,
        close: () => finish(new Error('sign-in was cancelled')),
      });
    });
  });
}

/**
 * The sign-in session, held as a cookie in the auth partition's cookie jar
 * (Electron `session.fromPartition(AUTH_PARTITION).cookies`), keyed to the
 * reliant-api origin. Also mirrors it, in development, into the renderer's
 * own jar for the dev server origin (see `devMirror`), where the Vite proxy —
 * not this process — forwards `/_api/@…`.
 */
class SessionCookie {
  /**
   * @param {{ cookies: object, apiUrl: string, devMirror?: { cookies: object, url: string } | null }} opts
   */
  constructor({ cookies, apiUrl, devMirror = null }) {
    this.cookies = cookies;
    this.url = new URL(apiUrl).origin + '/';
    this.name = sessionCookieName(apiUrl);
    this.devMirror = devMirror;
    this.cached = '';
  }

  async load() {
    const found = await this.cookies.get({ url: this.url, name: this.name });
    const value = found && found[0] && found[0].value;
    this.cached = isSessionId(value) ? value : '';
    return this.cached;
  }

  value() {
    return this.cached;
  }

  /** The Cookie header that presents the session, or '' with none. */
  header() {
    return this.cached ? `${this.name}=${this.cached}` : '';
  }

  async set(id, expiresAt) {
    if (!isSessionId(id)) throw new Error('not a session id');
    const expirationDate = expiresAt instanceof Date && !Number.isNaN(expiresAt.getTime())
      ? Math.floor(expiresAt.getTime() / 1000)
      : undefined;
    const secure = this.name === SECURE_COOKIE_NAME;
    await this.cookies.set({
      url: this.url, name: this.name, value: id, path: '/',
      httpOnly: true, secure, sameSite: 'lax', ...(expirationDate ? { expirationDate } : {}),
    });
    if (this.devMirror) {
      await this.devMirror.cookies.set({
        url: this.devMirror.url, name: DEV_COOKIE_NAME, value: id, path: '/',
        httpOnly: true, sameSite: 'lax', ...(expirationDate ? { expirationDate } : {}),
      });
    }
    this.cached = id;
    if (typeof this.cookies.flushStore === 'function') await this.cookies.flushStore();
  }

  async clear() {
    this.cached = '';
    await this.cookies.remove(this.url, this.name);
    if (this.devMirror) await this.devMirror.cookies.remove(this.devMirror.url, DEV_COOKIE_NAME);
    if (typeof this.cookies.flushStore === 'function') await this.cookies.flushStore();
  }

  /**
   * Development only: take the session the RENDERER's jar holds for the dev
   * server origin. There the Vite proxy, not this process, forwards
   * `/_api/@…`, so an in-window sign-in or sign-out lands in that jar; this
   * makes the auth partition follow it (a new id, a cleared one, another
   * account's). Packaged, it re-reads the partition.
   *
   * @returns {Promise<boolean>} whether the session changed
   */
  async syncFromDevMirror() {
    const before = this.cached;
    if (!this.devMirror) {
      await this.load();
      return this.cached !== before;
    }
    const found = await this.devMirror.cookies.get({ url: this.devMirror.url, name: DEV_COOKIE_NAME });
    const raw = found && found[0] && found[0].value;
    const value = isSessionId(raw) ? raw : '';
    if (value === before) return false;
    if (value) {
      const exp = found[0].expirationDate ? new Date(found[0].expirationDate * 1000) : undefined;
      const secure = this.name === SECURE_COOKIE_NAME;
      await this.cookies.set({
        url: this.url, name: this.name, value, path: '/', httpOnly: true, secure, sameSite: 'lax',
        ...(exp ? { expirationDate: Math.floor(exp.getTime() / 1000) } : {}),
      });
    } else {
      await this.cookies.remove(this.url, this.name);
    }
    this.cached = value;
    if (typeof this.cookies.flushStore === 'function') await this.cookies.flushStore();
    return true;
  }
}

/** Thrown by a sign-in that a sign-out or a newer sign-in replaced. */
class SignInSuperseded extends Error {
  constructor() {
    super('sign-in was replaced by a newer sign-in or a sign-out');
    this.name = 'SignInSuperseded';
  }
}

/**
 * The desktop app's forge sign-in: the handoff, the session, the principal.
 *
 * ── Ordering ──────────────────────────────────────────────────────────
 *
 * A sign-in spans the person's time in the browser plus two broker calls, and
 * a sign-out or a second sign-in can happen at any point of it. Every attempt
 * takes a GENERATION; a sign-out or a newer attempt bumps it, and an attempt
 * commits nothing once it is no longer current: a session it already redeemed
 * is revoked at the server instead of stored. Every change to the stored
 * session and the principal runs through one queue (serialize), and a
 * principal read that finishes after the session it read has changed is
 * discarded, so a late answer never resurrects a signed-out person.
 */
class ForgeAuth {
  /**
   * @param {{
   *   apiUrl: string,                  reliant-api (serves /auth/*)
   *   cookies: object,                 the auth partition's cookie jar
   *   devMirror?: { cookies: object, url: string } | null,
   *   fetch?: typeof fetch,
   *   openExternal: (url: string) => Promise<void> | void,
   *   onPrincipalChange?: (previous: object|null, next: object|null, reason: string) => void,
   *   allowedConsentOrigins?: string[], origins the consent page may be on; [] trusts the broker
   *   logger?: object,
   *   startLoopback?: typeof startLoopback,
   * }} opts
   */
  constructor(opts) {
    if (!opts || !opts.apiUrl) throw new Error('ForgeAuth: apiUrl is required');
    this.apiUrl = opts.apiUrl.replace(/\/+$/, '');
    this.cookie = new SessionCookie({ cookies: opts.cookies, apiUrl: this.apiUrl, devMirror: opts.devMirror });
    this.fetch = opts.fetch || ((...a) => globalThis.fetch(...a));
    this.openExternal = opts.openExternal;
    this.onPrincipalChange = opts.onPrincipalChange || (() => {});
    this.allowedConsentOrigins = opts.allowedConsentOrigins || [];
    this.log = opts.logger || { debug() {}, info() {}, warn() {}, error() {} };
    this.startLoopback = opts.startLoopback || startLoopback;
    /** @type {{ subject: string, email: string, name: string } | null} */
    this.principal = null;
    this.inFlight = null;
    this.generation = 0;
    this.queue = Promise.resolve();
  }

  /** Run fn after every change queued before it; its result or error is the caller's. */
  serialize(fn) {
    const run = this.queue.then(fn, fn);
    this.queue = run.catch(() => {});
    return run;
  }

  /** Load the stored session and who it is. Never throws. */
  async init() {
    try {
      await this.serialize(() => (this.cookie.devMirror ? this.cookie.syncFromDevMirror() : this.cookie.load()));
      if (this.cookie.value()) await this.refreshPrincipal('startup', { notify: false });
    } catch (err) {
      this.log.warn('[ForgeAuth] could not read the stored session:', err && err.message);
    }
    return this.principal;
  }

  /**
   * The renderer says the session changed (an in-window sign-in or
   * sign-out): follow its jar in development, re-read the partition when
   * packaged, then re-read who it is.
   */
  async syncFromRenderer() {
    await this.serialize(() => this.cookie.syncFromDevMirror());
    return this.refreshPrincipal('renderer');
  }

  /**
   * The authStorage shape BackendManager and daemon-creds read, synchronously:
   * the principal from the last /auth/session read, and the credential to mint
   * the daemon's PAT with (the session cookie — it never leaves this process).
   */
  storage() {
    return {
      loadStoredAuth: () => {
        if (!this.principal || !this.cookie.value()) return null;
        return {
          cookie: this.cookie.header(),
          user: { id: this.principal.subject, email: this.principal.email },
        };
      },
    };
  }

  /**
   * One call to the broker. It presents the stored session as the cookie,
   * or — `bearer` — the given session id as a bearer and no cookie (revoking
   * a session that was never stored).
   */
  async brokerCall(path, { method = 'GET', body, bearer } = {}) {
    const headers = { Accept: 'application/json' };
    if (bearer) {
      headers.Authorization = `Bearer ${bearer}`;
    } else {
      const cookie = this.cookie.header();
      if (cookie) headers.Cookie = cookie;
    }
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const res = await this.fetch(`${this.apiUrl}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      redirect: 'manual',
      signal: AbortSignal.timeout(BROKER_TIMEOUT_MS),
    });
    let data = null;
    try {
      data = await res.json();
    } catch {
      data = null;
    }
    return { status: res.status, ok: res.ok, data };
  }

  /**
   * Read GET /auth/session and record who is signed in. A session the server
   * no longer knows is dropped; an unreachable server keeps everything as it
   * is (the session may be fine). An answer about a session that has since
   * changed is discarded.
   */
  async refreshPrincipal(reason, { notify = true } = {}) {
    const asked = this.cookie.value();
    let next = null;
    let known = true;
    if (asked) {
      const { ok, status, data } = await this.brokerCall('/auth/session');
      if (!ok) throw new Error(`GET /auth/session answered HTTP ${status}`);
      if (data && data.authenticated === true && typeof data.subject === 'string' && data.subject) {
        next = {
          subject: data.subject,
          email: typeof data.email === 'string' ? data.email : '',
          name: typeof data.name === 'string' ? data.name : '',
        };
      } else {
        known = false;
      }
    }
    return this.serialize(async () => {
      if (this.cookie.value() !== asked) return this.principal; // stale: the session changed meanwhile
      if (!known) await this.cookie.clear();
      const previous = this.principal;
      this.principal = next;
      if (notify && (previous && previous.subject) !== (next && next.subject)) {
        this.onPrincipalChange(previous, next, reason);
      }
      return next;
    });
  }

  /** End, at the server, a session this process redeemed but will not keep. */
  async revokeAbandoned(session) {
    try {
      await this.brokerCall('/auth/logout', { method: 'POST', body: {}, bearer: session });
    } catch (err) {
      this.log.warn('[ForgeAuth] could not end an abandoned session (it expires on its own):', err && err.message);
    }
  }

  /**
   * Sign in through the system browser (forge's sign-in handoff). Resolves
   * with who signed in; the session id stays in this process. A second call
   * replaces the first (the person closed the tab and clicked again), and a
   * sign-out ends it: see Ordering above.
   */
  async signIn() {
    if (this.inFlight) this.inFlight.close();
    const gen = ++this.generation;
    const current = () => gen === this.generation;
    const { verifier, challenge } = newPkcePair();
    const state = base64url(crypto.randomBytes(24));
    const loop = await this.startLoopback({ state });
    this.inFlight = loop;
    try {
      if (!current()) throw new SignInSuperseded();
      const started = await this.brokerCall('/auth/handoff/start', {
        method: 'POST',
        body: { code_challenge: challenge, code_challenge_method: 'S256', redirect_uri: loop.redirectUri, state },
      });
      if (!started.ok || !started.data || typeof started.data.authorize_url !== 'string') {
        throw new Error(brokerError('start the sign-in', started));
      }
      const authorizeUrl = this.checkConsentUrl(started.data.authorize_url);
      if (!current()) throw new SignInSuperseded();
      await this.openExternal(authorizeUrl);

      const code = await loop.code;
      if (!current()) throw new SignInSuperseded();
      // One attempt per code: forge burns it on the first redeem, right or
      // wrong, so a failure here is reported, never retried.
      const redeemed = await this.brokerCall('/auth/handoff/redeem', {
        method: 'POST',
        body: { code, code_verifier: verifier, redirect_uri: loop.redirectUri },
      });
      const session = redeemed.data && redeemed.data.session;
      if (!redeemed.ok || redeemed.data.kind !== 'native' || !isSessionId(session)) {
        throw new Error(brokerError('finish the sign-in', redeemed));
      }
      const expiresAt = new Date(redeemed.data.expires_at);
      await this.serialize(async () => {
        if (!current()) {
          await this.revokeAbandoned(session);
          throw new SignInSuperseded();
        }
        await this.cookie.set(session, expiresAt);
      });
      const principal = await this.refreshPrincipal('sign-in');
      if (!current()) throw new SignInSuperseded();
      if (!principal) throw new Error('the new session was not accepted');
      this.log.info('[ForgeAuth] signed in through the system browser');
      return { subject: principal.subject, email: principal.email, name: principal.name };
    } finally {
      loop.close();
      if (this.inFlight === loop) this.inFlight = null;
    }
  }

  /**
   * The consent page must be a web page (http(s)), never a file: or custom
   * scheme the OS would hand to another app; and, when origins are pinned, on
   * one of them.
   */
  checkConsentUrl(raw) {
    let u;
    try {
      u = new URL(raw);
    } catch {
      throw new Error('the sign-in service answered an invalid consent page address');
    }
    if (u.protocol !== 'https:' && u.protocol !== 'http:') {
      throw new Error('the sign-in service answered a consent page that is not a web page');
    }
    if (this.allowedConsentOrigins.length > 0 && !this.allowedConsentOrigins.includes(u.origin)) {
      throw new Error(`the consent page is on ${u.origin}, which this app does not trust`);
    }
    return u.toString();
  }

  /**
   * Sign out: end the session at the server and forget it here. Any sign-in
   * still in progress is ended too, and commits nothing. Never throws.
   */
  async signOut() {
    if (this.inFlight) this.inFlight.close();
    this.generation++;
    await this.serialize(async () => {
      const previous = this.principal;
      if (this.cookie.value()) {
        try {
          await this.brokerCall('/auth/logout', { method: 'POST', body: {} });
        } catch (err) {
          // The session is forgotten here anyway; at the server it ends at
          // its idle or absolute expiry.
          this.log.warn('[ForgeAuth] sign-out could not reach the server:', err && err.message);
        }
      }
      await this.cookie.clear();
      this.principal = null;
      if (previous) this.onPrincipalChange(previous, null, 'sign-out');
    });
  }

  /**
   * Apply an upstream's Set-Cookie for the session cookie (the proxy calls
   * this; the renderer never receives one). A cleared cookie ends the
   * session here; a new session id (an in-window password sign-in through
   * the proxy) replaces it.
   *
   * @param {string[]} setCookies
   * @returns {Promise<boolean>} whether the session changed
   */
  async applySetCookies(setCookies) {
    const changed = await this.serialize(async () => {
      let any = false;
      for (const line of setCookies || []) {
        const parsed = parseSetCookie(line);
        if (!parsed || (parsed.name !== SECURE_COOKIE_NAME && parsed.name !== DEV_COOKIE_NAME)) continue;
        if (parsed.cleared || !isSessionId(parsed.value)) {
          if (this.cookie.value()) {
            await this.cookie.clear();
            any = true;
          }
        } else if (parsed.value !== this.cookie.value()) {
          await this.cookie.set(parsed.value, parsed.expiresAt);
          any = true;
        }
      }
      return any;
    });
    if (changed) {
      try {
        await this.refreshPrincipal('session-cookie');
      } catch (err) {
        this.log.warn('[ForgeAuth] could not read the session after it changed:', err && err.message);
      }
    }
    return changed;
  }
}

function brokerError(what, res) {
  const msg = res && res.data && (res.data.error || res.data.message);
  return `could not ${what}: ${msg || `HTTP ${res ? res.status : '?'}`}`;
}

/**
 * The name, value and lifetime of one Set-Cookie line.
 *
 * @returns {{ name: string, value: string, cleared: boolean, expiresAt?: Date } | null}
 */
function parseSetCookie(line, now = Date.now()) {
  if (typeof line !== 'string') return null;
  const parts = line.split(';');
  const eq = parts[0].indexOf('=');
  if (eq <= 0) return null;
  const name = parts[0].slice(0, eq).trim();
  const value = parts[0].slice(eq + 1).trim();
  let cleared = value === '';
  let expiresAt;
  for (const attr of parts.slice(1)) {
    const [k, ...rest] = attr.split('=');
    const key = k.trim().toLowerCase();
    const v = rest.join('=').trim();
    if (key === 'max-age') {
      const n = Number(v);
      if (Number.isFinite(n)) {
        if (n <= 0) cleared = true;
        else expiresAt = new Date(now + n * 1000);
      }
    } else if (key === 'expires' && !expiresAt) {
      const t = Date.parse(v);
      if (Number.isFinite(t)) {
        if (t <= now) cleared = true;
        else expiresAt = new Date(t);
      }
    }
  }
  return { name, value, cleared, expiresAt };
}

// ── The terminal websocket relay ─────────────────────────────────────────
//
// The terminal is a WebSocket (reliant-api /api/v2/terminal/ws), and the
// app:// protocol handler cannot upgrade a connection. Two ways keep the
// session out of the renderer:
//
//   - proxy the upgrade with the cookie attached: either a loopback listener
//     the renderer connects to (any local process, and any page in a browser
//     tab, could connect too, so it would need one-time tickets), or injecting
//     the cookie into the renderer's own handshake (webRequest), which the
//     browser tabs sharing that session would also pass through;
//   - THIS: the main process opens the socket and relays its frames over
//     IPC. Nothing listens, the renderer names only the query (an allowlist of
//     the terminal's parameters, never a URL), the upstream is always the
//     terminal path on reliant-api, each socket belongs to the window that
//     opened it, and a sign-out closes them all.
//
// The renderer side is frontends/web/src/lib/desktop-terminal-socket.ts: a
// WebSocket-shaped object over the preload's forgeAuth.terminal bridge.

/** The terminal websocket on reliant-api (internal/grpc/server.go). */
const TERMINAL_PATH = '/api/v2/terminal/ws';

/** The query parameters a terminal connection may carry; never `token`. */
const TERMINAL_QUERY_KEYS = ['workingDir', 'worktreeId', 'projectId', 'chatId', 'reconnect', 'traceparent', 'tracestate'];

/** How many terminals one window may hold open. */
const MAX_TERMINALS_PER_OWNER = 32;

/**
 * The upstream terminal URL for a renderer's query: ws(s) on reliant-api,
 * the terminal path, and only the allowlisted parameters.
 *
 * @param {string} apiUrl
 * @param {string | Record<string, string>} query
 */
function terminalUpstreamUrl(apiUrl, query) {
  const base = new URL(apiUrl);
  const u = new URL(`${apiUrl.replace(/\/+$/, '')}${TERMINAL_PATH}`);
  if (u.origin !== base.origin) throw new Error('terminal URL left the API');
  u.protocol = base.protocol === 'https:' ? 'wss:' : 'ws:';
  const given = new URLSearchParams(typeof query === 'string' ? query : query || {});
  for (const key of TERMINAL_QUERY_KEYS) {
    const value = given.get(key);
    if (value !== null && value.length <= 4096) u.searchParams.set(key, value);
  }
  return u.toString();
}

/** Stop a socket's events: its close was already reported (or nobody is left to tell). */
function detach(ws) {
  ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null;
}

/** A close code the renderer may ask for (1000, or an application code). */
function closeCodeFrom(code) {
  return code === 1000 || (Number.isInteger(code) && code >= 3000 && code <= 4999) ? code : undefined;
}

/**
 * The open terminal relays: sockets this process holds for renderer windows.
 */
// A renderer-chosen terminal id: "c" and a random UUID (or 32 hex digits).
// Never the shape of the ids this module mints itself (t1, t2, …).
const TERMINAL_CLIENT_ID = /^c[0-9a-f-]{32,36}$/i;

class TerminalRelays {
  /**
   * @param {{ auth: ForgeAuth, WebSocketImpl?: typeof WebSocket, logger?: object }} opts
   */
  constructor({ auth, WebSocketImpl, logger }) {
    this.auth = auth;
    this.WebSocketImpl = WebSocketImpl || globalThis.WebSocket;
    this.log = logger || { info() {}, warn() {} };
    this.sockets = new Map(); // id -> { ws, owner, emit }
    this.nextId = 1;
  }

  /**
   * Open a terminal for owner (a window's webContents id). emit receives
   * {id, type: 'open'|'message'|'error'|'close', data?, code?, reason?,
   * wasClean?}. Throws when signed out, over the per-window limit, or when
   * requestedId is already in use.
   *
   * requestedId is the renderer's own id for the socket (TERMINAL_CLIENT_ID):
   * every event carries it from the first, so the renderer can tell its
   * socket's events from another terminal's before this call has answered
   * (the upstream socket opens, and may send, before the invoke reply
   * reaches it). A renderer that names none, or a malformed one, gets an id
   * from here, as before.
   */
  open(owner, query, emit, requestedId) {
    const cookie = this.auth.cookie.header();
    if (!cookie) throw new Error('not signed in');
    let held = 0;
    for (const s of this.sockets.values()) if (s.owner === owner) held++;
    if (held >= MAX_TERMINALS_PER_OWNER) throw new Error('too many open terminals');
    if (typeof this.WebSocketImpl !== 'function') throw new Error('this build has no WebSocket client');

    let id;
    if (typeof requestedId === 'string' && TERMINAL_CLIENT_ID.test(requestedId)) {
      if (this.sockets.has(requestedId)) throw new Error('terminal id in use');
      id = requestedId;
    } else {
      id = `t${this.nextId++}`;
    }
    // No Origin: the server admits a cookie-authenticated upgrade from a
    // non-browser (internal/handlers/terminal sameOriginUpgrade).
    const ws = new this.WebSocketImpl(terminalUpstreamUrl(this.auth.apiUrl, query), { headers: { Cookie: cookie } });
    try {
      ws.binaryType = 'arraybuffer';
    } catch {
      // not settable on this implementation
    }
    const entry = { ws, owner, emit };
    this.sockets.set(id, entry);
    const deliver = (event) => {
      try {
        emit({ id, ...event });
      } catch (err) {
        this.log.warn('[ForgeAuth] terminal event could not be delivered:', err && err.message);
      }
    };
    ws.onopen = () => deliver({ type: 'open' });
    ws.onmessage = (e) => {
      const data = typeof e.data === 'string' ? e.data : Buffer.from(e.data).toString('utf8');
      deliver({ type: 'message', data });
    };
    ws.onerror = () => deliver({ type: 'error' });
    ws.onclose = (e) => {
      this.sockets.delete(id);
      deliver({ type: 'close', code: e.code, reason: e.reason || '', wasClean: Boolean(e.wasClean) });
    };
    return id;
  }

  /** Send a text frame on owner's socket id. False when it is not owner's or not open. */
  send(owner, id, data) {
    const entry = this.sockets.get(id);
    if (!entry || entry.owner !== owner || typeof data !== 'string') return false;
    if (entry.ws.readyState !== 1) return false;
    entry.ws.send(data);
    return true;
  }

  /** Close owner's socket id. */
  close(owner, id, code, reason) {
    const entry = this.sockets.get(id);
    if (!entry || entry.owner !== owner) return false;
    entry.ws.close(closeCodeFrom(code), typeof reason === 'string' ? reason.slice(0, 120) : undefined);
    return true;
  }

  /** Close every socket of a window (it went away). */
  closeOwner(owner) {
    for (const [id, entry] of this.sockets) {
      if (entry.owner === owner) {
        this.sockets.delete(id);
        detach(entry.ws);
        entry.ws.close(1000, 'window closed');
      }
    }
  }

  /** Close every socket (the session ended: a sign-out, another person). */
  closeAll(reason) {
    for (const [id, entry] of this.sockets) {
      this.sockets.delete(id);
      detach(entry.ws);
      entry.ws.close(4401, reason);
      entry.emit({ id, type: 'close', code: 4401, reason, wasClean: true });
    }
  }

  get size() {
    return this.sockets.size;
  }
}

// ── The app:// API proxy ────────────────────────────────────────────────

// Request headers the proxy never forwards: hop-by-hop, the browser's own
// credentials (the session is attached here, and only here), and forwarding
// claims the renderer could make up.
const DROPPED_REQUEST_HEADERS = new Set([
  'host', 'connection', 'keep-alive', 'transfer-encoding', 'upgrade', 'content-length',
  'cookie', 'authorization', 'origin', 'referer',
  'forwarded', 'x-forwarded-for', 'x-forwarded-host', 'x-forwarded-port', 'x-forwarded-proto',
]);

// Response headers the renderer never receives: the session's Set-Cookie
// (applied to the partition instead) and what fetch already undid.
const DROPPED_RESPONSE_HEADERS = new Set([
  'set-cookie', 'content-encoding', 'content-length', 'transfer-encoding', 'connection', 'keep-alive',
]);

/**
 * Where a proxied path goes: `/_api/@<name>/<rest>` → `<upstream of name>/<rest>`.
 * The upstream URL is built by concatenation and its origin re-checked, so no
 * path (`//evil.example/x`, `..`, an encoded slash) can leave the upstream.
 *
 * @param {string} requestUrl a full app:// URL
 * @param {Record<string, string>} upstreams name → base URL
 * @returns {string | null}
 */
function upstreamFor(requestUrl, upstreams) {
  let u;
  try {
    u = new URL(requestUrl);
  } catch {
    return null;
  }
  if (!u.pathname.startsWith(API_PREFIX)) return null;
  const rest = u.pathname.slice(API_PREFIX.length);
  const slash = rest.indexOf('/');
  const name = slash === -1 ? rest : rest.slice(0, slash);
  const tail = slash === -1 ? '/' : rest.slice(slash);
  const base = Object.prototype.hasOwnProperty.call(upstreams, name) ? upstreams[name] : '';
  if (!base) return null;
  if (/%2f|%5c|\\/i.test(tail)) return null;
  let baseUrl;
  try {
    baseUrl = new URL(base);
  } catch {
    return null;
  }
  const target = `${base.replace(/\/+$/, '')}/${tail.replace(/^\/+/, '')}${u.search}`;
  let t;
  try {
    t = new URL(target);
  } catch {
    return null;
  }
  if (t.origin !== baseUrl.origin) return null;
  return t.toString();
}

/**
 * Whether a request reaching the proxy came from the app's own pages. The
 * scheme is fetchable from any origin (corsEnabled), and CORS does not stop a
 * "simple" cross-origin POST from being SENT — so a page in a browser tab could
 * otherwise drive the session. Same-origin fetches only: a navigation or
 * another origin's request is refused.
 */
function isFromApp(headers, appOrigin) {
  const get = (h) => (headers && typeof headers.get === 'function' ? headers.get(h) : null);
  const origin = get('origin');
  if (origin && origin !== appOrigin) return false;
  const site = get('sec-fetch-site');
  if (site && site !== 'same-origin') return false;
  const mode = get('sec-fetch-mode');
  if (mode === 'navigate' || mode === 'nested-navigate') return false;
  return true;
}

/**
 * The app:// handler for `/_api/@…`: forwards the request to its upstream
 * with the session cookie, returns the answer without its Set-Cookie.
 *
 * @param {{ upstreams: Record<string, string>, auth: ForgeAuth, appOrigin: string,
 *           fetch?: typeof fetch, logger?: object }} opts
 * @returns {(request: Request) => Promise<Response>}
 */
function createApiProxy({ upstreams, auth, appOrigin, fetch: fetchImpl, logger }) {
  const doFetch = fetchImpl || ((...a) => globalThis.fetch(...a));
  const log = logger || { warn() {}, error() {} };
  return async function handleApiRequest(request) {
    if (!isFromApp(request.headers, appOrigin)) {
      log.warn('[ForgeAuth] refusing an API request that is not from the app');
      return new Response('Forbidden', { status: 403, headers: { 'content-type': 'text/plain' } });
    }
    const target = upstreamFor(request.url, upstreams);
    if (!target) {
      return new Response('Not found', { status: 404, headers: { 'content-type': 'text/plain' } });
    }
    const headers = new Headers();
    request.headers.forEach((value, key) => {
      const k = key.toLowerCase();
      if (!DROPPED_REQUEST_HEADERS.has(k) && !k.startsWith('sec-')) headers.set(key, value);
    });
    const cookie = auth.cookie.header();
    if (cookie) headers.set('Cookie', cookie);

    const method = request.method || 'GET';
    let body;
    if (method !== 'GET' && method !== 'HEAD') {
      body = Buffer.from(await request.arrayBuffer());
    }
    let upstream;
    try {
      upstream = await doFetch(target, { method, headers, body, redirect: 'manual' });
    } catch (err) {
      log.error('[ForgeAuth] API proxy could not reach', new URL(target).origin, err && err.message);
      return new Response('Bad gateway', { status: 502, headers: { 'content-type': 'text/plain' } });
    }

    const setCookies = typeof upstream.headers.getSetCookie === 'function' ? upstream.headers.getSetCookie() : [];
    if (setCookies.length > 0) {
      try {
        await auth.applySetCookies(setCookies);
      } catch (err) {
        log.error('[ForgeAuth] could not apply the session cookie:', err && err.message);
      }
    }
    const out = new Headers();
    upstream.headers.forEach((value, key) => {
      if (!DROPPED_RESPONSE_HEADERS.has(key.toLowerCase())) out.set(key, value);
    });
    // A redirect would point the renderer off the app's origin; the API
    // routes it calls answer JSON, so pass the status and let it decide.
    const nullBody = method === 'HEAD' || [101, 204, 205, 304].includes(upstream.status);
    return new Response(nullBody ? null : upstream.body, { status: upstream.status, headers: out });
  };
}

module.exports = {
  API_PREFIX,
  AUTH_PARTITION,
  AUTH_PROVIDER_FORGE,
  AUTH_PROVIDER_SUPABASE,
  CALLBACK_PATH,
  DEV_COOKIE_NAME,
  ForgeAuth,
  SECURE_COOKIE_NAME,
  SignInSuperseded,
  TERMINAL_PATH,
  TerminalRelays,
  terminalUpstreamUrl,
  SessionCookie,
  authProviderOf,
  createApiProxy,
  isFromApp,
  isSessionId,
  newPkcePair,
  originOf,
  parseSetCookie,
  sessionCookieName,
  startLoopback,
  upstreamFor,
};
