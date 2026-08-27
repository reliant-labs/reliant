/**
 * 401 → sign-out discrimination.
 *
 * THE BUG THIS CLOSES (2026-08-26, packaged desktop): a user stepping through
 * the onboarding tour was dropped to the login screen mid-tour. The log line
 * was
 *
 *   401 with active session — token rejected by backend; signing out
 *     { method: 'GetDefaultPreset',
 *       message: '[unauthenticated] missing authorization token' }
 *
 * "missing" is not "invalid". The server's auth interceptor
 * (internal/grpc/interceptors/auth.go:182) emits exactly that string ONLY on
 * the empty-Authorization-header branch; a token that is expired or malformed
 * takes a different branch and reports "invalid or expired token". So the
 * request that triggered the sign-out carried no credential at all, and the
 * handler destroyed a session that the backend had never rejected.
 *
 * That tokenless window is real and transient. Measured in the same log at
 * 16:10:45–52: three ListDaemons went out with no token while the session was
 * live and 25 seconds BEFORE any sign-out — `getToken()` resolved null while
 * `hasSession()` still answered true.
 *
 * The contract pinned here has two halves, and both matter:
 *   - a 401 on a request that presented NO token must not sign out;
 *   - a 401 on a request that DID present one still must.
 */
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { ConnectError, Code } from '@connectrpc/connect'

const mocks = vi.hoisted(() => ({
  getToken: vi.fn(),
  hasSession: vi.fn(),
  signOut: vi.fn(async () => undefined),
  logger: {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  },
}))

vi.mock('@/lib/logger', () => ({ logger: mocks.logger }))

vi.mock('@/api/authProvider', () => ({
  getAuthTokenProvider: () => ({
    getToken: mocks.getToken,
    hasSession: mocks.hasSession,
  }),
}))

vi.mock('@/store/authStore', () => ({
  useAuthStore: { getState: () => ({ signOut: mocks.signOut }) },
}))

vi.mock('@/lib/constants', () => ({
  DEFAULT_GRPC_TIMEOUT_MS: 10000,
  FILE_OPERATION_TIMEOUT_MS: 30000,
  CHAT_OPERATION_TIMEOUT_MS: 30000,
  MCP_OPERATION_TIMEOUT_MS: 60000,
  UPLOAD_TIMEOUT_MS: 60000,
  WORKTREE_OPERATION_TIMEOUT_MS: 30000,
  OAUTH_TIMEOUT_MS: 0,
  OAUTH_EXCHANGE_TIMEOUT_MS: 60000,
  PROVIDER_VALIDATION_TIMEOUT_MS: 60000,
}))

// The exact wire message the Go auth interceptor returns for an absent header.
const MISSING = '[unauthenticated] missing authorization token'
// ...and the one it returns for a credential it actually looked at.
const REJECTED = '[unauthenticated] invalid or expired token'

function buildRequest(headers: Record<string, string> = {}) {
  return {
    stream: false as const,
    method: { name: 'GetDefaultPreset' },
    service: { typeName: 'reliant.v1.PresetService' },
    header: new Headers(headers),
    signal: new AbortController().signal,
  }
}

/** The 401-signout interceptor is the last stage of the authed chain. */
async function loadUnauthInterceptor() {
  const { buildInterceptors } = await import('../transport')
  const chain = buildInterceptors({ withAuth: true })
  return chain[chain.length - 1]
}

describe('unauthInterceptor — 401 sign-out discrimination', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    // A believed-active session: this is the state in which the old handler
    // signed the user out regardless of what was actually sent.
    mocks.hasSession.mockResolvedValue(true)
    mocks.getToken.mockResolvedValue(null)
    vi.stubGlobal('location', {
      pathname: '/',
      href: '/',
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('does NOT sign out on a 401 for a request that carried no token', async () => {
    const interceptor = await loadUnauthInterceptor()
    const req = buildRequest() // no Authorization header — the reported bug
    const next = vi.fn(async () => {
      throw new ConnectError(MISSING, Code.Unauthenticated)
    })

    await expect(interceptor(next)(req)).rejects.toThrow()

    expect(mocks.signOut).not.toHaveBeenCalled()
  })

  it('DOES sign out on a 401 for a request that presented a token', async () => {
    const interceptor = await loadUnauthInterceptor()
    const req = buildRequest({ Authorization: 'Bearer real-token' })
    const next = vi.fn(async () => {
      throw new ConnectError(REJECTED, Code.Unauthenticated)
    })

    await expect(interceptor(next)(req)).rejects.toThrow()

    expect(mocks.signOut).toHaveBeenCalledTimes(1)
  })

  it('retries a tokenless request once when the provider resolves a token', async () => {
    // This is the transient race from the log: the attach missed, but by the
    // time the 401 comes back the provider can answer.
    mocks.getToken.mockResolvedValue('recovered-token')
    const interceptor = await loadUnauthInterceptor()
    const req = buildRequest()

    const next = vi
      .fn()
      .mockImplementationOnce(async () => {
        throw new ConnectError(MISSING, Code.Unauthenticated)
      })
      .mockImplementationOnce(async (passed: { header: Headers }) => ({
        ok: true,
        sent: passed.header.get('Authorization'),
      }))

    const result = await interceptor(next)(req)

    expect(next).toHaveBeenCalledTimes(2)
    expect((result as { sent: string }).sent).toBe('Bearer recovered-token')
    expect(mocks.signOut).not.toHaveBeenCalled()
  })

  it('signs out when the RETRY presents a token and is still rejected', async () => {
    // A genuinely dead credential must not be rescued by the retry path.
    mocks.getToken.mockResolvedValue('stale-token')
    const interceptor = await loadUnauthInterceptor()
    const req = buildRequest()

    const next = vi.fn(async () => {
      throw new ConnectError(MISSING, Code.Unauthenticated)
    })

    await expect(interceptor(next)(req)).rejects.toThrow()

    expect(next).toHaveBeenCalledTimes(2)
    expect(mocks.signOut).toHaveBeenCalledTimes(1)
  })

  it('does not sign out when there is no session at all', async () => {
    // Pre-existing guard: no session means nothing to tear down, and signing
    // out here would loop the sign-in screen against itself.
    mocks.hasSession.mockResolvedValue(false)
    const interceptor = await loadUnauthInterceptor()
    const req = buildRequest({ Authorization: 'Bearer whatever' })
    const next = vi.fn(async () => {
      throw new ConnectError(REJECTED, Code.Unauthenticated)
    })

    await expect(interceptor(next)(req)).rejects.toThrow()

    expect(mocks.signOut).not.toHaveBeenCalled()
  })

  it('records whether a token was attached at warn level so packaged builds can diagnose this', async () => {
    // "Auth token set for request" is info-level, and createLogFunction
    // no-ops info in packaged builds — so the one fact needed to tell an
    // attach-miss from a rejection was invisible in the very logs where the
    // incident was reported.
    const interceptor = await loadUnauthInterceptor()
    const req = buildRequest()
    const next = vi.fn(async () => {
      throw new ConnectError(MISSING, Code.Unauthenticated)
    })

    await expect(interceptor(next)(req)).rejects.toThrow()

    expect(mocks.logger.warn).toHaveBeenCalledWith(
      '[gRPC Client] 401 received',
      expect.objectContaining({
        method: 'GetDefaultPreset',
        tokenAttached: false,
      }),
    )
  })

  it('passes non-401 errors straight through', async () => {
    const interceptor = await loadUnauthInterceptor()
    const req = buildRequest({ Authorization: 'Bearer real-token' })
    const next = vi.fn(async () => {
      throw new ConnectError('boom', Code.Internal)
    })

    await expect(interceptor(next)(req)).rejects.toThrow('boom')

    expect(next).toHaveBeenCalledTimes(1)
    expect(mocks.signOut).not.toHaveBeenCalled()
  })
})
