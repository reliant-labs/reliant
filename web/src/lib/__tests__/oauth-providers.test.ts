import { describe, expect, it } from 'vitest'
import {
  REDIRECT_OAUTH_DISPLAY_NAMES,
  REDIRECT_OAUTH_PROVIDERS,
  isRedirectOAuthProvider,
  resolveOAuthFlow,
  type RedirectOAuthProvider,
} from '@/lib/oauth-providers'

/**
 * These tests pin the property that the deleted ternaries did not have: a
 * provider missing from a flow map is a VISIBLE failure, not a silent
 * redirect into some other provider's sign-in.
 */
describe('redirect OAuth provider registry', () => {
  it('includes every redirect provider the UI offers', () => {
    // Antigravity is the provider whose omission the two-way ternary hid.
    expect([...REDIRECT_OAUTH_PROVIDERS].sort()).toEqual([
      'antigravity',
      'claude',
      'codex',
    ])
  })

  it('names every redirect provider', () => {
    for (const provider of REDIRECT_OAUTH_PROVIDERS) {
      expect(REDIRECT_OAUTH_DISPLAY_NAMES[provider]).toBeTruthy()
    }
    expect(Object.keys(REDIRECT_OAUTH_DISPLAY_NAMES).sort()).toEqual(
      [...REDIRECT_OAUTH_PROVIDERS].sort(),
    )
  })

  it('resolves each provider to its OWN flow, never a neighbour', () => {
    const flows: Record<RedirectOAuthProvider, string> = {
      claude: 'claude-flow',
      codex: 'codex-flow',
      antigravity: 'antigravity-flow',
    }

    for (const provider of REDIRECT_OAUTH_PROVIDERS) {
      expect(resolveOAuthFlow(flows, provider)).toBe(`${provider}-flow`)
    }
  })

  it('throws instead of falling back when a provider is missing from the map', () => {
    // The exact regression: a map built before Antigravity existed. The old
    // `kind === "claude" ? claudeOAuth : codexOAuth` returned the Codex flow
    // here and connected the wrong account with no error at all.
    const incompleteFlows = {
      claude: 'claude-flow',
      codex: 'codex-flow',
    } as unknown as Record<RedirectOAuthProvider, string>

    expect(() => resolveOAuthFlow(incompleteFlows, 'antigravity')).toThrow(
      /No OAuth flow registered for provider "antigravity"/,
    )
    // The providers that ARE present still resolve, so the throw above is
    // about the missing entry and not a broken lookup.
    expect(resolveOAuthFlow(incompleteFlows, 'codex')).toBe('codex-flow')
  })

  it('recognises redirect providers and rejects device-flow / unknown ones', () => {
    expect(isRedirectOAuthProvider('antigravity')).toBe(true)
    expect(isRedirectOAuthProvider('claude')).toBe(true)
    expect(isRedirectOAuthProvider('codex')).toBe(true)
    // Copilot is device-code: it shares no part of the redirect flow.
    expect(isRedirectOAuthProvider('copilot')).toBe(false)
    expect(isRedirectOAuthProvider(false)).toBe(false)
    expect(isRedirectOAuthProvider(undefined)).toBe(false)
  })
})
