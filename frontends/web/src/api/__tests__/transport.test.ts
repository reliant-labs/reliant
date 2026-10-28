/**
 * Smoke test for the single-chain interceptor factory.
 *
 * We don't re-verify the behaviour of each individual interceptor here —
 * `transport.auth.test.ts` covers auth-token shaping, and the
 * timeout/tracing/upgrade interceptors are individually tested elsewhere.
 * What this file pins down is the chain's SHAPE: which stages each transport
 * option produces, and that the options only ever remove stages. Which
 * transport each service gets — and that every route's transport is built
 * from this factory — is `connect.routing.test.ts`.
 */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Interceptor } from '@connectrpc/connect'
import type { TransportBackend } from '../transport'

// Stub the lib/constants module so importing transport.ts in the test env
// doesn't pull in production timeout/feature flags via the long chain.
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
  logger: {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  },
}))

vi.mock('@/services/controlPlane/config', () => ({
  CONTROL_PLANE_API_URL: 'https://example.test',
}))

// Stub localStorage for transport.ts (which reads it inside the auth
// interceptor) and the auth-store dynamic import path (so the unauth
// 401-fallback never reaches it during the smoke test).
// Each test imports a FRESH transport (vi.resetModules), but the first import
// in a worker also transforms its whole module graph: ~4s alone, past the
// test timeout under a loaded full run. Transform it once here, on the
// hook's own budget, so every test's import is only an evaluation.
beforeAll(async () => {
  await import('../transport')
}, 60_000)

beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() => null),
    setItem: vi.fn(),
    removeItem: vi.fn(),
    clear: vi.fn(),
  })
})

describe('buildInterceptors factory', () => {
  it('produces an 11-stage authed chain in the documented order', async () => {
    const { buildInterceptors, traceStage } = await import('../transport')
    const chain = buildInterceptors({ withAuth: true })
    // error-normalize → timeout → auth → missing-token → org → daemon-last-seen →
    // trace → error-log → upgrade → machine-wake → unauth
    expect(chain).toHaveLength(11)
    // The trace stage (the forge runtime's traceInterceptor) is at its documented position.
    expect(chain[6]).toBe(traceStage)
    // Every entry must be a function (Connect Interceptor shape).
    for (const entry of chain) {
      expect(typeof entry).toBe('function')
    }
  })

  it('produces a 7-stage unauthed chain that omits auth, missing-token + 401-signout', async () => {
    const { buildInterceptors } = await import('../transport')
    const authed = buildInterceptors({ withAuth: true })
    const unauthed = buildInterceptors({ withAuth: false })
    // 11 - 4 (auth, missing-token, org, unauth) = 7
    expect(unauthed).toHaveLength(7)
    // The unauthed chain is a subset of the authed chain — every entry in
    // unauthed must also appear in authed at the same relative ordering. If
    // someone reorders authentication into the unauthed path the lengths
    // diverge AND this membership check fails.
    for (const entry of unauthed) {
      expect(authed).toContain(entry)
    }
  })

  it('defaults withAuth to true when no option object is passed', async () => {
    const { buildInterceptors } = await import('../transport')
    expect(buildInterceptors()).toHaveLength(11)
    expect(buildInterceptors({})).toHaveLength(11)
  })

  it('swaps exactly one stage (daemon-last-seen for forge-via) in the control-plane chain', async () => {
    const { buildInterceptors } = await import('../transport')
    const reliantApi = buildInterceptors({ withAuth: true })
    const controlPlane = buildInterceptors({ withAuth: true, backend: 'control-plane' })
    expect(controlPlane).toHaveLength(reliantApi.length)
    expect(controlPlane.filter((entry) => !reliantApi.includes(entry))).toHaveLength(1)
  })
})

// x-daemon-last-seen is read only by the reliant api-server. A production
// build calls both servers cross-origin, and the control-plane admin-server's
// CORS preflight does not allow this header, so attaching it there made the
// browser block every controlplane.v1 RPC once a machine heartbeated. These
// run the real chain end to end and look at what reaches the wire.
describe('x-daemon-last-seen scoping', () => {
  type Next = Parameters<Interceptor>[0]

  async function headersSent(backend: TransportBackend): Promise<Headers> {
    const { buildInterceptors, setDaemonLastSeen } = await import('../transport')
    const { setAuthTokenProvider } = await import('../authProvider')
    setAuthTokenProvider({
      getToken: async () => 'test-token',
      hasSession: async () => true,
      refresh: async () => null,
    })
    setDaemonLastSeen(1_700_000_000)

    let sent: Headers | undefined
    const wire = (async (req: { header: Headers }) => {
      sent = new Headers(req.header)
      return { stream: false, header: new Headers(), trailer: new Headers(), message: {} }
    }) as unknown as Next
    const run = buildInterceptors({ withAuth: true, backend }).reduceRight<Next>(
      (next, interceptor) => interceptor(next),
      wire,
    )
    await run({
      stream: false,
      service: { typeName: 'test.v1.TestService' },
      method: { name: 'Ping', input: { fields: [] } },
      header: new Headers(),
      signal: new AbortController().signal,
      message: {},
    } as unknown as Parameters<Next>[0])

    if (!sent) throw new Error('the chain never reached the wire')
    return sent
  }

  it('attaches it on reliant api-server transports', async () => {
    const sent = await headersSent('reliant-api')
    expect(sent.get('x-daemon-last-seen')).toBe('1700000000')
    expect(sent.get('authorization')).toBe('Bearer test-token')
  })

  it('never sends it to the control plane', async () => {
    const sent = await headersSent('control-plane')
    expect(sent.has('x-daemon-last-seen')).toBe(false)
    expect(sent.get('authorization')).toBe('Bearer test-token')
  })

  // The control plane's activity stream records how a change arrived; the
  // console names itself so a promote from here reads "via Console".
  it('names the console to the control plane in Forge-Via, and only there', async () => {
    expect((await headersSent('control-plane')).get('Forge-Via')).toBe('console')
    expect((await headersSent('reliant-api')).has('Forge-Via')).toBe(false)
  })
})

describe('per-procedure timeouts', () => {
  // Every forge call runs a `forge` subprocess on the daemon with a 15–110s
  // budget. At the 10s default the transport aborted them, the api-server saw
  // a cancelled request, and the UI rendered "no forge.yaml" for control-plane.
  it('gives every ForgeService method more than the 10s default', async () => {
    const { timeoutForProcedure } = await import('../transport')
    const { ForgeService } = await import('../../gen/reliant/v1/forge_pb')
    for (const method of ForgeService.methods) {
      expect(
        timeoutForProcedure(ForgeService.typeName, method.name),
        `${method.name} must not inherit the 10s default`,
      ).toBeGreaterThan(10_000)
    }
  })

  it('scopes the forge budget to ForgeService, not to the bare method name', async () => {
    // control-plane's DeployService also has a PlanDeploy, and it is a quick
    // read that should keep the short default.
    const { timeoutForProcedure } = await import('../transport')
    expect(timeoutForProcedure('reliant.v1.ForgeService', 'PlanDeploy')).toBeGreaterThan(100_000)
    expect(timeoutForProcedure('controlplane.v1.DeployService', 'PlanDeploy')).toBe(10_000)
  })
})

describe('in-flight starvation diagnostics', () => {
  // formatInFlight is the pure core of describeInFlight() — the line the
  // timeout handler logs so a wedged connection (2026-07-09 incident) is
  // diagnosable from the console alone.
  it('formats in-flight entries oldest-first with per-entry ages', async () => {
    const { formatInFlight } = await import('../transport')
    const now = 100_000
    const line = formatInFlight(
      [
        { method: 'ListApprovalsByChat', startedAt: now - 9_000 },
        { method: 'GetWorktreeChanges', startedAt: now - 43_000 },
        { method: 'GetChat', startedAt: now - 2_000 },
      ],
      now,
    )
    expect(line).toBe(
      '3 in flight, oldest: GetWorktreeChanges 43s ' +
        '[GetWorktreeChanges:43s, ListApprovalsByChat:9s, GetChat:2s]',
    )
  })

  it('caps the entry list at 8 and reports the overflow count', async () => {
    const { formatInFlight } = await import('../transport')
    const now = 50_000
    const entries = Array.from({ length: 11 }, (_, i) => ({
      method: `Method${i}`,
      // Method0 is oldest (11s), Method10 youngest (1s).
      startedAt: now - (11 - i) * 1_000,
    }))
    const line = formatInFlight(entries, now)
    expect(line).toContain('11 in flight, oldest: Method0 11s')
    expect(line).toContain('+3 more')
    // The cap keeps the youngest entries out of the bracket list.
    expect(line).not.toContain('Method8:')
    expect(line).not.toContain('Method10:')
  })

  it('reports an empty registry as "0 in flight"', async () => {
    const { formatInFlight, describeInFlight } = await import('../transport')
    expect(formatInFlight([], Date.now())).toBe('0 in flight')
    // Nothing has gone through the timeout interceptor in this module
    // instance, so the live registry snapshot is empty too.
    expect(describeInFlight()).toBe('0 in flight')
  })
})

// The CORS-free model hinges on ONE decision: http(s)-served renderers
// (web-dev AND electron-dev, which Electron loadURL()s from the Vite dev
// server) use a SAME-ORIGIN baseUrl so the Vite proxy fans RPCs out to their
// backends; only packaged Electron (file://) dials the absolute daemon URL.
// These tests pin that discriminator so a regression that reintroduces an
// absolute cross-origin baseUrl in dev (the original CORS bug) fails here.
describe('same-origin transport selection (CORS-free dev model)', () => {
  // Replace window.location wholesale (jsdom's native location.protocol is a
  // non-configurable getter, so per-property redefinition throws on the second
  // call). vi.unstubAllGlobals in afterEach restores the real one.
  const stubLocation = (protocol: 'http:' | 'https:' | 'file:', origin: string) => {
    vi.stubGlobal('location', { protocol, origin })
  }

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('reliantApiBaseUrl returns the document origin when served over http (dev)', async () => {
    stubLocation('http:', 'http://127.0.0.1:5173')
    const { reliantApiBaseUrl } = await import('../../lib/connect')
    expect(reliantApiBaseUrl()).toBe('http://127.0.0.1:5173')
  })

  it('reliantApiBaseUrl prefers RELIANT_CONFIG.grpcUrl when served from file:// (packaged Electron)', async () => {
    stubLocation('file:', 'null')
    ;(window as unknown as { RELIANT_CONFIG?: unknown }).RELIANT_CONFIG = {
      isElectron: true,
      grpcUrl: 'http://127.0.0.1:54321',
    }
    try {
      const { reliantApiBaseUrl } = await import('../../lib/connect')
      expect(reliantApiBaseUrl()).toBe('http://127.0.0.1:54321')
    } finally {
      delete (window as unknown as { RELIANT_CONFIG?: unknown }).RELIANT_CONFIG
    }
  })
})
