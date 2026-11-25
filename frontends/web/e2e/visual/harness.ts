/**
 * Visual-regression harness: puts a page in a fully deterministic state
 * before the app loads.
 *
 *   network     fixture mode: config.js names the shop pack
 *               (FIXTURES_SCENARIO = "shop", read-only), so the production
 *               build answers every RPC in the page, behind the app's real
 *               interceptors (src/fixtures/install.ts), and
 *               src/fixtures/outside.ts is the only fetch boundary
 *   variants    a surface's named overlays (src/fixtures/shop/overlays.ts),
 *               plus `empty-reads` on every page: a read no fixture answers
 *               gets the empty response, as the old fetch shim's `{}` did
 *   clock       Date frozen at FIXED_NOW (page.clock; timers keep running),
 *               and the pack clock pinned to it
 *   randomness  Math.random seeded (shim.ts)
 *   appearance  the project's theme stored before the bootstrap reads it;
 *               the default (Reliant) color scheme; reduced motion
 *   locale      en-US, UTC (playwright.visual.config.ts)
 *
 * `settle` then waits for web fonts and for the pack's calls to go quiet, and
 * toHaveScreenshot's own two-identical-frames check does the rest.
 */
import { test, type Page } from '@playwright/test'

import { FIXED_NOW } from './clock'
import { pageShim, type ShimOptions } from './shim'

/**
 * Runtime config replacing the env's config.js: the managed product, every URL
 * fake (fixture mode reaches none of them), and forge#2719's fixture keys.
 */
export const CONFIG = {
  AUTH_MODE: 'cloud',
  API_URL: 'https://api.visual.invalid',
  CONTROL_PLANE_API_URL: 'https://cp.visual.invalid',
  APP_URL: 'https://app.visual.invalid',
  GATEWAY_URL: 'https://gw.visual.invalid',
  SUPABASE_URL: 'https://visual.supabase.co',
  SUPABASE_ANON_KEY: 'visual-harness-anon-key',
  STRIPE_PUBLISHABLE_KEY: '',
  ENVIRONMENT: 'visual',
  OTEL_ENDPOINT: '',
  OTEL_REPLAY: 'false',
  FIXTURES_SCENARIO: 'shop',
  FIXTURES_READ_ONLY: 'true',
}

/** Every page's overlay: unanswered reads answer empty (plan D6). The hosted preview does not use it. */
const HARNESS_OVERLAYS = ['empty-reads']

export interface HarnessOptions {
  /** Overlays of the shop app pack for this page (src/fixtures/shop/overlays.ts), in order. */
  overlays?: string[]
  /** localStorage entries (settings) written before the app boots. */
  storage?: Record<string, string>
}

/** `window.__forgeFixtures` once fixture mode is installed (src/fixtures/install.ts ForgeFixturesProbe). */
export interface Probe {
  ready: boolean
  overlays: string[]
  started: number
  inflight: number
  rpcs: Record<string, number>
  refused: string[]
  unknown: string[]
  escaped: string[]
  outside: string[]
  unannotated: string[]
}

export const readProbe = (page: Page): Promise<Probe | null> =>
  page.evaluate(() => {
    const probe = (window as unknown as { __forgeFixtures?: Probe }).__forgeFixtures
    return probe?.ready ? JSON.parse(JSON.stringify(probe)) : null
  })

export async function installHarness(page: Page, options: HarnessOptions = {}): Promise<void> {
  const theme: ShimOptions['theme'] = test.info().project.use.colorScheme === 'light' ? 'light' : 'dark'
  if (options.storage) {
    await page.addInitScript((entries) => {
      for (const [key, value] of Object.entries(entries)) localStorage.setItem(key, value)
    }, options.storage)
  }
  await page.clock.setFixedTime(FIXED_NOW)
  await page.route('**/config.js', (route) =>
    route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify(CONFIG)};` }),
  )
  const overlays = [...(options.overlays ?? []), ...HARNESS_OVERLAYS]
  await page.addInitScript(pageShim, { now: FIXED_NOW, overlays, theme } satisfies ShimOptions)
}

export interface SettleOptions {
  /** How long the network may stay busy before settle fails (ms). */
  deadlineMs?: number
  /** How long it must stay quiet to count as settled (ms). */
  quietMs?: number
}

/**
 * Fonts loaded, no call in flight and none started for `quietMs`, then two
 * frames. The counts are the fixture probe's (src/fixtures/install.ts): a
 * server stream counts until its header, so an open subscription does not
 * hold the page busy, but a call that never answers does. Fails with "the
 * network never went quiet" once `deadlineMs` has passed, so a request that
 * never finishes is a red test, not a hang.
 *
 * Timed in Node with performance.now(): the page's Date is frozen at
 * FIXED_NOW (page.clock), and nothing here may read the page's clock.
 * harness.visual.ts proves the deadline fires.
 */
export async function settle(page: Page, { deadlineMs = 20_000, quietMs = 600 }: SettleOptions = {}): Promise<void> {
  const begin = performance.now()
  const elapsed = () => performance.now() - begin
  const remaining = () => Math.max(1, deadlineMs - elapsed())
  const overdue = () => new Error(`the network never went quiet within ${deadlineMs}ms`)
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => reject(overdue()), remaining())
  })
  try {
    await Promise.race([page.evaluate(() => document.fonts.ready.then(() => undefined)), timeout])
  } finally {
    clearTimeout(timer)
  }

  let last = -1
  let quietSince = elapsed()
  for (;;) {
    const counts = await page.evaluate(() => {
      const probe = (window as unknown as { __forgeFixtures?: { ready?: boolean; started: number; inflight: number } }).__forgeFixtures
      return probe?.ready ? { started: probe.started, inflight: probe.inflight } : null
    })
    if (!counts || counts.started !== last || counts.inflight > 0) {
      last = counts?.started ?? -1
      quietSince = elapsed()
    } else if (elapsed() - quietSince >= quietMs) {
      break
    }
    if (elapsed() >= deadlineMs) throw overdue()
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

/** Screenshot options shared by every capture. */
export const SHOT = {
  animations: 'disabled',
  caret: 'hide',
  scale: 'css',
  // Run-to-run noise measured on this suite: under 110 pixels per capture,
  // each off by at most 10 of 255 in one channel (glyph antialiasing), which
  // the default per-pixel threshold (0.2) already ignores. A count, not a
  // ratio: 0.1% of a desktop capture is 1,300 pixels, enough to hide a
  // hovered row.
  maxDiffPixels: 100,
} as const
