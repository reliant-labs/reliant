import * as Sentry from '@sentry/react'
import { settingsGrpc } from '@/api/settings-grpc'
import { startOAuthViaDaemon } from '@/api/daemon-grpc'
import { startOAuthViaLocalServer } from '@/lib/oauth-local'

const CLAUDE_OAUTH_AUTHORIZE_URL = 'https://claude.ai/oauth/authorize'
const CLAUDE_OAUTH_CLIENT_ID = '9d1c250a-e61b-44d9-88ed-5944d1962f5e'
const CLAUDE_OAUTH_DEFAULT_SCOPE =
  'org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload'
export const CLAUDE_OAUTH_STATE_PREFIX = 'reliant:oauth:claude:'

type ClaudeOAuthErrorCode =
  | 'pkce_generation_failed'
  | 'timeout'
  | 'cancelled'
  | 'state_mismatch'
  | 'token_exchange_failed'
  | 'daemon_error'

export type ClaudeOAuthResult =
  | {
      ok: true
      message: string
    }
  | {
      ok: false
      errorCode: ClaudeOAuthErrorCode
      message: string
    }

export interface ClaudeOAuthOptions {
  signal?: AbortSignal
  authorizeUrl?: string
  clientId?: string
  scope?: string
  statePrefix?: string
}

const errorResult = (errorCode: ClaudeOAuthErrorCode, message: string): ClaudeOAuthResult => ({
  ok: false,
  errorCode,
  message,
})

const base64UrlEncode = (bytes: Uint8Array): string => {
  let binary = ''
  for (const byte of bytes) {
    binary += String.fromCharCode(byte)
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

const randomBytes = (length: number): Uint8Array => {
  const values = new Uint8Array(length)
  crypto.getRandomValues(values)
  return values
}

const generateCodeVerifier = (): string => base64UrlEncode(randomBytes(64))

const generateCodeChallenge = async (codeVerifier: string): Promise<string> => {
  const encoded = new TextEncoder().encode(codeVerifier)
  const digest = await crypto.subtle.digest('SHA-256', encoded)
  return base64UrlEncode(new Uint8Array(digest))
}

export async function runClaudeOAuthFlow(options: ClaudeOAuthOptions = {}): Promise<ClaudeOAuthResult> {
  const statePrefix = options.statePrefix ?? CLAUDE_OAUTH_STATE_PREFIX
  const authorizeUrl = options.authorizeUrl ?? CLAUDE_OAUTH_AUTHORIZE_URL
  const clientId = options.clientId ?? CLAUDE_OAUTH_CLIENT_ID
  const scope = options.scope ?? CLAUDE_OAUTH_DEFAULT_SCOPE

  // Generate PKCE
  const codeVerifier = generateCodeVerifier()
  const codeChallenge = await generateCodeChallenge(codeVerifier)

  // Generate state
  const state = statePrefix + base64UrlEncode(randomBytes(24))

  // Build authorize URL with {redirect_uri} placeholder
  const url = new URL(authorizeUrl)
  url.searchParams.set('code', 'true')
  url.searchParams.set('client_id', clientId)
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('redirect_uri', '{redirect_uri}')
  url.searchParams.set('scope', scope)
  url.searchParams.set('code_challenge', codeChallenge)
  url.searchParams.set('code_challenge_method', 'S256')
  url.searchParams.set('state', state)

  // The URL will have {redirect_uri} URL-encoded in the query string.
  // We need to replace the encoded version with the literal placeholder.
  const authorizeURLTemplate = url.toString().replace(encodeURIComponent('{redirect_uri}'), '{redirect_uri}')

  try {
    // In Electron the daemon handles the localhost callback; in web mode
    // the user runs `reliant auth serve` which exposes an HTTP endpoint.
    const isElectron = !!window.electronAPI

    const oauthResp = isElectron
      ? await startOAuthViaDaemon(authorizeURLTemplate, options.signal)
      : await startOAuthViaLocalServer(authorizeURLTemplate, options.signal)

    // Validate state
    if (oauthResp.state !== state) {
      Sentry.captureMessage('Claude OAuth state mismatch', {
        tags: { component: 'oauth', provider: 'claude' },
        level: 'warning',
      })
      return errorResult('state_mismatch', 'OAuth state mismatch')
    }

    // Exchange code for tokens via the authenticated backend
    const result = await settingsGrpc.completeClaudeOAuth(
      oauthResp.code,
      codeVerifier,
      oauthResp.redirectUri,
      state
    )

    if (result.success) {
      return { ok: true, message: result.message || 'Claude connected successfully' }
    }
    Sentry.captureMessage('Claude OAuth token exchange failed', {
      tags: { component: 'oauth', provider: 'claude' },
      level: 'warning',
    })
    return errorResult('token_exchange_failed', result.message || 'Token exchange failed')
  } catch (error: any) {
    Sentry.captureException(error, {
      tags: { component: 'oauth', provider: 'claude' },
      level: 'warning',
    })
    return errorResult('daemon_error', error.message || 'OAuth flow failed')
  }
}