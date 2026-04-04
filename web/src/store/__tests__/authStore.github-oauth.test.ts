import { beforeEach, describe, expect, it, vi } from 'vitest'

const signInWithOAuthMock = vi.fn()
const setSessionMock = vi.fn()
const getSessionMock = vi.fn(async () => ({ data: { session: null }, error: null }))
const onAuthStateChangeMock = vi.fn(() => ({
  data: { subscription: { unsubscribe: vi.fn() } },
}))
const startGitHubOAuthSignInMock = vi.fn()

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
    startGitHubOAuthSignIn: startGitHubOAuthSignInMock,
    load: vi.fn(async () => ({ success: false })),
    save: vi.fn(async () => ({ success: true })),
    clear: vi.fn(async () => ({ success: true })),
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: {
    info: vi.fn(),
    error: vi.fn(),
    warn: vi.fn(),
    debug: vi.fn(),
  },
}))

vi.mock('@/lib/constants', () => ({
  getIsDev: () => false,
}))

vi.mock('@/lib/sentry', () => ({
  setSentryUser: vi.fn(),
}))

describe('authStore GitHub OAuth runtime split', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    delete (window as Window & { electronAPI?: unknown }).electronAPI
    startGitHubOAuthSignInMock.mockResolvedValue({
      accessToken: 'electron-access-token',
      refreshToken: 'electron-refresh-token',
      userId: 'electron-user-id',
      email: 'electron@example.com',
    })
    setSessionMock.mockResolvedValue({
      data: {
        user: { id: 'electron-user-id', email: 'electron@example.com' },
        session: { access_token: 'electron-access-token', refresh_token: 'electron-refresh-token' },
      },
      error: null,
    })
    signInWithOAuthMock.mockResolvedValue({
      data: { url: 'https://github.com/login/oauth/authorize?example=1' },
      error: null,
    })
  })

  it('uses the unauthenticated backend GitHub flow in Electron', async () => {
    ;(window as Window & { electronAPI?: unknown }).electronAPI = {
      analyticsTrack: vi.fn(),
    }

    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().signInWithGithub()

    expect(startGitHubOAuthSignInMock).toHaveBeenCalledWith(120)
    expect(setSessionMock).toHaveBeenCalledWith({
      access_token: 'electron-access-token',
      refresh_token: 'electron-refresh-token',
    })
    expect(signInWithOAuthMock).not.toHaveBeenCalled()
  })

  it('keeps the web flow on Supabase redirect + callback exchange path', async () => {
    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().signInWithGithub()

    expect(signInWithOAuthMock).toHaveBeenCalledWith({
      provider: 'github',
      options: {
        redirectTo: 'http://localhost:3000/auth/callback',
        skipBrowserRedirect: true,
      },
    })
    expect(startGitHubOAuthSignInMock).not.toHaveBeenCalled()
    expect(setSessionMock).not.toHaveBeenCalled()
  })
})