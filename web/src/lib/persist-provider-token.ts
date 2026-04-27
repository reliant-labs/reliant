import { settingsGrpc } from '@/api/settings-grpc'
import { logger } from '@/lib/logger'

/**
 * Persist an OAuth provider token to the daemon's settings store.
 * This is fire-and-forget — callers should `.catch()` to avoid unhandled rejections.
 *
 * The provider_token from Supabase OAuth is transient and only available
 * immediately after sign-in. If we don't capture it here, it's gone forever.
 *
 * Primary capture: OAuthCallback.tsx (after exchangeCodeForSession — reliable).
 * Backup capture: authStore.ts onAuthStateChange (best-effort, token may be absent).
 *
 * NOTE: Electron OAuth flows use devAuthGrpc.startOAuthSignIn → setSession,
 * which does not surface provider_token. Electron users may need to configure
 * their GitHub token separately (e.g. via the MCP GitHub server setup).
 */
export async function persistProviderToken(
  token: string,
  provider: string,
  scopes: string,
): Promise<void> {
  const key = `git_provider_token.${provider}`
  try {
    // Try update first (setting may already exist from a previous sign-in)
    await settingsGrpc.updateSetting(key, JSON.stringify({ token, provider, scopes }), 'secret')
  } catch {
    // If update fails (setting doesn't exist), create it
    await settingsGrpc.createSetting(key, JSON.stringify({ token, provider, scopes }), 'secret')
  }
  logger.info(`[persistProviderToken] Persisted ${provider} token with scopes: ${scopes}`)
}