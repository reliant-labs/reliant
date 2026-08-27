import * as Sentry from '@sentry/react'
import { settingsGrpc } from '@/api/settings-grpc'
import { startOAuthViaDaemon } from '@/api/daemon-grpc'
import { startOAuthViaLocalServer } from '@/lib/oauth-local'
import { startOAuthViaDesktop, supportsLocalProviderOAuth } from '@/lib/oauth-desktop'
import {
  InsecureContextError,
  base64UrlEncode,
  generateCodeChallenge,
  generateCodeVerifier,
  randomBytes,
} from '@/lib/pkce'

const CODEX_OAUTH_AUTHORIZE_URL = 'https://auth.openai.com/oauth/authorize'
const CODEX_OAUTH_CLIENT_ID = 'app_EMoamEEZ73f0CkXaXp7hrann'
// Align with Codex CLI authorize URL (codex-rs/login `build_authorize_url`).
// Only request OIDC scopes — extended API scopes (e.g. api.responses.write) are
// not registered on the OAuth client and cause an "invalid_scope" rejection.
const CODEX_OAUTH_DEFAULT_SCOPE = 'openid profile email offline_access'
const CODEX_OAUTH_ADDITIONAL_AUTHORIZE_PARAMS: Record<string, string> = {
  id_token_add_organizations: 'true',
  codex_cli_simplified_flow: 'true',
}
export const CODEX_OAUTH_STATE_PREFIX = 'reliant:oauth:codex:'

type CodexOAuthErrorCode =
  | 'pkce_generation_failed'
  | 'timeout'
  | 'cancelled'
  | 'state_mismatch'
  | 'token_exchange_failed'
  | 'daemon_error'

export type CodexOAuthResult =
  | {
      ok: true
      message: string
    }
  | {
      ok: false
      errorCode: CodexOAuthErrorCode
      message: string
    }

export interface CodexOAuthOptions {
  signal?: AbortSignal
  authorizeUrl?: string
  clientId?: string
  scope?: string
  statePrefix?: string
}

const errorResult = (errorCode: CodexOAuthErrorCode, message: string): CodexOAuthResult => ({
  ok: false,
  errorCode,
  message,
})

export async function runCodexOAuthFlow(options: CodexOAuthOptions = {}): Promise<CodexOAuthResult> {
  const statePrefix = options.statePrefix ?? CODEX_OAUTH_STATE_PREFIX
  const authorizeUrl = options.authorizeUrl ?? CODEX_OAUTH_AUTHORIZE_URL
  const clientId = options.clientId ?? CODEX_OAUTH_CLIENT_ID
  const scope = options.scope ?? CODEX_OAUTH_DEFAULT_SCOPE

  // Generate PKCE. This runs BEFORE the browser round trip and must not throw:
  // on a non-secure origin crypto.subtle is undefined, and the raw TypeError
  // ("reading 'digest'") escaped the flow entirely instead of reaching the UI.
  let codeVerifier: string
  let codeChallenge: string
  try {
    codeVerifier = generateCodeVerifier()
    codeChallenge = await generateCodeChallenge(codeVerifier)
  } catch (error) {
    const message =
      error instanceof InsecureContextError
        ? error.message
        : 'Could not generate the PKCE challenge required to sign in.'
    Sentry.captureException(error, {
      tags: { component: 'oauth', provider: 'codex', stage: 'pkce' },
    })
    return errorResult('pkce_generation_failed', message)
  }

  // Generate state
  const state = statePrefix + base64UrlEncode(randomBytes(24))

  // Build authorize URL with {redirect_uri} placeholder
  const url = new URL(authorizeUrl)
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('client_id', clientId)
  url.searchParams.set('redirect_uri', '{redirect_uri}')
  url.searchParams.set('state', state)
  url.searchParams.set('code_challenge', codeChallenge)
  url.searchParams.set('code_challenge_method', 'S256')
  url.searchParams.set('scope', scope)

  // Add Codex-specific extra params
  Object.entries(CODEX_OAUTH_ADDITIONAL_AUTHORIZE_PARAMS).forEach(([key, value]) => {
    url.searchParams.set(key, value)
  })

  // The URL will have {redirect_uri} URL-encoded in the query string.
  // We need to replace the encoded version with the literal placeholder.
  const authorizeURLTemplate = url.toString().replace(encodeURIComponent('{redirect_uri}'), '{redirect_uri}')

  try {
    // Desktop runs the callback receiver ITSELF (see lib/oauth-desktop.ts).
    // It used to delegate to the daemon over one RPC held open across the
    // user's browser round trip, which failed at ~15s and took the listener
    // down with it. The daemon RPC remains only as the fallback for builds
    // whose main process predates the local receiver.
    const isElectron = !!window.electronAPI

    const oauthResp = isElectron
      ? supportsLocalProviderOAuth()
        ? await startOAuthViaDesktop(authorizeURLTemplate, options.signal)
        : await startOAuthViaDaemon(authorizeURLTemplate, options.signal)
      : await startOAuthViaLocalServer(authorizeURLTemplate, options.signal)

    // Validate state
    if (oauthResp.state !== state) {
      Sentry.captureMessage('Codex OAuth state mismatch', {
        tags: { component: 'oauth', provider: 'codex' },
        level: 'warning',
      })
      return errorResult('state_mismatch', 'OAuth state mismatch')
    }

    const authCode = (oauthResp.code ?? '').trim()
    if (!authCode) {
      return errorResult(
        'daemon_error',
        'OAuth finished without an authorization code. Check the browser tab for an error from OpenAI, then try again.',
      )
    }

    // Exchange code for tokens via the authenticated backend
    const result = await settingsGrpc.completeCodexOAuth(
      authCode,
      codeVerifier,
      oauthResp.redirectUri,
    )

    if (result.success) {
      return { ok: true, message: result.message || 'Codex connected successfully' }
    }
    Sentry.captureMessage('Codex OAuth token exchange failed', {
      tags: { component: 'oauth', provider: 'codex' },
      level: 'warning',
    })
    return errorResult('token_exchange_failed', result.message || 'Token exchange failed')
  } catch (error: any) {
    Sentry.captureException(error, {
      tags: { component: 'oauth', provider: 'codex' },
      level: 'warning',
    })
    return errorResult('daemon_error', error.message || 'OAuth flow failed')
  }
}