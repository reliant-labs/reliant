/**
 * The hosted preview is inert. It serves prod's own build, so the real
 * sign-in and API code is in it; this proves that code reaches nothing.
 *
 * The page is `vite preview` of dist/ (the production build), with config.js
 * set to EXACTLY the preview env's rendered runtime config:
 * deploy/kcl/preview/runtime_config.golden.json, which internal/forgetest
 * TestThePreviewEnvRendersFixtureMode holds equal to the render. There is no
 * network shim, no fixed clock and no seeded storage: a first visit, as the
 * preview's visitors get it. Every request the browser makes off the page
 * origin is aborted and recorded, as is every WebSocket and every
 * sendBeacon. For each surface it asserts:
 *
 *   - zero requests left the page origin (fetch, XHR, WebSocket, sendBeacon);
 *   - Supabase's `/auth/v1/*` is answered in the page: the signed-in user
 *     comes back from the configured (fake) origin with no request sent;
 *   - the page is marked noindex;
 *   - no page error and no RPC escaped the fixture transport.
 *
 * Then it makes real writes through the UI, each on a fresh page, and
 * asserts the fail-closed read-only guard refused every one, still with
 * nothing sent off the page:
 *
 *   - sending a chat message from the home composer (ChatService/StartChat);
 *     the message goes back in the box and the page says changes are off;
 *   - settings saves: a palette pick and the monospace switch, each one
 *     SettingsService/BatchUpsertSettings.
 *
 * `@harness`: it runs once, in desktop-dark, and takes no screenshot.
 */
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test, type Page } from '@playwright/test'

const here = dirname(fileURLToPath(import.meta.url))
/** The preview env's config.js, as forge renders it. */
const CONFIG = JSON.parse(readFileSync(resolve(here, '../../../../deploy/kcl/preview/runtime_config.golden.json'), 'utf8')) as Record<string, string>

const SURFACES = ['/', '/projects', '/forge/p/shop', '/forge/p/shop/observability', '/settings', '/settings/appearance']

interface Probe {
  ready: boolean
  started: number
  inflight: number
  refused: string[]
  escaped: string[]
  outside: string[]
}

const readProbe = (page: Page) => page.evaluate(() => (window as unknown as { __forgeFixtures?: Probe }).__forgeFixtures ?? null)

/** No call in flight and none started for `quietMs`, or the deadline. */
async function quiet(page: Page, { deadlineMs = 20_000, quietMs = 600 } = {}): Promise<void> {
  const begin = performance.now()
  let last = -1
  let quietSince = 0
  while (performance.now() - begin < deadlineMs) {
    const now = performance.now() - begin
    const probe = await readProbe(page)
    if (!probe || probe.started !== last || probe.inflight > 0) {
      last = probe?.started ?? -1
      quietSince = now
    } else if (now - quietSince >= quietMs) {
      return
    }
    await new Promise((r) => setTimeout(r, 100))
  }
}

/**
 * Serve the preview's config.js; abort and record every request off the page
 * origin, every WebSocket, and every sendBeacon.
 */
async function preview(page: Page, origin: string) {
  const egress: string[] = []
  const pageErrors: string[] = []
  page.on('pageerror', (err) => pageErrors.push(err.message))
  await page.context().route(/.*/, (route) => {
    const req = route.request()
    if (new URL(req.url()).origin === origin) return route.continue()
    egress.push(`${req.resourceType()} ${req.method()} ${req.url()}`)
    return route.abort('blockedbyclient')
  })
  await page.routeWebSocket(/.*/, (ws) => {
    egress.push(`websocket ${ws.url()}`)
    void ws.close()
  })
  await page.route('**/config.js', (route) =>
    route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify(CONFIG)};` }),
  )
  await page.addInitScript(() => {
    const beacons: string[] = []
    ;(window as unknown as { __previewBeacons: string[] }).__previewBeacons = beacons
    const send = navigator.sendBeacon?.bind(navigator)
    if (send) {
      navigator.sendBeacon = (url, data) => {
        beacons.push(String(url))
        return send(url, data)
      }
    }
  })
  return {
    egress,
    pageErrors,
    beacons: () => page.evaluate(() => (window as unknown as { __previewBeacons: string[] }).__previewBeacons),
  }
}

for (const path of SURFACES) {
  test(`the preview loads ${path} with nothing sent off the page`, { tag: '@harness' }, async ({ page, baseURL }) => {
    const origin = new URL(baseURL!).origin
    const net = await preview(page, origin)

    await page.goto(path)
    await expect.poll(async () => (await readProbe(page))?.ready ?? false, { timeout: 20_000 }).toBe(true)
    await quiet(page)

    // The sign-in code asks the configured Supabase origin; the page answers it.
    const auth = await page.evaluate(async ({ url, key }) => {
      const res = await fetch(`${url}/auth/v1/user`, { headers: { apikey: key } })
      const stored = JSON.parse(localStorage.getItem('supabase-auth') ?? 'null') as { access_token?: string } | null
      return { status: res.status, user: (await res.json()) as { email?: string }, token: stored?.access_token ?? '' }
    }, { url: CONFIG.SUPABASE_URL, key: CONFIG.SUPABASE_ANON_KEY })
    expect(auth.status, '/auth/v1/user answered in the page').toBe(200)
    expect(auth.user.email, 'the pack user is signed in').toBeTruthy()
    expect(auth.token.split('.')[2], 'the session is the fixture one, signed by nobody').toBe('Zml4dHVyZXM')

    const probe = (await readProbe(page))!
    expect.soft(net.egress, 'requests that left the page origin (fetch, XHR, WebSocket, beacon)').toEqual([])
    expect.soft((await net.beacons()).filter((u) => new URL(u, origin).origin !== origin), 'sendBeacon off the page origin').toEqual([])
    expect.soft(probe.escaped, 'rpcs that reached fetch').toEqual([])
    expect.soft(probe.outside, 'fetches the boundary refused').toEqual([])
    expect.soft(net.pageErrors, 'page errors').toEqual([])
    expect(await page.locator('meta[name="robots"]').getAttribute('content'), 'the preview is noindex').toBe('noindex, nofollow')
  })
}

test('the preview refuses real writes from the UI, with nothing sent off the page', { tag: '@harness' }, async ({ page, baseURL }) => {
  const origin = new URL(baseURL!).origin
  const net = await preview(page, origin)
  const write = 'reliant.v1.SettingsService/BatchUpsertSettings'
  const refusals = async () => ((await readProbe(page))?.refused ?? []).filter((rpc) => rpc === write).length

  await page.goto('/settings/appearance')
  await expect.poll(async () => (await readProbe(page))?.ready ?? false, { timeout: 20_000 }).toBe(true)
  await quiet(page)

  // A palette pick saves the setting.
  let before = await refusals()
  await page.getByRole('radio', { name: /Moonlit Slate/ }).click()
  await expect.poll(refusals, { message: `${write} from the palette, refused` }).toBeGreaterThan(before)

  // So does a switch.
  before = await refusals()
  await page.getByRole('switch', { name: /Separate monospace font/ }).click()
  await expect.poll(refusals, { message: `${write} from the monospace switch, refused` }).toBeGreaterThan(before)

  await quiet(page)
  const probe = (await readProbe(page))!
  expect(probe.refused.every((rpc) => rpc === write), `only the writes made: ${probe.refused.join(', ')}`).toBe(true)
  expect.soft(net.egress, 'requests that left the page origin').toEqual([])
  expect.soft(probe.escaped, 'rpcs that reached fetch').toEqual([])
  expect.soft(probe.outside, 'fetches the boundary refused').toEqual([])
  expect.soft(net.pageErrors, 'page errors').toEqual([])
})

test('the preview refuses sending a chat message, with nothing sent off the page', { tag: '@harness' }, async ({ page, baseURL }) => {
  const origin = new URL(baseURL!).origin
  const net = await preview(page, origin)
  const write = 'reliant.v1.ChatService/StartChat'
  const message = 'Add a coupon field to checkout'

  await page.goto('/')
  await expect.poll(async () => (await readProbe(page))?.ready ?? false, { timeout: 20_000 }).toBe(true)
  await quiet(page)

  const composer = page.getByTestId('chat-input')
  await composer.fill(message)
  await composer.press('Enter')
  await expect.poll(async () => (await readProbe(page))?.refused ?? [], { message: `${write}, refused` }).toContain(write)
  // Nothing was sent: the message is back in the box.
  await expect(composer).toHaveValue(message)

  await quiet(page)
  const probe = (await readProbe(page))!
  expect.soft(net.egress, 'requests that left the page origin').toEqual([])
  expect.soft(probe.escaped, 'rpcs that reached fetch').toEqual([])
  expect.soft(probe.outside, 'fetches the boundary refused').toEqual([])
  expect.soft(net.pageErrors, 'page errors').toEqual([])
})
