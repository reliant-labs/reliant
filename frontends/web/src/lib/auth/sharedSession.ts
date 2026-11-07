// Copyright (c) 2025 Reliant Labs

/**
 * One sign-in session for every host under AUTH_COOKIE_DOMAIN.
 *
 * app.reliantlabs.io and forge.reliantlabs.io serve the same bundle and sign in
 * against the same GoTrue project, but supabase-js keeps its session in
 * localStorage, which is per ORIGIN: signing in on one host left the other
 * signed out (owner decision 2026-11-06: share it through a cookie on the
 * parent domain instead of a one-time handoff).
 *
 * WHAT THIS MODULE PROVIDES, all of it wired by lib/supabase.ts:
 *
 *   resolveAuthStorageMode  which storage this page load uses. The shared
 *                           cookie ONLY on a hosted https page inside the
 *                           configured domain, and NEVER on the demo host or
 *                           in the demo shell (no auth exists there).
 *   createSessionCookieStore  the session JSON as base64url chunks
 *                           `supabase-auth.0..n` on `Domain=<domain>; Path=/;
 *                           Secure; SameSite=Lax`. The session can exceed one
 *                           cookie's 4KB, so it is split into chunks of at
 *                           most CHUNK_SIZE characters, each tagged with a
 *                           write generation so a read that races another
 *                           host's write is detected instead of decoded.
 *   createSharedSessionStorage  the supabase-js storage adapter: the session
 *                           key goes to the cookie, every other key (the PKCE
 *                           code verifier) stays in this origin's
 *                           localStorage, because the OAuth round trip that
 *                           needs it returns to the origin that started it.
 *   migrateLocalSession     moves a pre-existing localStorage session into the
 *                           cookie once, so nobody is signed out by the switch.
 *   createSharedLock        the auth-js `lock`: the same-origin Web Lock, plus
 *                           a cookie lease across hosts while a refresh is
 *                           possible. Web Locks are per origin, so without the
 *                           lease two hosts could rotate the same refresh
 *                           token at once (see createSharedLock).
 *   watchSharedSession      reports a sign-in, refresh or sign-out made by
 *                           ANOTHER host, which auth-js has no event for.
 *
 * THE COST, accepted by the owner: every host under the domain receives the
 * cookie on each request and can read it from script. A host serving
 * third-party or user content under the domain can take the session.
 */

import type { LockFunc, SupportedStorage } from '@supabase/auth-js'

/** Largest chunk payload, in characters (name and attributes stay under ~3.5KB). */
export const CHUNK_SIZE = 3180

/**
 * Cookie lifetime, renewed on every write (each token refresh, about hourly
 * while the app is open). GoTrue refresh tokens carry no expiry of their own;
 * they live until used, revoked or timed out by the project's session
 * settings, so the cookie tracks the refresh token by rolling with it. 400
 * days is the longest lifetime Chrome honours.
 */
export const COOKIE_MAX_AGE_SECONDS = 400 * 24 * 60 * 60

/**
 * auth-js refreshes when the access token is within EXPIRY_MARGIN_MS (90s) of
 * expiry on a read, or within 3 auto-refresh ticks of 30s on the ticker. A
 * session further from expiry than this cannot be refreshed by an automatic
 * path, so the cross-host lease is skipped for it.
 */
export const REFRESH_WINDOW_MS = 150_000

/** The cookie jar, as `document.cookie` presents it. */
export interface CookieJar {
  read(): string
  write(cookie: string): void
}

export const documentCookieJar = (doc: Document = document): CookieJar => ({
  read: () => doc.cookie,
  write: (cookie) => {
    doc.cookie = cookie
  },
})

/** The cookies a jar presents, by name; the first of a duplicated name wins. */
export function parseCookies(header: string): Map<string, string> {
  const cookies = new Map<string, string>()
  for (const part of header.split(';')) {
    const eq = part.indexOf('=')
    if (eq < 0) continue
    const name = part.slice(0, eq).trim()
    if (name && !cookies.has(name)) cookies.set(name, part.slice(eq + 1).trim())
  }
  return cookies
}

const randomId = (): string => Math.random().toString(36).slice(2, 10) || '0'

// ── Domain and mode ─────────────────────────────────────────────────────

/**
 * A configured cookie domain as a bare lower-case domain, or "" when it is
 * unset or unusable. Accepts `.reliantlabs.io` and `reliantlabs.io`. A single
 * label (`io`) or an IP address is refused: the first is a public suffix the
 * browser rejects anyway, the second cannot carry a Domain attribute.
 */
export function normalizeCookieDomain(value: string | undefined | null): string {
  const domain = (value ?? '').trim().toLowerCase().replace(/^\.+/, '')
  if (!/^[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(domain)) return ''
  if (/^[0-9.]+$/.test(domain)) return ''
  return domain
}

/** True when `hostname` is the domain or a host under it. */
export function hostInCookieDomain(hostname: string, domain: string): boolean {
  const host = hostname.trim().toLowerCase()
  if (!host || !domain) return false
  return host === domain || host.endsWith(`.${domain}`)
}

export type AuthStorageMode = 'electron' | 'dev-grpc' | 'shared-cookie' | 'local'

export interface AuthStorageInputs {
  electron: boolean
  /** A browser dev server whose backend serves the DevAuth session file. */
  devGrpc: boolean
  /** AUTH_COOKIE_DOMAIN, as configured. */
  cookieDomain: string
  hostname: string
  protocol: string
  /** The demo host (shell.ts demoAppHost()). */
  demoHost: string
  /** True when this page load mounts the demo shell (including ?shell=demo). */
  demoShell: boolean
}

/**
 * Where this page load keeps its session.
 *
 * The shared cookie needs all of: a hosted browser (not Electron, whose
 * app://bundle origin has its own file storage; not a dev server), https (the
 * cookie is Secure), a configured domain the page is inside (so localhost and
 * preview hosts such as *.workspaces.reliantapi.com keep localStorage), and a
 * shell that is not the demo. The demo must never read or write the session:
 * it has no sign-in, and it is the one public page meant for anonymous
 * visitors.
 */
export function resolveAuthStorageMode(i: AuthStorageInputs): AuthStorageMode {
  if (i.electron) return 'electron'
  if (i.devGrpc) return 'dev-grpc'
  const domain = normalizeCookieDomain(i.cookieDomain)
  if (!domain) return 'local'
  if (i.protocol !== 'https:') return 'local'
  const host = i.hostname.trim().toLowerCase()
  if (i.demoShell || (i.demoHost && host === i.demoHost.trim().toLowerCase())) return 'local'
  return hostInCookieDomain(host, domain) ? 'shared-cookie' : 'local'
}

// ── Chunked session cookie ──────────────────────────────────────────────

const encodeValue = (value: string): string => {
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

const decodeValue = (encoded: string): string => {
  let b64 = encoded.replace(/-/g, '+').replace(/_/g, '/')
  while (b64.length % 4) b64 += '='
  const binary = atob(b64)
  const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0))
  return new TextDecoder('utf-8', { fatal: true }).decode(bytes)
}

export interface CookieOptions {
  domain: string
  jar?: CookieJar
}

const attributes = (domain: string, maxAgeSeconds: number): string =>
  `; Domain=${domain}; Path=/; Max-Age=${maxAgeSeconds}; Secure; SameSite=Lax`

export interface SessionCookieStore {
  /** The stored session JSON, or null. A torn read answers the last good value. */
  read(): string | null
  write(value: string): void
  clear(): void
  /**
   * Compares the cookie with what this tab last wrote or observed. `changed`
   * means ANOTHER host or tab wrote or cleared it since. A torn read is never
   * a change: the writer's next chunk is on its way.
   */
  poll(): { changed: boolean; previous: string | null; current: string | null }
}

export interface SessionCookieStoreOptions extends CookieOptions {
  /** The cookie name prefix: supabase-js's storageKey. */
  key: string
  chunkSize?: number
  maxAgeSeconds?: number
  generation?: () => string
}

type ChunkRead = { kind: 'none' } | { kind: 'torn' } | { kind: 'value'; value: string }

export function createSessionCookieStore({
  key,
  domain,
  jar = documentCookieJar(),
  chunkSize = CHUNK_SIZE,
  maxAgeSeconds = COOKIE_MAX_AGE_SECONDS,
  generation = randomId,
}: SessionCookieStoreOptions): SessionCookieStore {
  const chunkName = (i: number) => `${key}.${i}`
  const chunkPattern = new RegExp(`^${key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\.(\\d+)$`)

  const existingChunks = (cookies: Map<string, string>): number[] => {
    const found: number[] = []
    for (const name of cookies.keys()) {
      const m = chunkPattern.exec(name)
      if (m) found.push(Number(m[1]))
    }
    return found
  }

  const set = (name: string, value: string) => jar.write(`${name}=${value}${attributes(domain, maxAgeSeconds)}`)
  const remove = (name: string) => jar.write(`${name}=${attributes(domain, 0)}`)

  const readChunks = (): ChunkRead => {
    const cookies = parseCookies(jar.read())
    const head = cookies.get(chunkName(0))
    if (head === undefined || head === '') return { kind: 'none' }
    const m = /^([a-z0-9]+)~(\d+)~(.*)$/.exec(head)
    if (!m) return { kind: 'torn' }
    const [, gen, countText, first] = m
    const count = Number(countText)
    const parts = [first]
    for (let i = 1; i < count; i++) {
      const chunk = cookies.get(chunkName(i))
      if (!chunk || !chunk.startsWith(`${gen}~`)) return { kind: 'torn' }
      parts.push(chunk.slice(gen.length + 1))
    }
    try {
      return { kind: 'value', value: decodeValue(parts.join('')) }
    } catch {
      return { kind: 'torn' }
    }
  }

  // The last complete value this tab read or wrote: what a torn read answers,
  // so a read racing another host's write never reports "signed out".
  let lastGood: string | null = null
  // What this tab last wrote or observed through poll(): the baseline that
  // tells another host's write from this tab's own.
  let seen: string | null = null

  const initial = readChunks()
  if (initial.kind === 'value') lastGood = seen = initial.value

  return {
    read() {
      const r = readChunks()
      if (r.kind === 'none') {
        lastGood = null
        return null
      }
      if (r.kind === 'torn') return lastGood
      lastGood = r.value
      return r.value
    },
    write(value) {
      const encoded = encodeValue(value)
      const chunks: string[] = []
      for (let i = 0; i < encoded.length; i += chunkSize) chunks.push(encoded.slice(i, i + chunkSize))
      if (chunks.length === 0) chunks.push('')
      const gen = generation()
      const stale = existingChunks(parseCookies(jar.read())).filter((i) => i >= chunks.length)
      // Tail chunks first and the head last: a reader that finds the new head
      // finds its chunks already in place. One that finds the old head beside
      // new chunks sees mismatched generations and answers its last good value.
      for (let i = chunks.length - 1; i >= 1; i--) set(chunkName(i), `${gen}~${chunks[i]}`)
      set(chunkName(0), `${gen}~${chunks.length}~${chunks[0]}`)
      for (const i of stale) remove(chunkName(i))
      lastGood = seen = value
    },
    clear() {
      const present = existingChunks(parseCookies(jar.read()))
      // The head first: from then on every reader sees "no session".
      for (const i of present.sort((a, b) => a - b)) remove(chunkName(i))
      lastGood = seen = null
    },
    poll() {
      const r = readChunks()
      if (r.kind === 'torn') return { changed: false, previous: seen, current: seen }
      const current = r.kind === 'value' ? r.value : null
      const previous = seen
      if (current === previous) return { changed: false, previous, current }
      seen = current
      lastGood = current
      return { changed: true, previous, current }
    },
  }
}

/** Probes that the browser keeps a cookie on `domain` (cookies may be blocked). */
export function cookiesWritable({ domain, jar = documentCookieJar() }: CookieOptions): boolean {
  const name = 'supabase-auth-probe'
  const value = randomId()
  try {
    jar.write(`${name}=${value}${attributes(domain, 60)}`)
    const ok = parseCookies(jar.read()).get(name) === value
    jar.write(`${name}=${attributes(domain, 0)}`)
    return ok
  } catch {
    return false
  }
}

type KeyValueStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

/**
 * The supabase-js storage adapter: `key` (the session) in the shared cookie,
 * anything else (`<key>-code-verifier`) in this origin's localStorage.
 */
export function createSharedSessionStorage({
  key,
  store,
  local,
}: {
  key: string
  store: SessionCookieStore
  local: KeyValueStorage
}): SupportedStorage {
  return {
    getItem: (name) => (name === key ? store.read() : local.getItem(name)),
    setItem: (name, value) => (name === key ? store.write(value) : local.setItem(name, value)),
    removeItem: (name) => (name === key ? store.clear() : local.removeItem(name)),
  }
}

export type MigrationResult = 'none' | 'moved' | 'dropped-local' | 'failed'

/**
 * Moves a localStorage session (written before the cookie existed) into the
 * cookie, then removes the local copy.
 *
 * A cookie session already present wins: it is the shared one, and a local
 * copy beside it is older (this host stopped writing localStorage when the
 * cookie took over). A write the browser refused leaves the local copy in
 * place, so a failed move never signs anyone out.
 */
export function migrateLocalSession({
  key,
  local,
  store,
}: {
  key: string
  local: KeyValueStorage
  store: SessionCookieStore
}): MigrationResult {
  let value: string | null = null
  try {
    value = local.getItem(key)
  } catch {
    return 'none'
  }
  if (value === null) return 'none'
  if (store.read() !== null) {
    local.removeItem(key)
    return 'dropped-local'
  }
  store.write(value)
  if (store.read() !== value) return 'failed'
  local.removeItem(key)
  return 'moved'
}

// ── Cross-host refresh lock ─────────────────────────────────────────────

/** Thrown when the lease is held elsewhere; auth-js reads `isAcquireTimeout`. */
export class SharedLockAcquireTimeoutError extends Error {
  readonly isAcquireTimeout = true
  constructor(message: string) {
    super(message)
    this.name = 'SharedLockAcquireTimeoutError'
  }
}

export interface SharedLockOptions extends CookieOptions {
  /** The lease cookie's name. */
  name: string
  /** True when the stored session is close enough to expiry for a refresh. */
  refreshLikely: () => boolean
  /** The same-origin lock (auth-js navigatorLock) the lease runs inside. */
  localLock?: LockFunc
  ttlMs?: number
  renewMs?: number
  pollMs?: number
  /** How long a claim must survive before it counts (cookie propagation between processes). */
  confirmMs?: number
  owner?: string
  now?: () => number
  sleep?: (ms: number) => Promise<void>
}

/**
 * The auth-js `lock` for the shared session.
 *
 * WHY. GoTrue rotates the refresh token on every refresh and, outside its
 * short reuse interval, treats a second use of a rotated token as theft and
 * revokes the whole session. auth-js serializes refreshes with a Web Lock,
 * but Web Locks are per ORIGIN: a tab on app.* and one on forge.* both near
 * expiry would each read the same refresh token from the shared cookie and
 * both refresh it. The lease makes the second host wait until the first has
 * written the rotated session, which it then reads and does not refresh.
 *
 * HOW. Inside the same-origin lock, and only while a refresh is possible
 * (REFRESH_WINDOW_MS), a lease cookie `<owner>~<expiresAt>` on the shared
 * domain: claim it when free or expired, confirm after `confirmMs` that the
 * claim held, renew it while the operation runs, delete it after. Every other
 * auth operation (each RPC's getSession) skips the lease and costs nothing.
 * A tab that dies holding it frees it within `ttlMs`. When the lease is busy,
 * an acquireTimeout of 0 (auth-js's ticker) throws an error carrying
 * `isAcquireTimeout`, which the ticker treats as "skip this tick"; any other
 * timeout waits, as the Web Lock does.
 *
 * It is advisory, like the lock it extends: the cookie jar has no
 * compare-and-swap. The confirm step closes the window a concurrent claim
 * leaves open, and GoTrue's reuse interval covers what remains. One auth-js
 * path runs outside any lock: the INITIAL_SESSION emit of a late
 * onAuthStateChange (auth-js does not await it). It refreshes only a session
 * that crossed into the expiry margin after initialize(), which refreshes
 * under this lock when autoRefreshToken is on (lib/supabase.ts).
 */
export function createSharedLock({
  name,
  domain,
  jar = documentCookieJar(),
  refreshLikely,
  localLock,
  ttlMs = 10_000,
  renewMs = 3_000,
  pollMs = 50,
  confirmMs = 50,
  owner = randomId(),
  now = Date.now,
  sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
}: SharedLockOptions): LockFunc {
  const readLease = (): { owner: string; expiresAt: number } | null => {
    const value = parseCookies(jar.read()).get(name)
    const m = value ? /^([a-z0-9]+)~(\d+)$/.exec(value) : null
    return m ? { owner: m[1], expiresAt: Number(m[2]) } : null
  }
  const writeLease = () =>
    jar.write(`${name}=${owner}~${now() + ttlMs}${attributes(domain, Math.ceil(ttlMs / 1000) + 1)}`)
  const ours = () => readLease()?.owner === owner
  // This tab's own hold. Every call from this tab shares `owner`, so without
  // it a second call would "claim" the lease the first holds, and the first's
  // release would free it in the middle of the second's refresh.
  let holding = false

  const tryClaim = (): boolean => {
    if (holding) return false
    const lease = readLease()
    if (lease && lease.owner !== owner && lease.expiresAt > now()) return false
    writeLease()
    return ours()
  }

  const acquire = async (acquireTimeout: number): Promise<void> => {
    const deadline = acquireTimeout < 0 ? Infinity : now() + acquireTimeout
    for (;;) {
      if (tryClaim()) {
        holding = true
        await sleep(confirmMs)
        if (ours()) return
        holding = false
      }
      if (acquireTimeout === 0 || now() >= deadline) {
        throw new SharedLockAcquireTimeoutError(`Acquiring the shared auth lease "${name}" timed out`)
      }
      await sleep(pollMs + Math.floor(Math.random() * pollMs))
    }
  }

  const release = () => {
    holding = false
    if (ours()) jar.write(`${name}=${attributes(domain, 0)}`)
  }

  const guarded = async <R>(acquireTimeout: number, fn: () => Promise<R>): Promise<R> => {
    if (!refreshLikely()) return fn()
    await acquire(acquireTimeout)
    const renew = setInterval(() => {
      if (ours()) writeLease()
    }, renewMs)
    try {
      return await fn()
    } finally {
      clearInterval(renew)
      release()
    }
  }

  return <R>(lockName: string, acquireTimeout: number, fn: () => Promise<R>): Promise<R> =>
    localLock
      ? localLock(lockName, acquireTimeout, () => guarded(acquireTimeout, fn))
      : guarded(acquireTimeout, fn)
}

/** The refresh-window test createSharedLock needs, over the stored session JSON. */
export const sessionNearExpiry =
  (read: () => string | null, now: () => number = Date.now) =>
  (): boolean => {
    const value = read()
    if (!value) return false
    try {
      const expiresAt = (JSON.parse(value) as { expires_at?: unknown }).expires_at
      if (typeof expiresAt !== 'number') return true
      return expiresAt * 1000 - now() < REFRESH_WINDOW_MS
    } catch {
      return false
    }
  }

// ── Another host's change ───────────────────────────────────────────────

export type SharedSessionEvent = 'SIGNED_IN' | 'SIGNED_OUT' | 'TOKEN_REFRESHED'

/** The auth event that describes a change from `previous` to `current` session JSON. */
export function classifySessionChange(previous: string | null, current: string | null): SharedSessionEvent {
  if (current === null) return 'SIGNED_OUT'
  if (previous === null) return 'SIGNED_IN'
  const userId = (value: string): string | undefined => {
    try {
      return (JSON.parse(value) as { user?: { id?: string } }).user?.id
    } catch {
      return undefined
    }
  }
  const before = userId(previous)
  return before !== undefined && before === userId(current) ? 'TOKEN_REFRESHED' : 'SIGNED_IN'
}

/**
 * Watches the shared cookie for another host's sign-in, refresh or sign-out.
 *
 * auth-js reads storage on every getSession(), so RPCs always carry the
 * current token; what it lacks is an EVENT. Without one, signing out on
 * forge.* left app.* rendering a signed-in UI whose every call then failed,
 * and a refresh on one host left the other's auth store holding the old
 * session. Checked on focus, on becoming visible, and every `intervalMs`
 * while visible; a check is a cookie read and a string compare.
 */
export function watchSharedSession({
  store,
  onChange,
  win = window,
  doc = document,
  intervalMs = 5_000,
}: {
  store: SessionCookieStore
  onChange: (event: SharedSessionEvent, current: string | null) => void
  win?: Window
  doc?: Document
  intervalMs?: number
}): () => void {
  const check = () => {
    const { changed, previous, current } = store.poll()
    if (changed) onChange(classifySessionChange(previous, current), current)
  }
  const onVisibility = () => {
    if (doc.visibilityState === 'visible') check()
  }
  const timer = setInterval(() => {
    if (doc.visibilityState !== 'hidden') check()
  }, intervalMs)
  win.addEventListener('focus', check)
  win.addEventListener('pageshow', check)
  doc.addEventListener('visibilitychange', onVisibility)
  return () => {
    clearInterval(timer)
    win.removeEventListener('focus', check)
    win.removeEventListener('pageshow', check)
    doc.removeEventListener('visibilitychange', onVisibility)
  }
}

/** The one auth-js internal followSharedSession needs. */
interface AuthSubscribers {
  _notifyAllSubscribers?: (event: string, session: unknown, broadcast?: boolean) => Promise<void>
}

/**
 * Turns another host's change to the shared cookie into an auth event on
 * `auth` (a GoTrueClient), so authStore and every onAuthStateChange listener
 * follow it.
 *
 * auth-js has no public way to emit an event for a session written by
 * someone else: `_notifyAllSubscribers` is what its own BroadcastChannel path
 * runs for another same-origin tab. sharedSession.client.test.ts drives it
 * through the installed auth-js, so an upgrade that drops it fails there.
 * broadcast=false: same-origin tabs watch the cookie themselves.
 */
export function followSharedSession(
  auth: unknown,
  store: SessionCookieStore,
  options: { win?: Window; doc?: Document; intervalMs?: number } = {},
): () => void {
  const subscribers = auth as AuthSubscribers
  return watchSharedSession({
    ...options,
    store,
    onChange: (event, current) => {
      if (typeof subscribers._notifyAllSubscribers !== 'function') return
      let session: unknown = null
      try {
        session = current ? JSON.parse(current) : null
      } catch {
        return
      }
      void subscribers._notifyAllSubscribers(event, session, false).catch((error: unknown) => {
        console.error('[SharedSession] A listener failed on', event, error)
      })
    },
  })
}
