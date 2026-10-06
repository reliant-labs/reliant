import '@testing-library/jest-dom'
import { afterEach, vi } from 'vitest'

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

// ─── Unit tests are offline, and a test does not outlive its RPCs ───────────
//
// jsdom's origin is http://localhost:3000, so an RPC a test did not mock used
// to leave the process for real — connection refused in CI, and whatever dev
// server happened to own :3000 locally. It settled on network time, and the
// transport's interceptors log when it does ("Request failed", "401
// received", "No auth token available", and — once the file's mocks were
// gone — "[auth] Error getting session"). Sixteen files did this, through
// unmocked list/get calls fired by the components they render, and some of
// those calls had not even reached fetch when their file ended.
//
// A console call that lands while Vitest is closing a worker's RPC channel is
// rejected as `EnvironmentTeardownError: Closing rpc while "onUserConsoleLog"
// was pending`. The run exits 1 with every test green, blaming whichever file
// happened to be finishing. Both halves below are needed:
//
//   1. fetch refuses immediately. That is what CI's refused connection already
//      looked like to the code under test, so no test's behaviour changes; it
//      only stops depending on the network's clock, or on a dev server. A test
//      that needs a response mocks its client or stubs fetch, replacing this.
//   2. Every call made through a real Connect transport is tracked, and each
//      test waits for its calls to settle. The request path does real async
//      work before fetch (the auth interceptor imports supabase lazily), so an
//      immediate refusal alone still let a call finish after its file did.
//      Waiting here, after Testing Library has unmounted the tree, means every
//      log a call makes lands inside the test that made it.
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

globalThis.fetch = ((input: RequestInfo | URL) => {
  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
  return Promise.reject(new TypeError(`fetch failed: unit tests are offline (${url})`))
}) as typeof fetch

afterEach(async () => {
  // A call that settles can start another (a retry, a follow-up refetch).
  while (rpcs.inFlight.size > 0) {
    await Promise.allSettled([...rpcs.inFlight])
  }
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
vi.mock('@/api/attachment-grpc', async () => {
  const actual = await vi.importActual<typeof import('@/api/attachment-grpc')>('@/api/attachment-grpc')
  return {
    ...actual,
    attachmentGrpc: {
      ...actual.attachmentGrpc,
      deleteAttachment: vi.fn(async () => undefined),
    },
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

afterEach(() => {
  if (unmockedRequests.length === 0) return
  const requests = [...new Set(unmockedRequests.splice(0))]
  throw new Error(
    `This test made ${requests.length === 1 ? 'a real network request' : 'real network requests'}, ` +
      `which unit tests cannot (see "Unit tests do not reach the network" in src/test/setup.ts). ` +
      `Mock the hook or API module that sent it:\n  ${requests.join('\n  ')}`,
  )
})
