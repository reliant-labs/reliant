/**
 * Visual-regression harness: puts a page in a fully deterministic state
 * before the app loads.
 *
 *   network     every RPC answered from fixtures/*.ts (shim.ts + rpc.ts)
 *   clock       Date frozen at FIXED_NOW (timers keep running)
 *   randomness  Math.random seeded
 *   appearance  the project's theme stored before the bootstrap reads it;
 *               the default (Reliant) color scheme; reduced motion
 *   locale      en-US, UTC (playwright.visual.config.ts)
 *
 * `settle` then waits for web fonts and for the network to go quiet, and
 * toHaveScreenshot's own two-identical-frames check does the rest.
 */
import { test, type Page } from '@playwright/test'

import { FIXED_NOW, USER } from './fixtures/base'
import { allFixtures } from './fixtures'
import { FixtureServer, type Fixtures, type RpcCall } from './rpc'
import { networkShim, type ShimOptions } from './shim'

/** Runtime config replacing the env's config.js: the managed product, every URL fake (shim.ts answers them). */
const CONFIG = {
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
}

export interface HarnessOptions {
  /** Fixtures that override the shared tables for this page. */
  fixtures?: Fixtures
  /** localStorage entries (settings) written before the app boots. */
  storage?: Record<string, string>
}

export async function installHarness(page: Page, options: HarnessOptions = {}): Promise<FixtureServer> {
  const theme: ShimOptions['theme'] = test.info().project.use.colorScheme === 'light' ? 'light' : 'dark'
  const server = new FixtureServer({ ...allFixtures, ...options.fixtures })
  if (options.storage) {
    await page.addInitScript((entries) => {
      for (const [key, value] of Object.entries(entries)) localStorage.setItem(key, value)
    }, options.storage)
  }
  await page.clock.setFixedTime(FIXED_NOW)
  await page.exposeBinding('__visualRpc', (_source, call: RpcCall) => server.answer(call))
  await page.route('**/config.js', (route) =>
    route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify(CONFIG)};` }),
  )
  await page.addInitScript(networkShim, { now: FIXED_NOW, theme, user: USER } satisfies ShimOptions)
  return server
}

export interface SettleOptions {
  /** How long the network may stay busy before settle fails (ms). */
  deadlineMs?: number
  /** How long it must stay quiet to count as settled (ms). */
  quietMs?: number
}

/**
 * Fonts loaded, no RPC in flight and none started for `quietMs`, then two
 * frames. Fails with "the network never went quiet" once `deadlineMs` has
 * passed, so a request that never finishes is a red test, not a hang.
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
    const { started, inflight } = await page.evaluate(() => {
      const w = window as unknown as { __visualStarted: number; __visualInflight: number }
      return { started: w.__visualStarted, inflight: w.__visualInflight }
    })
    if (started !== last || inflight > 0) {
      last = started
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
