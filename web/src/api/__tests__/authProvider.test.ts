import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  getSession: vi.fn(),
  logger: {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  },
}))

vi.mock('@/lib/supabase', () => ({
  supabase: { auth: { getSession: mocks.getSession } },
}))

vi.mock('@/lib/logger', () => ({ logger: mocks.logger }))

/** localStorage stub — jsdom in this setup doesn't expose a real Storage. */
function stubStorage(value: string | null, opts: { throws?: boolean } = {}) {
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() => {
      if (opts.throws) throw new Error('SecurityError: localStorage disabled')
      return value
    }),
    setItem: vi.fn(),
    removeItem: vi.fn(),
    clear: vi.fn(),
  })
}

describe('authProvider', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    mocks.getSession.mockResolvedValue({ data: { session: null } })
    stubStorage(null)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  describe('browserAuthTokenProvider', () => {
    it('prefers the API key over the Supabase session', async () => {
      stubStorage('api-key-abc')
      mocks.getSession.mockResolvedValue({
        data: { session: { access_token: 'supabase-token' } },
      })

      const { browserAuthTokenProvider } = await import('../authProvider')

      expect(await browserAuthTokenProvider.getToken()).toBe('api-key-abc')
      // The API-key path must short-circuit — hitting Supabase on every RPC
      // when a long-lived key is present is wasted work.
      expect(mocks.getSession).not.toHaveBeenCalled()
    })

    it('falls back to the Supabase access token when no API key is stored', async () => {
      mocks.getSession.mockResolvedValue({
        data: { session: { access_token: 'supabase-token' } },
      })

      const { browserAuthTokenProvider } = await import('../authProvider')

      expect(await browserAuthTokenProvider.getToken()).toBe('supabase-token')
    })

    it('returns null (rather than throwing) when there is no session', async () => {
      const { browserAuthTokenProvider } = await import('../authProvider')

      expect(await browserAuthTokenProvider.getToken()).toBeNull()
    })

    it('survives a throwing localStorage (Safari private browsing)', async () => {
      stubStorage(null, { throws: true })
      mocks.getSession.mockResolvedValue({
        data: { session: { access_token: 'supabase-token' } },
      })

      const { browserAuthTokenProvider } = await import('../authProvider')

      // Safari throws on localStorage access in Private Browsing instead of
      // returning null. That must degrade to the Supabase path, not break auth.
      expect(await browserAuthTokenProvider.getToken()).toBe('supabase-token')
    })

    it('reports no session when neither source has credentials', async () => {
      const { browserAuthTokenProvider } = await import('../authProvider')

      expect(await browserAuthTokenProvider.hasSession()).toBe(false)
    })

    it('reports a session for an API key alone', async () => {
      stubStorage('api-key-abc')

      const { browserAuthTokenProvider } = await import('../authProvider')

      expect(await browserAuthTokenProvider.hasSession()).toBe(true)
      expect(mocks.getSession).not.toHaveBeenCalled()
    })

    it('swallows Supabase errors in hasSession so a 401 does not sign out blindly', async () => {
      mocks.getSession.mockRejectedValue(new Error('network down'))

      const { browserAuthTokenProvider } = await import('../authProvider')

      expect(await browserAuthTokenProvider.hasSession()).toBe(false)
    })
  })

  describe('setAuthTokenProvider', () => {
    it('routes token lookups through an injected provider', async () => {
      // This is the seam that lets a native shell read from Keychain and an
      // embedding host supply its own token — the whole point of the module.
      const { setAuthTokenProvider, getAuthTokenProvider } = await import(
        '../authProvider'
      )

      const custom = {
        getToken: vi.fn(async () => 'keychain-token'),
        hasSession: vi.fn(async () => true),
      }
      setAuthTokenProvider(custom)

      expect(await getAuthTokenProvider().getToken()).toBe('keychain-token')
      expect(custom.getToken).toHaveBeenCalledTimes(1)
      // An injected provider must fully replace the default, not layer on it.
      expect(mocks.getSession).not.toHaveBeenCalled()
    })

    it('restores the browser default when passed null', async () => {
      const { setAuthTokenProvider, getAuthTokenProvider, browserAuthTokenProvider } =
        await import('../authProvider')

      setAuthTokenProvider({
        getToken: async () => 'keychain-token',
        hasSession: async () => true,
      })
      setAuthTokenProvider(null)

      expect(getAuthTokenProvider()).toBe(browserAuthTokenProvider)
    })
  })
})
