/**
 * The phone's project pickers keep the pick in the URL (owner, 2026-11-24:
 * the URL is the only view state; the stores follow it, and a pick only
 * navigates). Runs in the visual lane's harness (fixture data, pinned image)
 * on the mobile project, and compares no pixels.
 *
 *   - the shell's project switcher (the drawer on /usage, /connectors,
 *     /projects, and the phone's own drawer on /m/chats, the same column)
 *     lands on the chat list scoped to the pick;
 *   - a /m/projects pick is the chat list's `?projects=`, and survives a reload;
 *   - a /m/new pick is `?project=`: it survives a reload, Back undoes it, and
 *     the chat starts in it;
 *   - a shared desktop link opens its scope on a phone.
 *
 * These assert on the request the app sends (noted by the shop pack's
 * `project-pickers` overlay), not on the rows it shows.
 */
import { expect, test, type Page } from '@playwright/test'

import { installHarness, settle } from './harness'

// The shop world's two projects (src/fixtures/shop/workspace.ts).
const SHOP = 'shop-proj-shop'
const DOCS = 'shop-proj-docs'

/**
 * Both projects ready on the cloud machine, so either can start a chat, and
 * every ListChats and StartChat request noted in the page: the
 * `project-pickers` overlay (src/fixtures/shop/overlays.ts). StartChat stays
 * refused; only its request is read.
 */
const PICKERS = { overlays: ['project-pickers'] }

type Requests = { listChats: Record<string, unknown>[]; startChat: Record<string, unknown>[] }
const requests = (page: Page): Promise<Requests> =>
  page.evaluate(() => {
    const noted = (window as unknown as { __visualRequests?: Record<string, Record<string, unknown>[]> }).__visualRequests ?? {}
    return { listChats: noted['reliant.v1.ChatService/ListChats'] ?? [], startChat: noted['reliant.v1.ChatService/StartChat'] ?? [] }
  })

/** Whether a ListChats request was scoped to `projectId`. */
const scopedTo = async (page: Page, projectId: string) => (await requests(page)).listChats.some((req) => req.projectId === projectId)

const query = (page: Page) => Object.fromEntries(new URL(page.url()).searchParams)
const pathOf = (page: Page) => new URL(page.url()).pathname

async function openMenu(page: Page) {
  await page.getByRole('button', { name: /^(Open menu|Show sidebar)/ }).first().click()
}

test.describe('mobile project pickers', () => {
  test.beforeAll(() => {
    if (process.env.VISUAL_IN_CONTAINER || process.env.VISUAL_ALLOW_HOST) return
    throw new Error('Run the picker flows through scripts/visual-docker.sh (VISUAL_ALLOW_HOST=1 to debug on the host).')
  })

  for (const start of ['/usage', '/connectors', '/projects', '/m/chats']) {
    test(`the shell switcher on ${start} scopes the chat list to the pick`, { tag: '@mobile' }, async ({ page }) => {
      await installHarness(page, PICKERS)
      await page.goto(start)
      await settle(page)
      await openMenu(page)
      await page.getByTestId('shell-project-switcher').first().click()
      // A project's row (an app's, `app:<name>`, would open its console).
      await page.getByTestId(/^project-selector-row-(?!app:)/).filter({ hasText: /^docs/ }).first().click()

      await expect.poll(() => pathOf(page)).toBe('/m/chats')
      expect(query(page)).toEqual({ projects: DOCS })
      await expect.poll(() => scopedTo(page, DOCS)).toBe(true)
    })
  }

  test('a /m/projects pick is the URL, and survives a reload', { tag: '@mobile' }, async ({ page }) => {
    await installHarness(page, PICKERS)
    await page.goto('/m/chats')
    // A bare list is filled from the tab's default: every project, here.
    await expect.poll(() => query(page)).toEqual({ projects: 'all' })
    await settle(page)
    // The drawer's project row is the shell's switcher now (covered above); the screen is a URL of its own.
    await page.goto('/m/projects')
    await settle(page)
    await page.getByText('docs', { exact: true }).first().click()

    await expect.poll(() => pathOf(page)).toBe('/m/chats')
    expect(query(page)).toEqual({ projects: DOCS })
    await expect.poll(() => scopedTo(page, DOCS)).toBe(true)

    // The reload starts a fresh page, and its own notes.
    await page.reload()
    await settle(page)
    expect(query(page)).toEqual({ projects: DOCS })
    await expect.poll(() => scopedTo(page, DOCS)).toBe(true)

    // The pick was a step: Back returns to the list of projects, the check mark on docs no longer.
    await page.goBack()
    await expect(page).toHaveURL(/\/m\/projects/)
    await page.goBack()
    await expect.poll(() => query(page)).toEqual({ projects: 'all' })
  })

  test('a /m/new pick is ?project=: a reload keeps it, Back undoes it, and the chat starts in it', { tag: '@mobile' }, async ({ page }) => {
    await installHarness(page, PICKERS)
    const row = page.getByTestId('mobile-new-chat-project')
    await page.goto(`/m/new?projects=${SHOP}`)
    // The page names the project it opens on, so Back can return to it.
    await expect.poll(() => query(page)).toEqual({ projects: SHOP, project: SHOP })
    await expect(row).toContainText('shop')
    await settle(page)

    await row.getByRole('button').click()
    await page.getByRole('dialog').getByText('docs', { exact: true }).click()
    // `project` wins; the list's scope stays for Back to the list.
    await expect.poll(() => query(page)).toMatchObject({ projects: SHOP, project: DOCS })
    await expect(row).toContainText('docs')
    await expect(page.locator('header').first()).toContainText('docs')

    await page.reload()
    await expect(row).toContainText('docs')
    expect(query(page)).toMatchObject({ projects: SHOP, project: DOCS })

    await page.goBack()
    await expect.poll(() => query(page)).toEqual({ projects: SHOP, project: SHOP })
    await expect(row).toContainText('shop')

    await page.goForward()
    await expect(row).toContainText('docs')
    await settle(page)
    await page.getByPlaceholder('What do you want to do?').fill('tidy the docs')
    await page.getByRole('button', { name: 'Send message' }).click()
    await expect.poll(async () => (await requests(page)).startChat.length).toBeGreaterThan(0)
    expect((await requests(page)).startChat[0].projectId).toBe(DOCS)
  })

  test('a shared desktop link opens its scope on a phone', { tag: '@mobile' }, async ({ page }) => {
    await installHarness(page, PICKERS)

    await page.goto(`/project/${DOCS}`)
    await expect.poll(() => pathOf(page)).toBe('/m/chats')
    expect(query(page)).toEqual({ projects: DOCS })
    await expect.poll(() => scopedTo(page, DOCS)).toBe(true)

    await page.goto(`/new?project=${DOCS}&projects=${SHOP}`)
    await expect.poll(() => pathOf(page)).toBe('/m/new')
    expect(query(page)).toEqual({ project: DOCS, projects: SHOP })
    await expect(page.getByTestId('mobile-new-chat-project')).toContainText('docs')
  })
})
