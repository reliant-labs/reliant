/**
 * Provider sign-in (Claude Code / Codex) on the desktop app.
 *
 * ── Why this exists ───────────────────────────────────────────────────
 *
 * Electron used to run these flows through `DaemonService/StartOAuthFlow`: one
 * request/response RPC that stayed open while the daemon bound a port, opened
 * a browser, and waited for the user to finish signing in. A human takes
 * longer than the network path allows. The packaged app failed at ~15s with
 * `Failed to fetch (api.reliantapi.com)`, and because cancelling the request
 * cancelled the daemon's context, the listener was gone by the time the
 * browser redirected — so the user also got ERR_CONNECTION_REFUSED. Two
 * reported symptoms, one cause.
 *
 * The desktop app has no reason to make that trip. It runs on the user's
 * machine, so it can hold the listener itself; the daemon, in distributed
 * mode, is a pod whose loopback address the user's browser cannot reach at
 * all.
 *
 * ── The shape that fixes it ───────────────────────────────────────────
 *
 * `startProviderOAuth` binds the port and returns in microseconds.
 * `waitForProviderOAuth` is the call that spans human time, and it is plain
 * IPC to the same machine — no proxy, gateway or NATS hop that could impose a
 * deadline on it. Starting BEFORE waiting also closes the race the old design
 * could not: the port is bound before the browser is ever opened.
 */
import type { OAuthStartResult } from '@/lib/oauth-local'

type ProviderOAuthBridge = {
  startProviderOAuth: (
    authorizeUrlTemplate: string,
  ) => Promise<{ flowId: string; redirectUri: string }>
  waitForProviderOAuth: (
    flowId: string,
  ) => Promise<{ code: string; state: string; redirectUri: string }>
  cancelProviderOAuth: (flowId: string) => Promise<unknown>
}

const bridge = (): ProviderOAuthBridge | undefined =>
  typeof window === 'undefined'
    ? undefined
    : (window as unknown as { electronAPI?: ProviderOAuthBridge }).electronAPI

/**
 * Run a provider sign-in through the desktop app's own loopback receiver.
 *
 * @param authorizeUrlTemplate Authorize URL carrying the literal
 *   `{redirect_uri}` placeholder. The main process substitutes the URI it
 *   actually bound, so the advertised address and the listening socket cannot
 *   disagree.
 */
export async function startOAuthViaDesktop(
  authorizeUrlTemplate: string,
  signal?: AbortSignal,
): Promise<OAuthStartResult> {
  const api = bridge()
  if (!api) {
    throw new Error('Provider sign-in through the desktop app needs the desktop app')
  }

  const { flowId } = await api.startProviderOAuth(authorizeUrlTemplate)

  // Cancelling releases the port immediately rather than at the main process's
  // timeout — which matters for Codex, whose port 1455 is fixed by OpenAI and
  // would block every later attempt while it leaked.
  const onAbort = () => {
    void api.cancelProviderOAuth(flowId)
  }
  signal?.addEventListener('abort', onAbort, { once: true })

  try {
    const { code, state, redirectUri } = await api.waitForProviderOAuth(flowId)
    return { code, state, redirectUri }
  } finally {
    signal?.removeEventListener('abort', onAbort)
  }
}
