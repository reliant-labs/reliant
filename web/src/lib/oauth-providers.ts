/**
 * The canonical set of redirect + PKCE OAuth providers, and the lookup that
 * replaces the two-way ternaries this module exists to delete.
 *
 * Three UI surfaces used to choose an OAuth hook with
 * `kind === "claude" ? claudeOAuth : codexOAuth`. That is correct for exactly
 * two providers and silently wrong for the third: adding `antigravity` to a
 * provider table produced no type error and no runtime error — the Antigravity
 * button ran the CODEX flow and connected the wrong account.
 *
 * `Record<RedirectOAuthProvider, T>` is what makes that impossible now. A
 * fourth provider added to REDIRECT_OAUTH_PROVIDERS fails to compile at every
 * flow map until it is handled, and {@link resolveOAuthFlow} throws rather
 * than falling back if one is ever missing at runtime (a map built from
 * untyped data, or a provider string that came off the wire).
 *
 * GitHub Copilot is deliberately NOT here. It is a device-code flow — no PKCE,
 * no localhost callback, a different RPC pair and a different panel — so it
 * cannot share these flows and must keep its own branch.
 */

export const REDIRECT_OAUTH_PROVIDERS = ['claude', 'codex', 'antigravity'] as const

export type RedirectOAuthProvider = (typeof REDIRECT_OAUTH_PROVIDERS)[number]

/** Every OAuth kind a provider table's `usesOAuth` field may carry. */
export type OAuthProviderKind = RedirectOAuthProvider | 'copilot'

/**
 * User-facing names for the redirect providers, used in success banners
 * ("Connected to X successfully!"). Keyed by the same union, so a new provider
 * must supply one.
 */
export const REDIRECT_OAUTH_DISPLAY_NAMES: Record<RedirectOAuthProvider, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
  antigravity: 'Antigravity',
}

export function isRedirectOAuthProvider(value: unknown): value is RedirectOAuthProvider {
  return (
    typeof value === 'string' &&
    (REDIRECT_OAUTH_PROVIDERS as readonly string[]).includes(value)
  )
}

/**
 * Look a provider's flow up in a complete map.
 *
 * Throws on a miss instead of returning a default. The default IS the bug this
 * replaces: a silent fallback routes the user through another provider's
 * OAuth, which succeeds and connects the wrong account, so there is nothing to
 * see afterwards that says it went wrong.
 */
export function resolveOAuthFlow<T>(
  flows: Record<RedirectOAuthProvider, T>,
  provider: RedirectOAuthProvider,
): T {
  const flow = flows[provider]
  if (flow === undefined) {
    throw new Error(
      `No OAuth flow registered for provider "${provider}". ` +
        `Add it to the flow map — there is deliberately no fallback, because ` +
        `falling back would sign the user in to a different provider.`,
    )
  }
  return flow
}
