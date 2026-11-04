/**
 * adoptSession installs the session the control plane released to this browser
 * (the /claim page). It must go through supabase.auth.setSession, not the
 * store's own setSession: only supabase persists and refreshes it, and every
 * RPC after the install reads the token from there.
 */
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

const setSessionMock = vi.fn()
const loggerError = vi.fn()

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: vi.fn(async () => ({ data: { session: null }, error: null })),
      onAuthStateChange: vi.fn(() => ({ data: { subscription: { unsubscribe: vi.fn() } } })),
      setSession: setSessionMock,
    },
  },
  setAuthStorageUnreadableHandler: vi.fn(),
}))

vi.mock('@/api/grpc-unauth', () => ({
  devAuthGrpc: { load: vi.fn(async () => ({ success: false })), save: vi.fn(), clear: vi.fn() },
}))
vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), error: loggerError, warn: vi.fn(), debug: vi.fn() },
}))
vi.mock('@/lib/telemetry', () => ({ setTelemetryUserId: vi.fn() }))

const loadStore = async () => (await import('@/store/authStore')).useAuthStore

beforeAll(async () => {
  await import('@/store/authStore')
}, 60_000)

beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
})

const TOKENS = { accessToken: 'access.jwt.value', refreshToken: 'refresh-value' }

describe('adoptSession', () => {
  it('installs the released tokens through supabase and puts the user in the store', async () => {
    const user = { id: 'anon-1', is_anonymous: true }
    const session = { access_token: TOKENS.accessToken, user }
    setSessionMock.mockResolvedValue({ data: { user, session }, error: null })
    const store = await loadStore()

    const adopted = await store.getState().adoptSession(TOKENS)

    expect(setSessionMock).toHaveBeenCalledWith({ access_token: TOKENS.accessToken, refresh_token: TOKENS.refreshToken })
    expect(adopted).toBe(user)
    expect(store.getState().user).toBe(user)
    expect(store.getState().session).toBe(session)
  })

  it('throws what supabase refused, leaves the store alone, and never logs a token', async () => {
    const refusal = Object.assign(new Error('Invalid Refresh Token'), { code: 'refresh_token_not_found' })
    setSessionMock.mockResolvedValue({ data: { user: null, session: null }, error: refusal })
    const store = await loadStore()

    await expect(store.getState().adoptSession(TOKENS)).rejects.toBe(refusal)

    expect(store.getState().user).toBeNull()
    const logged = JSON.stringify(loggerError.mock.calls)
    expect(logged).not.toContain(TOKENS.accessToken)
    expect(logged).not.toContain(TOKENS.refreshToken)
  })

  it('treats a response with no session as a failure', async () => {
    setSessionMock.mockResolvedValue({ data: { user: null, session: null }, error: null })
    const store = await loadStore()
    await expect(store.getState().adoptSession(TOKENS)).rejects.toThrow(/could not be installed/)
  })
})
