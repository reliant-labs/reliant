/**
 * The in-page half of the visual harness: a fetch-level fake network,
 * installed by `page.addInitScript` before the app's own scripts run.
 *
 * Ported from the November UX review's harness (research/app-ux-review-2026-11/
 * harness/engine.js, documented in dev-docs/design/app-ux-review-2026-11/
 * HARNESS.md). The difference is where the fixtures live: the page forwards
 * every Connect RPC to Node through the `__visualRpc` binding, and Node
 * answers from typed fixtures (fixtures/*.ts), encoding binary-proto streams
 * with the app's own generated schemas.
 *
 * Why a fetch shim and not `page.route`: `route.fulfill` sends a whole body
 * and closes the response, so a live subscription (StreamUserUpdates,
 * StreamChatUpdates) would end on every load and the app would reconnect on a
 * backoff timer — a screenshot taken during that is a flake. Here a stream
 * without `end` stays open, exactly as an idle live subscription does.
 *
 * This function is serialized into the page: it may not close over anything.
 */
export interface ShimOptions {
  /** The fixed clock, epoch ms (page.clock.setFixedTime holds Date at it). */
  now: number
  theme: 'light' | 'dark'
  user: { id: string; email: string; name: string }
}

export function networkShim(opts: ShimOptions): void {
  type Reply = {
    status: number
    contentType: string
    body?: string
    frames?: string[]
    end?: boolean
    pending?: boolean
  }
  const w = window as unknown as {
    __visualRpc: (call: { key: string; contentType: string; body: string; query: string | null }) => Promise<Reply>
    __visualInflight: number
    __visualStarted: number
  }
  w.__visualInflight = 0
  w.__visualStarted = 0

  // Deterministic randomness: skeleton widths, jitter and generated ids that
  // reach the DOM render the same on every run.
  let seed = 0x2f6b3c1d
  Math.random = () => {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0
    return seed / 4294967296
  }

  const toB64 = (bytes: Uint8Array): string => {
    let s = ''
    for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
    return btoa(s)
  }
  const fromB64 = (b64: string): Uint8Array => {
    const s = atob(b64)
    const out = new Uint8Array(s.length)
    for (let i = 0; i < s.length; i += 1) out[i] = s.charCodeAt(i)
    return out
  }

  // ── Supabase: a fabricated session, valid for a month past the fixed clock ──
  const secs = Math.floor(opts.now / 1000)
  const b64url = (o: unknown) => btoa(JSON.stringify(o)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_')
  const createdAt = '2026-09-01T12:00:00.000Z'
  const user = {
    id: opts.user.id, aud: 'authenticated', role: 'authenticated', email: opts.user.email,
    email_confirmed_at: createdAt, phone: '', confirmed_at: createdAt, last_sign_in_at: new Date(opts.now).toISOString(),
    app_metadata: { provider: 'email', providers: ['email'] },
    user_metadata: { full_name: opts.user.name, name: opts.user.name },
    identities: [], created_at: createdAt, updated_at: new Date(opts.now).toISOString(), is_anonymous: false,
  }
  const accessToken = [
    b64url({ alg: 'ES256', typ: 'JWT', kid: 'visual' }),
    b64url({ sub: user.id, email: user.email, role: 'authenticated', aud: 'authenticated', exp: secs + 86400 * 30, iat: secs, session_id: 'visual-session', is_anonymous: false }),
    'dmlzdWFs',
  ].join('.')
  const session = { access_token: accessToken, token_type: 'bearer', expires_in: 86400 * 30, expires_at: secs + 86400 * 30, refresh_token: 'visual-refresh', user }

  try {
    localStorage.setItem('supabase-auth', JSON.stringify(session))
    localStorage.setItem('appearance.theme', opts.theme)
    // The Setup Guide (nav footer) is dismissed unless a surface asks for it (harness `storage`, written first).
    if (localStorage.getItem('reliant.checklist.panelState') === null) localStorage.setItem('reliant.checklist.panelState', 'dismissed')
  } catch {
    /* storage disabled: the app falls back to its defaults */
  }

  const json = (status: number, body: unknown) =>
    new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })

  const answerSupabase = (url: URL): Response => {
    if (url.pathname === '/auth/v1/user') return json(200, user)
    if (url.pathname === '/auth/v1/token') return json(200, session)
    if (url.pathname === '/auth/v1/logout') return new Response(null, { status: 204 })
    if (url.pathname === '/auth/v1/settings') return json(200, { external: { email: true, github: true, google: true }, disable_signup: false })
    return json(200, {})
  }

  const readBody = async (input: RequestInfo | URL, init?: RequestInit): Promise<Uint8Array> => {
    const b = init?.body
    if (b != null) {
      if (typeof b === 'string') return new TextEncoder().encode(b)
      if (b instanceof Uint8Array) return b
      if (b instanceof ArrayBuffer) return new Uint8Array(b)
      return new Uint8Array(await new Response(b as BodyInit).arrayBuffer())
    }
    if (input instanceof Request) return new Uint8Array(await input.clone().arrayBuffer())
    return new Uint8Array(0)
  }

  const aborted = () => new DOMException('aborted', 'AbortError')

  const answerRpc = async (key: string, url: URL, input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const signal = init?.signal ?? (input instanceof Request ? input.signal : undefined)
    const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined))
    const contentType = headers.get('content-type') ?? ''
    w.__visualInflight += 1
    w.__visualStarted += 1
    let reply: Reply
    try {
      const body = toB64(await readBody(input, init))
      reply = await w.__visualRpc({ key, contentType, body, query: url.searchParams.get('message') })
    } catch (error) {
      w.__visualInflight -= 1
      throw error
    }
    if (reply.pending) {
      // Never answers, so it stays in flight (settle() then fails at its
      // deadline) until the app aborts it.
      return new Promise<Response>((_resolve, reject) =>
        signal?.addEventListener('abort', () => {
          w.__visualInflight -= 1
          reject(aborted())
        }),
      )
    }
    w.__visualInflight -= 1
    if (!reply.frames) {
      return new Response(reply.body ?? '', { status: reply.status, headers: { 'content-type': reply.contentType } })
    }
    const frames = reply.frames.map(fromB64)
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        for (const frame of frames) controller.enqueue(frame)
        if (reply.end) {
          controller.close()
          return
        }
        signal?.addEventListener('abort', () => {
          try {
            controller.error(aborted())
          } catch {
            /* already closed */
          }
        })
      },
    })
    return new Response(stream, { status: 200, headers: { 'content-type': reply.contentType } })
  }

  const SINKS = /(posthog|sentry|segment|google-analytics|googletagmanager|otel|telemetry|stripe\.com|stripe\.network|hcaptcha|intercom)/i
  const realFetch = window.fetch.bind(window)
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const href = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const url = new URL(href, location.href)
    const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase()
    const rpc = url.pathname.match(/\/([a-z_]\w*(?:\.[a-z_]\w*)*\.v\d\w*(?:\.[a-z_]\w*)*\.[A-Z]\w*)\/([A-Z]\w*)$/)
    if (rpc) return answerRpc(`${rpc[1]}/${rpc[2]}`, url, input, init)
    if (url.hostname.endsWith('supabase.co')) return answerSupabase(url)
    if (/\/(health|healthz|readyz)$/.test(url.pathname)) return json(200, { status: 'ok' })
    if (url.pathname === '/__forge/log' || url.pathname.includes('/v1/traces') || url.pathname.includes('/v1/logs')) {
      return new Response(null, { status: 204 })
    }
    if (SINKS.test(url.hostname)) return new Response(null, { status: 204 })
    if (url.origin === location.origin && method === 'GET') return realFetch(input, init)
    if (url.origin === location.origin) return json(404, { code: 'not_found', message: 'visual harness: no backend' })
    throw new TypeError(`visual harness: network disabled (${url.origin})`)
  }

  // WebSockets (the terminal) never connect: they stay CONNECTING, so no
  // reconnect loop runs on a timer.
  class FakeSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    url: string
    readyState = 0
    bufferedAmount = 0
    protocol = ''
    extensions = ''
    binaryType = 'blob'
    onopen = null
    onclose = null
    onerror = null
    onmessage = null
    constructor(url: string | URL) {
      this.url = String(url)
    }
    send() {}
    close() {
      this.readyState = 3
    }
    addEventListener() {}
    removeEventListener() {}
    dispatchEvent() {
      return true
    }
  }
  ;(window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket
  if (navigator.sendBeacon) navigator.sendBeacon = () => true
}
