import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createContextValues, type Interceptor } from '@connectrpc/connect'

// The request-sent stage: a caller learns when its request is handed to the
// network, after every other stage (the token lookup above all) has run.

const mocks = vi.hoisted(() => ({
  getSession: vi.fn(),
}))

vi.mock('@/lib/supabase', () => ({
  supabase: { auth: { getSession: mocks.getSession } },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}))

type Next = Parameters<Interceptor>[0]
type Req = Parameters<Next>[0]

function request(contextValues = createContextValues()): Req {
  return {
    stream: false,
    method: { name: 'GetProviderStatuses', input: { fields: [] } },
    service: { typeName: 'reliant.v1.SettingsService' },
    header: new Headers(),
    signal: new AbortController().signal,
    message: {},
    contextValues,
  } as unknown as Req
}

describe('the request-sent stage', () => {
  beforeEach(() => {
    vi.resetModules()
    mocks.getSession.mockResolvedValue({ data: { session: { access_token: 'tok' } } })
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => null),
      setItem: vi.fn(),
      removeItem: vi.fn(),
      clear: vi.fn(),
    })
  })

  it('tells the caller once its request reaches the wire, with the token already attached', async () => {
    const { buildInterceptors, withRequestSent } = await import('../transport')
    const events: string[] = []
    const wire = (async (req: Req) => {
      events.push(`wire auth=${req.header.get('Authorization') ?? 'none'}`)
      return { stream: false, header: new Headers(), trailer: new Headers(), message: {} }
    }) as unknown as Next
    const run = buildInterceptors({ withAuth: true }).reduceRight<Next>(
      (next, interceptor) => interceptor(next),
      wire,
    )

    await run(request(withRequestSent(undefined, () => events.push('sent'))))

    expect(events).toEqual(['sent', 'wire auth=Bearer tok'])
  })

  it('runs no callback for a request that attached none, and a throwing one never fails the request', async () => {
    const { buildInterceptors, withRequestSent } = await import('../transport')
    const wire = (async () => ({ stream: false, header: new Headers(), trailer: new Headers(), message: {} })) as unknown as Next
    const run = buildInterceptors({ withAuth: true }).reduceRight<Next>(
      (next, interceptor) => interceptor(next),
      wire,
    )

    await expect(run(request())).resolves.toBeDefined()
    await expect(
      run(request(withRequestSent(undefined, () => { throw new Error('observer bug') }))),
    ).resolves.toBeDefined()
  })
})
