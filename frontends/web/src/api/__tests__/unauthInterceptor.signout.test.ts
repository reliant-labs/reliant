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
  refresh: vi.fn(),
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
    refresh: mocks.refresh,
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
    // By default the session cannot be refreshed, so a rejected token signs
    // the user out — the behaviour the tests above this change pinned.
    mocks.refresh.mockResolvedValue(null)
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

/**
 * An expired token, seen by every request in flight at once.
 *
 * THE BUG THIS CLOSES (gtm/reviews/product-ux-findings.md A-6): one expired
 * token produced ~75 failures EACH of ListModels, ListSettings, ListProjects,
 * DeleteSetting and GetPrivacySettings, and `SettingsSync failed after
 * retries` x76, instead of one re-login. Every 401 was handled on its own: an
 * expired token was never refreshed, and the guard against concurrent
 * sign-outs was checked before an await and set after it, so every 401 in a
 * burst signed out again.
 */
describe('unauthInterceptor — one recovery per rejected token', () => {
  const SERVICES = ['ListModels', 'ListSettings', 'ListProjects', 'DeleteSetting', 'GetPrivacySettings']

  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    mocks.hasSession.mockResolvedValue(true)
    mocks.getToken.mockResolvedValue(null)
    vi.stubGlobal('location', { pathname: '/', href: '/' })
    vi.stubGlobal('window', { location: globalThis.location, setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms) })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function burstRequest(method: string) {
    return { ...buildRequest({ Authorization: 'Bearer expired-token' }), method: { name: method } }
  }

  /** A backend that rejects the expired token and accepts the fresh one. */
  function backend() {
    return vi.fn(async (passed: { header: Headers; method: { name: string } }) => {
      if (passed.header.get('Authorization') === 'Bearer expired-token') {
        throw new ConnectError(REJECTED, Code.Unauthenticated)
      }
      return { ok: true, method: passed.method.name }
    })
  }

  it('refreshes the session ONCE and retries every request in the burst with the new token', async () => {
    let resolveRefresh: (token: string) => void = () => {}
    mocks.refresh.mockImplementation(() => new Promise<string>((resolve) => { resolveRefresh = resolve }))
    const interceptor = await loadUnauthInterceptor()
    const next = backend()

    const burst = SERVICES.map((m) => interceptor(next)(burstRequest(m)))
    // Let every 401 land before the refresh answers — the worst case.
    for (let i = 0; i < 20; i += 1) await Promise.resolve()
    resolveRefresh('fresh-token')
    const results = await Promise.all(burst)

    expect(mocks.refresh).toHaveBeenCalledTimes(1)
    expect(mocks.signOut).not.toHaveBeenCalled()
    expect(results.map((r) => (r as { ok: boolean }).ok)).toEqual(SERVICES.map(() => true))
  })

  it('signs out exactly ONCE when the session cannot be refreshed', async () => {
    mocks.refresh.mockResolvedValue(null)
    const interceptor = await loadUnauthInterceptor()
    const next = backend()

    const outcomes = await Promise.allSettled(SERVICES.map((m) => interceptor(next)(burstRequest(m))))

    expect(outcomes.every((o) => o.status === 'rejected')).toBe(true)
    // One decision to sign out for the whole burst. The old guard was checked
    // before an await and set after it, so all five 401s decided separately.
    const signOutDecisions = mocks.logger.warn.mock.calls.filter(([msg]) => String(msg).includes('signing out'))
    expect(signOutDecisions).toHaveLength(1)
    expect(mocks.signOut).toHaveBeenCalledTimes(1)
    expect(mocks.refresh).toHaveBeenCalledTimes(1)
    // Each request was sent once: nothing retried against a dead session.
    expect(next).toHaveBeenCalledTimes(SERVICES.length)
  })

  it('a 401 that lands just after recovery finished joins it instead of starting another', async () => {
    mocks.refresh.mockResolvedValue(null)
    const interceptor = await loadUnauthInterceptor()
    const next = backend()

    await expect(interceptor(next)(burstRequest('ListSettings'))).rejects.toThrow()
    // A request that was already on the wire with the same expired token.
    await expect(interceptor(next)(burstRequest('ListProjects'))).rejects.toThrow()

    expect(mocks.refresh).toHaveBeenCalledTimes(1)
    expect(mocks.signOut).toHaveBeenCalledTimes(1)
  })

  it('signs out rather than loops when the refresh hands back the rejected token', async () => {
    mocks.refresh.mockResolvedValue('expired-token')
    const interceptor = await loadUnauthInterceptor()
    const next = backend()

    await expect(interceptor(next)(burstRequest('ListSettings'))).rejects.toThrow()

    expect(next).toHaveBeenCalledTimes(1)
    expect(mocks.signOut).toHaveBeenCalledTimes(1)
  })
})
