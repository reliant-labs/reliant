import '@testing-library/jest-dom'
import { configure } from '@testing-library/react'
import { afterEach, beforeEach, vi } from 'vitest'
import { resetConfigForTests } from '@/lib/config_gen'

// Runtime config: every test starts from an EMPTY config.js document, i.e.
// config_gen.ts's defaults (the self-hosted app). An absent document is not
// the same thing under vitest: config_gen.ts would fall back to reading
// process.env, so whatever API_URL / AUTH_MODE the shell running the tests
// exports would leak into them. Set before any test file imports a module
// that reads the config at module scope; tests override it with
// src/test/runtimeConfig.ts setRuntimeConfig().
window.__FORGE_CONFIG__ = {}
beforeEach(() => {
  window.__FORGE_CONFIG__ = {}
  resetConfigForTests()
})

// findBy*/waitFor budget, scaled with host load like the test timeout (see
// "Vitest sizing" in vite.config.ts). The fixed 1s default failed tests on a
// loaded host while the element they waited for was still rendering.
const asyncUtilTimeout = Number(import.meta.env.RELIANT_ASYNC_UTIL_TIMEOUT_MS)
if (asyncUtilTimeout > 0) configure({ asyncUtilTimeout })

// Web Storage polyfill.
//
// `localStorage` resolves to an object here but carries no methods, so zustand's
// persist middleware throws "storage.setItem is not a function" on the FIRST
// write to any persisted store. That makes a whole class of store behaviour
// untestable: the failure fires inside setState, so it takes out test setup
// before a single assertion runs, and it presents as an opaque zustand
// stacktrace rather than a missing-API message.
//
// Backing both localStorage and sessionStorage with a real Map-based
// implementation is what lets tests exercise persisted stores at all.
function createStorageStub(): Storage {
  let entries = new Map<string, string>()
  return {
    get length() {
      return entries.size
    },
    key: (index: number) => Array.from(entries.keys())[index] ?? null,
    getItem: (key: string) => (entries.has(key) ? entries.get(key)! : null),
    setItem: (key: string, value: string) => {
      entries.set(key, String(value))
    },
    removeItem: (key: string) => {
      entries.delete(key)
    },
    clear: () => {
      entries = new Map()
    },
  } as Storage
}

for (const name of ['localStorage', 'sessionStorage'] as const) {
  if (typeof globalThis[name]?.setItem !== 'function') {
    Object.defineProperty(globalThis, name, {
      value: createStorageStub(),
      configurable: true,
      writable: true,
    })
  }
}

// Keep test output readable by suppressing known non-fatal noise.
const originalConsoleError = console.error.bind(console)

const SUPPRESSED_CONSOLE_ERROR_PATTERNS = [
  '[WorkspaceState] Failed to rehydrate:',
  '[SettingsSync] Failed to get JSON setting',
]

function shouldSuppressConsoleError(firstArg: unknown): boolean {
  if (typeof firstArg !== 'string') return false
  return SUPPRESSED_CONSOLE_ERROR_PATTERNS.some((pattern) => firstArg.includes(pattern))
}

vi.spyOn(console, 'error').mockImplementation((...args: unknown[]) => {
  if (shouldSuppressConsoleError(args[0])) return
  originalConsoleError(...args)
})

// Stub supabase so that importing grpc-client (which eagerly calls createClient)
// doesn't throw "supabaseKey is required" in the test environment.
vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: vi.fn(async () => ({ data: { session: null }, error: null })),
      onAuthStateChange: vi.fn(() => ({
        data: { subscription: { unsubscribe: vi.fn() } },
      })),
    },
  },
}))

// Prevent auth/session bootstrap from making real gRPC calls in unit tests.
vi.mock('@/api/grpc-unauth', () => ({
  devAuthGrpc: {
    startOAuthSignIn: vi.fn(async () => ({
      accessToken: 'access-token',
      refreshToken: 'refresh-token',
      userId: 'user-id',
      email: 'user@example.com',
    })),
    load: vi.fn(async () => ({ success: false })),
    save: vi.fn(async () => ({ success: true })),
    clear: vi.fn(async () => ({ success: true })),
  },
}))

// Keep attachment-store tests deterministic by stubbing network deletion.
vi.mock('@/hooks/attachment-queries', async () => {
  const actual = await vi.importActual<typeof import('@/hooks/attachment-queries')>('@/hooks/attachment-queries')
  return {
    ...actual,
    deleteAttachment: vi.fn(async () => undefined),
  }
})

// Mock WebSocket
const WebSocketMock = vi.fn().mockImplementation(() => ({
  close: vi.fn(),
  send: vi.fn(),
  addEventListener: vi.fn(),
  removeEventListener: vi.fn(),
  readyState: 0,
  CONNECTING: 0,
  OPEN: 1,
  CLOSING: 2,
  CLOSED: 3,
}))

// Add static constants to the constructor
WebSocketMock.CONNECTING = 0
WebSocketMock.OPEN = 1
WebSocketMock.CLOSING = 2
WebSocketMock.CLOSED = 3

global.WebSocket = WebSocketMock as unknown as typeof WebSocket
// ── Unit tests do not reach the network ────────────────────────────────────
//
// jsdom's origin is http://localhost:3000 and the gRPC transport targets
// window.location.origin, so an RPC a test did not mock used to go to whatever
// listened on :3000 — on a developer's machine, the live dev stack; in CI,
// nothing — and settle whenever that answer came back. Usually that was AFTER
// the test, often after the whole file, and the transport logs every failed
// RPC. A log that landed while vitest was closing the worker's RPC channel
// failed the entire run with
//
//   EnvironmentTeardownError: [vitest-worker]: Closing rpc while
//   "onUserConsoleLog" was pending
//
// blaming whichever file happened to be tearing down (ChatPresenter.*,
// Sidebar.*, …), with every test green. A log that landed a little earlier
// printed under no test at all ("stderr | Object.warn (logger.ts)").
//
// So the network is not there. fetch rejects at once, naming the request, and
// the test that made it fails: a test owns its I/O and mocks it (the hook, the
// *-grpc module, or fetch itself with vi.spyOn/vi.stubGlobal, which replace
// this stub for that test).
//
// "The test that made it" needs one more piece: an RPC does real async work
// BEFORE it reaches fetch (the auth interceptor imports supabase lazily), so a
// call a test started could reach fetch only after that test's afterEach had
// checked — or after the whole file, unflagged, logging into teardown exactly
// as before. Observed: a Sidebar test's ListRuns reached fetch after its
// file's last hook. So every call made through a real Connect transport is
// tracked, and the check below first waits for the test's calls to settle.
const rpcs = vi.hoisted(() => ({ inFlight: new Set<Promise<unknown>>() }))

vi.mock('@connectrpc/connect-web', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@connectrpc/connect-web')>()
  const track = <T,>(call: Promise<T>): Promise<T> => {
    rpcs.inFlight.add(call)
    const settled = () => {
      rpcs.inFlight.delete(call)
    }
    call.then(settled, settled)
    return call
  }
  return {
    ...actual,
    createConnectTransport: (...args: Parameters<typeof actual.createConnectTransport>) => {
      const transport = actual.createConnectTransport(...args)
      return {
        ...transport,
        unary: (...call: Parameters<typeof transport.unary>) => track(transport.unary(...call)),
        stream: (...call: Parameters<typeof transport.stream>) => track(transport.stream(...call)),
      }
    },
  }
})

const unmockedRequests: string[] = []

function describeRequest(input: RequestInfo | URL, init?: RequestInit): string {
  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
  const method = init?.method ?? (typeof input === 'object' && 'method' in input ? input.method : 'GET')
  return `${method} ${url}`
}

globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
  const request = describeRequest(input, init)
  unmockedRequests.push(request)
  return Promise.reject(new TypeError(`Unit tests have no network: unmocked request ${request}`))
}) as typeof fetch

afterEach(async () => {
  // A call that settles can start another (a retry, a follow-up refetch).
  while (rpcs.inFlight.size > 0) {
    await Promise.allSettled([...rpcs.inFlight])
  }
  if (unmockedRequests.length === 0) return
  const requests = [...new Set(unmockedRequests.splice(0))]
  throw new Error(
    `This test made ${requests.length === 1 ? 'a real network request' : 'real network requests'}, ` +
      `which unit tests cannot (see "Unit tests do not reach the network" in src/test/setup.ts). ` +
      `Mock the hook or API module that sent it:\n  ${requests.join('\n  ')}`,
  )
})
