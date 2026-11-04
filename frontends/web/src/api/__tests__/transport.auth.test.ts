import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Interceptor } from '@connectrpc/connect'

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

type Next = Parameters<Interceptor>[0]

/**
 * Run one request through the authed reliant-api chain — the one
 * src/lib/connect.ts gives every reliant.v1 route — and return the headers
 * that reached the wire.
 */
async function headersOnTheWire(): Promise<Headers> {
  const { buildInterceptors } = await import('../transport')
  let sent: Headers | undefined
  const wire = (async (req: { header: Headers }) => {
    sent = new Headers(req.header)
    return { stream: false, header: new Headers(), trailer: new Headers(), message: {} }
  }) as unknown as Next
  const run = buildInterceptors({ withAuth: true }).reduceRight<Next>(
    (next, interceptor) => interceptor(next),
    wire,
  )
  await run({
    stream: false,
    method: { name: 'GetProviderStatuses', input: { fields: [] } },
    service: { typeName: 'reliant.v1.SettingsService' },
    header: new Headers(),
    signal: new AbortController().signal,
    message: {},
  } as unknown as Parameters<Next>[0])
  if (!sent) throw new Error('the chain never reached the wire')
  return sent
}

describe('the reliant-api chain\'s auth stages', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
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

    const sent = await headersOnTheWire()

    expect(sent.get('Authorization')).toBe('Bearer token-123')
    // The happy path logs nothing: a per-RPC line here was the single largest
    // producer of dev log volume. Attach-failure is still logged (below), and
    // a 401 records whether a token was attached (unauthInterceptor).
    expect(mocks.logger.info).not.toHaveBeenCalledWith(
      expect.stringContaining('Auth token set'),
      expect.anything()
    )
  })

  it('warns and continues when no Supabase session exists', async () => {
    mocks.getSession.mockResolvedValue({ data: { session: null } })

    const sent = await headersOnTheWire()

    expect(sent.get('Authorization')).toBeNull()
    // The interceptor now resolves a token through the pluggable
    // AuthTokenProvider (see api/authProvider.ts) rather than reading the
    // Supabase session inline, so it can no longer distinguish "no session"
    // from "session without a token" — it only knows there is no token. The
    // contract this test protects is unchanged: no Authorization header, the
    // request still proceeds, and the miss is logged.
    expect(mocks.logger.warn).toHaveBeenCalledWith(
      '[gRPC Client] No auth token available for request:',
      expect.objectContaining({
        method: 'GetProviderStatuses',
      })
    )
  })
})
