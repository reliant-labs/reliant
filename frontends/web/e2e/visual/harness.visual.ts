/**
 * The harness itself: `settle` must fail, not hang, when a request never
 * finishes. The page's Date is frozen (page.clock), so a wait timed on it
 * would never reach its deadline; settle times itself in Node.
 */
import { expect, test } from '@playwright/test'

import { installHarness, settle } from './harness'

const NEVER_ANSWERED = 'reliant.v1.ChatService/ListChats'

test('settle fails at its deadline while a request never finishes', { tag: '@harness' }, async ({ page }) => {
  const server = await installHarness(page, { fixtures: { [NEVER_ANSWERED]: { __pending: true } } })
  await page.goto('/')
  // Start the clock only once the never-answered request is in flight. The
  // app boots in code-split chunks, and a chunk download is not an RPC, so
  // the network can look quiet for settle's 600ms window before the sidebar
  // asks for its chats: settle then (correctly) returns, and the test would
  // be proving the app's boot order rather than settle's deadline.
  await expect.poll(() => server.calls.includes(NEVER_ANSWERED), { timeout: 20_000 }).toBe(true)

  const begin = performance.now()
  await expect(settle(page, { deadlineMs: 3_000 })).rejects.toThrow('the network never went quiet within 3000ms')
  const took = performance.now() - begin
  expect(took).toBeGreaterThanOrEqual(3_000)
  expect(took).toBeLessThan(10_000)
})

test('settle returns once every request has answered', { tag: '@harness' }, async ({ page }) => {
  await installHarness(page)
  await page.goto('/')
  await settle(page, { deadlineMs: 20_000 })
})
