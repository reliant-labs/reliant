/**
 * Local OAuth helper server used in web mode.
 *
 * Served by `reliant daemon start` when the daemon runs on THIS machine, or by
 * `reliant auth serve` standalone (the remote-daemon case).
 *
 * Reaching it is also how the app knows the helper is co-located with the
 * browser. Nothing on the server side can establish that: a daemon knows its
 * own hostname and instance id, but nothing in its connection says which
 * machine rendered this page, and behind NAT many machines share one address.
 * "localhost" means the same machine to the browser and to whoever answers, so
 * a successful probe IS the proof.
 */

import type { StartOAuthFlowResponse } from '@/gen/reliant/v1/daemon_pb'

export const OAUTH_LOCAL_SERVER_PORT = 19284
export const OAUTH_LOCAL_SERVER_URL = `http://127.0.0.1:${OAUTH_LOCAL_SERVER_PORT}`

/** The `service` value reliant's helper reports on /health. */
export const OAUTH_HELPER_SERVICE = 'reliant'

/** The /health document (internal/auth/oauthhelper.HealthResponse). */
export interface OAuthHelperHealth {
  status: string
  service: string
  ready: boolean
  version: string
  /** Which command is serving: 'daemon' | 'auth-serve'. Diagnostic only. */
  source: string
}

/**
 * Probe the helper and return its identity, or null when nothing reliant is
 * there.
 *
 * Checking the `service` marker is the point. A bare 200 proves only that
 * SOMETHING holds port 19284 — any other dev server on that port answers a
 * health probe too, and the UI would then offer an OAuth flow that silently
 * fails. Requiring `service === 'reliant'` distinguishes "reliant is here"
 * from "a port is here".
 */
export async function probeOAuthHelper(
  timeoutMs = 2000,
): Promise<OAuthHelperHealth | null> {
  try {
    const resp = await fetch(`${OAUTH_LOCAL_SERVER_URL}/health`, {
      signal: AbortSignal.timeout(timeoutMs),
    })
    if (!resp.ok) return null
    const body = (await resp.json()) as Partial<OAuthHelperHealth>
    if (body?.service !== OAUTH_HELPER_SERVICE) return null
    return {
      status: body.status ?? 'unknown',
      service: body.service,
      ready: body.ready ?? false,
      version: body.version ?? 'unknown',
      source: body.source ?? 'unknown',
    }
  } catch {
    return null
  }
}

/** Shape matching the daemon's StartOAuthFlowResponse (camelCase). */
export interface OAuthStartResult {
  code: string
  state: string
  redirectUri: string
}

/**
 * Ask the daemon to open its helper port, then confirm it is reachable HERE.
 *
 * This two-step is the co-location test, and neither half works alone. The
 * request goes over the connection the daemon already holds — so it reaches
 * the daemon wherever it is — and the probe is a plain localhost fetch from
 * the browser. If the probe succeeds, the daemon that just opened the port is
 * demonstrably on this machine. If it fails, the daemon is remote: the port it
 * opened is on the wrong host, and an OAuth redirect to `localhost` would
 * never reach it.
 *
 * Returns the helper's identity on success, or null when the daemon is remote
 * (or nothing answered). Callers that get null should fall back to
 * `reliant auth serve` and say so.
 *
 * On a null result the port opened on the remote daemon is released, so a
 * failed probe does not leave a listener behind on someone else's machine.
 */
export async function requestOAuthHelper(
  signal?: AbortSignal,
): Promise<OAuthHelperHealth | null> {
  try {
    await openOAuthHelper(window.location.origin, signal)
  } catch {
    // The daemon is unreachable, too old to know the RPC, or refused. Fall
    // back to probing in case a standalone `auth serve` is already running.
    return probeOAuthHelper()
  }

  // The daemon reports success as soon as its listener is bound, but that
  // listener may be on another machine — only this probe can tell.
  const health = await probeOAuthHelper()
  if (!health) {
    void closeOAuthHelper()
    return null
  }
  return health
}

/** Release the helper port. Safe to call when nothing is open. */
export async function releaseOAuthHelper(): Promise<void> {
  await closeOAuthHelper()
}

/**
 * Start an OAuth flow via the local helper HTTP server.
 * Returns the same shape as the daemon's StartOAuthFlowResponse so callers
 * don't need to care which path was used.
 */
export async function startOAuthViaLocalServer(
  authorizeUrlTemplate: string,
  signal?: AbortSignal,
): Promise<OAuthStartResult> {
  let resp: Response
  try {
    resp = await fetch(`${OAUTH_LOCAL_SERVER_URL}/oauth/start`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        authorize_url_template: authorizeUrlTemplate,
      }),
      signal,
    })
  } catch (err) {
    // A user-initiated cancel is not a failure — let it through untouched so
    // callers can tell "I stopped this" from "the helper is gone".
    if (err instanceof DOMException && err.name === 'AbortError') throw err

    // Everything else here is the helper being unreachable: `reliant auth
    // serve` was stopped, or never started. The browser reports that as a bare
    // "Failed to fetch", which tells the user nothing and names no remedy.
    throw new Error(
      'The local OAuth helper is no longer running. Start the daemon on this ' +
        'machine with `reliant daemon start` (it serves the helper), or run ' +
        '`reliant auth serve` on its own, then retry.',
    )
  }

  if (!resp.ok) {
    const body = await resp.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(body.error || `OAuth start failed (${resp.status})`)
  }

  const body = await resp.json()

  // Normalize snake_case JSON from Go server to camelCase matching the daemon proto
  return {
    code: body.code,
    state: body.state,
    redirectUri: body.redirect_uri,
  }
}

// ─── DaemonService: the daemon's side of the helper ───────────────────────

/**
 * DaemonService on the app's routed transport. Loaded lazily, so importing
 * this module (useOAuthAvailability does, at startup) does not pull in the
 * Connect client graph before a flow actually asks the daemon for something.
 */
async function daemonClient() {
  const [{ connectClient }, { DaemonService }] = await Promise.all([
    import('@/lib/connect'),
    import('@/gen/reliant/v1/daemon_pb'),
  ])
  return connectClient(DaemonService)
}

/**
 * Start an OAuth flow via the daemon. The daemon spins up a localhost callback
 * server, opens the user's browser to the authorize URL (with the real redirect
 * URI substituted for the `{redirect_uri}` placeholder), waits for the callback,
 * then returns the authorization code, state, and redirect URI.
 *
 * ⚠️ This RPC CANNOT complete a real sign-in and is not used by the desktop
 * app any more. It blocks one request/response RPC on a human working through
 * a consent screen; in the packaged app that failed at ~15s with "Failed to
 * fetch", and cancelling the request tore down the daemon's listener, so the
 * browser's redirect hit a closed port. It is also unreachable by construction
 * in distributed mode, where the daemon is a pod and its loopback listener is
 * on the wrong machine entirely.
 *
 * Electron now uses its own local receiver (`startProviderOAuth` /
 * `waitForProviderOAuth` in electron/src/oauth-provider-login.js), which binds
 * the port and returns immediately, then waits over IPC that no network
 * deadline bounds. Kept only for the non-Electron fallback path.
 */
export async function startOAuthViaDaemon(
  authorizeUrlTemplate: string,
  signal?: AbortSignal,
): Promise<StartOAuthFlowResponse> {
  const client = await daemonClient()
  return client.startOAuthFlow({ authorizeUrlTemplate }, { signal })
}

/**
 * Ask the daemon to open its localhost OAuth helper port for ONE linking
 * session.
 *
 * Returns immediately — unlike `startOAuthViaDaemon` this does not block on a
 * human at a consent screen, which is what made that RPC fail at ~15s in the
 * packaged app.
 *
 * Pair every call with `closeOAuthHelper`. The daemon also closes the port
 * after an idle timeout, but that is the backstop for a UI that never got to
 * ask (a closed tab, a crash) — not the normal path.
 */
async function openOAuthHelper(webOrigin: string, signal?: AbortSignal): Promise<void> {
  const client = await daemonClient()
  await client.openOAuthHelper({ webOrigin }, { signal })
}

/**
 * Release the helper port. Best-effort by design: the caller is usually in a
 * `finally`, and a failure here must not mask the outcome of the flow itself.
 * The daemon's idle timeout covers a missed close.
 */
async function closeOAuthHelper(): Promise<void> {
  try {
    const client = await daemonClient()
    await client.closeOAuthHelper({})
  } catch {
    // Ignored: the idle timeout is the backstop.
  }
}