import { expect, test, type Page } from '@playwright/test';
import { mockMobileBackend } from './mobileMocks';
import { logChatGeometry, settledChatGeometry } from './helpers/chatGeometry';

// Vertical budget of the mobile chat screen: how much of a phone screen the
// conversation actually gets. dev-docs/design/mobile-chat-redesign.md §1.1
// records the numbers this prints and §12 sets the target.
//
// The chat is the busiest one the fixtures can make (`busyChat`): a running
// workflow with two threads, and a long last turn the timeline pins. RPC
// fixtures isolate layout from services.
//
// 390×480 is a short window, NOT a software keyboard: browser emulation
// cannot open one (dev-docs/responsive-ui-standards.md, "Native acceptance").
// It stands in for the ~400–480px a phone has left with the keyboard up, and
// the keyboard-open evidence stays a Device Hub task.

test.use({ hasTouch: true, isMobile: true, deviceScaleFactor: 2 });

const PHONE = { width: 390, height: 844 };
const SHORT = { width: 390, height: 480 };

async function openBusyChat(page: Page, viewport: { width: number; height: number }) {
  await mockMobileBackend(page, { busyChat: true });
  await page.setViewportSize(viewport);
  await page.goto('/m/chats/chat_a');
  await expect(page.locator('[data-context="transcript"]')).toBeVisible();
  // A one-line draft, then blur: the empty composer's placeholder is a random
  // example prompt that wraps to one or two lines, so its height is not a
  // fixed number. Blurring keeps any focus-driven chrome out of the reading.
  const composer = page.getByRole('textbox', { name: 'Message the agent', exact: true });
  await composer.fill('Looks good, ship it.');
  await composer.blur();
  // The long last turn is rendered and its prompt has scrolled under the top
  // edge: the pinned header is the tallest state the transcript chrome has.
  await expect(page.getByRole('heading', { name: 'Not done', exact: true })).toBeVisible();
  await expect(page.getByTestId('pinned-user-message-header')).toBeVisible();
}

for (const viewport of [PHONE, SHORT]) {
  test(`chat band heights at ${viewport.width}×${viewport.height}`, async ({ page }, info) => {
    await openBusyChat(page, viewport);
    const geometry = await settledChatGeometry(page);
    await logChatGeometry(info, '/m/chats/chat_a', geometry);
    // Sanity, not a target: the slots tile the screen and the transcript has one.
    const total = geometry.bands.filter((b) => b.kind === 'band').reduce((sum, b) => sum + b.height, geometry.transcript);
    expect(total).toBe(viewport.height);
    expect(geometry.transcript).toBeGreaterThan(0);
  });
}

// At ≥ 840px /m/chats/$chatId is list–detail: the chat list stays beside the
// thread. The bands are measured beside the list, so the aside never counts
// as chrome. (The 834px tablet project is under the breakpoint.)
test('expanded width shows the chat list beside the thread', async ({ page }, info) => {
  await openBusyChat(page, { width: 1024, height: 768 });
  await expect(page.getByRole('complementary', { name: 'Chats' })).toBeVisible();
  await expect(page.getByTestId('mobile-chat-top-bar')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Back to chats' })).toHaveCount(0);
  const geometry = await settledChatGeometry(page);
  await logChatGeometry(info, '/m/chats/chat_a (split)', geometry);
  expect(geometry.transcript).toBeGreaterThan(0);
});

// The §12 acceptance gate: transcript ≥ 66% of the visible height with no
// status line. Declared fixme while the redesign phases land; the redesign's
// final verification task flips it to `test(...)`. A declaration, not a
// runtime skip, and outside the required mobile-responsive suite.
test.fixme('readable transcript is at least 66% of a 390×480 window', async ({ page }) => {
  await openBusyChat(page, SHORT);
  const geometry = await settledChatGeometry(page);
  await expect(page.getByTestId('composer-status-slot'), 'the fixture must show no status line').toHaveCount(0);
  expect(geometry.readable / SHORT.height).toBeGreaterThanOrEqual(0.66);
});
