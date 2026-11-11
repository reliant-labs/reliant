import { afterEach, describe, expect, it, vi } from 'vitest'
import type { LockFunc } from '@supabase/auth-js'
import {
  CHUNK_SIZE,
  COOKIE_MAX_AGE_SECONDS,
  type AuthStorageInputs,
  classifySessionChange,
  cookiesWritable,
  createSessionCookieStore,
  createSharedLock,
  createSharedSessionStorage,
  hostInCookieDomain,
  migrateLocalSession,
  normalizeCookieDomain,
  resolveAuthStorageMode,
  sessionNearExpiry,
  SharedLockAcquireTimeoutError,
  watchSharedSession,
} from '../sharedSession'
import { FakeCookieBrowser, memoryStorage } from './fakeCookieJar'

const KEY = 'supabase-auth'
const DOMAIN = 'reliantlabs.io'

// Read once: a session() built a second later must still equal the one
// stored, or an assertion straddling a second boundary fails at random.
const EXPIRES_AT = Math.floor(Date.now() / 1000) + 3600

const session = (overrides: Record<string, unknown> = {}) =>
  JSON.stringify({
    access_token: 'access-1',
    refresh_token: 'refresh-1',
    expires_at: EXPIRES_AT,
    token_type: 'bearer',
    user: { id: 'user-1', email: 'a@example.com' },
    ...overrides,
  })

/** A session too large for one cookie, as a GitHub-linked user's is. */
const bigSession = () =>
  session({
    user: {
      id: 'user-1',
      identities: Array.from({ length: 12 }, (_, i) => ({
        provider: 'github',
        identity_data: { avatar_url: `https://avatars.example/${i}`, bio: 'ünïcødé '.repeat(40) },
      })),
    },
  })

const hosted = (overrides: Partial<AuthStorageInputs> = {}): AuthStorageInputs => ({
  electron: false,
  devGrpc: false,
  cookieDomain: '.reliantlabs.io',
  hostname: 'app.reliantlabs.io',
  protocol: 'https:',
  demoHost: 'demo.reliantlabs.io',
  demoShell: false,
  ...overrides,
})

describe('cookie domain', () => {
  it('normalizes a leading dot and refuses a single label or an IP', () => {
    expect(normalizeCookieDomain('.ReliantLabs.io ')).toBe(DOMAIN)
    expect(normalizeCookieDomain('reliantlabs.io')).toBe(DOMAIN)
    expect(normalizeCookieDomain('io')).toBe('')
    expect(normalizeCookieDomain('10.0.0.1')).toBe('')
    expect(normalizeCookieDomain('')).toBe('')
    expect(normalizeCookieDomain(undefined)).toBe('')
  })

  it('matches the domain and hosts under it, not look-alikes', () => {
    expect(hostInCookieDomain('app.reliantlabs.io', DOMAIN)).toBe(true)
    expect(hostInCookieDomain('reliantlabs.io', DOMAIN)).toBe(true)
    expect(hostInCookieDomain('evilreliantlabs.io', DOMAIN)).toBe(false)
    expect(hostInCookieDomain('app.reliantlabs.io.evil.com', DOMAIN)).toBe(false)
  })
})

describe('resolveAuthStorageMode', () => {
  it('shares the cookie on app.* and forge.* when the domain is configured', () => {
    expect(resolveAuthStorageMode(hosted())).toBe('shared-cookie')
    expect(resolveAuthStorageMode(hosted({ hostname: 'forge.reliantlabs.io' }))).toBe('shared-cookie')
  })

  it('keeps per-origin storage when AUTH_COOKIE_DOMAIN is unset (the default)', () => {
    expect(resolveAuthStorageMode(hosted({ cookieDomain: '' }))).toBe('local')
  })

  it('never uses the cookie on the demo host or in the demo shell', () => {
    expect(resolveAuthStorageMode(hosted({ hostname: 'demo.reliantlabs.io' }))).toBe('local')
    expect(resolveAuthStorageMode(hosted({ hostname: 'DEMO.reliantlabs.io' }))).toBe('local')
    // ?shell=demo on app.* mounts the demo, which has no auth either.
    expect(resolveAuthStorageMode(hosted({ demoShell: true }))).toBe('local')
  })

  it('keeps Electron, dev servers, localhost, preview hosts and plain http on their own storage', () => {
    expect(resolveAuthStorageMode(hosted({ electron: true }))).toBe('electron')
    expect(resolveAuthStorageMode(hosted({ devGrpc: true }))).toBe('dev-grpc')
    expect(resolveAuthStorageMode(hosted({ hostname: 'localhost' }))).toBe('local')
    expect(resolveAuthStorageMode(hosted({ hostname: 'web-3000.ab12.workspaces.reliantapi.com' }))).toBe('local')
    expect(resolveAuthStorageMode(hosted({ protocol: 'http:' }))).toBe('local')
    expect(resolveAuthStorageMode(hosted({ protocol: 'app:', hostname: 'bundle' }))).toBe('local')
  })
})

describe('session cookie store', () => {
  it('writes Secure, SameSite=Lax cookies on the parent domain with a rolling lifetime', () => {
    const browser = new FakeCookieBrowser()
    const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') })
    store.write(session())
    const [head] = browser.named(`${KEY}.`)
    expect(head.domain).toBe(DOMAIN)
    expect(head.hostOnly).toBe(false)
    expect(head.attributes).toMatchObject({
      path: '/',
      secure: true,
      samesite: 'Lax',
      'max-age': String(COOKIE_MAX_AGE_SECONDS),
    })
    expect(head.attributes.httponly).toBeUndefined()
  })

  it('chunks a session larger than one cookie and every host in the domain reads it back', () => {
    const browser = new FakeCookieBrowser()
    const app = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') })
    const value = bigSession()
    expect(value.length).toBeGreaterThan(4096)
    app.write(value)

    const chunks = browser.named(`${KEY}.`)
    expect(chunks.length).toBeGreaterThan(1)
    for (const c of browser.writes) {
      // name=value plus attributes: the whole Set-Cookie stays under ~3.5KB.
      expect(c.cookie.length).toBeLessThan(3500)
    }
    for (const c of chunks) expect(c.value.length).toBeLessThanOrEqual(CHUNK_SIZE + 20)

    const forge = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('forge.reliantlabs.io') })
    expect(forge.read()).toBe(value)
    // A host outside the domain sees nothing.
    const other = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('reliantapi.com') })
    expect(other.read()).toBeNull()
  })

  it('removes chunks a smaller session no longer needs, and clear removes them all', () => {
    const browser = new FakeCookieBrowser()
    const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') })
    store.write(bigSession())
    expect(browser.named(`${KEY}.`).length).toBeGreaterThan(1)
    store.write(session())
    expect(browser.named(`${KEY}.`).map((c) => c.name)).toEqual([`${KEY}.0`])
    expect(store.read()).toBe(session())
    store.clear()
    expect(browser.named(`${KEY}.`)).toEqual([])
    expect(store.read()).toBeNull()
  })

  it('answers the last good value for a read that races another host\'s chunked write', () => {
    const browser = new FakeCookieBrowser()
    const forgeJar = browser.jar('forge.reliantlabs.io')
    const app = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') })
    const forge = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: forgeJar })
    const first = bigSession()
    app.write(first)
    expect(forge.read()).toBe(first)

    // Half of another write: a new tail chunk beside the old head.
    const head = browser.named(`${KEY}.0`)[0].value
    forgeJar.write(`${KEY}.1=zzzz~AAAA; Domain=${DOMAIN}; Path=/; Secure`)
    expect(browser.named(`${KEY}.0`)[0].value).toBe(head)
    expect(forge.read()).toBe(first)
    // ...and a torn state is never reported as another host's change.
    expect(forge.poll().changed).toBe(false)
  })
})

describe('shared session storage adapter', () => {
  it('keeps the session in the cookie and the PKCE verifier in this origin', () => {
    const browser = new FakeCookieBrowser()
    const local = memoryStorage()
    const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('forge.reliantlabs.io') })
    const storage = createSharedSessionStorage({ key: KEY, store, local })
    void storage.setItem(KEY, session())
    void storage.setItem(`${KEY}-code-verifier`, 'verifier')
    expect(local.map.has(KEY)).toBe(false)
    expect(local.getItem(`${KEY}-code-verifier`)).toBe('verifier')
    expect(browser.named(`${KEY}-code-verifier`)).toEqual([])
    expect(storage.getItem(KEY)).toBe(session())
    void storage.removeItem(KEY)
    expect(storage.getItem(KEY)).toBeNull()
  })
})

describe('migrateLocalSession', () => {
  const setup = (host = 'app.reliantlabs.io') => {
    const browser = new FakeCookieBrowser()
    const local = memoryStorage()
    const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar(host) })
    return { browser, local, store }
  }

  it('moves a signed-in localStorage session into the cookie and drops the local copy', () => {
    const { local, store, browser } = setup()
    local.setItem(KEY, session())
    expect(migrateLocalSession({ key: KEY, local, store })).toBe('moved')
    expect(local.getItem(KEY)).toBeNull()
    expect(store.read()).toBe(session())
    // The other host is now signed in too.
    expect(createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('forge.reliantlabs.io') }).read()).toBe(
      session(),
    )
  })

  it('keeps the cookie session when both exist (the shared one is newer)', () => {
    const { local, store } = setup()
    store.write(session({ access_token: 'shared' }))
    local.setItem(KEY, session({ access_token: 'stale' }))
    expect(migrateLocalSession({ key: KEY, local, store })).toBe('dropped-local')
    expect(JSON.parse(store.read() as string).access_token).toBe('shared')
    expect(local.getItem(KEY)).toBeNull()
  })

  it('leaves the local session in place when the browser refuses the cookie', () => {
    const browser = new FakeCookieBrowser()
    const local = memoryStorage()
    // http: the Secure cookie is refused, as a browser blocking cookies would.
    const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io', { https: false }) })
    local.setItem(KEY, session())
    expect(migrateLocalSession({ key: KEY, local, store })).toBe('failed')
    expect(local.getItem(KEY)).toBe(session())
  })

  it('keeps the local session when the probe passes but a chunk of the session is refused', () => {
    const browser = new FakeCookieBrowser()
    const real = browser.jar('app.reliantlabs.io')
    // The small probe cookie fits; the session's second chunk is over quota.
    const jar = {
      read: real.read,
      write: (cookie: string) => {
        if (cookie.startsWith(`${KEY}.1=`) && !cookie.includes('Max-Age=0')) return
        real.write(cookie)
      },
    }
    expect(cookiesWritable({ domain: DOMAIN, jar })).toBe(true)
    const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar })
    const local = memoryStorage()
    const value = bigSession()
    local.setItem(KEY, value)

    expect(migrateLocalSession({ key: KEY, local, store })).toBe('failed')
    expect(local.getItem(KEY)).toBe(value)
    // The torn chunks the write left are gone, so no host reads half a session.
    expect(browser.named(`${KEY}.`)).toEqual([])
  })

  it('does nothing for a signed-out visitor', () => {
    const { local, store } = setup()
    expect(migrateLocalSession({ key: KEY, local, store })).toBe('none')
    expect(store.read()).toBeNull()
  })

  it('cookiesWritable reports a refusing browser so the page keeps localStorage', () => {
    const browser = new FakeCookieBrowser()
    expect(cookiesWritable({ domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') })).toBe(true)
    expect(cookiesWritable({ domain: DOMAIN, jar: browser.jar('app.reliantlabs.io', { https: false }) })).toBe(false)
    expect(browser.cookies).toEqual([])
  })
})

describe('sign-out and refresh across hosts', () => {
  afterEach(() => vi.useRealTimers())

  it('a sign-out on forge.* is reported on app.* as SIGNED_OUT', () => {
    vi.useFakeTimers()
    const browser = new FakeCookieBrowser()
    const app = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') })
    app.write(session())
    const forge = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('forge.reliantlabs.io') })
    const onChange = vi.fn()
    const stop = watchSharedSession({ store: app, onChange, intervalMs: 1000 })

    forge.clear()
    expect(app.read()).toBeNull()
    vi.advanceTimersByTime(1000)
    expect(onChange).toHaveBeenCalledWith('SIGNED_OUT', null)
    stop()
  })

  it('a refresh on one host is TOKEN_REFRESHED on the other, on focus; its own writes are not reported', () => {
    const browser = new FakeCookieBrowser()
    const app = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('app.reliantlabs.io') })
    const forge = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: browser.jar('forge.reliantlabs.io') })
    app.write(session())
    const onChange = vi.fn()
    const stop = watchSharedSession({ store: app, onChange })

    app.write(session({ access_token: 'own-refresh' }))
    window.dispatchEvent(new Event('focus'))
    expect(onChange).not.toHaveBeenCalled()

    const refreshed = session({ access_token: 'access-2', refresh_token: 'refresh-2' })
    forge.write(refreshed)
    window.dispatchEvent(new Event('focus'))
    expect(onChange).toHaveBeenCalledWith('TOKEN_REFRESHED', refreshed)
    stop()
  })

  it('classifies a change by its user', () => {
    expect(classifySessionChange(null, session())).toBe('SIGNED_IN')
    expect(classifySessionChange(session(), null)).toBe('SIGNED_OUT')
    expect(classifySessionChange(session(), session({ access_token: 'x' }))).toBe('TOKEN_REFRESHED')
    expect(classifySessionChange(session(), session({ user: { id: 'user-2' } }))).toBe('SIGNED_IN')
  })
})

describe('createSharedLock', () => {
  const nearExpiry = () => session({ expires_at: Math.floor(Date.now() / 1000) + 30 })

  it('lets only one host refresh a token both see expiring; the other then reads the rotated one', async () => {
    const browser = new FakeCookieBrowser()
    const hosts = ['app.reliantlabs.io', 'forge.reliantlabs.io'].map((host) => {
      const jar = browser.jar(host)
      const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar })
      const lock = createSharedLock({
        name: `${KEY}-lock`,
        domain: DOMAIN,
        jar,
        refreshLikely: sessionNearExpiry(() => store.read()),
        confirmMs: 5,
        pollMs: 5,
      })
      return { store, lock }
    })
    hosts[0].store.write(nearExpiry())

    const used: string[] = []
    let rotation = 0
    // auth-js's refresh-if-expiring, as each host runs it inside the lock.
    const refreshIfNeeded = (store: (typeof hosts)[number]['store']) => async () => {
      const current = JSON.parse(store.read() as string)
      if (current.expires_at * 1000 - Date.now() > 90_000) return current.access_token as string
      used.push(current.refresh_token)
      await new Promise((r) => setTimeout(r, 20))
      rotation += 1
      store.write(
        session({
          access_token: `access-r${rotation}`,
          refresh_token: `refresh-r${rotation}`,
          expires_at: Math.floor(Date.now() / 1000) + 3600,
        }),
      )
      return `access-r${rotation}`
    }

    const tokens = await Promise.all(hosts.map((h) => h.lock('lock:supabase-auth', 10_000, refreshIfNeeded(h.store))))
    expect(used).toEqual(['refresh-1'])
    expect(tokens).toEqual(['access-r1', 'access-r1'])
    // The lease is released.
    expect(browser.named(`${KEY}-lock`)).toEqual([])
  })

  it('serializes two calls from the same tab: the second never runs inside the first\'s lease', async () => {
    const browser = new FakeCookieBrowser()
    const jar = browser.jar('app.reliantlabs.io')
    const lock = createSharedLock({ name: `${KEY}-lock`, domain: DOMAIN, jar, refreshLikely: () => true, confirmMs: 2, pollMs: 2 })
    let inside = 0
    let overlap = false
    const op = async () => {
      inside += 1
      if (inside > 1) overlap = true
      await new Promise((r) => setTimeout(r, 15))
      inside -= 1
    }
    await Promise.all([lock('lock:supabase-auth', 10_000, op), lock('lock:supabase-auth', 10_000, op)])
    expect(overlap).toBe(false)
    expect(browser.named(`${KEY}-lock`)).toEqual([])
  })

  it('makes the auto-refresh tick (timeout 0) skip while another host holds the lease', async () => {
    const browser = new FakeCookieBrowser()
    const appJar = browser.jar('app.reliantlabs.io')
    const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar: appJar })
    store.write(nearExpiry())
    browser
      .jar('forge.reliantlabs.io')
      .write(`${KEY}-lock=other~${Date.now() + 10_000}; Domain=${DOMAIN}; Path=/; Max-Age=11; Secure; SameSite=Lax`)
    const lock = createSharedLock({
      name: `${KEY}-lock`,
      domain: DOMAIN,
      jar: appJar,
      refreshLikely: sessionNearExpiry(() => store.read()),
    })
    const fn = vi.fn(async () => 'ran')
    const error = await lock('lock:supabase-auth', 0, fn).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(SharedLockAcquireTimeoutError)
    expect((error as { isAcquireTimeout: boolean }).isAcquireTimeout).toBe(true)
    expect(fn).not.toHaveBeenCalled()
  })

  it('takes over a lease whose holder died once it expires', async () => {
    const browser = new FakeCookieBrowser()
    const jar = browser.jar('app.reliantlabs.io')
    let clock = Date.now()
    jar.write(`${KEY}-lock=dead~${clock + 100}; Domain=${DOMAIN}; Path=/; Max-Age=11; Secure`)
    const lock = createSharedLock({
      name: `${KEY}-lock`,
      domain: DOMAIN,
      jar,
      refreshLikely: () => true,
      now: () => clock,
      sleep: async (ms) => {
        clock += ms
      },
    })
    await expect(lock('lock:supabase-auth', -1, async () => 'ran')).resolves.toBe('ran')
  })

  it('skips the lease when no refresh is possible, so an RPC\'s getSession costs nothing', async () => {
    const browser = new FakeCookieBrowser()
    const jar = browser.jar('app.reliantlabs.io')
    const store = createSessionCookieStore({ key: KEY, domain: DOMAIN, jar })
    store.write(session())
    const writes = browser.writes.length
    const lock = createSharedLock({ name: `${KEY}-lock`, domain: DOMAIN, jar, refreshLikely: sessionNearExpiry(() => store.read()) })
    await expect(lock('lock:supabase-auth', 10_000, async () => 'ran')).resolves.toBe('ran')
    expect(browser.writes.length).toBe(writes)
  })

  it('runs inside the same-origin lock when one is given', async () => {
    const browser = new FakeCookieBrowser()
    const calls: [string, number][] = []
    const localLock: LockFunc = async (name, timeout, fn) => {
      calls.push([name, timeout])
      return fn()
    }
    const lock = createSharedLock({
      name: `${KEY}-lock`,
      domain: DOMAIN,
      jar: browser.jar('app.reliantlabs.io'),
      refreshLikely: () => false,
      localLock,
    })
    await lock('lock:supabase-auth', 5, async () => 1)
    expect(calls).toEqual([['lock:supabase-auth', 5]])
  })
})
