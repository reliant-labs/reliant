/**
 * The harness itself: `settle` must fail, not hang, when a request never
 * finishes. The page's Date is frozen (page.clock), so a wait timed on it
 * would never reach its deadline; settle times itself in Node.
 */
import { expect, test } from '@playwright/test'

import { installHarness, settle } from './harness'

test('settle fails at its deadline while a request never finishes', { tag: '@harness' }, async ({ page }) => {
  await installHarness(page, { fixtures: { 'reliant.v1.ChatService/ListChats': { __pending: true } } })
  await page.goto('/')

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
