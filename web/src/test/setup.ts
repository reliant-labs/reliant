import '@testing-library/jest-dom'
import { vi } from 'vitest'

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