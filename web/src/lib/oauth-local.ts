/**
 * Local OAuth helper server used in web mode.
 * Started via `reliant auth serve` on the user's machine.
 */

export const OAUTH_LOCAL_SERVER_PORT = 19284
export const OAUTH_LOCAL_SERVER_URL = `http://127.0.0.1:${OAUTH_LOCAL_SERVER_PORT}`

/** Shape matching the daemon's StartOAuthFlowResponse (camelCase). */
export interface OAuthStartResult {
  code: string
  state: string
  redirectUri: string
}

/**
 * Start an OAuth flow via the local `reliant auth serve` HTTP server.
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
      'The local OAuth helper is no longer running. Start it again with ' +
        '`reliant auth serve`, then retry.',
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