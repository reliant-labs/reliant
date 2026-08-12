import { beforeEach, describe, expect, it, vi } from 'vitest'

const signInWithOAuthMock = vi.fn()
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
    startOAuthSignInMock.mockResolvedValue({
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

  it('uses the unauthenticated backend flow in Electron', async () => {
    ;(window as Window & { electronAPI?: unknown }).electronAPI = {
      analyticsTrack: vi.fn(),
    }

    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().signInWithGithub()

    // StartOAuthSignInRequest carries only the provider — the callback wait is
    // owned by the backend, so no timeout is sent over the wire.
    expect(startOAuthSignInMock).toHaveBeenCalledWith('github')
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
    expect(startOAuthSignInMock).not.toHaveBeenCalled()
    expect(setSessionMock).not.toHaveBeenCalled()
  })
})

describe('authStore Google OAuth runtime split', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    delete (window as Window & { electronAPI?: unknown }).electronAPI
    startOAuthSignInMock.mockResolvedValue({
      accessToken: 'google-access-token',
      refreshToken: 'google-refresh-token',
      userId: 'google-user-id',
      email: 'google@example.com',
    })
    setSessionMock.mockResolvedValue({
      data: {
        user: { id: 'google-user-id', email: 'google@example.com' },
        session: { access_token: 'google-access-token', refresh_token: 'google-refresh-token' },
      },
      error: null,
    })
    signInWithOAuthMock.mockResolvedValue({
      data: { url: 'https://accounts.google.com/o/oauth2/v2/auth?example=1' },
      error: null,
    })
  })

  it('uses the unauthenticated backend flow in Electron', async () => {
    ;(window as Window & { electronAPI?: unknown }).electronAPI = {
      analyticsTrack: vi.fn(),
    }

    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().signInWithGoogle()

    expect(startOAuthSignInMock).toHaveBeenCalledWith('google')
    expect(setSessionMock).toHaveBeenCalledWith({
      access_token: 'google-access-token',
      refresh_token: 'google-refresh-token',
    })
    expect(signInWithOAuthMock).not.toHaveBeenCalled()
  })

  it('keeps the web flow on Supabase redirect + callback exchange path', async () => {
    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().signInWithGoogle()

    expect(signInWithOAuthMock).toHaveBeenCalledWith({
      provider: 'google',
      options: {
        redirectTo: 'http://localhost:3000/auth/callback',
        skipBrowserRedirect: true,
      },
    })
    expect(startOAuthSignInMock).not.toHaveBeenCalled()
    expect(setSessionMock).not.toHaveBeenCalled()
  })
})

describe('authStore Apple OAuth runtime split', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    delete (window as Window & { electronAPI?: unknown }).electronAPI
    startOAuthSignInMock.mockResolvedValue({
      accessToken: 'apple-access-token',
      refreshToken: 'apple-refresh-token',
      userId: 'apple-user-id',
      email: 'apple@example.com',
    })
    setSessionMock.mockResolvedValue({
      data: {
        user: { id: 'apple-user-id', email: 'apple@example.com' },
        session: { access_token: 'apple-access-token', refresh_token: 'apple-refresh-token' },
      },
      error: null,
    })
    signInWithOAuthMock.mockResolvedValue({
      data: { url: 'https://appleid.apple.com/auth/authorize?example=1' },
      error: null,
    })
  })

  it('uses the unauthenticated backend flow in Electron', async () => {
    ;(window as Window & { electronAPI?: unknown }).electronAPI = {
      analyticsTrack: vi.fn(),
    }

    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().signInWithApple()

    expect(startOAuthSignInMock).toHaveBeenCalledWith('apple')
    expect(setSessionMock).toHaveBeenCalledWith({
      access_token: 'apple-access-token',
      refresh_token: 'apple-refresh-token',
    })
    expect(signInWithOAuthMock).not.toHaveBeenCalled()
  })

  it('keeps the web flow on Supabase redirect + callback exchange path', async () => {
    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().signInWithApple()

    expect(signInWithOAuthMock).toHaveBeenCalledWith({
      provider: 'apple',
      options: {
        redirectTo: 'http://localhost:3000/auth/callback',
        skipBrowserRedirect: true,
      },
    })
    expect(startOAuthSignInMock).not.toHaveBeenCalled()
    expect(setSessionMock).not.toHaveBeenCalled()
  })
})
