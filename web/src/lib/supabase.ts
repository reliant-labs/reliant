import { createClient, SupabaseClient } from '@supabase/supabase-js'
import type { SupportedStorage } from '@supabase/auth-js'
import { devAuthGrpc } from '@/api/grpc-unauth'
import { getIsDev } from './constants'

const supabaseUrl = import.meta.env.VITE_SUPABASE_URL || 'http://127.0.0.1:54321'
const supabaseAnonKey = import.meta.env.VITE_SUPABASE_ANON_KEY || ''

export const MISSING_ANON_KEY_MESSAGE =
  'VITE_SUPABASE_ANON_KEY is not set in this build, so authentication is unavailable. ' +
  'Source builds get it from the environment; released builds get it from ' +
  'electron/release.config.json via the workflow\'s "Export renderer build config" step.'

const isElectron = !!window.electronAPI

// Custom storage adapter for Electron that uses our file-based auth storage
// This prevents dual storage (localStorage + file) which causes refresh token reuse issues
// IMPORTANT: For PKCE flow, we need to store the code verifier temporarily in memory
// since it's only needed during the OAuth exchange (not persisted to file)
// We also use sessionStorage as a backup since in-memory storage is lost on page refresh
const pkceStorage = new Map<string, string>()

// PKCE helpers with sessionStorage fallback for page refresh resilience
const getPkceValue = (key: string): string | null => {
  // First check in-memory
  const memValue = pkceStorage.get(key);
  if (memValue) return memValue;
  
  // Fallback to sessionStorage (survives page refresh within session)
  try {
    const sessionValue = sessionStorage.getItem(`pkce:${key}`);
    if (sessionValue) {
      // Restore to memory for faster subsequent access
      pkceStorage.set(key, sessionValue);
      return sessionValue;
    }
  } catch {
    // sessionStorage not available
  }
  return null;
};

const setPkceValue = (key: string, value: string): void => {
  // Store in memory
  pkceStorage.set(key, value);
  // Also store in sessionStorage as backup
  try {
    sessionStorage.setItem(`pkce:${key}`, value);
  } catch {
    // sessionStorage not available
  }
};

const removePkceValue = (key: string): void => {
  pkceStorage.delete(key);
  try {
    sessionStorage.removeItem(`pkce:${key}`);
  } catch {
    // sessionStorage not available
  }
};

/**
 * Fires when the stored session existed but could not be read back, and the
 * failure is permanent (the OS encryption key that wrote it is gone).
 *
 * Why this is not just a log line: Supabase's storage contract has no way to
 * say "unreadable" — `getItem` returns a string or null, and null means "no
 * session". So an unreadable blob is indistinguishable from a signed-out user
 * to `getSession()`, which is exactly how the app ended up half-authenticated:
 * the in-memory auth store still held a session from a prior
 * `onAuthStateChange`, so the UI stayed signed IN, while `getToken()` resolved
 * null and every RPC went out with no Authorization header. Escalating out of
 * band is the only way to turn that invisible degradation into a real,
 * actionable auth state.
 */
type AuthStorageUnreadableHandler = (detail: { reason: string }) => void

let onStorageUnreadable: AuthStorageUnreadableHandler | null = null

// One escalation per app run. The adapter is read on every getSession(), and a
// burst of concurrent RPCs would otherwise each report the same dead blob.
let reportedUnreadable = false

// The failure, held until a handler exists to receive it.
//
// This is NOT belt-and-braces: `createClient` below reads storage during its
// own initialization (auth-js `_initialize` → `_recoverAndRefresh`), and that
// read happens at MODULE LOAD — strictly before authStore.initialize() can
// register a handler. That first read is precisely the one that discovers a
// dead blob, so firing and forgetting would drop the real incident on the
// floor every time and only ever escalate on some later, incidental read.
let pendingUnreadable: { reason: string } | null = null

/** Registered once by authStore during initialize(). */
export const setAuthStorageUnreadableHandler = (
  handler: AuthStorageUnreadableHandler | null,
): void => {
  onStorageUnreadable = handler

  // Replay a failure that was detected before anyone was listening.
  if (handler && pendingUnreadable) {
    const detail = pendingUnreadable
    pendingUnreadable = null
    handler(detail)
  }
}

/** Records the failure, delivering it now or as soon as a handler appears. */
const reportUnreadableStorage = (detail: { reason: string }): void => {
  if (onStorageUnreadable) {
    onStorageUnreadable(detail)
    return
  }
  pendingUnreadable = detail
}

/** Test seam — resets the once-per-run latch. */
export const __resetAuthStorageUnreadableForTests = (): void => {
  reportedUnreadable = false
  pendingUnreadable = null
}

const electronStorage: SupportedStorage = {
  getItem: async (key: string) => {
    try {
      // Check if this is a PKCE-related key (code verifier)
      if (key.includes('code-verifier') || key.includes('pkce')) {
        return getPkceValue(key);
      }

      // For session data, load from file
      const result = await window.electronAPI!.authLoad()

      // An unrecoverable read means a session WAS stored and is now
      // permanently unreadable. Returning null here (which we still must do —
      // there is genuinely no session to hand back) would leave the app
      // running against a session it can never authenticate, so escalate it
      // as a real auth state instead of degrading silently.
      const failure = result?.failure
      if (failure && !failure.recoverable && !reportedUnreadable) {
        reportedUnreadable = true
        console.error(
          '[ElectronStorage] Stored session is permanently unreadable; re-authentication required',
          failure,
        )
        reportUnreadableStorage({ reason: failure.reason })
      }

      if (result?.success && result?.session) {
        return JSON.stringify(result.session)
      }
      return null
    } catch (error) {
      console.error('[ElectronStorage] Failed to load:', error)
      return null
    }
  },
  setItem: async (key: string, value: string) => {
    try {
      // Store PKCE-related data with sessionStorage backup
      if (key.includes('code-verifier') || key.includes('pkce')) {
        setPkceValue(key, value);
        return;
      }

      // For session data, save to file
      const session = JSON.parse(value)
      await window.electronAPI!.authSave(session)
    } catch (error) {
      console.error('[ElectronStorage] Failed to save:', error)
    }
  },
  removeItem: async (key: string) => {
    try {
      // Remove PKCE data from memory and sessionStorage
      if (key.includes('code-verifier') || key.includes('pkce')) {
        removePkceValue(key);
        return;
      }

      // Clear session file
      await window.electronAPI!.authClear()
    } catch (error) {
      console.error('[ElectronStorage] Failed to clear:', error)
    }
  },
}

// gRPC-based storage adapter for browser development mode
// Allows testing global auth in browser by using gRPC to access auth file on backend.
// Falls back to localStorage when the backend doesn't support DevAuth (e.g. cloud/split mode).
let grpcStorageAvailable: boolean | null = null
// In-flight promise cache to deduplicate concurrent getItem calls during Supabase init
let grpcLoadPromise: Promise<string | null> | null = null

const grpcStorage: SupportedStorage = {
  getItem: async (key: string) => {
    if (grpcStorageAvailable === false) return window.localStorage.getItem(key)
    // Deduplicate concurrent calls (Supabase init triggers ~4 getItem calls)
    if (grpcLoadPromise) return grpcLoadPromise
    grpcLoadPromise = (async () => {
      try {
        const result = await devAuthGrpc.load()
        grpcStorageAvailable = true
        if (result?.success && result?.sessionJson) {
          // Mirror to localStorage so co-located apps (admin-web) can read the session
          window.localStorage.setItem(key, result.sessionJson)
          return result.sessionJson
        }
        return null
      } catch (error) {
        // If DevAuth is unavailable (cloud/split mode), fall back to localStorage permanently
        if (grpcStorageAvailable === null) {
          console.info('[GRPCStorage] DevAuth unavailable, falling back to localStorage')
          grpcStorageAvailable = false
          return window.localStorage.getItem(key)
        }
        console.error('[GRPCStorage] Failed to load session:', error)
        return null
      } finally {
        // Clear after a short delay to allow batching during init
        setTimeout(() => { grpcLoadPromise = null }, 100)
      }
    })()
    return grpcLoadPromise
  },
  setItem: async (key: string, value: string) => {
    // Always mirror to localStorage so co-located apps (admin-web) can read the session
    window.localStorage.setItem(key, value)
    if (grpcStorageAvailable === false) { return }
    try {
      await devAuthGrpc.save(value)
    } catch (error) {
      if (grpcStorageAvailable === null) { grpcStorageAvailable = false; return }
      console.error('[GRPCStorage] Failed to save session:', error)
    }
  },
  removeItem: async (key: string) => {
    // Always mirror to localStorage so co-located apps (admin-web) see the clear
    window.localStorage.removeItem(key)
    if (grpcStorageAvailable === false) { return }
    try {
      await devAuthGrpc.clear()
    } catch (error) {
      if (grpcStorageAvailable === null) { grpcStorageAvailable = false; return }
      console.error('[GRPCStorage] Failed to clear session:', error)
    }
  },
}

// Select the appropriate storage based on environment
const getStorage = (): SupportedStorage => {
  if (isElectron) {
    return electronStorage
  }
  if (getIsDev() && import.meta.env.VITE_AUTH_MODE !== 'cloud') {
    // In browser dev mode, use gRPC storage to access global auth file on backend
    // (falls back to localStorage if DevAuth endpoints aren't available)
    return grpcStorage
  }
  // In production browser mode, use localStorage
  return window.localStorage
}

/** True when this build carries the config Supabase needs to start. */
export const isSupabaseConfigured = (): boolean => Boolean(supabaseAnonKey)

let client: SupabaseClient | null = null

/**
 * Build the real client on first use.
 *
 * This is deliberately NOT called at module scope. supabase-js throws
 * `supabaseKey is required.` from createClient when the key is empty, and this
 * module is imported transitively from the app entry (authStore ->
 * AuthInitializer), so a module-scope call turned one missing build variable
 * into a blank page: React never mounted and `#root` stayed empty, with the
 * only evidence a console error nobody sees in a packaged app.
 *
 * Deferring the construction moves that failure to the first auth call, where
 * a React error boundary can catch it and show the reason. Everything that
 * doesn't touch auth keeps working.
 *
 * We do NOT substitute a fallback anon key. There is no local Supabase in this
 * repo to fall back TO — no supabase/config.toml, no compose service, and
 * scripts/dev.sh sets no VITE_SUPABASE_*, so the pre-existing
 * `http://127.0.0.1:54321` URL default already points at nothing. Inventing a
 * credential would make a misconfigured build look healthy while pointing
 * somewhere useless, which is the exact failure sync-release-config.mjs was
 * written to prevent ("a packaged app that silently ships pointing at
 * localhost or at nothing — it builds green and fails on a user's machine").
 * A released artifact still cannot reach this path: that script fails the
 * build when vite.VITE_SUPABASE_ANON_KEY is empty.
 */
const getClient = (): SupabaseClient => {
  if (client) return client
  if (!supabaseAnonKey) throw new Error(MISSING_ANON_KEY_MESSAGE)

  client = createClient(supabaseUrl, supabaseAnonKey, {
    auth: {
      autoRefreshToken: true,
      persistSession: true,
      detectSessionInUrl: false,
      storage: getStorage(),
      storageKey: 'supabase-auth',
      flowType: 'pkce',
    },
  })
  return client
}

/**
 * Drop-in replacement for the eagerly-created client.
 *
 * Callers keep writing `supabase.auth.signInWithPassword(...)`; the proxy
 * constructs the underlying client on the first property access instead of at
 * import time. Existing call sites are unchanged.
 */
export const supabase: SupabaseClient = new Proxy({} as SupabaseClient, {
  get: (_target, property) => {
    const resolved = getClient()
    const value = Reflect.get(resolved, property) as unknown
    // Bind methods to the real client so `this` is never the proxy, which
    // would re-enter this trap for every internal property access.
    return typeof value === 'function' ? value.bind(resolved) : value
  },
  set: (_target, property, value) => Reflect.set(getClient(), property, value),
  has: (_target, property) => Reflect.has(getClient(), property),
})

// NOTE: OAuth callback handling is done in authStore.ts to keep all auth logic centralized