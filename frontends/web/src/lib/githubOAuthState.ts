// Copyright (c) 2025 Reliant Labs

/**
 * Client-side reading of the control plane's signed GitHub OAuth `state`.
 *
 * `state` is `base64url(JSON payload) + "." + base64url(HMAC)`. The payload
 * carries `uid`, `n`, `exp`, `rt` and, when a desktop app started the flow,
 * `lb`: the loopback URL its main process is listening on.
 *
 * The decode here is UNVERIFIED on purpose. The web callback page only uses
 * `lb` to decide where to relay the code, and a code is useless without a
 * validly signed state — the control plane re-verifies the signature when the
 * desktop app exchanges it. A forged `lb` therefore cannot do more than send a
 * code and a state that nobody can redeem to a loopback address, which is why
 * the address itself is still validated strictly (see parseLoopbackRedirect).
 */

export interface GitHubStatePayload {
  /** Loopback URL of the desktop app that started the flow. */
  lb?: string
  /** Where the web flow returns to afterwards. */
  rt?: string
}

function decodeBase64Url(value: string): string {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4)
  const binary = atob(padded)
  const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

/** The payload of a signed state, or null when it is not one. */
export function decodeGitHubState(state: string | undefined): GitHubStatePayload | null {
  if (!state) return null
  const dot = state.indexOf('.')
  if (dot <= 0) return null
  try {
    const payload: unknown = JSON.parse(decodeBase64Url(state.slice(0, dot)))
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null
    const { lb, rt } = payload as Record<string, unknown>
    return {
      lb: typeof lb === 'string' && lb !== '' ? lb : undefined,
      rt: typeof rt === 'string' && rt !== '' ? rt : undefined,
    }
  } catch {
    return null
  }
}

const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '[::1]'])

/**
 * Parse a loopback redirect, or null when it is not one. Mirrors the control
 * plane's validation: scheme http, host exactly 127.0.0.1 / localhost / [::1],
 * an explicit numeric port, no userinfo, no fragment. The path is free.
 */
export function parseLoopbackRedirect(value: string | undefined): URL | null {
  if (!value) return null
  let url: URL
  try {
    url = new URL(value)
  } catch {
    return null
  }
  if (url.protocol !== 'http:') return null
  if (!LOOPBACK_HOSTS.has(url.hostname)) return null
  if (url.username || url.password) return null
  if (url.hash || value.includes('#')) return null
  // URL drops a default port, so an explicit `:80` reads as none; the control
  // plane requires an explicit port and the receiver never listens on 80.
  if (!/^\d+$/.test(url.port)) return null
  return url
}
