/**
 * Dev sign-in end to end through the REAL storage and the REAL supabase-js
 * client: adoption writes with the default writer (lib/supabase.ts
 * writeStoredSessionForDevSignIn, into the storage the page selected), and a
 * fresh page's client recovers it as the signed-in session without asking the
 * auth server. Only the page restart is stubbed (jsdom cannot navigate).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setRuntimeConfig } from '@/test/runtimeConfig'

// setup.ts mocks the module globally; this file exercises the real one.
vi.unmock('@/lib/supabase')

const AUTH_SERVER = 'https://auth.example.invalid'
/** 2100-01-01T00:00:00Z, in seconds: an exp no wall clock reaches. */
const FAR_FUTURE = 4_102_444_800

function b64url(value: unknown): string {
  return btoa(JSON.stringify(value)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

const TOKEN = `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url({
  sub: 'dev-browser-user',
  email: 'dev-browser-user@dev.test',
  email_verified: true,
  role: 'authenticated',
  aud: 'authenticated',
  iat: FAR_FUTURE - 7200,
  exp: FAR_FUTURE,
})}.c2lnbmF0dXJl`

/** Every request the page makes; a request to the auth server fails the test. */
let requests: string[]

function configurePage() {
  // AUTH_MODE "cloud" with no AUTH_COOKIE_DOMAIN: the localhost browser path,
  // this origin's localStorage (lib/supabase.ts getStorage), as on dev's
  // reliant-web.
  setRuntimeConfig({
    AUTH_MODE: 'cloud',
    API_URL: 'http://localhost:3090',
    CONTROL_PLANE_API_URL: 'http://localhost:8090',
    SUPABASE_URL: AUTH_SERVER,
    SUPABASE_ANON_KEY: 'sb_publishable_test_key',
  })
}

beforeEach(() => {
  vi.resetModules()
  window.localStorage.clear()
  requests = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      requests.push(url)
      throw new Error(`unexpected request to ${url}`)
    }),
  )
  configurePage()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

describe('dev sign-in through the real supabase client', () => {
  it('a fresh page recovers the adopted token as the session, with no auth-server round trip', async () => {
    vi.stubEnv('VITE_DEV_SIGN_IN', 'true')
    const { adoptDevToken } = await import('../devSignIn')
    const restart = vi.fn()
    await adoptDevToken(TOKEN, '/', { restart })
    expect(restart).toHaveBeenCalledWith('/')

    // The restart: a new page evaluates every module afresh.
    vi.resetModules()
    configurePage()
    const { supabase } = await import('@/lib/supabase')
    const { data, error } = await supabase.auth.getSession()
    supabase.auth.stopAutoRefresh()

    expect(error).toBeNull()
    expect(data.session?.access_token).toBe(TOKEN)
    expect(data.session?.user.id).toBe('dev-browser-user')
    expect(data.session?.user.email).toBe('dev-browser-user@dev.test')
    expect(requests.filter((url) => url.startsWith(AUTH_SERVER))).toEqual([])
  })

  it('leaves storage untouched when dev sign-in is off', async () => {
    window.localStorage.setItem('unrelated', 'kept')
    const before = JSON.stringify({ ...window.localStorage })
    const { adoptDevToken } = await import('../devSignIn')
    const restart = vi.fn()
    await expect(adoptDevToken(TOKEN, '/', { restart })).rejects.toThrow(/off in this build/)
    expect(JSON.stringify({ ...window.localStorage })).toBe(before)
    expect(restart).not.toHaveBeenCalled()
    expect(requests).toEqual([])
  })
})
