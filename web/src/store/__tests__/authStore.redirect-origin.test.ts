import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * Regression cover for the loopback OAuth redirect.
 *
 * A packaged Electron build exposes `window.electronAPI`, so `isElectron` is
 * true, but the preload has never implemented `getOAuthRedirectUrl`. The
 * redirect helper's guard therefore fell through to
 * `${window.location.origin}/auth/callback`, and in a packaged build the
 * renderer's origin is whatever local address the window happens to be served
 * from — an ephemeral loopback port. That address was then handed to the
 * identity provider as a public redirect target, which is what produced
 * callbacks like `http://127.0.0.1:61655/auth/callback?code=...`.
 *
 * The fix routes the redirect through the hosted app URL instead of the
 * renderer's own origin, so a loopback origin can never leak into a
 * provider-facing redirect.
 */

const signInWithOAuthMock = vi.fn()
const linkIdentityMock = vi.fn()
const setSessionMock = vi.fn()
const getSessionMock = vi.fn(async () => ({ data: { session: null }, error: null }))
const onAuthStateChangeMock = vi.fn(() => ({
  data: { subscription: { unsubscribe: vi.fn() } },
}))
const startOAuthSignInMock = vi.fn()

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: getSessionMock,
      onAuthStateChange: onAuthStateChangeMock,
      signInWithOAuth: signInWithOAuthMock,
      linkIdentity: linkIdentityMock,
      setSession: setSessionMock,
    },
  },
}))

vi.mock('@/api/grpc-unauth', () => ({
  devAuthGrpc: {
    startOAuthSignIn: startOAuthSignInMock,
    load: vi.fn(async () => ({ success: false })),
    save: vi.fn(async () => ({ success: true })),
    clear: vi.fn(async () => ({ success: true })),
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), error: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

// Only getIsDev is stubbed; getAppURL is the real implementation under test.
vi.mock('@/lib/constants', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/constants')>()),
  getIsDev: () => false,
}))

vi.mock('@/lib/sentry', () => ({
  setSentryUser: vi.fn(),
}))

type TestWindow = Window & { electronAPI?: unknown }

/**
 * Repoint the jsdom document at `origin`. The packaged renderer is served from
 * a local address, so this is how we reproduce the origin the bug read from.
 */
const setDocumentOrigin = (origin: string) => {
  const url = new URL(origin)
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...window.location, origin: url.origin, href: `${url.origin}/`, protocol: url.protocol },
  })
}

/** The loopback origin a packaged renderer reports — never a valid redirect. */
const LOOPBACK_ORIGIN = 'http://127.0.0.1:61655'

const redirectTargetFrom = (mock: ReturnType<typeof vi.fn>): string => {
  const call = mock.mock.calls.at(-1)
  if (!call) throw new Error('provider redirect was never requested')
  return (call[0] as { options: { redirectTo: string } }).options.redirectTo
}

describe('OAuth redirect target in a packaged Electron build', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    setDocumentOrigin(LOOPBACK_ORIGIN)

    // A packaged build DOES expose electronAPI — but without
    // getOAuthRedirectUrl, which is precisely the shipped 1.6.3 surface.
    ;(window as TestWindow).electronAPI = { analyticsTrack: vi.fn(), openExternal: vi.fn() }

    signInWithOAuthMock.mockResolvedValue({
      data: { url: 'https://accounts.google.com/o/oauth2/auth?example=1' },
      error: null,
    })
    linkIdentityMock.mockResolvedValue({
      data: { url: 'https://accounts.google.com/o/oauth2/auth?example=1' },
      error: null,
    })
  })

  it('never sends a loopback address as the identity provider redirect', async () => {
    // linkIdentity is the flow that runs in packaged Electron (sign-in proper
    // goes through the daemon), so it is the one that leaked the port.
    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().linkOAuthIdentity('google')

    const redirectTo = redirectTargetFrom(linkIdentityMock)
    expect(redirectTo).not.toContain('127.0.0.1')
    expect(redirectTo).not.toContain('localhost')
    expect(new URL(redirectTo).hostname).not.toBe('127.0.0.1')
  })

  it('points the redirect at the hosted app URL', async () => {
    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().linkOAuthIdentity('google')

    expect(redirectTargetFrom(linkIdentityMock)).toBe(
      'https://app.reliantlabs.io/auth/callback',
    )
  })
})
