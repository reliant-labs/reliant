import { createClient, SupabaseClient } from '@supabase/supabase-js'
import type { SupportedStorage } from '@supabase/auth-js'
import { devAuthGrpc } from '@/api/grpc-unauth'
import { getIsDev } from './constants'

const supabaseUrl = import.meta.env.VITE_SUPABASE_URL || 'http://127.0.0.1:54321'
const supabaseAnonKey = import.meta.env.VITE_SUPABASE_ANON_KEY || ''

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

const electronStorage: SupportedStorage = {
  getItem: async (key: string) => {
    try {
      // Check if this is a PKCE-related key (code verifier)
      if (key.includes('code-verifier') || key.includes('pkce')) {
        return getPkceValue(key);
      }

      // For session data, load from file
      const result = await window.electronAPI!.authLoad()
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

export const supabase: SupabaseClient = createClient(supabaseUrl, supabaseAnonKey, {
  auth: {
    autoRefreshToken: true,
    persistSession: true,
    detectSessionInUrl: false,
    storage: getStorage(),
    storageKey: 'supabase-auth',
    flowType: 'pkce',
  },
})

// NOTE: OAuth callback handling is done in authStore.ts to keep all auth logic centralized