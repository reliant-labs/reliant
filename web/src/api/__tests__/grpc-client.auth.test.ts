import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  createConnectTransport: vi.fn((options) => options),
  getSession: vi.fn(),
  getIsDev: vi.fn(),
  logger: {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  },
}))

vi.mock('@connectrpc/connect-web', () => ({
  createConnectTransport: mocks.createConnectTransport,
}))

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: mocks.getSession,
    },
  },
}))

vi.mock('@/lib/constants', () => ({
  getIsDev: mocks.getIsDev,
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

vi.mock('@/lib/logger', () => ({
  logger: mocks.logger,
}))

function buildRequest() {
  return {
    method: { name: 'SyncReliantProvider' },
    service: { typeName: 'reliant.v1.SettingsService' },
    header: new Headers(),
    signal: new AbortController().signal,
  }
}

describe('grpc-client auth interceptor', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    mocks.createConnectTransport.mockImplementation((options) => options)
    mocks.getIsDev.mockReturnValue(false)
    mocks.getSession.mockResolvedValue({ data: { session: null } })
  })

  it('forwards a Supabase bearer token in dev when one exists', async () => {
    mocks.getIsDev.mockReturnValue(true)
    mocks.getSession.mockResolvedValue({
      data: { session: { access_token: 'token-123' } },
    })

    const { getTransport } = await import('../grpc-client')
    const transport = getTransport() as { interceptors: Array<(next: (req: unknown) => Promise<unknown>) => (req: unknown) => Promise<unknown>> }
    const authInterceptor = transport.interceptors[1]
    const req = buildRequest()
    const next = vi.fn(async (passedReq) => passedReq)

    await authInterceptor(next)(req)

    expect(req.header.get('Authorization')).toBe('Bearer token-123')
    expect(next).toHaveBeenCalledTimes(1)
    expect(mocks.logger.info).toHaveBeenCalledWith(
      '[gRPC Client] Auth token set for request:',
      expect.objectContaining({
        method: 'SyncReliantProvider',
        tokenLength: 9,
        isDev: true,
      })
    )
  })

  it('preserves dev fallback when no Supabase session exists', async () => {
    mocks.getIsDev.mockReturnValue(true)
    mocks.getSession.mockResolvedValue({ data: { session: null } })

    const { getTransport } = await import('../grpc-client')
    const transport = getTransport() as { interceptors: Array<(next: (req: unknown) => Promise<unknown>) => (req: unknown) => Promise<unknown>> }
    const authInterceptor = transport.interceptors[1]
    const req = buildRequest()
    const next = vi.fn(async (passedReq) => passedReq)

    await authInterceptor(next)(req)

    expect(req.header.get('Authorization')).toBeNull()
    expect(next).toHaveBeenCalledTimes(1)
    expect(mocks.logger.info).toHaveBeenCalledWith(
      '[gRPC Client] Dev mode - no auth token available, relying on backend DevUser:',
      expect.objectContaining({
        method: 'SyncReliantProvider',
        hasSession: false,
      })
    )
  })
})
