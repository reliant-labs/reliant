/**
 * The phone's project pickers keep the pick in the URL (owner, 2026-11-24:
 * the URL is the only view state; the stores follow it, and a pick only
 * navigates). Runs in the visual lane's harness (fixture data, pinned image)
 * on the mobile project, and compares no pixels.
 *
 *   - the shell's project switcher (the drawer on /usage, /connectors,
 *     /projects) lands on the chat list scoped to the pick;
 *   - a /m/projects pick is the chat list's `?projects=`, and survives a reload;
 *   - a /m/new pick is `?project=`: it survives a reload, Back undoes it, and
 *     the chat starts in it;
 *   - a shared desktop link opens its scope on a phone.
 *
 * The fixture's ListChats answers every scope with the same rows, so these
 * assert on the request the app sends, not on the rows it shows.
 */
import { expect, test, type Page } from '@playwright/test'

import { chatFixtures } from './fixtures/chat'
import { IDS } from './fixtures/base'
import { installHarness, settle } from './harness'
import type { Fixtures } from './rpc'

const SHOP = IDS.project
const DOCS = IDS.docsProject
const LIST_CHATS = 'reliant.v1.ChatService/ListChats'

/** Both projects ready on the cloud machine, so either can start a chat; every ListChats and StartChat request is recorded. */
function recordingFixtures() {
  const listChats: Record<string, unknown>[] = []
  const startChat: Record<string, unknown>[] = []
  const fixtures: Fixtures = {
    [LIST_CHATS]: (req) => {
      listChats.push(req)
      return chatFixtures[LIST_CHATS] as Record<string, unknown>
    },
    'reliant.v1.ChatService/StartChat': (req) => {
      startChat.push(req)
      return {}
    },
    'reliant.v1.ProjectService/ListProjectDaemons': {
      projectDaemons: [SHOP, DOCS].map((projectId) => ({
        projectId,
        daemonId: IDS.cloudDaemon,
        path: '/home/workspace/projects/x',
        clonedAt: '2026-11-01T00:00:00Z',
        installState: 'PROJECT_INSTALL_STATE_INSTALLED',
        installError: '',
        daemonName: 'cloud-machine',
      })),
    },
  }
  return { fixtures, listChats, startChat }
}

/** Whether a ListChats request was scoped to `projectId`. */
const scopedTo = (requests: Record<string, unknown>[], projectId: string) => requests.some((req) => req.projectId === projectId)

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

  for (const start of ['/usage', '/connectors', '/projects']) {
    test(`the shell switcher on ${start} scopes the chat list to the pick`, { tag: '@mobile' }, async ({ page }) => {
      const { fixtures, listChats } = recordingFixtures()
      await installHarness(page, { fixtures })
      await page.goto(start)
      await settle(page)
      await openMenu(page)
      await page.getByTestId('shell-project-switcher').first().click()
      // A project's row (an app's, `app:<name>`, would open its console).
      await page.getByTestId(/^project-selector-row-(?!app:)/).filter({ hasText: 'docs-site' }).first().click()

      await expect.poll(() => pathOf(page)).toBe('/m/chats')
      expect(query(page)).toEqual({ projects: DOCS })
      await expect.poll(() => scopedTo(listChats, DOCS)).toBe(true)
    })
  }

  test('a /m/projects pick is the URL, and survives a reload', { tag: '@mobile' }, async ({ page }) => {
    const { fixtures, listChats } = recordingFixtures()
    await installHarness(page, { fixtures })
    await page.goto('/m/chats')
    // A bare list is filled from the tab's default: every project, here.
    await expect.poll(() => query(page)).toEqual({ projects: 'all' })
    await settle(page)
    await openMenu(page)
    await page.getByTestId('mobile-nav-project').click()
    await expect(page).toHaveURL(/\/m\/projects/)
    await page.getByText('docs-site', { exact: true }).first().click()

    await expect.poll(() => pathOf(page)).toBe('/m/chats')
    expect(query(page)).toEqual({ projects: DOCS })
    await expect.poll(() => scopedTo(listChats, DOCS)).toBe(true)

    listChats.length = 0
    await page.reload()
    await settle(page)
    expect(query(page)).toEqual({ projects: DOCS })
    await expect.poll(() => scopedTo(listChats, DOCS)).toBe(true)

    // The pick was a step: Back returns to the list of projects, the check mark on docs-site no longer.
    await page.goBack()
    await expect(page).toHaveURL(/\/m\/projects/)
    await page.goBack()
    await expect.poll(() => query(page)).toEqual({ projects: 'all' })
  })

  test('a /m/new pick is ?project=: a reload keeps it, Back undoes it, and the chat starts in it', { tag: '@mobile' }, async ({ page }) => {
    const { fixtures, startChat } = recordingFixtures()
    await installHarness(page, { fixtures })
    const row = page.getByTestId('mobile-new-chat-project')
    await page.goto(`/m/new?projects=${SHOP}`)
    // The page names the project it opens on, so Back can return to it.
    await expect.poll(() => query(page)).toEqual({ projects: SHOP, project: SHOP })
    await expect(row).toContainText('shopfront')
    await settle(page)

    await row.getByRole('button').click()
    await page.getByRole('dialog').getByText('docs-site', { exact: true }).click()
    // `project` wins; the list's scope stays for Back to the list.
    await expect.poll(() => query(page)).toMatchObject({ projects: SHOP, project: DOCS })
    await expect(row).toContainText('docs-site')
    await expect(page.locator('header').first()).toContainText('docs-site')

    await page.reload()
    await expect(row).toContainText('docs-site')
    expect(query(page)).toMatchObject({ projects: SHOP, project: DOCS })

    await page.goBack()
    await expect.poll(() => query(page)).toEqual({ projects: SHOP, project: SHOP })
    await expect(row).toContainText('shopfront')

    await page.goForward()
    await expect(row).toContainText('docs-site')
    await settle(page)
    await page.getByPlaceholder('What do you want to do?').fill('tidy the docs')
    await page.getByRole('button', { name: 'Send message' }).click()
    await expect.poll(() => startChat.length).toBeGreaterThan(0)
    expect(startChat[0].projectId).toBe(DOCS)
  })

  test('a shared desktop link opens its scope on a phone', { tag: '@mobile' }, async ({ page }) => {
    const { fixtures, listChats } = recordingFixtures()
    await installHarness(page, { fixtures })

    await page.goto(`/project/${DOCS}`)
    await expect.poll(() => pathOf(page)).toBe('/m/chats')
    expect(query(page)).toEqual({ projects: DOCS })
    await expect.poll(() => scopedTo(listChats, DOCS)).toBe(true)

    await page.goto(`/new?project=${DOCS}&projects=${SHOP}`)
    await expect.poll(() => pathOf(page)).toBe('/m/new')
    expect(query(page)).toEqual({ project: DOCS, projects: SHOP })
    await expect(page.getByTestId('mobile-new-chat-project')).toContainText('docs-site')
  })
})
