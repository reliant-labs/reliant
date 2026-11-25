/**
 * forge sign-in: a 401 on a request that carried the session cookie (and no
 * Authorization header, by design) is judged by the login broker. The store
 * re-reads /auth/session: still signed in → the user stays (the RPC refused on
 * its own, or a server that does not read the cookie yet answered); signed out
 * → the page follows the ended session to the sign-in screen. Never a retry
 * with a bearer, and never a sign-out the broker did not confirm.
 */
import { beforeAll, beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { ConnectError, Code } from '@connectrpc/connect'

const mocks = vi.hoisted(() => ({
  state: { identity: null as null | { id: string }, afterRefresh: null as null | { id: string } },
  refreshSession: vi.fn(),
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}))

vi.mock('@/lib/logger', () => ({ logger: mocks.logger }))
vi.mock('@/lib/auth/forge-api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/auth/forge-api')>()),
  isForgeAuth: () => true,
}))
vi.mock('@/store/authStore', () => ({
  useAuthStore: {
    getState: () => ({
      identity: mocks.state.identity,
      refreshSession: async () => {
        mocks.refreshSession()
        mocks.state.identity = mocks.state.afterRefresh
      },
    }),
  },
}))

type LooseInterceptor = (next: (req: any) => Promise<unknown>) => (req: unknown) => Promise<unknown>

async function loadUnauthInterceptor(): Promise<LooseInterceptor> {
  const { buildInterceptors, resetAuthRecoveryForTests } = await import('../transport')
  resetAuthRecoveryForTests()
  const chain = buildInterceptors({ withAuth: true })
  return chain[chain.length - 3] as unknown as LooseInterceptor
}

function buildRequest() {
  return {
    stream: false as const,
    method: { name: 'ListChats' },
    service: { typeName: 'reliant.v1.ChatService' },
    header: new Headers(),
    signal: new AbortController().signal,
  }
}

const rejected = () => {
  throw new ConnectError('[unauthenticated] missing authorization token', Code.Unauthenticated)
}

beforeAll(async () => {
  await import('../transport')
})

describe('unauthInterceptor with the forge session cookie', () => {
  let location: { pathname: string; href: string }
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    location = { pathname: '/chats', href: '/chats' }
    vi.stubGlobal('location', location)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('stays signed in when the broker still has the session', async () => {
    mocks.state.identity = { id: 'alice' }
    mocks.state.afterRefresh = { id: 'alice' }
    const interceptor = await loadUnauthInterceptor()
    const next = vi.fn(async () => rejected())
    await expect(interceptor(next)(buildRequest())).rejects.toThrow()
    expect(mocks.refreshSession).toHaveBeenCalledTimes(1)
    expect(next).toHaveBeenCalledTimes(1)
    expect(location.href).toBe('/chats')
  })

  it('follows an ended session to the sign-in screen', async () => {
    mocks.state.identity = { id: 'alice' }
    mocks.state.afterRefresh = null
    const interceptor = await loadUnauthInterceptor()
    await expect(interceptor(vi.fn(async () => rejected()))(buildRequest())).rejects.toThrow()
    expect(location.href).toMatch(/^\/auth/)
  })

  it('asks the broker once for a burst of 401s', async () => {
    mocks.state.identity = { id: 'alice' }
    mocks.state.afterRefresh = { id: 'alice' }
    const interceptor = await loadUnauthInterceptor()
    const next = vi.fn(async () => rejected())
    await Promise.allSettled([interceptor(next)(buildRequest()), interceptor(next)(buildRequest())])
    expect(mocks.refreshSession).toHaveBeenCalledTimes(1)
  })

  it('does nothing for a signed-out page', async () => {
    mocks.state.identity = null
    mocks.state.afterRefresh = null
    const interceptor = await loadUnauthInterceptor()
    await expect(interceptor(vi.fn(async () => rejected()))(buildRequest())).rejects.toThrow()
    expect(location.href).toBe('/chats')
  })
})
