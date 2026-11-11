import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setRuntimeConfig } from '@/test/runtimeConfig'

/**
 * lib/supabase.ts's choice of storage on a page the shared cookie does not
 * apply to. jsdom serves http://localhost, which is exactly a dev page: even
 * with AUTH_COOKIE_DOMAIN set, the session stays in this origin and no auth
 * cookie is written. (The hosted choice is resolveAuthStorageMode's, covered
 * in sharedSession.test.ts; jsdom cannot load a page on reliantlabs.io.)
 */

// src/test/setup.ts stubs this module globally; this suite is about the real one.
vi.unmock('@/lib/supabase')

describe('lib/supabase storage wiring off the shared domain', () => {
  beforeEach(() => {
    vi.resetModules()
    window.localStorage.clear()
    setRuntimeConfig({
      AUTH_MODE: 'cloud',
      AUTH_COOKIE_DOMAIN: '.reliantlabs.io',
      API_URL: 'https://api.example.test',
      CONTROL_PLANE_API_URL: 'https://admin.example.test',
      SUPABASE_URL: 'https://dash.example.test',
      SUPABASE_ANON_KEY: 'test-anon-key',
    })
  })

  it('keeps localStorage on localhost and never writes the auth cookie', async () => {
    const { authStorageMode, supabase } = await import('@/lib/supabase')
    expect(authStorageMode()).toBe('local')

    const session = {
      access_token: 'a',
      refresh_token: 'r',
      expires_at: Math.floor(Date.now() / 1000) + 3600,
      token_type: 'bearer',
      user: { id: 'u' },
    }
    window.localStorage.setItem('supabase-auth', JSON.stringify(session))
    const { data } = await supabase.auth.getSession()
    expect(data.session?.access_token).toBe('a')
    expect(document.cookie).not.toContain('supabase-auth')
    // Nothing migrated it away.
    expect(window.localStorage.getItem('supabase-auth')).not.toBeNull()
  })
})
