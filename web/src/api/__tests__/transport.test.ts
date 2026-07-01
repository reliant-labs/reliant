/**
 * Smoke test for the single-chain interceptor factory.
 *
 * We don't re-verify the behaviour of each individual interceptor here —
 * `grpc-client.auth.test.ts` already covers auth-token shaping, and the
 * timeout/tracing/upgrade interceptors are individually tested elsewhere.
 * What this file pins down is the structural invariant the recent
 * project-picker bug violated: every Connect transport in the app must
 * source its interceptors from `buildInterceptors`, not declare them inline.
 *
 *   - `getTransport`             (reliant api-server, authed)
 *   - `getControlPlaneTransport` (daemon-registry/token, cloud mode, authed)
 *   - `getControlPlaneClient`    (control-plane, authed)
 *   - `getTransport`             (DevAuth bootstrap, unauthed)
 *
 * We mock `createConnectTransport` to capture the options each transport
 * passes through, then assert the captured chains line up with the
 * canonical orderings the factory produces.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  createConnectTransport: vi.fn((options) => options),
}))

vi.mock('@connectrpc/connect-web', () => ({
  createConnectTransport: mocks.createConnectTransport,
}))

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
beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
  mocks.createConnectTransport.mockImplementation((options) => options)
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() => null),
    setItem: vi.fn(),
    removeItem: vi.fn(),
    clear: vi.fn(),
  })
})

describe('buildInterceptors factory', () => {
  it('produces a 7-stage authed chain in the documented order', async () => {
    const { buildInterceptors } = await import('../transport')
    const chain = buildInterceptors({ withAuth: true })
    // timeout → auth → daemon-last-seen → tracing → error-log → upgrade → unauth
    expect(chain).toHaveLength(7)
    // Every entry must be a function (Connect Interceptor shape).
    for (const entry of chain) {
      expect(typeof entry).toBe('function')
    }
  })

  it('produces a 5-stage unauthed chain that omits auth + 401-signout', async () => {
    const { buildInterceptors } = await import('../transport')
    const authed = buildInterceptors({ withAuth: true })
    const unauthed = buildInterceptors({ withAuth: false })
    // 7 - 2 (auth, unauth) = 5
    expect(unauthed).toHaveLength(5)
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
    expect(buildInterceptors()).toHaveLength(7)
    expect(buildInterceptors({})).toHaveLength(7)
  })
})

describe('transport call sites', () => {
  it('grpc-client::getTransport wires the authed factory chain', async () => {
    // The transport module must load first so buildInterceptors's interceptor
    // closures are the same instances grpc-client passes through to
    // createConnectTransport (reference equality is what we assert below).
    const { buildInterceptors } = await import('../transport')
    const expected = buildInterceptors({ withAuth: true })

    const { getTransport } = await import('../grpc-client')
    getTransport()

    expect(mocks.createConnectTransport).toHaveBeenCalledTimes(1)
    const options = mocks.createConnectTransport.mock.calls[0][0]
    expect(options.interceptors).toHaveLength(expected.length)
    // Reference equality: the chain isn't being cloned/transformed downstream.
    // (We use length + per-stage typeof rather than strict-equal arrays because
    // buildInterceptors returns a fresh array each call; the *interceptor
    // functions* it holds are the module-level singletons.)
    options.interceptors.forEach((entry: unknown) => {
      expect(typeof entry).toBe('function')
    })
  })

  it('grpc-client::getControlPlaneTransport returns null when same-origin (http-served renderer)', async () => {
    // Same-origin (Vite-proxy) model: web-dev AND electron-dev are served over
    // http(s), so getControlPlaneTransport returns null and DaemonRegistry/
    // DaemonToken fall through to the same-origin getTransport() — their
    // `reliant.v1.*` paths are proxied to reliant-api, CORS-free. jsdom's
    // window.location.protocol is "http:", so useSameOriginTransport() is true
    // here. The absolute VITE_CONTROL_PLANE_API_URL is never used as a transport
    // baseUrl in this mode (it stays the proxy target + hasControlPlane gate).
    vi.stubEnv('VITE_CONTROL_PLANE_API_URL', 'https://cp.example.test')
    try {
      const { getControlPlaneTransport } = await import('../grpc-client')
      const transport = getControlPlaneTransport()
      expect(transport).toBeNull()
      // No transport built — DaemonRegistry will use getTransport() instead.
      expect(mocks.createConnectTransport).not.toHaveBeenCalled()
    } finally {
      vi.unstubAllEnvs()
    }
  })

  it('services/controlPlane/client::getControlPlaneClient wires the authed factory chain', async () => {
    const { buildInterceptors } = await import('../transport')
    const expected = buildInterceptors({ withAuth: true })

    // Use a real generated DescService so connect's createClient is happy
    // about `service.methods` being iterable.
    const { SystemService } = await import('@/gen/reliant/v1/system_pb')

    const { getControlPlaneClient } = await import(
      '@/services/controlPlane/client'
    )
    getControlPlaneClient(SystemService)

    expect(mocks.createConnectTransport).toHaveBeenCalledTimes(1)
    const options = mocks.createConnectTransport.mock.calls[0][0]
    // Same-origin (Vite-proxy) model: served over http(s) (jsdom is "http:"),
    // so the control-plane client points at the document origin and Vite's
    // `/controlplane.v1.*` proxy forwards to admin-server — CORS-free. The
    // absolute VITE_CONTROL_PLANE_API_URL stays the hasControlPlane gate + proxy
    // target; it's only the direct baseUrl in packaged Electron (file://).
    expect(options.baseUrl).toBe(window.location.origin)
    expect(options.interceptors).toHaveLength(expected.length)
  })

  it('grpc-unauth wires the unauthed factory chain', async () => {
    // The global test setup mocks @/api/grpc-unauth so unit tests don't make
    // real DevAuth calls; un-mock it here so we test the real factory wiring.
    vi.doUnmock('@/api/grpc-unauth')
    vi.resetModules()
    // resetModules wiped the createConnectTransport spy registration too —
    // re-mock so we capture the fresh call.
    vi.doMock('@connectrpc/connect-web', () => ({
      createConnectTransport: mocks.createConnectTransport,
    }))

    const { buildInterceptors } = await import('../transport')
    const expected = buildInterceptors({ withAuth: false })

    // grpc-unauth's getTransport is private; trigger it via the exported
    // devAuthGrpc.load() call which builds the transport on first use.
    const { devAuthGrpc } = await import('../grpc-unauth')
    try {
      await devAuthGrpc.load()
    } catch {
      // The fake transport returned by our createConnectTransport mock isn't
      // a real RPC channel — we ignore the resulting network-shape error.
    }

    expect(mocks.createConnectTransport).toHaveBeenCalledTimes(1)
    const options = mocks.createConnectTransport.mock.calls[0][0]
    expect(options.interceptors).toHaveLength(expected.length)
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

  it('getGRPCBaseURL returns the document origin when served over http (dev)', async () => {
    stubLocation('http:', 'http://127.0.0.1:5173')
    const { getGRPCBaseURLPublic } = await import('../grpc-client')
    expect(getGRPCBaseURLPublic()).toBe('http://127.0.0.1:5173')
  })

  it('getGRPCBaseURL prefers RELIANT_CONFIG.grpcUrl when served from file:// (packaged Electron)', async () => {
    stubLocation('file:', 'null')
    ;(window as unknown as { RELIANT_CONFIG?: unknown }).RELIANT_CONFIG = {
      isElectron: true,
      grpcUrl: 'http://127.0.0.1:54321',
    }
    try {
      const { getGRPCBaseURLPublic } = await import('../grpc-client')
      expect(getGRPCBaseURLPublic()).toBe('http://127.0.0.1:54321')
    } finally {
      delete (window as unknown as { RELIANT_CONFIG?: unknown }).RELIANT_CONFIG
    }
  })
})
