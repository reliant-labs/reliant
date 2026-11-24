/**
 * Fixture mode on the production build (src/lib/fixturesConfig.ts,
 * src/fixtures/install.ts): every surface the visual baseline captures, loaded
 * with config.js naming the shop pack (FIXTURES_SCENARIO = "shop", read-only)
 * and NO network shim. The page's own fixture transport answers every RPC
 * behind the app's real interceptors, and outside.ts is the only fetch
 * boundary. Each surface must load with
 *
 *   - no RPC escaped: none reached fetch (`escaped`), and the browser sent no
 *     request off the page's origin;
 *   - no write refused at load (`refused`), but those KNOWN_REFUSALS names;
 *   - no page error;
 *   - no fetch outside the allowed set (`outside`).
 *
 * It takes NO screenshot and touches no baseline: the shop pack replaces the
 * shim's data in the next step (P2.2), which re-baselines once. The surface
 * list is read from surfaces.visual.ts's SURFACES (paths and harness storage),
 * so the two cannot drift; importing that file would register its tests here.
 *
 * Each surface's probe (rpcs, unknown reads, reads passed only by name) is
 * printed as one `[fixture-mode]` JSON line; FIXTURE_MODE_REPORT=<file>
 * also appends them there.
 */
import { appendFileSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test, type Page } from '@playwright/test'

import { FIXED_NOW, IDS } from './fixtures/base'
import { CHAT_IDS } from './fixtures/chat'
import { FILTER_PROJECT_IDS, NO_APP_PROJECT_ID } from './fixtures/projects'

/** The managed product's runtime config, every URL fake, plus forge#2719's fixture keys. */
const CONFIG = {
  AUTH_MODE: 'cloud',
  API_URL: 'https://api.visual.invalid',
  CONTROL_PLANE_API_URL: 'https://cp.visual.invalid',
  APP_URL: 'https://app.visual.invalid',
  GATEWAY_URL: 'https://gw.visual.invalid',
  SUPABASE_URL: 'https://visual.supabase.co',
  SUPABASE_ANON_KEY: 'visual-harness-anon-key',
  STRIPE_PUBLISHABLE_KEY: '',
  OTEL_ENDPOINT: '',
  OTEL_REPLAY: 'false',
  FIXTURES_SCENARIO: 'shop',
  FIXTURES_READ_ONLY: 'true',
}

/**
 * Writes a load is KNOWN to make, by path, each with why it is not the pack's
 * to fix. Asserted exactly: when the app stops making one, this test fails
 * until its entry goes.
 */
const KNOWN_REFUSALS: Record<string, { rpcs: string[]; why: string }> = {
  '/settings/appearance': {
    rpcs: ['reliant.v1.SettingsService/BatchUpsertSettings'],
    why: 'ColorSchemeSelector saves the colour scheme every time the page opens (settingsSync.setSetting, not setSettingIfChanged): an app write on read, fixed in the app (P2.2), not in the pack',
  },
}

interface Load {
  path: string
  storage: Record<string, string>
  surfaces: string[]
}

/** SURFACES' distinct loads (path + storage), from surfaces.visual.ts's source. */
function surfaceLoads(): Load[] {
  const here = dirname(fileURLToPath(import.meta.url))
  const src = readFileSync(resolve(here, 'surfaces.visual.ts'), 'utf8')
  const start = src.indexOf('const SURFACES: Surface[] = [')
  const end = src.indexOf('\n]\n', start)
  if (start < 0 || end < 0) throw new Error('surfaces.visual.ts: SURFACES not found')
  const scope = { IDS, CHAT_IDS, FILTER_PROJECT_IDS, NO_APP_PROJECT_ID }
  const evaluate = (expr: string): unknown => new Function(...Object.keys(scope), `return (${expr})`)(...Object.values(scope))
  const loads = new Map<string, Load>()
  for (const block of src.slice(start, end).split(/\n {2}\{\n/).slice(1)) {
    const name = block.match(/name: '([^']+)'/)?.[1] ?? '?'
    const path = block.match(/\bpath: ('[^']*'|`[^`]*`)/)?.[1]
    if (!path) continue
    const storageExpr = block.match(/\bstorage: (\{[^}]*\})/)?.[1]
    const load = { path: String(evaluate(path)), storage: (storageExpr ? evaluate(storageExpr) : {}) as Record<string, string> }
    const key = JSON.stringify(load)
    const seen = loads.get(key)
    if (seen) seen.surfaces.push(name)
    else loads.set(key, { ...load, surfaces: [name] })
  }
  if (loads.size === 0) throw new Error('surfaces.visual.ts: no surface paths found')
  return [...loads.values()]
}

interface Probe {
  ready: boolean
  started: number
  inflight: number
  rpcs: Record<string, number>
  refused: string[]
  unknown: string[]
  escaped: string[]
  outside: string[]
  unannotated: string[]
}

const readProbe = (page: Page) => page.evaluate(() => (window as unknown as { __forgeFixtures?: Probe }).__forgeFixtures ?? null)

/** No call in flight and none started for `quietMs`; false once `deadlineMs` passes (timed in Node). */
async function quiet(page: Page, { deadlineMs = 20_000, quietMs = 600 } = {}): Promise<boolean> {
  const begin = performance.now()
  let last = -1
  let quietSince = 0
  for (;;) {
    const now = performance.now() - begin
    const probe = await readProbe(page)
    if (!probe || probe.started !== last || probe.inflight > 0) {
      last = probe?.started ?? -1
      quietSince = now
    } else if (now - quietSince >= quietMs) {
      return true
    }
    if (now >= deadlineMs) return false
    await new Promise((r) => setTimeout(r, 100))
  }
}

for (const load of surfaceLoads()) {
  const label = `${load.path}${Object.keys(load.storage).length ? ` ${JSON.stringify(load.storage)}` : ''}`
  test(`fixture mode loads ${label} with no escaped rpc, refused write or page error`, { tag: '@harness' }, async ({ page, baseURL }) => {
    const origin = new URL(baseURL!).origin
    const pageErrors: string[] = []
    const offOrigin: string[] = []
    page.on('pageerror', (err) => pageErrors.push(err.message))
    page.on('request', (req) => {
      const url = new URL(req.url())
      if (url.protocol.startsWith('http') && url.origin !== origin) offOrigin.push(`${req.method()} ${req.url()}`)
    })
    page.on('websocket', (ws) => offOrigin.push(`WS ${ws.url()}`))

    await page.clock.setFixedTime(FIXED_NOW)
    await page.route('**/config.js', (route) =>
      route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify(CONFIG)};` }),
    )
    await page.addInitScript(
      ({ now, storage }) => {
        ;(window as unknown as { __forgeFixtures: unknown }).__forgeFixtures = { now }
        // Nothing else: a fresh profile, as the hosted preview's first visit is.
        try {
          for (const [key, value] of Object.entries(storage)) localStorage.setItem(key, value)
        } catch {
          /* storage disabled */
        }
      },
      { now: FIXED_NOW, storage: load.storage },
    )

    await page.goto(load.path)
    await expect.poll(async () => (await readProbe(page))?.ready ?? false, { timeout: 20_000 }).toBe(true)
    const settled = await quiet(page)
    const probe = (await readProbe(page))!

    const report = {
      path: load.path, storage: load.storage, surfaces: load.surfaces, settled,
      rpcs: probe.rpcs, refused: [...new Set(probe.refused)], escaped: probe.escaped, outside: probe.outside,
      unknown: [...new Set(probe.unknown)], unannotated: probe.unannotated, pageErrors, offOrigin,
    }
    console.log(`[fixture-mode] ${JSON.stringify(report)}`)
    if (process.env.FIXTURE_MODE_REPORT) appendFileSync(process.env.FIXTURE_MODE_REPORT, `${JSON.stringify(report)}\n`)
    await test.info().attach('probe.json', { body: JSON.stringify(report, null, 2), contentType: 'application/json' })

    expect.soft(report.escaped, 'rpcs that reached fetch').toEqual([])
    expect.soft(offOrigin, 'requests the browser sent off the page origin').toEqual([])
    const known = KNOWN_REFUSALS[load.path]
    // These routes mount the chat workspace, which records opening its project.
    // Keep the write refused (never grant a fixture transport write permission).
    const touchesProject = ['/', '/m/chats', '/m/new', '/m/workflows', '/m/runs', '/workflows', '/workflows/library', '/workflows/runs', `/project/${IDS.project}`].includes(new URL(load.path, origin).pathname)
      || new URL(load.path, origin).pathname.startsWith('/m/chats/')
      || load.path === '/connectors?tab=mcp'
    const expectedRefusals = [...(known?.rpcs ?? []), ...(touchesProject ? ['reliant.v1.ProjectService/TouchProject'] : [])]
    expect.soft(report.refused, known ? `writes refused at load (known: ${known.why})` : 'writes refused at load (TouchProject only on chat workspace routes)').toEqual(expectedRefusals)
    expect.soft(report.outside, 'fetches outside the allowed set').toEqual([])
    expect.soft(pageErrors, 'page errors').toEqual([])
    expect(probe.started, 'the app made no rpc at all').toBeGreaterThan(0)
  })
}
