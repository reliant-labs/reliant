/**
 * daemon-creds — pure helpers for the Electron "auto-mint daemon PAT"
 * preflight that runs immediately before the daemon binary is spawned.
 *
 * Why this module exists
 * ----------------------
 * The Go daemon, when launched without credentials for its --server origin,
 * falls into an interactive registration flow (Supabase device login +
 * CreateDaemonToken). That works on a TTY but is broken inside Electron
 * (no stdin, no browser handoff). In Electron we already have a live
 * Supabase session stored via auth-storage, so we can do the mint ourselves
 * and drop a valid daemon.json on disk before the daemon ever starts. The
 * daemon then reads the existing entry for `endpointKey(--server)` and skips
 * its own registration flow entirely.
 *
 * Token freshness
 * ---------------
 * The stored session's access token is a short-lived JWT (~1h). This
 * preflight runs on COLD LAUNCH, typically hours-to-days after the session
 * was last persisted — so the disk token is usually EXPIRED and minting with
 * it verbatim 401s deterministically, dumping the daemon into the broken
 * interactive flow ("No machine connected"). We therefore refresh the
 * session against the GoTrue token endpoint (using the stored refresh_token)
 * before minting: proactively when `expires_at` says the token is stale, and
 * reactively — once — when the mint comes back 401 anyway. The refreshed
 * session is persisted back through authStorage so the renderer inherits the
 * ROTATED refresh token instead of burning the old one twice.
 *
 * Contract with the Go side
 * -------------------------
 * The on-disk format (`~/.reliant/daemon.json`) and the keying scheme
 * (origin = scheme://host[:port], lowercased, path/query/fragment stripped)
 * are owned by `internal/auth/daemon_file.go`. Anything in this module that
 * touches the file MUST match the Go semantics exactly — drift here causes
 * silent split-brain (Electron writes, daemon can't find it).
 *
 * No Electron imports
 * -------------------
 * This module deliberately avoids requiring `electron` so it can be loaded
 * under plain `node --test`. The orchestrator in backend-manager.js is the
 * piece that knows about `authStorage`, `apiUrl`, `gatewayUrl`, etc.
 */

const fs = require('fs');
const os = require('os');
const path = require('path');
const https = require('https');

const DAEMON_DIR_NAME = '.reliant';
const DAEMON_FILE_NAME = 'daemon.json';
const MINT_RPC_PATH = '/reliant.v1.DaemonTokenService/CreateDaemonToken';
// Per-attempt timeout. mintDaemonPAT retries transient failures up to
// MINT_MAX_ATTEMPTS times with backoff (see MINT_RETRY_BACKOFFS_MS), so this
// is *one* attempt — not the budget for the whole mint. Kept short because
// we're inside BackendManager.start() and blocking daemon spawn.
const MINT_TIMEOUT_MS = 5_000;
// Total mint attempts (1 initial + 2 retries). Worst-case wall time bounded
// by MINT_MAX_ATTEMPTS * MINT_TIMEOUT_MS + sum(MINT_RETRY_BACKOFFS_MS).
const MINT_MAX_ATTEMPTS = 3;
// Wait between attempt N and attempt N+1. Length must equal
// MINT_MAX_ATTEMPTS - 1. Kept small (~1s total) because daemon spawn is hot.
const MINT_RETRY_BACKOFFS_MS = [200, 800];
// HTTP status codes that indicate the upstream may succeed on a retry.
// 502/503/504 are the classic transient-upstream codes. 4xx (including 401)
// are *not* retried — bad credentials/bad request won't fix themselves.
const MINT_RETRYABLE_STATUSES = new Set([502, 503, 504]);
// GoTrue session-refresh endpoint (relative to the Supabase project base
// URL). Same endpoint supabase-js uses under the hood.
const REFRESH_TOKEN_PATH = '/auth/v1/token?grant_type=refresh_token';
// Per-call timeout for the session refresh. One roundtrip to GoTrue; kept as
// short as the mint's because we're blocking daemon spawn.
const REFRESH_TIMEOUT_MS = 5_000;
// Proactive-refresh margin: treat the stored access token as stale when it
// expires within this window. Wide enough to absorb modest clock skew;
// narrow enough that a mid-session relaunch reuses the live token.
const TOKEN_EXPIRY_MARGIN_S = 60;
// Node error codes that mean "couldn't even reach the server" — almost
// always transient when `air` is mid-restart of the api-server.
const MINT_RETRYABLE_ERROR_CODES = new Set([
  'ECONNREFUSED',
  'ECONNRESET',
  'ETIMEDOUT',
  'EAI_AGAIN',
  'ENETUNREACH',
  'EHOSTUNREACH',
  'EPIPE',
]);

/**
 * Collapse a server URL to its origin key, matching the Go-side
 * `endpointKey` at internal/auth/daemon_file.go:56.
 *
 * Result: `scheme://host` where `host` already includes an explicit port
 * if one was given. Path/query/fragment dropped. Lowercased.
 * Returns "" for any input the URL parser rejects or that has no host —
 * callers must treat "" as "invalid, refuse to write".
 *
 * @param {string} serverURL
 * @returns {string}
 */
function endpointKey(serverURL) {
  const s = (serverURL ?? '').trim();
  if (!s) return '';
  let u;
  try {
    u = new URL(s);
  } catch {
    return '';
  }
  if (!u.host) return '';
  const scheme = (u.protocol || 'https:').replace(':', '').toLowerCase();
  return `${scheme}://${u.host.toLowerCase()}`;
}

/**
 * Mirror of Go's `shouldSkipTLSVerify` at cmd/reliant/commands/daemon.go:255.
 * Localhost HTTPS targets in dev typically use self-signed certs; we mirror
 * the daemon's tolerance so the mint preflight doesn't fail where the daemon
 * itself would have succeeded.
 *
 * @param {string} serverURL
 * @returns {boolean}
 */
function shouldSkipTLSVerify(serverURL) {
  if (process.env.RELIANT_SKIP_TLS_VERIFY === '1') return true;
  const s = (serverURL ?? '').toLowerCase();
  return (
    s.startsWith('https://localhost') ||
    s.startsWith('https://127.0.0.1') ||
    s.startsWith('https://[::1]')
  );
}

/**
 * Absolute path to ~/.reliant/daemon.json. Exposed for tests via overrides
 * — see readDaemonStore / writeDaemonStore.
 *
 * @returns {{ dir: string, file: string }}
 */
function daemonStorePaths() {
  const dir = path.join(os.homedir(), DAEMON_DIR_NAME);
  return { dir, file: path.join(dir, DAEMON_FILE_NAME) };
}

/**
 * Read the daemon credentials store from disk. Treats any of {missing file,
 * unreadable, unparseable JSON, non-object root} as "empty store" rather
 * than throwing. This matches Go's readStore semantics — on a format mismatch
 * we start fresh rather than crash, since the alternative is bricking the
 * mint preflight on a stale file the user can't easily inspect.
 *
 * @param {{ filePath?: string, logger?: { warn?: Function } }} [opts]
 * @returns {Record<string, object>}
 */
function readDaemonStore(opts = {}) {
  const file = opts.filePath ?? daemonStorePaths().file;
  const logger = opts.logger;
  let raw;
  try {
    raw = fs.readFileSync(file, 'utf8');
  } catch (e) {
    if (e.code !== 'ENOENT' && logger?.warn) {
      logger.warn('[daemon-creds] daemon.json unreadable, starting fresh:', e.message);
    }
    return {};
  }
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed;
    }
    return {};
  } catch (e) {
    if (logger?.warn) {
      logger.warn('[daemon-creds] daemon.json parse failed, starting fresh:', e.message);
    }
    return {};
  }
}

/**
 * Atomically replace the daemon credentials store on disk. Mode 0700 on the
 * directory, 0600 on the file — matches Go's writeStore.
 *
 * Atomicity matters here: the Go daemon may be racing to read the same file
 * during regular operation. We write to a tmp sibling and rename so a reader
 * never observes a truncated/partial JSON.
 *
 * @param {Record<string, object>} store
 * @param {{ filePath?: string }} [opts]
 */
function writeDaemonStore(store, opts = {}) {
  const { dir, file } = (() => {
    if (opts.filePath) {
      return { dir: path.dirname(opts.filePath), file: opts.filePath };
    }
    return daemonStorePaths();
  })();

  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const tmp = `${file}.tmp.${process.pid}`;
  fs.writeFileSync(tmp, JSON.stringify(store, null, 2), { mode: 0o600 });
  fs.renameSync(tmp, file);
}

/**
 * Read-modify-write a single entry into the daemon credentials store.
 * Snake_case keys (`pat`, `server_url`, `gateway_url`, `registered_at`)
 * match the Go struct tags at internal/auth/daemon_file.go:21-26.
 *
 * Throws on an invalid apiUrl (empty endpoint key) — the caller in
 * backend-manager.js wraps everything in try/catch and swallows, so this
 * never blocks daemon spawn. Throwing here keeps the helper honest for
 * tests; the orchestrator decides whether a failure aborts startup.
 *
 * `sub` is the Supabase subject the PAT was minted for. The Go side never
 * sets it but declares it on DaemonCredentials (internal/auth/daemon_file.go)
 * purely so the daemon's own entry rewrites (persisting daemon_id after
 * registration) round-trip it — Go struct unmarshal DROPS unknown JSON keys,
 * so without that field every registration erased `sub` and forced a
 * re-mint on the next cold launch. BackendManager reads it via
 * `entryOwnerSub` to decide whether the cached PAT belongs to the currently
 * signed-in user — without it, sign-out-sign-in-as-different-user reuses the
 * old user's PAT, the daemon registers under the wrong owner, and the new
 * user's ListDaemons returns empty.
 *
 * daemon_id preservation: the Go daemon owns `daemon_id` — the server assigns
 * a stable identity on first registration and the daemon persists it into the
 * origin's entry. This preflight only ever rewrites the PAT, so it MUST carry
 * any existing `daemon_id` forward. Clobbering it would orphan the daemon's
 * identity on every PAT re-mint, re-triggering the exact hostname-churn bug
 * the stable id exists to prevent. A fresh entry leaves daemon_id unset so
 * the server assigns one.
 *
 * @param {{ apiUrl: string, gatewayUrl?: string, pat: string, sub?: string,
 *           filePath?: string, logger?: { warn?: Function } }} args
 */
function upsertEntry({ apiUrl, gatewayUrl, pat, sub, filePath, logger }) {
  const key = endpointKey(apiUrl);
  if (!key) {
    throw new Error(`invalid --server URL: ${apiUrl}`);
  }
  if (!pat) {
    throw new Error('refusing to write empty PAT to daemon.json');
  }
  const store = readDaemonStore({ filePath, logger });
  const prior = store[key];
  const priorDaemonId =
    prior && typeof prior.daemon_id === 'string' ? prior.daemon_id : '';
  store[key] = {
    pat,
    server_url: apiUrl,
    gateway_url: gatewayUrl || '',
    registered_at: new Date().toISOString(),
    ...(sub ? { sub } : {}),
    ...(priorDaemonId ? { daemon_id: priorDaemonId } : {}),
  };
  writeDaemonStore(store, { filePath });
}

/**
 * Remove the credentials entry for `apiUrl`'s origin from daemon.json.
 * Mirrors Go's DeleteDaemonCredentials — a no-op when no entry exists.
 *
 * Called on logout: dropping the whole origin entry clears the PAT, the
 * owner `sub`, AND the stable `daemon_id` together. That's deliberate —
 * logout may precede a user switch, so the next login should mint a fresh
 * PAT and let the server assign a fresh daemon id rather than resurrecting
 * the prior user's identity.
 *
 * Returns true if an entry was removed, false if there was nothing to remove
 * (missing origin, invalid apiUrl). Never throws on a valid apiUrl.
 *
 * @param {{ apiUrl: string, filePath?: string,
 *           logger?: { warn?: Function } }} args
 * @returns {boolean}
 */
function deleteEntry({ apiUrl, filePath, logger }) {
  const key = endpointKey(apiUrl);
  if (!key) return false;
  const store = readDaemonStore({ filePath, logger });
  if (!Object.prototype.hasOwnProperty.call(store, key)) {
    return false;
  }
  delete store[key];
  writeDaemonStore(store, { filePath });
  return true;
}

/**
 * Returns the `sub` of the cached entry for `apiUrl`, or null if no entry
 * exists / the entry has no recorded `sub` (e.g. from an older write before
 * we tracked it).
 *
 * @param {{ apiUrl: string, filePath?: string,
 *           logger?: { warn?: Function } }} args
 * @returns {string | null}
 */
function entryOwnerSub({ apiUrl, filePath, logger }) {
  const key = endpointKey(apiUrl);
  if (!key) return null;
  const store = readDaemonStore({ filePath, logger });
  const entry = store[key];
  if (!entry || typeof entry !== 'object') return null;
  return typeof entry.sub === 'string' ? entry.sub : null;
}

/**
 * POST <apiUrl>/reliant.v1.DaemonTokenService/CreateDaemonToken using the
 * caller's Supabase JWT as bearer credentials. Uses raw HTTP+JSON (no
 * Connect SDK) so this module stays a thin pure-Node dependency.
 *
 * Retry contract
 * --------------
 * BackendManager calls this during daemon spawn, and `air` may be mid-restart
 * of `reliant server api` — leading to a transient ECONNREFUSED/502 even
 * though the next attempt 200ms later would have succeeded. We retry on
 * connection-level errors and 502/503/504 up to MINT_MAX_ATTEMPTS times with
 * MINT_RETRY_BACKOFFS_MS backoff. 4xx (including 401), 200-with-missing-token,
 * and missing-apiUrl/accessToken are terminal — retrying won't help. Sleeps
 * use the provided `sleep` (default real timers); tests pass a fake to make
 * retry behavior synchronous.
 *
 * On exhausted retries, we throw the LAST error so `ensureDaemonCreds` can
 * log+swallow as it does today.
 *
 * Localhost-HTTPS gets a self-signed-tolerant agent — same set of origins
 * the Go daemon already accepts (`shouldSkipTLSVerify`).
 *
 * `fetch` is taken off `globalThis` rather than the built-in so tests can
 * stub it. We deliberately do NOT pass a Node `Agent` via `dispatcher`
 * (that's undici-specific and unreliable across Node versions); instead,
 * for the rare localhost-HTTPS case we fall through to a `https.request`
 * implementation that accepts a classic `https.Agent`.
 *
 * @param {{ apiUrl: string, accessToken: string, name?: string,
 *           logger?: { debug?: Function, warn?: Function },
 *           sleep?: (ms: number) => Promise<void> }} args
 * @returns {Promise<{ token: string, tokenId?: string }>}
 */
async function mintDaemonPAT({ apiUrl, accessToken, name, logger, sleep }) {
  if (!apiUrl) throw new Error('mintDaemonPAT: missing apiUrl');
  if (!accessToken) throw new Error('mintDaemonPAT: missing accessToken');

  const url = `${apiUrl.replace(/\/+$/, '')}${MINT_RPC_PATH}`;
  const body = JSON.stringify({ name: name || os.hostname() });
  const headers = {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${accessToken}`,
  };
  const sleeper = typeof sleep === 'function'
    ? sleep
    : (ms) => new Promise((r) => setTimeout(r, ms));

  let lastErr;
  for (let attempt = 1; attempt <= MINT_MAX_ATTEMPTS; attempt++) {
    try {
      return await mintDaemonPATOnce({ url, headers, body, apiUrl, logger });
    } catch (err) {
      lastErr = err;
      const retryable = isRetryableMintError(err);
      if (!retryable || attempt === MINT_MAX_ATTEMPTS) {
        throw err;
      }
      const backoff = MINT_RETRY_BACKOFFS_MS[attempt - 1] ?? 0;
      if (logger?.warn) {
        const cause = err?.mintStatus
          ? `HTTP ${err.mintStatus}`
          : (err?.code || err?.cause?.code || err?.message || 'unknown');
        logger.warn(
          `[daemon-creds] mintDaemonPAT attempt ${attempt}/${MINT_MAX_ATTEMPTS} failed (${cause}); retrying in ${backoff}ms`
        );
      }
      if (backoff > 0) {
        await sleeper(backoff);
      }
    }
  }
  // Unreachable — the loop either returns or throws.
  throw lastErr;
}

/**
 * Single attempt of the mint RPC. Throws a tagged error on failure so
 * `mintDaemonPAT`'s retry loop can decide whether to retry. HTTP failures
 * carry `err.mintStatus`; network failures pass through Node's `err.code`
 * (or `err.cause.code` when wrapped by undici-fetch).
 *
 * Non-retryable terminal failures (missing token in 200, AbortSignal timeouts
 * caused by parent cancellation) are tagged with `err.mintTerminal = true`
 * so the loop doesn't waste backoff on them.
 */
async function mintDaemonPATOnce({ url, headers, body, apiUrl, logger }) {
  const skipTLS = shouldSkipTLSVerify(apiUrl);

  // For localhost-HTTPS with self-signed certs, drop down to https.request
  // so we can hand it a rejectUnauthorized:false agent — fetch on Node 20+
  // does not accept a classic Agent in any portable way.
  if (skipTLS && url.toLowerCase().startsWith('https://')) {
    return await mintViaHttpsRequest({ url, headers, body, logger });
  }

  const fetchImpl = globalThis.fetch;
  if (typeof fetchImpl !== 'function') {
    throw new Error('mintDaemonPAT: globalThis.fetch is not available');
  }

  const res = await fetchImpl(url, {
    method: 'POST',
    headers,
    body,
    signal: AbortSignal.timeout(MINT_TIMEOUT_MS),
  });

  if (!res.ok) {
    const text = await safeReadText(res);
    const e = new Error(
      `CreateDaemonToken failed: HTTP ${res.status}${text ? ` — ${text}` : ''}`
    );
    e.mintStatus = res.status;
    throw e;
  }

  const data = await res.json();
  if (!data || typeof data.token !== 'string' || !data.token) {
    const e = new Error('CreateDaemonToken response missing token');
    e.mintTerminal = true; // schema mismatch — won't fix itself on retry
    throw e;
  }
  return { token: data.token, tokenId: data.tokenId || data.token_id };
}

/**
 * Decide whether a mintDaemonPATOnce failure is worth retrying. Per the
 * contract on mintDaemonPAT:
 *   - retry on 502/503/504 (transient upstream)
 *   - retry on connection-level Node error codes (ECONNREFUSED etc.)
 *   - do NOT retry on 4xx (401 means bad token; retrying won't help)
 *   - do NOT retry on schema mismatches (mintTerminal === true)
 *   - do NOT retry on AbortError caused by parent cancellation —
 *     AbortSignal.timeout() per-attempt timeouts are surfaced as TimeoutError
 *     (DOMException name "TimeoutError"), which IS treated as retryable
 *     because it means the call took too long, not that the caller cancelled.
 */
function isRetryableMintError(err) {
  if (!err) return false;
  if (err.mintTerminal) return false;
  if (typeof err.mintStatus === 'number') {
    return MINT_RETRYABLE_STATUSES.has(err.mintStatus);
  }
  // AbortError from a user/parent AbortController — don't paper over it.
  // (AbortSignal.timeout produces a TimeoutError, not an AbortError.)
  if (err.name === 'AbortError') return false;

  // Per-attempt timeout from AbortSignal.timeout(): retry it like a network
  // error — the upstream was just too slow, the next attempt may succeed.
  if (err.name === 'TimeoutError') return true;

  // Node fetch wraps the underlying network error under err.cause.
  const code = err.code || err.cause?.code;
  if (code && MINT_RETRYABLE_ERROR_CODES.has(code)) return true;

  // Fall back to message inspection for our stubbed-out tests that throw
  // a plain Error('ECONNREFUSED'). This is best-effort: if a message
  // contains a recognized code substring, treat it as retryable.
  const msg = String(err.message || '');
  for (const c of MINT_RETRYABLE_ERROR_CODES) {
    if (msg.includes(c)) return true;
  }
  return false;
}

/**
 * Localhost-HTTPS fallback path — see mintDaemonPAT.
 */
function mintViaHttpsRequest({ url, headers, body, logger }) {
  return new Promise((resolve, reject) => {
    const parsed = new URL(url);
    const req = https.request(
      {
        method: 'POST',
        hostname: parsed.hostname,
        port: parsed.port || 443,
        path: `${parsed.pathname}${parsed.search}`,
        headers: { ...headers, 'Content-Length': Buffer.byteLength(body) },
        rejectUnauthorized: false,
        timeout: MINT_TIMEOUT_MS,
      },
      (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => {
          const text = Buffer.concat(chunks).toString('utf8');
          if (res.statusCode < 200 || res.statusCode >= 300) {
            const httpErr = new Error(
              `CreateDaemonToken failed: HTTP ${res.statusCode}${text ? ` — ${text}` : ''}`
            );
            httpErr.mintStatus = res.statusCode;
            reject(httpErr);
            return;
          }
          try {
            const data = JSON.parse(text);
            if (!data || typeof data.token !== 'string' || !data.token) {
              const schemaErr = new Error('CreateDaemonToken response missing token');
              schemaErr.mintTerminal = true;
              reject(schemaErr);
              return;
            }
            resolve({ token: data.token, tokenId: data.tokenId || data.token_id });
          } catch (e) {
            const parseErr = new Error(`CreateDaemonToken response invalid JSON: ${e.message}`);
            parseErr.mintTerminal = true;
            reject(parseErr);
          }
        });
      }
    );
    req.on('timeout', () => {
      const timeoutErr = new Error(`CreateDaemonToken timed out after ${MINT_TIMEOUT_MS}ms`);
      timeoutErr.name = 'TimeoutError';
      req.destroy(timeoutErr);
    });
    req.on('error', (err) => {
      if (logger?.debug) logger.debug('[daemon-creds] https.request error', err.message);
      reject(err);
    });
    req.write(body);
    req.end();
  });
}

async function safeReadText(res) {
  try {
    return await res.text();
  } catch {
    return '';
  }
}

/**
 * Whether the stored session's access token is expired or about to expire
 * (within TOKEN_EXPIRY_MARGIN_S). `expires_at` is unix SECONDS in Supabase
 * session objects. A session with no usable `expires_at` returns false —
 * we can't tell, so we let the mint's 401-triggered refresh path decide
 * instead of burning a refresh-token rotation on a guess.
 *
 * @param {{ expires_at?: number } | null | undefined} session
 * @param {number} [nowMs] injectable clock for tests
 * @returns {boolean}
 */
function sessionNeedsRefresh(session, nowMs = Date.now()) {
  const expiresAt = Number(session?.expires_at);
  if (!Number.isFinite(expiresAt) || expiresAt <= 0) return false;
  return expiresAt * 1000 <= nowMs + TOKEN_EXPIRY_MARGIN_S * 1000;
}

/**
 * Exchange the session's refresh_token for a fresh session at the GoTrue
 * token endpoint. Throws on any failure — the caller decides whether a
 * failed refresh is fatal (it isn't: we fall back to minting with the
 * stored token, which preserves the pre-refresh behavior exactly).
 *
 * Returns a NEW session object in the stored-session shape (auth-storage
 * requires access_token + refresh_token to persist): unspecified fields are
 * carried forward from the old session, `expires_at` is normalized from
 * `expires_in` when GoTrue omits it, and a missing rotated refresh_token
 * (shouldn't happen, but never brick the store on it) keeps the old one.
 *
 * `fetch` is taken off `globalThis` so tests can stub it, same as the mint.
 * No localhost-HTTPS fallback here: GoTrue is either a real-cert hosted
 * project or a plain-HTTP self-hosted dev stack, never self-signed HTTPS.
 *
 * @param {{ authUrl: string, anonKey: string, session: object,
 *           logger?: { debug?: Function } }} args
 * @returns {Promise<object>} the refreshed session
 */
async function refreshSupabaseSession({ authUrl, anonKey, session, logger }) {
  if (!authUrl || !anonKey) {
    throw new Error('refreshSupabaseSession: auth provider not configured');
  }
  const refreshToken = session?.refresh_token;
  if (!refreshToken) {
    throw new Error('refreshSupabaseSession: session has no refresh_token');
  }
  const fetchImpl = globalThis.fetch;
  if (typeof fetchImpl !== 'function') {
    throw new Error('refreshSupabaseSession: globalThis.fetch is not available');
  }

  const url = `${authUrl.replace(/\/+$/, '')}${REFRESH_TOKEN_PATH}`;
  if (logger?.debug) logger.debug('[daemon-creds] refreshing Supabase session via', url);
  const res = await fetchImpl(url, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      apikey: anonKey,
    },
    body: JSON.stringify({ refresh_token: refreshToken }),
    signal: AbortSignal.timeout(REFRESH_TIMEOUT_MS),
  });

  if (!res.ok) {
    const text = await safeReadText(res);
    throw new Error(
      `session refresh failed: HTTP ${res.status}${text ? ` — ${text}` : ''}`
    );
  }

  const data = await res.json();
  if (!data || typeof data.access_token !== 'string' || !data.access_token) {
    throw new Error('session refresh response missing access_token');
  }

  const expiresIn = Number(data.expires_in);
  const rawExpiresAt = Number(data.expires_at);
  const expiresAt =
    Number.isFinite(rawExpiresAt) && rawExpiresAt > 0
      ? rawExpiresAt
      : Number.isFinite(expiresIn) && expiresIn > 0
        ? Math.floor(Date.now() / 1000) + expiresIn
        : undefined;

  return {
    ...session,
    access_token: data.access_token,
    refresh_token:
      typeof data.refresh_token === 'string' && data.refresh_token
        ? data.refresh_token
        : session.refresh_token,
    ...(typeof data.token_type === 'string' && data.token_type
      ? { token_type: data.token_type }
      : {}),
    ...(Number.isFinite(expiresIn) && expiresIn > 0 ? { expires_in: expiresIn } : {}),
    ...(expiresAt ? { expires_at: expiresAt } : {}),
    user: data.user || session.user,
  };
}

// No-op logger used when the caller doesn't supply one. Keeps the orchestrator
// pure-function from the test's perspective (no console noise leaks out).
const NOOP_LOGGER = { debug() {}, info() {}, warn() {}, error() {} };

/**
 * Pre-spawn orchestration: ensure ~/.reliant/daemon.json has a PAT entry for
 * the current `--server` origin that belongs to the currently signed-in user.
 *
 *   - If `authStorage` is null → no-op (caller wasn't given an auth source).
 *   - If no stored session → no-op (user hasn't signed in yet; daemon will
 *     fall into its own headless-broken flow, which is the pre-existing
 *     behavior).
 *   - If an existing entry matches the current session's `sub` → no-op (reuse).
 *   - Otherwise → mint a fresh PAT via CreateDaemonToken and write it,
 *     refreshing the stored session first when its access token is stale
 *     (see "Token freshness" in the module doc). A 401 from the mint with a
 *     token that LOOKED fresh triggers one refresh + one retry — server-side
 *     clock skew or out-of-band revocation, both fixed by a new token.
 *
 * `authUrl` + `authAnonKey` identify the GoTrue provider for the refresh.
 * When either is missing the refresh is disabled and behavior degrades to
 * the historical mint-with-stored-token (OSS builds with no hosted config).
 *
 * Failure-mode contract: this function NEVER throws. Every failure path is
 * logged and swallowed so the caller can spawn the daemon unconditionally.
 *
 * @param {{
 *   authStorage: { loadStoredAuth: () => object|null,
 *                  saveAuth?: (session: object) => boolean } | null,
 *   apiUrl: string,
 *   gatewayUrl?: string,
 *   authUrl?: string,
 *   authAnonKey?: string,
 *   logger?: { debug?: Function, info?: Function, warn?: Function, error?: Function },
 * }} args
 */
async function ensureDaemonPATForOrigin({
  authStorage,
  apiUrl,
  gatewayUrl,
  authUrl,
  authAnonKey,
  logger,
}) {
  const log = logger || NOOP_LOGGER;
  try {
    if (!authStorage) {
      log.debug?.('[daemon-creds] ensureDaemonPATForOrigin: no authStorage injected, skipping');
      return;
    }

    const key = endpointKey(apiUrl);
    if (!key) {
      log.warn?.('[daemon-creds] ensureDaemonPATForOrigin: invalid --server URL, skipping:', apiUrl);
      return;
    }

    // Read the current session BEFORE the idempotency check — we need its
    // sub to decide whether the cached PAT still belongs to the right user.
    // All three are mutable: a session refresh below swaps them wholesale.
    let session = authStorage.loadStoredAuth();
    let accessToken = session?.access_token;
    let currentSub = session?.user?.id;

    const store = readDaemonStore({ logger: log });
    const existing = store[key];
    const existingPat = existing && typeof existing.pat === 'string' ? existing.pat : '';
    const existingSub = existing && typeof existing.sub === 'string' ? existing.sub : '';

    // Reuse the cached PAT only when it belongs to the currently signed-in
    // user. Without this guard, sign-out + sign-in-as-different-user keeps
    // the prior user's PAT, the daemon re-registers under the wrong owner,
    // and ListDaemons for the new user comes back empty (the daemon row
    // belongs to the old internal_user_id). An entry written before we
    // tracked `sub` has existingSub === '' — treat that as unknown and
    // re-mint to be safe.
    if (existingPat && existingSub && currentSub && existingSub === currentSub) {
      log.debug?.('[daemon-creds] ensureDaemonPATForOrigin: existing PAT for origin matches current user, skipping mint:', key);
      return;
    }

    // Need to mint. If no session yet, the user hasn't signed in; the
    // daemon falls back to its own (headless-broken) flow, which is fine —
    // the renderer is about to surface the sign-in screen.
    if (!accessToken) {
      log.info?.('[daemon-creds] ensureDaemonPATForOrigin: no stored session, skipping mint (user not signed in yet)');
      return;
    }

    if (existingPat && existingSub && currentSub && existingSub !== currentSub) {
      log.info?.('[daemon-creds] ensureDaemonPATForOrigin: cached PAT belongs to a different user — re-minting for current session');
    } else if (existingPat && !existingSub) {
      log.info?.('[daemon-creds] ensureDaemonPATForOrigin: cached PAT has no owner sub recorded — re-minting');
    }

    // ── Session freshness ────────────────────────────────────────────────
    // The disk session is from the PREVIOUS app run; on a cold launch its
    // access token has usually outlived its ~1h TTL and the mint would 401
    // deterministically. Refresh first when we can tell it's stale, and
    // persist the rotated session so the renderer (which reads the same
    // store at startup) doesn't replay the consumed refresh token later.
    const canRefresh = Boolean(authUrl && authAnonKey && session?.refresh_token);
    let refreshed = false;
    const applyRefreshedSession = (next) => {
      session = next;
      accessToken = next.access_token;
      currentSub = next.user?.id || currentSub;
      refreshed = true;
      try {
        if (typeof authStorage.saveAuth === 'function' && !authStorage.saveAuth(next)) {
          log.warn?.('[daemon-creds] ensureDaemonPATForOrigin: could not persist refreshed session (continuing with in-memory session)');
        }
      } catch (persistErr) {
        log.warn?.('[daemon-creds] ensureDaemonPATForOrigin: persisting refreshed session threw (continuing):', persistErr?.message || persistErr);
      }
    };

    if (canRefresh && sessionNeedsRefresh(session)) {
      log.info?.('[daemon-creds] ensureDaemonPATForOrigin: stored access token expired/expiring — refreshing session before mint');
      try {
        applyRefreshedSession(
          await refreshSupabaseSession({ authUrl, anonKey: authAnonKey, session, logger: log })
        );
      } catch (refreshErr) {
        // Not fatal: fall through and mint with the stored token — identical
        // to the pre-refresh behavior. The 401 path below can't retry
        // (refresh already failed once; a second attempt won't do better).
        log.warn?.('[daemon-creds] ensureDaemonPATForOrigin: pre-mint session refresh failed, minting with stored token:', refreshErr.message);
        refreshed = true; // consume the one refresh attempt
      }
    }

    log.info?.('[daemon-creds] ensureDaemonPATForOrigin: minting daemon PAT for origin', key);
    let minted;
    try {
      minted = await mintDaemonPAT({
        apiUrl,
        accessToken,
        name: os.hostname(),
        logger: log,
      });
    } catch (mintErr) {
      // A 401 with a token we HAVEN'T refreshed yet means the token is stale
      // in a way expires_at didn't reveal (clock skew, revocation). One
      // refresh + one retry; anything else — 403/5xx/network — won't be
      // fixed by a new token, so log + skip (the pre-existing contract).
      const retriableAuthFailure = mintErr?.mintStatus === 401 && canRefresh && !refreshed;
      if (!retriableAuthFailure) {
        log.warn?.('[daemon-creds] ensureDaemonPATForOrigin: mint failed, falling back to daemon flow:', mintErr.message);
        return;
      }
      log.info?.('[daemon-creds] ensureDaemonPATForOrigin: mint got 401 — refreshing session and retrying once');
      try {
        applyRefreshedSession(
          await refreshSupabaseSession({ authUrl, anonKey: authAnonKey, session, logger: log })
        );
      } catch (refreshErr) {
        log.warn?.('[daemon-creds] ensureDaemonPATForOrigin: session refresh after 401 failed, falling back to daemon flow:', refreshErr.message);
        return;
      }
      try {
        minted = await mintDaemonPAT({
          apiUrl,
          accessToken,
          name: os.hostname(),
          logger: log,
        });
      } catch (retryErr) {
        log.warn?.('[daemon-creds] ensureDaemonPATForOrigin: mint retry after refresh failed, falling back to daemon flow:', retryErr.message);
        return;
      }
    }

    try {
      const resolvedGatewayUrl = gatewayUrl || '';
      upsertEntry({
        apiUrl,
        gatewayUrl: resolvedGatewayUrl,
        pat: minted.token,
        sub: currentSub,
        logger: log,
      });
      log.info?.(
        '[daemon-creds] ensureDaemonPATForOrigin: wrote PAT to daemon.json for origin',
        key,
        'gateway:', resolvedGatewayUrl || '(empty)',
        'sub:', currentSub || '(unknown)'
      );
    } catch (writeErr) {
      log.error?.('[daemon-creds] ensureDaemonPATForOrigin: failed to persist PAT:', writeErr.message);
    }
  } catch (e) {
    // Top-level safety net — nothing in the body should escape, but if
    // something does (e.g. logger blew up), don't let it block spawn.
    log.error?.('[daemon-creds] ensureDaemonPATForOrigin: unexpected error, swallowing:', e?.message || e);
  }
}

module.exports = {
  endpointKey,
  shouldSkipTLSVerify,
  daemonStorePaths,
  readDaemonStore,
  writeDaemonStore,
  upsertEntry,
  deleteEntry,
  entryOwnerSub,
  mintDaemonPAT,
  sessionNeedsRefresh,
  refreshSupabaseSession,
  ensureDaemonPATForOrigin,
  MINT_RPC_PATH,
  MINT_TIMEOUT_MS,
  MINT_MAX_ATTEMPTS,
  MINT_RETRY_BACKOFFS_MS,
  REFRESH_TOKEN_PATH,
  TOKEN_EXPIRY_MARGIN_S,
};