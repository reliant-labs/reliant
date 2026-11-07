import { afterEach, describe, expect, it } from 'vitest'
import { createClient, type SupabaseClient } from '@supabase/supabase-js'
import type { LockFunc } from '@supabase/auth-js'
import {
  createSessionCookieStore,
  createSharedLock,
  createSharedSessionStorage,
  followSharedSession,
  type SessionCookieStore,
  sessionNearExpiry,
} from '../sharedSession'
import { FakeCookieBrowser, memoryStorage } from './fakeCookieJar'

/**
 * The shared session through the REAL supabase-js client (the installed
 * auth-js), two hosts in one browser: app.reliantlabs.io and
 * forge.reliantlabs.io, each with its own client, storage view and lease
 * owner, against a fake GoTrue that rotates refresh tokens and flags reuse
 * the way GoTrue's reuse detection does.
 */

const KEY = 'supabase-auth'
const DOMAIN = 'reliantlabs.io'
const URL = 'https://dash.reliantlabs.io'

const b64url = (value: unknown) =>
  btoa(JSON.stringify(value)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const jwt = (sub: string, exp: number) => `${b64url({ alg: 'HS256' })}.${b64url({ sub, exp })}.sig`

const sessionJSON = (n: number, expiresAt: number) => ({
  access_token: jwt('user-1', expiresAt),
  refresh_token: `refresh-${n}`,
  expires_at: expiresAt,
  expires_in: expiresAt - Math.floor(Date.now() / 1000),
  token_type: 'bearer',
  user: { id: 'user-1', aud: 'authenticated', email: 'a@example.com' },
})

class FakeGoTrue {
  active = 'refresh-0'
  rotation = 0
  refreshes: string[] = []
  reused: string[] = []
  signedOut = false

  fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new globalThis.URL(String(input))
    if (url.pathname.endsWith('/token') && url.searchParams.get('grant_type') === 'refresh_token') {
      const { refresh_token } = JSON.parse(String(init?.body)) as { refresh_token: string }
      this.refreshes.push(refresh_token)
      await new Promise((r) => setTimeout(r, 25))
      if (this.signedOut || refresh_token !== this.active) {
        this.reused.push(refresh_token)
        return Response.json({ error_code: 'refresh_token_already_used', msg: 'Invalid Refresh Token' }, { status: 400 })
      }
      this.rotation += 1
      this.active = `refresh-${this.rotation}`
      return Response.json(sessionJSON(this.rotation, Math.floor(Date.now() / 1000) + 3600))
    }
    if (url.pathname.endsWith('/logout')) {
      this.signedOut = true
      return new Response(null, { status: 204 })
    }
    return Response.json({ msg: `unexpected ${url.pathname}` }, { status: 404 })
  }
}

interface Host {
  client: SupabaseClient
  store: SessionCookieStore
  local: Storage
  stop: () => void
}

const stops: (() => void)[] = []
afterEach(() => {
  while (stops.length) stops.pop()?.()
})

const host = (browser: FakeCookieBrowser, name: string, gotrue: FakeGoTrue, { lease = true } = {}): Host => {
  const jar = browser.jar(name)
  const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar })
  const local = memoryStorage()
  const lock: LockFunc = lease
    ? createSharedLock({ name: `${KEY}-lock`, domain: DOMAIN, jar, refreshLikely: sessionNearExpiry(() => store.read()), confirmMs: 10, pollMs: 10 })
    : async (_n, _t, fn) => fn()
  const client = createClient(URL, 'test-anon-key', {
    global: { fetch: gotrue.fetch },
    auth: {
      // As lib/supabase.ts configures it. It also matters to the race: with
      // it on, initialize() refreshes an expiring session under the lock.
      // With it off the first refresh can come from the INITIAL_SESSION
      // emit, which auth-js runs outside its lock (onAuthStateChange
      // does not await _emitInitialSession), so no lock can cover it.
      autoRefreshToken: true,
      persistSession: true,
      detectSessionInUrl: false,
      storage: createSharedSessionStorage({ key: KEY, store, local }),
      storageKey: KEY,
      flowType: 'pkce',
      lock,
    },
  })
  const stop = followSharedSession(client.auth, store)
  stops.push(stop, () => void client.auth.stopAutoRefresh())
  return { client, store, local, stop }
}

const seedExpiring = (browser: FakeCookieBrowser) =>
  createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') }).write(
    JSON.stringify(sessionJSON(0, Math.floor(Date.now() / 1000) + 30)),
  )

describe('shared session through supabase-js', () => {
  it('two hosts loading with an expiring session refresh it ONCE and end up on the same token', async () => {
    const browser = new FakeCookieBrowser()
    const gotrue = new FakeGoTrue()
    seedExpiring(browser)

    const app = host(browser, 'app.reliantlabs.io', gotrue)
    const forge = host(browser, 'forge.reliantlabs.io', gotrue)
    const [a, f] = await Promise.all([app.client.auth.getSession(), forge.client.auth.getSession()])

    expect(gotrue.refreshes).toEqual(['refresh-0'])
    expect(gotrue.reused).toEqual([])
    expect(a.data.session?.refresh_token).toBe('refresh-1')
    expect(f.data.session?.refresh_token).toBe('refresh-1')
  })

  it('without the lease both hosts refresh the same token (the race the lease exists for)', async () => {
    const browser = new FakeCookieBrowser()
    const gotrue = new FakeGoTrue()
    seedExpiring(browser)

    const app = host(browser, 'app.reliantlabs.io', gotrue, { lease: false })
    const forge = host(browser, 'forge.reliantlabs.io', gotrue, { lease: false })
    await Promise.all([app.client.auth.getSession(), forge.client.auth.getSession()])

    expect(gotrue.refreshes).toEqual(['refresh-0', 'refresh-0'])
    expect(gotrue.reused).toEqual(['refresh-0'])
  })

  it('signing out on forge.* signs app.* out: storage is empty and app.* gets SIGNED_OUT', async () => {
    const browser = new FakeCookieBrowser()
    const gotrue = new FakeGoTrue()
    createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') }).write(
      JSON.stringify(sessionJSON(0, Math.floor(Date.now() / 1000) + 3600)),
    )
    const app = host(browser, 'app.reliantlabs.io', gotrue)
    const forge = host(browser, 'forge.reliantlabs.io', gotrue)
    expect((await app.client.auth.getSession()).data.session).not.toBeNull()

    const events: string[] = []
    app.client.auth.onAuthStateChange((event) => {
      events.push(event)
    })

    await forge.client.auth.signOut()
    expect(browser.named(`${KEY}.`)).toEqual([])
    expect((await app.client.auth.getSession()).data.session).toBeNull()

    window.dispatchEvent(new Event('focus'))
    await new Promise((r) => setTimeout(r, 0))
    expect(events).toContain('SIGNED_OUT')
  })

  it('a refresh on app.* is picked up by forge.*: its next getSession and a TOKEN_REFRESHED event', async () => {
    const browser = new FakeCookieBrowser()
    const gotrue = new FakeGoTrue()
    createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') }).write(
      JSON.stringify(sessionJSON(0, Math.floor(Date.now() / 1000) + 3600)),
    )
    const app = host(browser, 'app.reliantlabs.io', gotrue)
    const forge = host(browser, 'forge.reliantlabs.io', gotrue)
    await Promise.all([app.client.auth.getSession(), forge.client.auth.getSession()])

    const events: { event: string; token?: string }[] = []
    forge.client.auth.onAuthStateChange((event, session) => {
      events.push({ event, token: session?.refresh_token })
    })

    await app.client.auth.refreshSession()
    expect((await forge.client.auth.getSession()).data.session?.refresh_token).toBe('refresh-1')

    window.dispatchEvent(new Event('focus'))
    await new Promise((r) => setTimeout(r, 0))
    expect(events).toContainEqual({ event: 'TOKEN_REFRESHED', token: 'refresh-1' })
  })

  it('a sign-in keeps its PKCE code verifier in its own origin, never in the shared cookie', async () => {
    const browser = new FakeCookieBrowser()
    const gotrue = new FakeGoTrue()
    const app = host(browser, 'app.reliantlabs.io', gotrue)
    const forge = host(browser, 'forge.reliantlabs.io', gotrue)

    // Two sign-ins started at once, one per host.
    await app.client.auth.signInWithOAuth({ provider: 'github', options: { skipBrowserRedirect: true } })
    await forge.client.auth.signInWithOAuth({ provider: 'github', options: { skipBrowserRedirect: true } })

    const appVerifier = app.local.getItem(`${KEY}-code-verifier`)
    const forgeVerifier = forge.local.getItem(`${KEY}-code-verifier`)
    expect(appVerifier).toBeTruthy()
    expect(forgeVerifier).toBeTruthy()
    expect(appVerifier).not.toBe(forgeVerifier)
    expect(browser.cookies.filter((c) => c.name.includes('verifier'))).toEqual([])
  })
})
