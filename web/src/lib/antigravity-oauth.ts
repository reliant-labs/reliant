import * as Sentry from '@sentry/react'
import { settingsGrpc } from '@/api/settings-grpc'
import { startOAuthViaDaemon } from '@/api/daemon-grpc'
import { startOAuthViaLocalServer } from '@/lib/oauth-local'
import { startOAuthViaDesktop, supportsLocalProviderOAuth } from '@/lib/oauth-desktop'
import { isAbort } from '@/lib/oauth-abort'
import {
  InsecureContextError,
  base64UrlEncode,
  generateCodeChallenge,
  generateCodeVerifier,
  randomBytes,
} from '@/lib/pkce'

const ANTIGRAVITY_OAUTH_AUTHORIZE_URL = 'https://accounts.google.com/o/oauth2/auth'
const ANTIGRAVITY_OAUTH_CLIENT_ID =
  '1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com'
// Exactly the scope set the Antigravity CLI requests. Google rejects the whole
// authorize request if any one of these is not registered on the client, so
// they are kept byte-identical to the captured client rather than trimmed to
// the ones Reliant appears to need.
const ANTIGRAVITY_OAUTH_DEFAULT_SCOPE = [
  'https://www.googleapis.com/auth/cloud-platform',
  'https://www.googleapis.com/auth/userinfo.email',
  'https://www.googleapis.com/auth/userinfo.profile',
  'https://www.googleapis.com/auth/cclog',
  'https://www.googleapis.com/auth/experimentsandconfigs',
  'https://www.googleapis.com/auth/aicode',
  'openid',
].join(' ')
// `access_type=offline` is what makes Google return a refresh token at all, and
// `prompt=consent` is what makes it do so on every sign-in rather than only the
// first. Without both, a reconnect yields an access token that expires in an
// hour with nothing to refresh it from.
const ANTIGRAVITY_OAUTH_ADDITIONAL_AUTHORIZE_PARAMS: Record<string, string> = {
  access_type: 'offline',
  prompt: 'consent',
}
export const ANTIGRAVITY_OAUTH_STATE_PREFIX = 'reliant:oauth:antigravity:'

type AntigravityOAuthErrorCode =
  | 'pkce_generation_failed'
  | 'timeout'
  | 'cancelled'
  | 'state_mismatch'
  | 'token_exchange_failed'
  | 'daemon_error'

export type AntigravityOAuthResult =
  | {
      ok: true
      message: string
    }
  | {
      ok: false
      errorCode: AntigravityOAuthErrorCode
      message: string
    }

export interface AntigravityOAuthOptions {
  signal?: AbortSignal
  authorizeUrl?: string
  clientId?: string
  scope?: string
  statePrefix?: string
}

const errorResult = (
  errorCode: AntigravityOAuthErrorCode,
  message: string,
): AntigravityOAuthResult => ({
  ok: false,
  errorCode,
  message,
})

export async function runAntigravityOAuthFlow(
  options: AntigravityOAuthOptions = {},
): Promise<AntigravityOAuthResult> {
  const statePrefix = options.statePrefix ?? ANTIGRAVITY_OAUTH_STATE_PREFIX
  const authorizeUrl = options.authorizeUrl ?? ANTIGRAVITY_OAUTH_AUTHORIZE_URL
  const clientId = options.clientId ?? ANTIGRAVITY_OAUTH_CLIENT_ID
  const scope = options.scope ?? ANTIGRAVITY_OAUTH_DEFAULT_SCOPE

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
      tags: { component: 'oauth', provider: 'antigravity', stage: 'pkce' },
    })
    return errorResult('pkce_generation_failed', message)
  }

  // Generate state.
  //
  // Google's token endpoint does not want `state` echoed back on the exchange,
  // so CompleteAntigravityOAuthRequest carries no state field — but the value
  // is still generated and still compared below. State is the CSRF defence for
  // the REDIRECT, not for the exchange: without this comparison an attacker's
  // authorization code could be delivered to our callback and silently linked
  // to the signed-in user's account.
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

  // Add Google-specific extra params
  Object.entries(ANTIGRAVITY_OAUTH_ADDITIONAL_AUTHORIZE_PARAMS).forEach(([key, value]) => {
    url.searchParams.set(key, value)
  })

  // The URL will have {redirect_uri} URL-encoded in the query string.
  // We need to replace the encoded version with the literal placeholder.
  const authorizeURLTemplate = url
    .toString()
    .replace(encodeURIComponent('{redirect_uri}'), '{redirect_uri}')

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
      Sentry.captureMessage('Antigravity OAuth state mismatch', {
        tags: { component: 'oauth', provider: 'antigravity' },
        level: 'warning',
      })
      return errorResult('state_mismatch', 'OAuth state mismatch')
    }

    const authCode = (oauthResp.code ?? '').trim()
    if (!authCode) {
      return errorResult(
        'daemon_error',
        'OAuth finished without an authorization code. Check the browser tab for an error from Google, then try again.',
      )
    }

    // Exchange code for tokens via the authenticated backend
    const result = await settingsGrpc.completeAntigravityOAuth(
      authCode,
      codeVerifier,
      oauthResp.redirectUri,
    )

    if (result.success) {
      return { ok: true, message: result.message || 'Antigravity connected successfully' }
    }
    Sentry.captureMessage('Antigravity OAuth token exchange failed', {
      tags: { component: 'oauth', provider: 'antigravity' },
      level: 'warning',
    })
    return errorResult('token_exchange_failed', result.message || 'Token exchange failed')
  } catch (error: any) {
    // A cancelled flow is not a failure, and must never be reported as one.
    // Clicking Connect a second time aborts the first run's AbortController,
    // which rejects the in-flight fetch; without this branch that rejection
    // surfaces as an error banner for the very action that is succeeding.
    // See lib/oauth-abort for why the signal, not the error identity, is the
    // reliable witness.
    if (isAbort(error, options.signal)) {
      return errorResult('cancelled', 'Sign-in cancelled.')
    }
    Sentry.captureException(error, {
      tags: { component: 'oauth', provider: 'antigravity' },
      level: 'warning',
    })
    return errorResult('daemon_error', error.message || 'OAuth flow failed')
  }
}
