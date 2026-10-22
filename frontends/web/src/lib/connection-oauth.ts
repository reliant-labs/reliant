// Copyright (c) 2025 Reliant Labs

/**
 * Connecting an integration (Slack, Gmail, any `oauth2` manifest) by signing
 * in at the provider, from the web app or the desktop app.
 *
 * ── Why it is shaped like this ────────────────────────────────────────
 *
 * The provider redirects to the API (`/integrations/oauth/{p}/callback`): that
 * is the redirect URI registered with it, and the API holds the client
 * secret. But the browser that arrives there carries no credential for the
 * API. In production the app (app.reliantlabs.io) and the API
 * (api.reliantapi.com) are different SITES, so a cookie the API set from a
 * fetch is never stored (SameSite, third-party-cookie blocking), and on the
 * desktop the consent runs in the system browser, whose cookie jar is not the
 * app's. A flow that tried to bind the callback to a cookie failed with
 * `connection_error=session` everywhere except the Vite dev server, which hid
 * it by making everything one origin.
 *
 * So the API only RELAYS. StartOAuth binds the flow to the signed-in user and
 * names where the code goes back to; the callback sends the browser there;
 * this client finishes with CompleteOAuth, authenticated as that user:
 *
 *   web      the API relays to `/connections/oauth/callback` on this app's
 *            own origin (the server takes it from the request's Origin and
 *            checks it against the origins it serves). That route is
 *            ConnectionOAuthCallback. The page leaves and comes back.
 *   desktop  the API relays to a loopback receiver this app's main process
 *            holds (RFC 8252), so the dialog stays open, collects the code,
 *            finishes the flow and hands back the connection.
 *
 * A code is useless to anyone else: CompleteOAuth refuses a user other than
 * the one who started the flow, and burns the flow when it does.
 */
import { connectionGrpc, type Connection } from '@/api/connection-grpc'

/** The web route the API relays a browser flow to (Go: connections.AppCallbackPath). */
export const CONNECTION_OAUTH_CALLBACK_PATH = '/connections/oauth/callback'

type ConnectionOAuthBridge = {
  startConnectionOAuthReceiver?: () => Promise<{ flowId: string; redirectUri: string }>
  waitForProviderOAuth?: (flowId: string) => Promise<{ code: string; state: string }>
  cancelProviderOAuth?: (flowId: string) => Promise<unknown>
  openExternal?: (url: string) => Promise<unknown>
}

const bridge = (): ConnectionOAuthBridge | undefined =>
  typeof window === 'undefined'
    ? undefined
    : (window as unknown as { electronAPI?: ConnectionOAuthBridge }).electronAPI

/**
 * Whether consent happens in the system browser with this app waiting for it
 * (the desktop app), rather than by navigating this page (the web app).
 */
export const connectsThroughSystemBrowser = (): boolean => {
  const api = bridge()
  return Boolean(api?.startConnectionOAuthReceiver && api?.waitForProviderOAuth && api?.openExternal)
}

export interface ConnectionOAuthInput {
  integrationId: string
  name?: string
  /** Relative path to land on afterwards (web). */
  redirectAfter: string
  params?: Record<string, string>
}

export type ConnectionOAuthOutcome =
  /** Web: this page is navigating to the provider and will come back. */
  | { kind: 'redirecting' }
  /** Desktop: the flow finished while the dialog waited. */
  | { kind: 'connected'; connection: Connection }

/** The error the desktop flow rejects with when it was cancelled. */
export class ConnectionOAuthCancelled extends Error {
  constructor() {
    super('Connection cancelled')
    this.name = 'ConnectionOAuthCancelled'
  }
}

/** A message for an error class the API relays instead of a code. */
export function relayedErrorMessage(errorClass: string): string {
  switch (errorClass) {
    case 'denied':
      return "Access wasn't granted at the provider."
    case 'invalid':
      return "The provider didn't return a sign-in code."
    default:
      return 'The provider sign-in did not complete.'
  }
}

/**
 * Start (and, on the desktop, finish) an OAuth connection.
 *
 * `signal` cancels a desktop flow: the loopback receiver is released at once
 * rather than when it times out.
 */
export async function connectWithOAuth(
  input: ConnectionOAuthInput,
  signal?: AbortSignal,
): Promise<ConnectionOAuthOutcome> {
  const api = bridge()
  if (!api?.startConnectionOAuthReceiver || !api.waitForProviderOAuth || !api.openExternal) {
    const authorizeUrl = await connectionGrpc.startOAuth(input)
    window.location.assign(authorizeUrl)
    return { kind: 'redirecting' }
  }

  // The receiver comes first: the API must be told where to relay before it
  // can start the flow.
  const { flowId, redirectUri } = await api.startConnectionOAuthReceiver()
  const release = () => void api.cancelProviderOAuth?.(flowId)
  signal?.addEventListener('abort', release, { once: true })
  try {
    if (signal?.aborted) throw new ConnectionOAuthCancelled()
    const authorizeUrl = await connectionGrpc.startOAuth({ ...input, loopbackRedirect: redirectUri })
    if (signal?.aborted) throw new ConnectionOAuthCancelled()
    await api.openExternal(authorizeUrl)

    let relayed: { code: string; state: string }
    try {
      relayed = await api.waitForProviderOAuth(flowId)
    } catch (err) {
      if (signal?.aborted) throw new ConnectionOAuthCancelled()
      // The receiver rejects with the error class the API relayed.
      const errorClass = err instanceof Error ? err.message.trim() : ''
      throw new Error(relayedErrorMessage(errorClass))
    }
    if (signal?.aborted) throw new ConnectionOAuthCancelled()

    const { connection } = await connectionGrpc.completeOAuth(relayed)
    return { kind: 'connected', connection }
  } catch (err) {
    release()
    throw err
  } finally {
    signal?.removeEventListener('abort', release)
  }
}
