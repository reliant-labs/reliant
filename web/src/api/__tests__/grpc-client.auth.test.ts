import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  createConnectTransport: vi.fn((options) => options),
  getSession: vi.fn(),
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
    mocks.getSession.mockResolvedValue({ data: { session: null } })
    // jsdom in this vitest setup doesn't expose a real Storage on globalThis;
    // the auth interceptor reads localStorage first, so stub a minimal shim.
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => null),
      setItem: vi.fn(),
      removeItem: vi.fn(),
      clear: vi.fn(),
    })
  })

  it('forwards a Supabase bearer token when one exists', async () => {
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
      })
    )
  })

  it('warns and continues when no Supabase session exists', async () => {
    mocks.getSession.mockResolvedValue({ data: { session: null } })

    const { getTransport } = await import('../grpc-client')
    const transport = getTransport() as { interceptors: Array<(next: (req: unknown) => Promise<unknown>) => (req: unknown) => Promise<unknown>> }
    const authInterceptor = transport.interceptors[1]
    const req = buildRequest()
    const next = vi.fn(async (passedReq) => passedReq)

    await authInterceptor(next)(req)

    expect(req.header.get('Authorization')).toBeNull()
    expect(next).toHaveBeenCalledTimes(1)
    expect(mocks.logger.warn).toHaveBeenCalledWith(
      '[gRPC Client] No auth token available for request:',
      expect.objectContaining({
        method: 'SyncReliantProvider',
        hasSession: false,
      })
    )
  })
})
