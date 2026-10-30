import { test, expect, type Page } from '@playwright/test';
import { devHarnessPresent } from './devHarness';
import { mockMobileBackend, PHONE, STREAM_ERROR_MESSAGE } from './mobileMocks';

// The phone: 390×844, touch. Every surface the desktop sidebar offers must be
// reachable from the mobile drawer, a desktop link must land on its mobile
// twin, and the Inbox must be usable — approve, answer an async question —
// with nothing sideways-scrolling. Against mocked RPCs (mobileMocks.ts); no
// backend, no session. research/MOBILE_AUDIT.md has the audit this pins.

test.use(PHONE);
// A cold dev server compiles each lazy screen on first request; the first
// assertion on a screen waits for that, not for the app.
const LOADED = { timeout: 20_000 };

function collectErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (error) => {
    // A source build without Supabase keys says so; API-key login does not need them.
    if (!error.message.includes('VITE_SUPABASE_ANON_KEY')) errors.push(error.message);
  });
  return errors;
}

async function noSidewaysScroll(page: Page) {
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(width).toBeLessThanOrEqual(390);
}

test.describe('mobile surface', () => {
  test('the drawer reaches the Inbox, Apps and Observability, with the Inbox badge', async ({ page }) => {
    const errors = collectErrors(page);
    await mockMobileBackend(page);
    await page.goto('/m/chats');

    // Three blocking items in the mocks: the hamburger says so before it is opened.
    const menu = page.getByRole('button', { name: /Open menu, 3 items need you/ });
    await expect(menu).toBeVisible(LOADED);
    await menu.click();

    const drawer = page.getByRole('dialog', { name: 'Navigation' });
    await expect(drawer.getByRole('link', { name: /^Inbox/ })).toHaveAttribute('href', '/m/inbox');
    await expect(drawer.getByTestId('drawer-inbox-count')).toHaveText('3');
    await expect(drawer.getByRole('link', { name: /^Apps/ })).toHaveAttribute('href', '/apps');
    await expect(drawer.getByRole('link', { name: /^Observability/ })).toHaveAttribute('href', '/forge/observability');

    await drawer.getByRole('link', { name: /^Inbox/ }).click();
    await expect(page).toHaveURL(/\/m\/inbox$/, LOADED);
    await expect(page.getByRole('heading', { level: 1, name: 'Inbox' })).toBeVisible(LOADED);
    expect(errors).toEqual([]);
  });

  test('a desktop Inbox link lands on the mobile Inbox, not the chat list', async ({ page }) => {
    await mockMobileBackend(page);
    await page.goto('/inbox');
    await expect(page).toHaveURL(/\/m\/inbox$/, LOADED);
  });

  test('a run link opens the run as its chat', async ({ page }) => {
    await mockMobileBackend(page);
    await page.goto('/workflows/runs/chat_a');
    await expect(page).toHaveURL(/\/m\/chats\/chat_a$/, LOADED);
  });

  test('the Apps console stays on a phone instead of bouncing to the chat list', async ({ page }) => {
    await mockMobileBackend(page);
    await page.goto('/forge');
    await page.waitForTimeout(1500);
    expect(new URL(page.url()).pathname.startsWith('/m/')).toBe(false);
  });

  test('the Inbox approves, and answers an async question, at touch size', async ({ page }) => {
    const errors = collectErrors(page);
    const mock = await mockMobileBackend(page);
    let approved = false;
    await page.route('**/reliant.v1.ApprovalService/Approve*', (route) => {
      approved = true;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true }) });
    });
    await page.goto('/m/inbox');

    const approval = page.getByTestId('inbox-item-approval:ap_1');
    const approve = approval.getByRole('button', { name: 'Approve' });
    await expect(approve).toBeVisible(LOADED);
    const box = await approve.boundingBox();
    expect(box!.height).toBeGreaterThanOrEqual(44);
    await approve.click();
    await expect.poll(() => approved).toBe(true);

    const question = page.getByTestId('inbox-item-question:q_1');
    await expect(question.getByText(/Not blocking/)).toBeVisible();
    await question.getByRole('button', { name: /Answer/ }).click();
    await question.getByRole('radio', { name: /Read replica/ }).click();
    await question.getByRole('button', { name: /Submit/ }).click();
    await expect.poll(() => mock.answered.length).toBe(1);
    expect(mock.answered[0]).toMatchObject({ questionId: 'q_1', action: 'reply' });

    await noSidewaysScroll(page);
    expect(errors).toEqual([]);
  });

  test('every control in the chat timeline and composer is a 44px target', async ({ page }) => {
    await mockMobileBackend(page);
    await page.goto('/m/chats/chat_a');
    await expect(page.getByTestId('ask-user-card')).toBeVisible(LOADED);
    const small = await page.evaluate(() =>
      Array.from(document.querySelectorAll('button, a[href], [role="button"], [role="radio"], textarea, select'))
        .filter((el) => {
          const r = el.getBoundingClientRect();
          const s = getComputedStyle(el);
          return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.pointerEvents !== 'none' && Number(s.opacity) > 0;
        })
        .filter((el) => {
          const r = el.getBoundingClientRect();
          return r.width < 44 || r.height < 44;
        })
        .map((el) => el.getAttribute('aria-label') || (el as HTMLElement).innerText.trim().slice(0, 30) || el.tagName),
    );
    expect(small).toEqual([]);
    // No text field small enough for iOS to zoom the page into it.
    const fieldFont = await page.getByPlaceholder('Or type your own answer...').evaluate((el) => parseFloat(getComputedStyle(el).fontSize));
    expect(fieldFont).toBeGreaterThanOrEqual(16);
    await noSidewaysScroll(page);
  });

  test('an async question is answered in the timeline, then collapses to one line', async ({ page }) => {
    const mock = await mockMobileBackend(page);
    await page.goto('/m/chats/chat_a');
    const card = page.getByTestId('ask-user-card');
    await expect(card).toBeVisible(LOADED);
    await card.getByRole('radio', { name: /Login only/ }).click();
    const submit = card.getByRole('button', { name: /Submit/ });
    expect((await submit.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await submit.click();
    await expect.poll(() => mock.answered.length).toBe(1);
    expect(mock.answered[0]).toMatchObject({ questionId: 'q_timeline', action: 'reply' });

    const summary = card.getByTestId('ask-user-summary');
    await expect(summary).toBeVisible(LOADED);
    await expect(summary).toHaveAttribute('aria-expanded', 'false');
    await expect(summary).toContainText('Should the retry fix cover the signup flow too?');
    await expect(summary).toContainText('Login only');
    expect((await summary.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await summary.click();
    await expect(card.getByTestId('ask-user-answers')).toContainText('Login only');
  });

  test('Diagnostics shows the stream state and copies a report', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await mockMobileBackend(page, { stream: 'unavailable' });
    await page.goto('/m/diagnostics');
    const screen = page.getByTestId('mobile-diagnostics');
    await expect(screen.getByText('Update stream', { exact: true })).toBeVisible(LOADED);
    // The mocked stream fails, and the failure is recorded on the device.
    await expect(screen.getByTestId('diagnostics-error').filter({ hasText: STREAM_ERROR_MESSAGE }).first()).toBeVisible();
    await screen.getByRole('button', { name: 'Copy diagnostics' }).click();
    await expect(screen.getByRole('button', { name: 'Copied' })).toBeVisible();
    const copied = await page.evaluate(() => navigator.clipboard.readText());
    expect(copied).toContain('Reliant diagnostics');
    expect(copied).toContain('Recent errors');
    await noSidewaysScroll(page);
  });

  test('the console nav is an off-canvas drawer, and nothing scrolls sideways', async ({ page }) => {
    await mockMobileBackend(page);
    await page.goto('/forge-workloads-preview?desktop=1');
    const shell = page.getByTestId('forge-shell').first();
    const present = await devHarnessPresent(page, '/forge-workloads-preview', shell);
    test.skip(!present, 'the fixture harness is a dev-build-only route');

    await noSidewaysScroll(page);
    const open = page.getByRole('button', { name: 'Open navigation' }).first();
    await expect(open).toBeVisible();
    await open.click();
    const nav = page.getByRole('dialog').filter({ has: page.getByRole('link', { name: 'Releases' }) }).first();
    await expect(nav).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(nav).toBeHidden();
  });
});
