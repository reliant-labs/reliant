/**
 * The phone's drawer flow (U6 3/16): a drill keeps the drawer open, a
 * destination closes it. Runs in the visual lane's harness (fixture data,
 * pinned image) on the mobile project, and compares no pixels.
 */
import { expect, test } from '@playwright/test'

import { installHarness, settle } from './harness'

test.describe('shell drawer flow', () => {
  test.beforeAll(() => {
    if (process.env.VISUAL_IN_CONTAINER || process.env.VISUAL_ALLOW_HOST) return
    throw new Error('Run the drawer flow through scripts/visual-docker.sh (VISUAL_ALLOW_HOST=1 to debug on the host).')
  })

  test('drill into Agents, back, into Observability, tap Logs: the drawer closes on Logs', { tag: '@mobile' }, async ({ page }) => {
    await installHarness(page)
    await page.goto('/forge/p/shop')
    await expect(page.getByText('worker', { exact: true }).first()).toBeVisible({ timeout: 20_000 })
    await settle(page)

    // The shell's drawer on an app page; the phone's own (a dialog) once Agents drills onto /m/chats.
    const drawer = page.getByRole('dialog', { name: 'Navigation' }).or(page.getByTestId('shell-nav')).first()
    await page.getByRole('button', { name: 'Show sidebar' }).click()
    await expect(drawer.getByText('Configure', { exact: true })).toBeVisible()

    await page.getByTestId('nav-agents').click()
    await expect(page.getByTestId('nav-drill-in')).toHaveAttribute('data-area', 'agents')
    await expect(drawer).toBeVisible()

    await page.getByTestId('nav-back').click()
    await expect(drawer.getByText('Configure', { exact: true })).toBeVisible()

    await page.getByTestId('nav-observability').click()
    await expect(page.getByTestId('nav-drill-in')).toHaveAttribute('data-area', 'observability')
    await expect(drawer).toBeVisible()

    await page.getByTestId('nav-obs-logs').or(page.getByRole('link', { name: 'Logs', exact: true })).first().click()
    await expect(page).toHaveURL(/\/forge\/p\/shop\/observability\/logs/)
    await expect(page.getByTestId('shell-nav')).toHaveAttribute('data-state', 'closed')
  })
})
