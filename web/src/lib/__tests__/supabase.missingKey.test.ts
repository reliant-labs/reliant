import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// src/test/setup.ts globally mocks '@/lib/supabase' precisely because importing
// it used to throw "supabaseKey is required." That mock is what kept this
// defect invisible to the unit lane, so this file tests the real module.
vi.unmock('@/lib/supabase')

// Stub supabase-js so these tests assert OUR construction behavior (when
// createClient is called, and with what) without opening a network client.
// The real library's own guard — `if (!supabaseKey) throw` — is the thing this
// module must now avoid tripping at import, so we reproduce it here rather
// than trusting a stub that always succeeds.
vi.mock('@supabase/supabase-js', () => ({
  createClient: vi.fn((url: string, key: string) => {
    if (!key) throw new Error('supabaseKey is required.')
    return {
      supabaseUrl: url,
      auth: {
        getSession: async function () {
          // Reading `this` proves the proxy bound the method to the real
          // client; an unbound call would see the proxy (or undefined) here.
          if (!this || typeof this.getSession !== 'function') {
            throw new Error('getSession lost its `this` binding')
          }
          return { data: { session: null }, error: null }
        },
      },
    }
  }),
}))

const ORIGINAL_ENV = { ...import.meta.env }

function setEnv(vars: Record<string, string | undefined>) {
  for (const [key, value] of Object.entries(vars)) {
    if (value === undefined) {
      delete (import.meta.env as Record<string, unknown>)[key]
    } else {
      (import.meta.env as Record<string, unknown>)[key] = value
    }
  }
}

beforeEach(() => {
  vi.resetModules()
  // window.electronAPI is absent, and VITE_AUTH_MODE is unset, so getStorage()
  // takes the browser path; nothing here needs an Electron or gRPC backend.
  setEnv({ VITE_SUPABASE_URL: undefined, VITE_SUPABASE_ANON_KEY: undefined })
})

afterEach(() => {
  for (const key of Object.keys(import.meta.env)) {
    delete (import.meta.env as Record<string, unknown>)[key]
  }
  Object.assign(import.meta.env, ORIGINAL_ENV)
})

describe('supabase module with no anon key configured', () => {
  // The regression itself: with VITE_SUPABASE_ANON_KEY unset, this module used
  // to throw at IMPORT time. It is imported transitively from the app entry via
  // authStore -> AuthInitializer, so the throw happened before React could
  // mount, leaving #root empty and every Playwright test timing out on a blank
  // page. Importing must now succeed.
  it('imports without throwing', async () => {
    await expect(import('@/lib/supabase')).resolves.toBeDefined()
  })

  it('reports that Supabase is not configured', async () => {
    const { isSupabaseConfigured } = await import('@/lib/supabase')
    expect(isSupabaseConfigured()).toBe(false)
  })

  it('does not invent a fallback key to make a broken build look healthy', async () => {
    const { createClient } = await import('@supabase/supabase-js')
    const createClientSpy = vi.mocked(createClient)
    await import('@/lib/supabase')
    expect(createClientSpy).not.toHaveBeenCalled()
  })

  // The failure is deferred, not swallowed. It surfaces at first auth use,
  // where a React error boundary can render the reason, and it names the
  // missing variable rather than supabase-js's opaque "supabaseKey is required."
  it('throws an actionable error at first use, not at import', async () => {
    const { supabase, MISSING_ANON_KEY_MESSAGE } = await import('@/lib/supabase')
    expect(() => supabase.auth).toThrow(MISSING_ANON_KEY_MESSAGE)
    expect(MISSING_ANON_KEY_MESSAGE).toContain('VITE_SUPABASE_ANON_KEY')
  })
})

describe('supabase module with an anon key configured', () => {
  beforeEach(() => {
    setEnv({
      VITE_SUPABASE_URL: 'https://dash.example.invalid',
      VITE_SUPABASE_ANON_KEY: 'sb_publishable_test_key',
    })
  })

  it('reports that Supabase is configured', async () => {
    const { isSupabaseConfigured } = await import('@/lib/supabase')
    expect(isSupabaseConfigured()).toBe(true)
  })

  it('builds the client lazily and reuses it across property accesses', async () => {
    const { createClient } = await import('@supabase/supabase-js')
    const createClientSpy = vi.mocked(createClient)
    const { supabase } = await import('@/lib/supabase')

    expect(createClientSpy).not.toHaveBeenCalled()

    void supabase.auth
    void supabase.auth
    expect(createClientSpy).toHaveBeenCalledTimes(1)
    expect(createClientSpy).toHaveBeenCalledWith(
      'https://dash.example.invalid',
      'sb_publishable_test_key',
      expect.objectContaining({ auth: expect.objectContaining({ flowType: 'pkce' }) }),
    )
  })

  it('preserves `this` when a client method is called through the proxy', async () => {
    const { supabase } = await import('@/lib/supabase')
    await expect(supabase.auth.getSession()).resolves.toEqual({
      data: { session: null },
      error: null,
    })
  })
})
