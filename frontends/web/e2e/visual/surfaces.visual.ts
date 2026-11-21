/**
 * The visual baseline: the key surfaces of the production build, against the
 * fixture data in fixtures/, in dark and light at 1440×900 and dark at
 * 390×844 (the projects in playwright.visual.config.ts). Run it with
 * `task test:web:visual`; e2e/visual/README.md says how to read and update it.
 *
 * A capture that differs from its baseline fails with a diff image in
 * test-results-visual/. When a change is MEANT to move pixels, re-run with
 * `-- --update-snapshots` and commit the new baselines: the PR diff then shows
 * the before and after side by side.
 */
import { expect, test, type Locator, type Page } from '@playwright/test'

import { CHAT_IDS, CHAT_TITLES, chatWithoutFailedSend } from './fixtures/chat'
import { workflowFixtures } from './fixtures/workflows'
import { installHarness, settle, SHOT, type HarnessOptions } from './harness'

type Viewport = 'desktop' | 'mobile'

interface Surface {
  name: string
  path: string
  on: Viewport[]
  /** Something that is on screen only once the surface has its data. */
  ready: (page: Page) => Locator
  /** After the load, before the capture (opening the chat, say). */
  act?: (page: Page) => Promise<void>
  harness?: HarnessOptions
}

const LONG_CHAT_TITLE = 'Refactor cart totals and fix multi-quantity discounts'
const SHORT_CHAT_TITLE = 'Load-test the limiter (spawned from rate limiting)'

/** The fixture chat's first line, a line of its middle turn, and its last line. */
const FIXTURE_TOP = 'Discount codes are wrong when a cart line'
const FIXTURE_MIDDLE = 'Here is the checkout screenshot'
const FIXTURE_BOTTOM = 'Also add a CHANGELOG entry'
/** The fixture chat's last line from the server: the failed send after it is the client's. */
const FIXTURE_LAST_SERVER_LINE = 'The branch is ready'

const transcript = (page: Page) => page.locator('[data-context="transcript"]')
/** Shown once the reader is away from the end; it fades in, so wait for it. */
const scrollToBottomButton = (page: Page) => page.getByRole('button', { name: 'Scroll to bottom' })

/** Two animation frames: long enough for the virtualizer to measure and correct. */
const frames = (page: Page) =>
  page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))

/**
 * Scroll the transcript to its first row. Rows above the viewport are
 * measured as they render, and the virtualizer corrects the offset for them,
 * so it repeats until the top stays put.
 */
async function scrollToTop(page: Page) {
  for (let stable = 0, i = 0; stable < 3 && i < 40; i++) {
    const top = await transcript(page).evaluate((el) => {
      el.scrollTop = 0
      return el.scrollTop
    })
    await frames(page)
    stable = top === 0 && (await transcript(page).evaluate((el) => el.scrollTop)) === 0 ? stable + 1 : 0
  }
}

/**
 * Scroll the transcript to its end, where a chat opens: content that arrives
 * after the first paint (approvals, a question, an attachment) can leave it
 * short of the end, so it repeats until the end stays put.
 */
async function scrollToEnd(page: Page) {
  for (let stable = 0, i = 0; stable < 3 && i < 40; i++) {
    const gap = await transcript(page).evaluate((el) => {
      el.scrollTop = el.scrollHeight
      return el.scrollHeight - el.clientHeight - el.scrollTop
    })
    await frames(page)
    stable = gap <= 1 ? stable + 1 : 0
  }
}

/**
 * Scroll the transcript until the row holding `text` starts at the top of the
 * viewport: from the top, a page at a time until the row renders (the rows are
 * virtualized), then onto it until it stops moving.
 */
async function scrollToText(page: Page, text: string) {
  await scrollToTop(page)
  const target = page.getByText(text, { exact: false }).first()
  for (let i = 0; i < 60 && (await target.count()) === 0; i++) {
    await transcript(page).evaluate((el) => {
      el.scrollTop += el.clientHeight * 0.8
    })
    await frames(page)
  }
  for (let i = 0; i < 4; i++) {
    await target.evaluate((el) => el.scrollIntoView({ block: 'start' }))
    await frames(page)
  }
}

/** Open the fixture chat from the sidebar, at its end. */
async function openFixtureChat(page: Page) {
  await page.getByText(LONG_CHAT_TITLE).first().click()
  await expect(page.getByText(FIXTURE_TOP).or(page.getByText(FIXTURE_BOTTOM)).first()).toBeVisible({ timeout: 20_000 })
  await settle(page)
  await scrollToEnd(page)
}

/** Open one of the sidebar's chats by title, at its end. */
async function openChatByTitle(page: Page, title: string, firstLine: string) {
  await page.getByText(title).first().click()
  await expect(page.getByText(firstLine).first()).toBeVisible({ timeout: 20_000 })
  await settle(page)
  await scrollToEnd(page)
}

/** The mobile chat screen at its end (a chat with no failed send of its own). */
async function mobileChatAtEnd(page: Page, firstLine: string) {
  await expect(page.getByText(firstLine).first()).toBeVisible({ timeout: 20_000 })
  await settle(page)
  await scrollToEnd(page)
}

const TOOLS_LAST_LINE = 'Tax rates now live in'
const FAILURE_FIRST_LINE = 'Upgrade the Stripe SDK to v18 and fix whatever breaks'
const APPROVAL_LINE = 'Pushing it needs your approval'

const SURFACES: Surface[] = [
  {
    name: 'home-sidebar',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText('Migrate order confirmation emails', { exact: false }).first(),
  },
  {
    // The fixture chat (fixtures/chat.ts), at its end: the live turn, a
    // question, a retrying provider error, an approval and a failed send.
    name: 'chat',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(FIXTURE_BOTTOM).first(),
    act: openFixtureChat,
  },
  {
    // The fixture chat from its first line: the first turn and its answer.
    name: 'chat-top',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(FIXTURE_TOP).first(),
    act: async (page) => {
      await openFixtureChat(page)
      await scrollToTop(page)
      await expect(scrollToBottomButton(page)).toBeVisible()
    },
  },
  {
    // The fixture chat's middle: the review and verify threads, the message
    // with attachments, a final failure.
    name: 'chat-middle',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(FIXTURE_MIDDLE).first(),
    act: async (page) => {
      await openFixtureChat(page)
      await scrollToText(page, 'Reviewed the change')
      await expect(scrollToBottomButton(page)).toBeVisible()
    },
  },
  {
    // Detailed thread mode: every tool call rendered, one per tool renderer.
    name: 'chat-detailed',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(FIXTURE_BOTTOM).first(),
    act: openFixtureChat,
    harness: { storage: { 'appearance.chatThreadMode': 'detailed' } },
  },
  {
    // A short chat: one question, a little work, a short answer.
    name: 'chat-short',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText('starts rejecting at about', { exact: false }).first(),
    act: (page) => page.getByText(SHORT_CHAT_TITLE).first().click(),
  },
  {
    name: 'chat-list',
    path: '/m/chats',
    on: ['mobile'],
    ready: (page) => page.getByText('Write the webhooks guide').first(),
  },
  {
    name: 'chat',
    // The fixture chat's end, less the failed send (fixtures/chat.ts, chatStream).
    path: `/m/chats/${CHAT_IDS.long}`,
    on: ['mobile'],
    ready: (page) => page.getByText(FIXTURE_LAST_SERVER_LINE).first(),
    act: async (page) => {
      await expect(page.getByText(FIXTURE_TOP).or(page.getByText(FIXTURE_LAST_SERVER_LINE)).first()).toBeVisible({ timeout: 20_000 })
      await settle(page)
      await scrollToEnd(page)
    },
    harness: { fixtures: chatWithoutFailedSend },
  },
  {
    // A finished turn of sixteen tool calls (reads, greps, edits with a diff,
    // shell) between short narration, with a thinking block and a redacted one.
    name: 'chat-tools',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(TOOLS_LAST_LINE).first(),
    act: (page) => openChatByTitle(page, CHAT_TITLES.tools, TOOLS_LAST_LINE),
  },
  {
    name: 'chat-tools',
    path: `/m/chats/${CHAT_IDS.tools}`,
    on: ['mobile'],
    ready: (page) => page.getByText(TOOLS_LAST_LINE).first(),
    act: (page) => mobileChatAtEnd(page, TOOLS_LAST_LINE),
  },
  {
    // The same turn with its first work fold opened, down to a read: the
    // fold, its "Explored …" group, and the tool rows themselves.
    name: 'chat-tools-open',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText('src/cart/totals.ts').first(),
    act: async (page) => {
      await openChatByTitle(page, CHAT_TITLES.tools, TOOLS_LAST_LINE)
      await transcript(page).getByRole('button', { name: /^Activity/ }).first().click()
      // The run's reads are one "Explored …" row: open it, then the first read.
      await transcript(page).getByRole('button', { name: /^Explored/ }).first().click()
      await transcript(page).getByRole('button', { name: /^Toggle tool details for (view|read)/ }).first().click()
      await settle(page)
      await scrollToTop(page)
    },
  },
  {
    // The edits fold opened: four edits, each with its diff.
    name: 'chat-tools-edits',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByRole('button', { name: 'config.ts' }).first(),
    act: async (page) => {
      await openChatByTitle(page, CHAT_TITLES.tools, TOOLS_LAST_LINE)
      // Every run is one fold. The edits are the fold's whole run, so it
      // opens straight onto their rows: open it, then the first edit.
      await transcript(page).getByRole('button', { name: /^Activity, .*edited/ }).first().click()
      await transcript(page).getByRole('button', { name: /^Toggle tool details for edit/ }).first().click()
      await settle(page)
      await scrollToText(page, 'Three call sites')
    },
  },
  {
    // The fixture chat scrolled to the shell call waiting for approval.
    name: 'chat-approval',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(APPROVAL_LINE).first(),
    act: async (page) => {
      await openFixtureChat(page)
      await scrollToText(page, APPROVAL_LINE)
    },
  },
  {
    name: 'chat-approval',
    path: `/m/chats/${CHAT_IDS.long}`,
    on: ['mobile'],
    ready: (page) => page.getByText(APPROVAL_LINE).first(),
    act: async (page) => {
      await expect(page.getByText(FIXTURE_TOP).or(page.getByText(APPROVAL_LINE)).first()).toBeVisible({ timeout: 20_000 })
      await settle(page)
      await scrollToText(page, APPROVAL_LINE)
    },
    harness: { fixtures: chatWithoutFailedSend },
  },
  {
    // A turn that stopped: the last tool call failed and the provider failed after it.
    name: 'chat-failure',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(FAILURE_FIRST_LINE).first(),
    act: (page) => openChatByTitle(page, CHAT_TITLES.failure, FAILURE_FIRST_LINE),
  },
  {
    name: 'chat-failure',
    path: `/m/chats/${CHAT_IDS.failure}`,
    on: ['mobile'],
    ready: (page) => page.getByText(FAILURE_FIRST_LINE).first(),
    act: (page) => mobileChatAtEnd(page, FAILURE_FIRST_LINE),
  },
  {
    // The Workflows tab: built-in and your own workflows, two on a schedule.
    name: 'workflows',
    path: '/workflows/library',
    on: ['desktop'],
    ready: (page) => page.getByText('Nightly CI triage').first(),
    harness: { fixtures: workflowFixtures },
  },
  {
    name: 'workflows',
    path: '/m/workflows',
    on: ['mobile'],
    ready: (page) => page.getByText('Nightly CI triage').first(),
    harness: { fixtures: workflowFixtures },
  },
  {
    // The Runs tab: live, needs-input, finished and failed runs.
    name: 'runs',
    path: '/workflows/runs',
    on: ['desktop'],
    ready: (page) => page.getByText('Draft release notes for v2.14').first(),
    harness: { fixtures: workflowFixtures },
  },
  {
    name: 'runs',
    path: '/m/runs',
    on: ['mobile'],
    ready: (page) => page.getByText('Draft release notes for v2.14').first(),
    harness: { fixtures: workflowFixtures },
  },
  {
    // The org-wide Observability home.
    name: 'observability-overview',
    path: '/forge/observability',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('worker', { exact: true }).first(),
  },
  {
    name: 'app-overview',
    path: '/forge/p/shopfront',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('worker', { exact: true }).first(),
  },
  {
    name: 'observability-errors',
    path: '/forge/p/shopfront/observability/errors',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('idempotency key reused', { exact: false }).first(),
  },
  {
    name: 'environment',
    path: '/forge/p/shopfront/environments/prod',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('shopfront-prod.reliant.app', { exact: false }).first(),
  },
  {
    // The chat's settings: scrolled to the "Fonts and size" panel.
    name: 'settings-appearance',
    path: '/settings/appearance',
    on: ['desktop'],
    ready: (page) => page.getByRole('radiogroup', { name: 'Chat thread' }),
    act: async (page) => {
      await page.getByRole('radiogroup', { name: 'Chat thread' }).scrollIntoViewIfNeeded()
      await page.getByRole('radiogroup', { name: 'Chat thread' }).evaluate((el) => el.scrollIntoView({ block: 'end' }))
    },
  },
  {
    name: 'billing',
    path: '/settings/billing',
    on: ['desktop'],
    ready: (page) => page.getByText('Projected this period').first(),
  },
  {
    name: 'usage',
    path: '/settings/billing?tab=usage',
    on: ['desktop'],
    ready: (page) => page.getByText('Machine usage and allowance').first(),
  },
  {
    name: 'hosting',
    path: '/settings/billing?tab=hosting',
    on: ['desktop'],
    ready: (page) => page.getByTestId('billing-hosting-tab'),
  },
]

/**
 * What differs between two runs and is not styling: the version and commit in
 * the settings footer (masked, so a baseline is not tied to the commit that
 * built it), and the transcript's "Scroll to bottom" pill.
 */
const masks = (page: Page) => [
  page.locator('[data-section-id="about"]'),
  // A floating pill over the transcript, blurred over whatever scrolls under
  // it: its pixels follow the text beneath, not its own styling.
  scrollToBottomButton(page),
]

test.describe('visual baseline', () => {
  // Font rasterization differs per OS and per installed font set, so the
  // baselines are captured in, and compared in, the pinned Playwright image
  // (scripts/visual-docker.sh sets VISUAL_IN_CONTAINER). Anywhere else a
  // comparison means nothing, so it is refused rather than skipped.
  // VISUAL_ALLOW_HOST=1 runs it on the host anyway, to debug a surface: its
  // pixels are not comparable with the baselines.
  test.beforeAll(() => {
    if (process.env.VISUAL_IN_CONTAINER || process.env.VISUAL_ALLOW_HOST) return
    throw new Error(
      'The visual baseline runs in the pinned Playwright image: use `task test:web:visual` ' +
        '(frontends/web/scripts/visual-docker.sh). VISUAL_ALLOW_HOST=1 runs it here, for debugging only.',
    )
  })

  for (const surface of SURFACES) {
    for (const viewport of surface.on) {
      // The tag picks the projects that capture it (playwright.visual.config.ts).
      test(`${surface.name} (${viewport})`, { tag: `@${viewport}` }, async ({ page }, testInfo) => {
        const errors: string[] = []
        page.on('pageerror', (error) => errors.push(error.message))

        const server = await installHarness(page, surface.harness)
        await page.goto(surface.path)
        if (surface.act) {
          await settle(page)
          await surface.act(page)
        }
        await expect(surface.ready(page)).toBeVisible({ timeout: 20_000 })
        // Park the pointer off the page: a click in `act` leaves it over a
        // row, whose hover actions would otherwise show in some runs only.
        await page.mouse.move(-1, -1)
        await settle(page)

        if (process.env.VISUAL_FXLOG && server.unknown.size) {
          console.log(`[visual] ${surface.name} (${testInfo.project.name}): no fixture for ${[...server.unknown.keys()].join(', ')}`)
        }
        await expect(page).toHaveScreenshot(`${surface.name}.png`, { ...SHOT, mask: masks(page) })
        expect(errors, 'uncaught page errors').toEqual([])
      })
    }
  }
})
