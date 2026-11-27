/**
 * The visual baseline: the key surfaces of the production build, against the
 * shop fixture pack served in the page (fixture mode: src/fixtures/shop, the
 * one world the public demo and the hosted preview also show), in dark and
 * light at 1440×900 and dark at 390×844 (the projects in
 * playwright.visual.config.ts). Run it with
 * `task test:web:visual`; e2e/visual/README.md says how to read and update it.
 *
 * A capture that differs from its baseline fails with a diff image in
 * test-results-visual/. When a change is MEANT to move pixels, re-run with
 * `-- --update-snapshots` and commit the new baselines: the PR diff then shows
 * the before and after side by side.
 */
import { expect, test, type Locator, type Page } from '@playwright/test'

import { CONFIG, installHarness, readProbe, settle, SHOT, type HarnessOptions } from './harness'

type Viewport = 'desktop' | 'mobile'

/**
 * The shop world's ids (src/fixtures/shop/workspace.ts and data/catalog.json),
 * written out: the app's modules do not load in Playwright's Node process.
 * A wrong one fails its surface's `ready`.
 */
const PROJECT_ID = 'shop-proj-shop'
const CHAT_IDS = {
  long: 'shop-chat-cart-totals', tools: 'shop-chat-tax-rates', failure: 'shop-chat-stripe-sdk',
  /** Twelve sub-agents at once: the chat-agents overlay's chat (src/fixtures/shop/overlays/agents.ts), opened by URL, never listed. */
  agents: 'shop-chat-agents',
}
const CHAT_TITLES = { tools: 'Move tax rates behind the pricing config', failure: 'Upgrade the Stripe SDK to v18' }
/** The chat filter's three projects: shop, docs and mobile-api. */
const FILTER_PROJECT_IDS = ['shop-proj-shop', 'shop-proj-docs', 'shop-proj-mobile-api']
/** A project with no app (`infra`): a project's own page, which a deployed project's id redirects past. */
const NO_APP_PROJECT_ID = 'shop-proj-infra'

/**
 * Writes the app is KNOWN to make, by rpc: each says why, and names the
 * SURFACES whose load (or `act`) makes it, by `name`, or `name (viewport)`
 * where only one viewport does. An entry with `except` is made by every
 * surface but those, and `maybe` names surfaces that make it in some runs
 * only (a race the app owns), where it is allowed but not required. Every
 * one stays refused (a fixture transport never
 * answers a write), none shows anything on screen (each is caught and
 * logged, which the baselines prove), and each surface asserts its set
 * EXACTLY: a surface that stops making one fails until its name goes, and a
 * new write fails until it is named here. The one table: fixture mode's own
 * checks run in this loop.
 */
const KNOWN_REFUSALS: Record<string, { why: string; surfaces?: string[]; except?: string[]; maybe?: string[] }> = {
  // reliant#3458. Every surface that selects a project, which with `shop`
  // paths includes the console's; not the org-wide and settings pages, a
  // project's own page, or a project with nothing deployed.
  'reliant.v1.ProjectService/TouchProject': {
    why: 'projectStore touches the selected project on every selection, fire-and-forget (hooks/project-queries touchProject; reliant#3458)',
    except: [
      'projects', 'projects-project-page', 'billing', 'usage', 'hosting', 'shell-usage-billing', 'shell-settings (desktop)',
      'shell-inbox (desktop)', 'gtm-strategy', 'gtm-strategy-library', 'gtm-channels', 'gtm-ideas', 'gtm-campaigns', 'gtm-calendar', 'gtm-positioning', 'shell-apps', 'shell-connectors', 'shell-empty-cta', 'shell-daemonless-home (desktop)',
      'observability-overview', 'settings-appearance', 'settings-org-github',
    ],
    // The desktop daemonless home sends Alex to Projects; a project selected
    // before that redirect lands is touched, one run in a few.
    maybe: ['shell-daemonless-home (desktop)'],
  },
  'reliant.v1.ChatService/DismissChat': {
    why: 'opening a chat from the sidebar dismisses its unread state',
    surfaces: ['chat-short'],
  },
  'controlplane.v1.DashboardService/RecordDashboardView': {
    why: 'a dashboard records each view (the analytics overview is one)',
    surfaces: ['shell-analytics', 'shell-chart-hover', 'shell-sparkline-hover', 'shell-sandboxes-dashboard'],
  },
}

/** The writes a surface on a viewport must make, and those it may make besides, sorted. */
function knownRefusals(name: string, viewport: Viewport): { must: string[]; may: string[] } {
  const names = (list: string[] | undefined) => !!list && (list.includes(name) || list.includes(`${name} (${viewport})`))
  const entries = Object.entries(KNOWN_REFUSALS)
  return {
    must: entries.filter(([, entry]) => (entry.surfaces ? names(entry.surfaces) : !names(entry.except))).map(([rpc]) => rpc).sort(),
    may: entries.filter(([, entry]) => names(entry.maybe)).map(([rpc]) => rpc).sort(),
  }
}

interface Surface {
  name: string
  path: string
  on: Viewport[]
  /** Something that is on screen only once the surface has its data. */
  ready: (page: Page) => Locator
  /** After the load, before the capture (opening the chat, say). */
  act?: (page: Page) => Promise<void>
  /**
   * After the surface is ready, the pointer parked and the page settled,
   * right before the capture: puts the pointer (or a finger) where a hover
   * surface is about (a chart's crosshair and tooltip), or grows the viewport
   * to a long page's whole height.
   */
  beforeShot?: (page: Page) => Promise<void>
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

/** Open the nav drawer on a phone: the menu button of the phone's shell, or the shell's own on an app page. */
async function openNavDrawer(page: Page) {
  await page.getByRole('button', { name: /^(Open menu|Show sidebar)/ }).first().click()
  await expect(page.getByRole('dialog', { name: 'Navigation' }).or(page.getByTestId('shell-nav')).first()).toBeVisible()
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

/**
 * Rest the pointer at `fraction` of a chart's plot (uPlot's `.u-over`), or tap
 * there on a touch viewport, and wait for the chart's tooltip: the hover state
 * the chart surfaces capture.
 */
async function pointAtChart(page: Page, chart: Locator, fraction: number) {
  const plot = chart.locator('.u-over').first()
  await plot.scrollIntoViewIfNeeded()
  const box = await plot.boundingBox()
  if (!box) throw new Error('the chart has no plot to point at')
  const position = { x: Math.round(box.width * fraction), y: Math.round(box.height / 2) }
  if (page.viewportSize()!.width <= 768) await plot.tap({ position })
  else await plot.hover({ position })
  await expect(page.getByTestId('chart-tooltip')).toBeVisible()
  await frames(page)
}

/**
 * Grow the viewport to the page's whole height, so a long dashboard is one
 * capture with every panel loaded (panels query only near the viewport, and
 * the page scrolls inside the shell, not the document). Repeats until the
 * height stops moving: panels that load can change it.
 */
async function fitViewportToContent(page: Page) {
  const width = page.viewportSize()!.width
  for (let i = 0; i < 6; i++) {
    const needed = await page.evaluate(() => {
      let extra = 0
      for (const el of Array.from(document.querySelectorAll<HTMLElement>('*'))) {
        if (el.scrollHeight > el.clientHeight + 1 && getComputedStyle(el).overflowY.match(/auto|scroll/)) extra = Math.max(extra, el.scrollHeight - el.clientHeight)
      }
      return Math.ceil(window.innerHeight + extra)
    })
    if (needed <= page.viewportSize()!.height) return
    await page.setViewportSize({ width, height: needed })
    await settle(page)
  }
}

const CHART_HOVER_PATH = '/forge/p/shop/observability/dashboards/release/chart-hover'

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
    // The fixture chat (src/fixtures/shop/data/transcripts/cart-totals.json),
    // at its end: the live turn, a question, a retrying provider error, an
    // approval and a failed send.
    name: 'chat',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(FIXTURE_BOTTOM).first(),
    act: openFixtureChat,
  },
  {
    // The fixture chat with a second project attached: the composer lists
    // both as chips (the attached one removable) and offers Add project.
    name: 'chat-projects',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText(FIXTURE_BOTTOM).first(),
    act: async (page) => {
      await openFixtureChat(page)
      await expect(page.getByTestId('chat-project-chips')).toBeVisible()
    },
    harness: { overlays: ['chat-attached-projects'] },
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
      // The pinned prompt is resolved on scroll events, and the last one can
      // fire before the virtualizer has re-measured the rows above, leaving a
      // later prompt pinned at the very top (seen once in two runs). At the
      // top nothing is pinned: re-run the resolution until it agrees.
      await expect
        .poll(async () => {
          await transcript(page).evaluate((el) => el.dispatchEvent(new Event('scroll')))
          await frames(page)
          return page.getByTestId('pinned-user-message-header').filter({ visible: true }).count()
        })
        .toBe(0)
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
    // A turn that started twelve sub-agents at once (8 done, 3 running, 1
    // failed): one group row, open while any runs, one dense row per agent.
    // The whole fan-out fits on one screen.
    name: 'chat-agents',
    path: `/c/${CHAT_IDS.agents}`,
    on: ['desktop'],
    ready: (page) => page.getByText('Survey currency rounding rules').first(),
    act: async (page) => {
      await settle(page)
      await scrollToEnd(page)
    },
    harness: { overlays: ['chat-agents'] },
  },
  {
    // The same fan-out on a phone: touch has no hover, so each running
    // agent's Cancel is always shown (its title opens its thread).
    name: 'chat-agents',
    path: `/m/chats/${CHAT_IDS.agents}`,
    on: ['mobile'],
    ready: (page) => page.getByText('Survey currency rounding rules').first(),
    act: (page) => mobileChatAtEnd(page, 'Survey currency rounding rules'),
    harness: { overlays: ['chat-agents'] },
  },
  {
    name: 'chat-list',
    path: '/m/chats',
    on: ['mobile'],
    ready: (page) => page.getByText('Write the webhooks guide').first(),
  },
  {
    name: 'chat',
    // The fixture chat's end, less the failed send (the chat-no-failed-send overlay).
    path: `/m/chats/${CHAT_IDS.long}`,
    on: ['mobile'],
    ready: (page) => page.getByText(FIXTURE_LAST_SERVER_LINE).first(),
    act: async (page) => {
      await expect(page.getByText(FIXTURE_TOP).or(page.getByText(FIXTURE_LAST_SERVER_LINE)).first()).toBeVisible({ timeout: 20_000 })
      await settle(page)
      await scrollToEnd(page)
    },
    harness: { overlays: ['chat-no-failed-send'] },
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
    harness: { overlays: ['chat-no-failed-send'] },
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
    // The same turn with its errors opened: the failed step's error result
    // and the provider error's detail, each a quiet line under the indent
    // rule, not a tinted box or a bordered card.
    name: 'chat-failure-open',
    path: `/c/${CHAT_IDS.failure}`,
    on: ['desktop'],
    ready: (page) => page.getByText('The AI provider failed').first(),
    act: async (page) => {
      await expect(page.getByText(FAILURE_FIRST_LINE).first()).toBeVisible({ timeout: 20_000 })
      await settle(page)
      await transcript(page).getByRole('button', { name: /^Activity/ }).first().click()
      await transcript(page).getByRole('button', { name: /Toggle tool details for shell/ }).last().click()
      await transcript(page).getByRole('button', { name: /Click for details/ }).first().click()
      // The click leaves focus on the error row, which reveals its Copy: blur
      // it so the capture is the row at rest.
      await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
      await settle(page)
      await scrollToEnd(page)
    },
  },
  {
    // The Workflows tab: built-in and your own workflows, two on a schedule.
    name: 'workflows',
    path: '/workflows/library',
    on: ['desktop'],
    ready: (page) => page.getByText('Nightly CI triage').first(),
  },
  {
    name: 'workflows',
    path: '/m/workflows',
    on: ['mobile'],
    ready: (page) => page.getByText('Nightly CI triage').first(),
  },
  {
    // The Runs tab: live, needs-input, finished and failed runs.
    name: 'runs',
    path: '/workflows/runs',
    on: ['desktop'],
    ready: (page) => page.getByText('Draft release notes for v1.8.4').first(),
  },
  {
    name: 'runs',
    path: '/m/runs',
    on: ['mobile'],
    ready: (page) => page.getByText('Draft release notes for v1.8.4').first(),
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
    path: '/forge/p/shop',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('worker', { exact: true }).first(),
  },
  {
    // The overview's Code card (AppCodeCard, U13): shop deployed from two
    // repositories (the D5 warning), held by two of the person's projects
    // (deployed from here, same name) and a teammate's shared one. Scrolled to,
    // so the phone shows it.
    name: 'app-code-card',
    path: '/forge/p/shop',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByTestId('app-code-sources'),
    act: async (page) => {
      // The rail fills in above the card (Recent releases), so scroll once everything has landed.
      await expect(page.getByTestId('app-code-sources')).toBeVisible({ timeout: 20_000 })
      await expect(page.getByTestId('overview-recent-releases').getByText('live in prod').first()).toBeVisible({ timeout: 20_000 })
      await settle(page)
      await page.getByTestId('app-code-card').evaluate((el) => el.scrollIntoView({ block: 'start' }))
    },
    harness: { overlays: ['app-code-card'] },
  },
  {
    name: 'observability-errors',
    path: '/forge/p/shop/observability/errors',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('charge failed: provider timeout', { exact: false }).first(),
  },
  {
    name: 'environment',
    path: '/forge/p/shop/environments/prod',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('shop.example.com', { exact: true }).first(),
  },
  {
    // The project selector (Projects/ProjectSelector), as Switch project…
    // opens it over the page: pinned, recent and all, with app chips of
    // every link kind and an app with no code here. Desktop only: on a phone
    // the one shell's nav leads to the mobile surface's own project list
    // (/m/projects), so nothing there opens the selector yet.
    name: 'project-selector',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByTestId('project-selector-row-app:status-page'),
    act: async (page) => {
      // The home focuses its composer once it has rendered, and the selector
      // closes on losing focus (as project-switcher-several says): open it after.
      await expect(page.getByTestId('chat-input')).toBeFocused()
      await page.evaluate(() => window.dispatchEvent(new CustomEvent('open-project-switcher')))
    },
    harness: { overlays: ['project-selector'] },
  },
  {
    // The app's one project filter, choose N of M: the nav's switcher in
    // multi-select mode ("Select several projects…"), three projects of
    // eight chosen by the URL. Desktop only, like the selector: the phone
    // surface's chat list has its own project list.
    name: 'project-switcher-several',
    path: `/?projects=${FILTER_PROJECT_IDS.join(',')}`,
    on: ['desktop'],
    ready: (page) => page.getByTestId('project-selector-chosen-count').getByText('3 chosen'),
    act: async (page) => {
      // The home focuses its composer once it has rendered. Opening the
      // switcher before then lets that late focus land after the click, and
      // the panel closes on losing focus. So wait for the composer to have it.
      await expect(page.getByTestId('chat-input')).toBeFocused()
      await page.getByTestId('shell-project-switcher').click()
      await page.getByTestId('shell-switcher-select-several').click()
    },
    harness: { overlays: ['project-selector'] },
  },
  {
    // The chat list once three projects are chosen, from the URL: the
    // switcher names them; the Chats heading has no filter of its own.
    name: 'chat-list-several',
    path: `/?projects=${FILTER_PROJECT_IDS.join(',')}`,
    on: ['desktop'],
    ready: (page) => page.getByTestId('shell-project-name').getByText('3 projects'),
    harness: { overlays: ['project-selector'] },
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
    // The org's projects and apps, one list (design §8.3), over the same
    // catalog and pins as the selector, plus a second app without code here:
    // pinned and recent projects, a shared one, chips of each link kind, a
    // static site, and two app-only rows.
    name: 'projects',
    path: '/projects',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('ledger-worker', { exact: true }).first(),
    harness: { overlays: ['projects-page'] },
  },
  {
    // A project's page (§8.4) for a project with nothing deployed (a deployed
    // one lands on its app's Overview): the way to deploy it, its code and
    // machines, and its chats.
    name: 'projects-project-page',
    path: `/projects/${NO_APP_PROJECT_ID}`,
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByTestId('project-repo').first(),
    harness: { overlays: ['projects-page'] },
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
    // Settings › Organization GitHub for an admin whose GitHub is connected:
    // Acme's GitHub organization linked, and Alex's personal installation
    // offered to link (handlers/github.ts).
    name: 'settings-org-github',
    path: '/settings/org-github',
    on: ['desktop'],
    ready: (page) => page.getByRole('button', { name: 'Link alex-demo' }),
  },
  // The one shell (U6): the same destinations the shell hosts, named so they can be re-captured
  // as the shell changes. `ready` waits on page content, never on the sidebar.
  {
    name: 'shell-chat-home',
    path: '/',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('Migrate order confirmation emails', { exact: false }).first(),
  },
  // The floating Setup Guide (Onboarding/OnboardingChecklist.tsx): open above the chat home's
  // composer, and collapsed and open on an app's console; then the nav footer's Help menu (sidebar/HelpMenu.tsx).
  // `ready` waits on the fixture's settled progress, not on the guide alone. The guide's state and
  // progress are Alex's saved settings (the setup-guide-* overlays): the server's answer wins.
  {
    name: 'shell-setup-guide',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByRole('region', { name: 'Setup guide' }).getByText('6 of 8 done'),
    harness: { overlays: ['setup-guide-expanded'] },
  },
  {
    name: 'shell-setup-guide-app-collapsed',
    path: '/forge/p/shop',
    on: ['desktop'],
    ready: (page) => page.getByRole('button', { name: 'Get started, 6 of 8 done' }),
    harness: { overlays: ['setup-guide-collapsed'] },
  },
  {
    name: 'shell-setup-guide-app-expanded',
    path: '/forge/p/shop',
    on: ['desktop'],
    ready: (page) => page.getByRole('region', { name: 'Setup guide' }).getByText('6 of 8 done'),
    harness: { overlays: ['setup-guide-expanded'] },
  },
  {
    name: 'shell-help-menu',
    path: '/',
    on: ['desktop'],
    ready: (page) => page.getByText('Migrate order confirmation emails', { exact: false }).first(),
    act: async (page) => {
      // The composer takes focus as the chat home settles, and a menu closes when focus leaves it: open it after.
      await page.waitForFunction(() => document.activeElement?.tagName === 'TEXTAREA')
      await page.getByTestId('nav-help').click()
      await page.getByRole('menuitem', { name: 'Keyboard shortcuts' }).waitFor()
    },
  },
  {
    name: 'shell-inbox',
    path: '/inbox',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByRole('heading', { name: 'Inbox' }).first(),
  },
  {
    // The GTM area (components/Gtm, sample pack in src/fixtures/shop/handlers/reliant/gtm.ts):
    // each page waits on a sample record that only its own read can supply.
    name: 'gtm-strategy',
    path: '/gtm/strategy',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('Subscriber-led growth this half').first(),
  },
  {
    // The strategy library: real idea-bank entries that fit the sample org's
    // business (it opens on "fits my business"); the first of them by category.
    name: 'gtm-strategy-library',
    path: '/gtm/strategy?view=library',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('Annual prepay discount').first(),
  },
  {
    name: 'gtm-channels',
    path: '/gtm/channels',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('Podcast guest spots').first(),
  },
  {
    name: 'gtm-ideas',
    path: '/gtm/ideas',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('Explain roast dates in one post').first(),
  },
  {
    name: 'gtm-campaigns',
    path: '/gtm/campaigns',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('Weekly Box launch').first(),
  },
  {
    // The sample's Show HN item is dated today, so it is on the month grid at
    // desktop width and first in the phone's agenda, which is the only place it
    // is legible at 390px. The agenda shows below 768px (max-width: 767px).
    name: 'gtm-calendar',
    path: '/gtm/calendar',
    on: ['desktop', 'mobile'],
    ready: (page) =>
      page.viewportSize()!.width <= 767
        ? page.getByTestId('gtm-calendar-agenda').getByText('Show HN: the grind-size calculator we built for subscribers')
        : page.getByText('Show HN: the grind-size calculator we built for subscribers').first(),
  },
  {
    name: 'gtm-positioning',
    path: '/gtm/positioning',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('Home brewers').first(),
  },
  {
    name: 'shell-workflows',
    path: '/workflows',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByRole('heading', { name: 'Workflows' }).first(),
  },
  {
    name: 'shell-apps',
    // Projects, narrowed to the apps: the nav's Apps heading, and where the retired /apps redirects.
    path: '/projects?show=apps',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('shop', { exact: true }).first(),
  },
  {
    name: 'shell-app-overview',
    path: '/forge/p/shop',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('worker', { exact: true }).first(),
  },
  {
    // The merged project landing: /projects/<id> lands on the app's Overview (U6 5/16).
    name: 'shell-overview',
    path: `/projects/${PROJECT_ID}`,
    on: ['desktop'],
    ready: (page) => page.getByTestId('overview-attention'),
  },
  {
    name: 'shell-obs-overview',
    path: '/forge/p/shop/observability',
    on: ['desktop'],
    ready: (page) => page.getByTestId('obs-overview-slowest').getByText('CheckoutService/Checkout').first(),
  },
  {
    // Site insights › Speed: the Real Experience Score, the vitals, the routes (U6 7/16).
    name: 'shell-speed-insights',
    path: '/forge/p/shop/site-insights',
    on: ['desktop'],
    ready: (page) => page.getByTestId('speed-insights-routes').getByText('/checkout').first(),
  },
  {
    // The same page on its mobile device and P90 toggles, read from the URL.
    name: 'shell-speed-insights-mobile',
    path: '/forge/p/shop/site-insights?device=mobile&pct=p90',
    on: ['desktop'],
    ready: (page) => page.getByTestId('speed-insights-routes').getByText('/checkout').first(),
  },
  {
    // A project with nothing deployed: the page's own action, the agent and Move to Forge
    // calls to action, and the section's Learn link (U6 14/16).
    name: 'shell-empty-cta',
    path: '/forge/p/docs/services',
    on: ['desktop'],
    ready: (page) => page.getByTestId('app-section-empty'),
  },
  {
    name: 'shell-app-observability',
    path: '/forge/p/shop/observability/errors',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('charge failed: provider timeout', { exact: false }).first(),
  },
  {
    // Data is the database's home: each managed Postgres in scope (shop's is outside the app, so
    // none), then the tables. Staging: prod opens the audited-read dialog first.
    name: 'shell-data',
    path: '/forge/p/shop/data?env=staging',
    on: ['desktop'],
    ready: (page) => page.getByText('customers', { exact: true }).first(),
  },
  {
    // Storage is buckets only: the scoped environment's buckets. Staging: shop's prod storage is
    // audited, so prod opens the audited-read dialog first.
    name: 'shell-storage',
    path: '/forge/p/shop/storage?env=staging',
    on: ['desktop'],
    ready: (page) => page.getByText('uploads', { exact: true }).first(),
  },
  {
    name: 'shell-analytics',
    path: '/forge/p/shop/analytics',
    on: ['desktop'],
    ready: (page) => page.getByRole('heading', { name: 'Analytics' }).first(),
  },
  {
    name: 'shell-analytics-events',
    path: '/forge/p/shop/analytics/events',
    on: ['desktop'],
    ready: (page) => page.getByText('orders.created', { exact: true }).first(),
  },
  {
    // The forge host's shell (lib/shell.ts reads this key before the router is built).
    name: 'shell-forge-host-app-overview',
    path: '/forge/p/shop',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('worker', { exact: true }).first(),
    harness: { storage: { 'reliant.shell': 'forge' } },
  },
  {
    // The hybrid nav at its top level: switcher, Find, the rows and their drill-in chevrons, Settings.
    name: 'shell-nav-top',
    path: '/forge/p/shop',
    on: ['desktop'],
    ready: (page) => page.getByTestId('shell-nav').getByText('Configure', { exact: true }),
  },
  {
    // Drilled into Observability by the route: Back, the views, the Performance & issues divider.
    name: 'shell-nav-observability',
    path: '/forge/p/shop/observability/alerts',
    on: ['desktop'],
    ready: (page) => page.getByTestId('shell-nav').getByText('Performance & issues', { exact: true }),
  },
  {
    // On a phone the nav is a drawer: the page header's Show sidebar opens it over the page.
    name: 'shell-nav-drawer',
    path: '/forge/p/shop',
    on: ['mobile'],
    ready: (page) => page.getByTestId('shell-nav').getByText('Configure', { exact: true }),
    act: async (page) => {
      await page.getByRole('button', { name: 'Show sidebar' }).click()
    },
  },
  {
    // The phone's drawer (Mobile/MobileNavDrawer, or the shell's narrow drawer on an app page)
    // draws the one hybrid nav: open at the top level, then drilled into Observability.
    name: 'shell-m-drawer-top',
    path: '/forge/p/shop',
    on: ['mobile'],
    ready: (page) => page.getByTestId('nav-observability'),
    act: openNavDrawer,
  },
  {
    // The phone's own drawer on its chat list: drilled into Agents, Workflows open. Its rows are
    // drawn like the top level's (owner, 2026-11-24: same size, colour, indent, and an icon).
    name: 'shell-m-drawer-agents',
    path: '/m/chats',
    on: ['mobile'],
    ready: (page) => page.getByTestId('nav-workflows-library'),
    act: async (page) => {
      await expect(page.getByText('Write the webhooks guide').first()).toBeVisible({ timeout: 20_000 })
      await openNavDrawer(page)
      await expect(page.getByTestId('nav-drill-in')).toHaveAttribute('data-area', 'agents')
      await page.getByTestId('nav-workflows').click()
    },
  },
  {
    // The phone's new chat: the Project row names the machine the project is on.
    name: 'mobile-new-chat',
    path: '/m/new',
    on: ['mobile'],
    ready: (page) => page.getByTestId('mobile-new-chat-project').getByText('On cloud-machine'),
    harness: { overlays: ['new-chat-installs'] },
  },
  {
    // Its picker: shop is on the cloud machine; docs is checked out only on a machine that is
    // gone, so it offers to set it up where the chat runs.
    name: 'mobile-new-chat-projects',
    path: '/m/new',
    on: ['mobile'],
    ready: (page) => page.getByText('Which project', { exact: true }),
    act: async (page) => {
      await page.getByTestId('mobile-new-chat-project').getByText('On cloud-machine').waitFor()
      await page.getByTestId('mobile-new-chat-project').getByRole('button').click()
    },
    harness: { overlays: ['new-chat-installs'] },
  },
  {
    name: 'shell-m-drawer-observability',
    path: '/forge/p/shop',
    on: ['mobile'],
    ready: (page) => page.getByTestId('nav-drill-in'),
    act: async (page) => {
      await openNavDrawer(page)
      await page.getByTestId('nav-observability').click()
      await expect(page.getByTestId('nav-drill-in')).toBeVisible()
    },
  },
  {
    name: 'shell-services',
    path: '/forge/p/shop/services',
    on: ['desktop'],
    ready: (page) => page.getByTestId('services-row-api').first(),
  },
  {
    name: 'shell-service-detail',
    path: '/forge/p/shop/services/api',
    on: ['desktop'],
    ready: (page) => page.getByTestId('service-instances').first(),
  },
  {
    name: 'shell-services-charts',
    path: '/forge/p/shop/services?view=charts',
    on: ['desktop'],
    ready: (page) => page.getByTestId('services-charts').first(),
  },
  {
    // The All dashboards gallery: cards with lazy sparkline previews, source badges, last viewed, stars.
    name: 'shell-dashboards',
    path: '/forge/p/shop/observability/dashboards',
    on: ['desktop'],
    ready: (page) => page.getByTestId('dashboards-card-checkout'),
  },
  {
    // A full time-series chart under the pointer: the crosshair on the
    // sample, a point on each series, and the tooltip with the sample's time
    // and both series' values (the chart-hover overlay's dashboard).
    name: 'shell-chart-hover',
    path: CHART_HOVER_PATH,
    on: ['desktop'],
    ready: (page) => page.getByRole('group', { name: 'Requests and errors per second' }),
    beforeShot: (page) => pointAtChart(page, page.getByRole('group', { name: 'Requests and errors per second' }), 0.62),
    harness: { overlays: ['chart-hover'] },
  },
  {
    // A figure's card sparkline under the pointer (a tap on the phone): the
    // same crosshair and tooltip as a full chart.
    name: 'shell-sparkline-hover',
    path: CHART_HOVER_PATH,
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByTestId('obs-panel-sparkline').first(),
    beforeShot: (page) => pointAtChart(page, page.getByTestId('obs-panel-sparkline').first(), 0.4),
    harness: { overlays: ['chart-hover'] },
  },
  {
    // The platform's Sandboxes dashboard (control-plane
    // deploy/observability/dashboards/sandboxes.json, copied into
    // src/fixtures/shop/overlays/sandboxes.dashboard.json) through the real
    // DashboardView, every panel answered in the shape its SQL returns (the
    // sandboxes-dashboard overlay), whole, in one tall capture.
    name: 'shell-sandboxes-dashboard',
    path: '/forge/p/shop/observability/dashboards/release/sandboxes',
    on: ['desktop'],
    ready: (page) => page.getByText('Noisiest sandboxes', { exact: true }).first(),
    beforeShot: fitViewportToContent,
    harness: { overlays: ['sandboxes-dashboard'] },
  },
  {
    // A gallery card's preview sparkline under the pointer.
    name: 'shell-dashboards-card-hover',
    path: '/forge/p/shop/observability/dashboards',
    on: ['desktop'],
    // The Overview card is first and above the fold, so its preview (Requests) has loaded
    // (previews load near the viewport).
    ready: (page) => page.getByRole('group', { name: 'Requests, last 24 hours' }).first().locator('.u-over'),
    beforeShot: (page) => pointAtChart(page, page.getByRole('group', { name: 'Requests, last 24 hours' }).first(), 0.7),
  },
  {
    // The gallery's search, from the URL: `?q=order` keeps the cards that match.
    name: 'shell-dashboards-search',
    path: '/forge/p/shop/observability/dashboards?q=order',
    on: ['desktop'],
    ready: (page) => page.getByTestId('dashboards-gallery').getByText('Orders by state').first(),
  },
  {
    name: 'shell-auth',
    path: '/forge/p/shop/auth?env=prod',
    on: ['desktop'],
    ready: (page) => page.getByText('jon@example.com').first(),
  },
  {
    name: 'shell-auth-signin',
    path: '/forge/p/shop/auth/sign-in?env=prod',
    on: ['desktop'],
    ready: (page) => page.getByText('Configure sign-in').first(),
  },
  {
    name: 'shell-project-usage',
    path: '/forge/p/shop/usage',
    on: ['desktop'],
    ready: (page) => page.getByTestId('usage-by-env'),
  },
  {
    name: 'shell-usage-billing',
    path: '/usage',
    on: ['desktop'],
    ready: (page) => page.getByTestId('usage-billing-total'),
  },
  {
    name: 'shell-connectors',
    path: '/connectors',
    on: ['desktop'],
    ready: (page) => page.getByTestId('connectors-page'),
  },
  {
    name: 'shell-connectors-mcp',
    path: '/connectors?tab=mcp',
    on: ['desktop'],
    ready: (page) => page.locator('[data-testid="connectors-page"][data-tab="mcp"]'),
  },
  {
    // Agents with no sandbox to run a chat: ListDaemons answers empty. The
    // project's Agents page, not `/`: with no daemon the desktop home is
    // Projects (shell-daemonless-home), outside the Agents drill-in.
    name: 'shell-agents-no-sandbox',
    path: `/project/${PROJECT_ID}`,
    on: ['desktop'],
    ready: (page) => page.getByTestId('nav-drill-in'),
    harness: { overlays: ['no-daemons'] },
  },
  {
    // The daemonless home (U6 3/4): an onboarded person whose daemon list is
    // empty. The desktop home sends them to Projects, with the Set up chats
    // card there and in place of the nav's chat list. The phone keeps its chat
    // list (its home rule is unchanged; a phone opening `/` lands on /m/chats).
    name: 'shell-daemonless-home',
    path: '/',
    on: ['desktop', 'mobile'],
    ready: (page) =>
      page.viewportSize()!.width > 768
        ? page.getByRole('heading', { name: 'Projects' }).first()
        : page.getByText('Write the webhooks guide').first(),
    harness: { overlays: ['no-daemons'] },
  },
  {
    name: 'shell-settings',
    path: '/settings',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByText('alex@example.com').first(),
  },
  {
    name: 'hosting',
    path: '/settings/billing?tab=hosting',
    on: ['desktop'],
    ready: (page) => page.getByTestId('billing-hosting-tab'),
  },
  {
    // Tests › Overview on the shop fixture report (src/fixtures/shop/handlers/tests.ts, answered over
    // TestsService like every other RPC here): coverage, pass rate, flakes, the last run.
    name: 'shell-tests',
    path: '/forge/p/shop/tests',
    on: ['desktop', 'mobile'],
    ready: (page) => page.getByTestId('tests-headline'),
  },
  {
    // The same page on a control plane that does not serve TestsService (Unimplemented):
    // "this control plane doesn't store test runs yet", and no nav badge.
    name: 'shell-tests-not-configured',
    path: '/forge/p/shop/tests',
    on: ['desktop'],
    ready: (page) => page.getByText("This control plane doesn't store test runs yet").first(),
    harness: { overlays: ['tests-unimplemented'] },
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
      test(`${surface.name} (${viewport})`, { tag: `@${viewport}` }, async ({ page, baseURL }, testInfo) => {
        const errors: string[] = []
        const offOrigin: string[] = []
        const origin = new URL(baseURL!).origin
        page.on('pageerror', (error) => errors.push(error.message))
        // Fixture mode reaches nothing off the page: the pack answers every
        // RPC, and src/fixtures/outside.ts answers or refuses every fetch. The
        // configured auth origin's `/auth/v1/*` is exempt, though outside.ts
        // answers it in the page, so it should never reach the network; any
        // other path on that origin, and every other origin, fails.
        const authOrigin = new URL(CONFIG.SUPABASE_URL).origin
        page.on('request', (req) => {
          const url = new URL(req.url())
          const auth = url.origin === authOrigin && url.pathname.startsWith('/auth/v1/')
          if (url.protocol.startsWith('http') && url.origin !== origin && !auth) offOrigin.push(`${req.method()} ${req.url()}`)
        })

        await installHarness(page, surface.harness)
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
        if (surface.beforeShot) await surface.beforeShot(page)

        const probe = (await readProbe(page))!
        if (process.env.VISUAL_FXLOG) {
          const report = { rpcs: Object.keys(probe.rpcs), unknown: [...new Set(probe.unknown)], refused: [...new Set(probe.refused)] }
          console.log(`[visual] ${surface.name} (${testInfo.project.name}): ${JSON.stringify(report)}`)
        }
        await expect(page).toHaveScreenshot(`${surface.name}.png`, { ...SHOT, mask: masks(page) })
        expect(errors, 'uncaught page errors').toEqual([])
        const after = (await readProbe(page))!
        expect(after.escaped, 'rpcs that reached fetch (escaped the fixture transport)').toEqual([])
        expect(after.outside, 'fetches outside the fixture boundary').toEqual([])
        expect(offOrigin, 'requests the browser sent off the page origin').toEqual([])
        // A known write can land after the page goes quiet (a project is
        // selected once its list arrives), so the set is polled up to it.
        const { must, may } = knownRefusals(surface.name, viewport)
        const refused = async () => [...new Set((await readProbe(page))?.refused ?? [])].filter((rpc) => !may.includes(rpc)).sort()
        await expect
          .poll(refused, {
            message: `writes refused by the read-only pack (known: ${must.map((rpc) => `${rpc}: ${KNOWN_REFUSALS[rpc].why}`).join('; ') || 'none'})`,
            timeout: 10_000,
          })
          .toEqual(must)
      })
    }
  }
})
