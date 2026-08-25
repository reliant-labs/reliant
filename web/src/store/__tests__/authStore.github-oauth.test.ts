import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * Provider sign-in is now ONE code path on both surfaces.
 *
 * This file used to be titled "runtime split" and asserted the opposite: that
 * Electron called a server RPC (SystemService/StartOAuthSignIn) while the web
 * used Supabase. That split was the bug — the RPC ran the CLI login flow
 * (open a browser, listen on 127.0.0.1) inside the hosted API pod, where there
 * is no browser and loopback is not the user's machine. Against prod it failed
 * closed with "RELIANT_AUTH_URL must be set".
 *
 * What these tests now pin is that BOTH surfaces call signInWithOAuth with the
 * same options, and differ only in `redirectTo` and in who opens the consent
 * page. If someone reintroduces a surface-specific sign-in branch, these fail.
 */

const signInWithOAuthMock = vi.fn()
const setSessionMock = vi.fn()
const getSessionMock = vi.fn(async () => ({ data: { session: null }, error: null }))
const onAuthStateChangeMock = vi.fn(() => ({
  data: { subscription: { unsubscribe: vi.fn() } },
}))
const startOAuthRedirectMock = vi.fn()
const windowOpenMock = vi.fn()

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: getSessionMock,
      onAuthStateChange: onAuthStateChangeMock,
      signInWithOAuth: signInWithOAuthMock,
      setSession: setSessionMock,
    },
  },
}))

vi.mock('@/api/grpc-unauth', () => ({
  devAuthGrpc: {
    load: vi.fn(async () => ({ success: false })),
    save: vi.fn(async () => ({ success: true })),
    clear: vi.fn(async () => ({ success: true })),
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), error: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

vi.mock('@/lib/constants', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/constants')>()),
  getIsDev: () => false,
}))

vi.mock('@/lib/sentry', () => ({ setSentryUser: vi.fn() }))

const LOOPBACK_URI = 'http://127.0.0.1:52111/auth/callback'

const asDesktop = () => {
  ;(window as Window & { electronAPI?: unknown }).electronAPI = {
    analyticsTrack: vi.fn(),
    startOAuthRedirect: startOAuthRedirectMock,
  }
}

beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
  delete (window as Window & { electronAPI?: unknown }).electronAPI
  startOAuthRedirectMock.mockResolvedValue({ redirectUri: LOOPBACK_URI })
  signInWithOAuthMock.mockResolvedValue({
    data: { url: 'https://accounts.google.com/o/oauth2/v2/auth?example=1' },
    error: null,
  })
  vi.stubGlobal('open', windowOpenMock)
})

describe.each(['google', 'github', 'apple'] as const)('%s sign-in', (provider) => {
  const call = async (provider: 'google' | 'github' | 'apple') => {
    const { useAuthStore } = await import('@/store/authStore')
    const store = useAuthStore.getState()
    if (provider === 'google') return store.signInWithGoogle()
    if (provider === 'github') return store.signInWithGithub()
    return store.signInWithApple()
  }

  it('uses Supabase signInWithOAuth on the web, redirecting to the hosted callback', async () => {
    await call(provider)

    expect(signInWithOAuthMock).toHaveBeenCalledWith({
      provider,
      options: {
        redirectTo: 'http://localhost:3000/auth/callback',
        skipBrowserRedirect: true,
      },
    })
  })

  it('uses the SAME Supabase call on desktop, with the loopback redirect URI', async () => {
    asDesktop()

    await call(provider)

    // The only difference between the two surfaces: where the provider is told
    // to send the user back. Same method, same options shape, same provider.
    expect(signInWithOAuthMock).toHaveBeenCalledWith({
      provider,
      options: {
        redirectTo: LOOPBACK_URI,
        skipBrowserRedirect: true,
      },
    })
  })

  it('never establishes a session itself — that belongs to /auth/callback', async () => {
    asDesktop()

    await call(provider)

    // The renderer holds the PKCE verifier and exchanges the code in
    // OAuthCallback.tsx, the same component the browser build uses. A
    // setSession here would mean tokens were minted somewhere else.
    expect(setSessionMock).not.toHaveBeenCalled()
  })

  it('opens consent in the system browser on desktop, not in the app window', async () => {
    asDesktop()

    await call(provider)

    // Google refuses OAuth in an embedded webview (disallowed_useragent), and
    // navigating the packaged window would be externalized by
    // electron/src/navigation-policy.js anyway, stranding the renderer.
    expect(windowOpenMock).toHaveBeenCalledWith(
      'https://accounts.google.com/o/oauth2/v2/auth?example=1',
      '_blank',
      'noopener,noreferrer',
    )
  })

  it('falls back to the hosted callback when the loopback listener fails', async () => {
    asDesktop()
    startOAuthRedirectMock.mockRejectedValue(new Error('EADDRINUSE'))

    await call(provider)

    // A listener that cannot start must not block sign-in: the HOSTED callback
    // still completes the flow, so this degrades rather than throwing.
    // getAppURL() resolves the hosted origin here rather than the document
    // origin, because a desktop renderer's own origin is unreachable from the
    // system browser that finishes the round trip.
    expect(signInWithOAuthMock).toHaveBeenCalledWith({
      provider,
      options: {
        redirectTo: 'https://app.reliantlabs.io/auth/callback',
        skipBrowserRedirect: true,
      },
    })
  })

  it('falls back to the hosted callback in builds with no desktop bridge', async () => {
    // Every build shipped before the loopback receiver existed: electronAPI is
    // present but startOAuthRedirect is undefined.
    ;(window as Window & { electronAPI?: unknown }).electronAPI = {
      analyticsTrack: vi.fn(),
    }

    await call(provider)

    expect(signInWithOAuthMock).toHaveBeenCalledWith({
      provider,
      options: {
        redirectTo: 'https://app.reliantlabs.io/auth/callback',
        skipBrowserRedirect: true,
      },
    })
  })
})
