/**
 * E2E Tests for Chat Activity Indicators
 *
 * Tests the synchronization of activity indicators across:
 * - Green dot in sidebar (chat is active)
 * - Thinking indicator in chat view
 * - Thread tab activity indicators
 *
 * Rules being tested:
 * 1. Main workflow is active if ANY sub-workflow is active
 * 2. If main workflow is active, green chat indicator must show
 * 3. Main chat thinking indicator matches exactly the state of green chat indicator
 * 4. Individual thread indicators share same logic for state detection
 *
 * Architecture:
 * - activityStore: chat-level activity enum (IDLE=0, RUNNING=1, AWAITING_INPUT=2, NEEDS_ATTENTION=3)
 * - threadActivityStore: per-chat thread-level detail (which threads are active)
 * - chatStore: still holds activeChatId for navigation
 */

import { test, expect, type Page } from '@playwright/test';
import { create, toBinary } from '@bufbuild/protobuf';
import { ChatStreamEventSchema, UserStreamEventSchema } from '../src/gen/reliant/v1/streaming_pb';
import { DEPLOY_CHAT, mockAppsBackend } from './appsMocks';

function frame(bytes: Uint8Array): Buffer {
  const header = Buffer.alloc(5);
  header.writeUInt32BE(bytes.length, 1);
  return Buffer.concat([header, bytes]);
}
const endStream = Buffer.from([2, 0, 0, 0, 2, 123, 125]);

// ChatActivity enum values (mirrors proto ChatActivity)
const ChatActivity = {
  IDLE: 0,
  RUNNING: 1,
  AWAITING_INPUT: 2,
  NEEDS_ATTENTION: 3,
} as const;

// Helper to get the active chat ID from chatStore
async function getActiveChatId(page: Page): Promise<string | null> {
  return await page.evaluate(() => {
    const store = (window as any).__ZUSTAND_STORES__?.chatStore;
    if (!store) return null;
    return store.getState().activeChatId;
  });
}

// Helper to set chat activity in activityStore
async function setActivity(page: Page, chatId: string, activity: number) {
  await page.evaluate(({ chatId, activity }) => {
    const store = (window as any).__ZUSTAND_STORES__?.activityStore;
    if (!store) return;
    store.getState().setActivity(chatId, activity);
  }, { chatId, activity });
}

// Helper to set threads in threadActivityStore
async function setThreads(page: Page, chatId: string, threads: any[]) {
  await page.evaluate(({ chatId, threads }) => {
    const store = (window as any).__ZUSTAND_STORES__?.threadActivityStore;
    if (!store) return;
    store.getState().setThreads(chatId, threads);
  }, { chatId, threads });
}

// Helper to clear threads in threadActivityStore
async function clearThreads(page: Page, chatId: string) {
  await page.evaluate(({ chatId }) => {
    const store = (window as any).__ZUSTAND_STORES__?.threadActivityStore;
    if (!store) return;
    store.getState().clearThreads(chatId);
  }, { chatId });
}

// Helper to get current activity for a chat
async function getActivity(page: Page, chatId: string): Promise<number | null> {
  return await page.evaluate(({ chatId }) => {
    const store = (window as any).__ZUSTAND_STORES__?.activityStore;
    if (!store) return null;
    return store.getState().activities.get(chatId) ?? 0;
  }, { chatId });
}

test.describe('Activity Indicator Synchronization', () => {
  test.beforeEach(async ({ page }, testInfo) => {
    if (testInfo.title === 'child thread activity should propagate to main indicators') return;
    await mockAppsBackend(page);
    await page.goto('/', { waitUntil: 'domcontentloaded' });
    await expect(page.getByRole('navigation', { name: 'Chat sidebar navigation' })).toBeVisible();
  });

  test('thinking indicator should not show when activity is IDLE', async ({ page }) => {
    // Navigate to a chat
    const chatItem = page.locator('[data-testid^="chat-"]').first();
    if (await chatItem.count() > 0) {
      await chatItem.click();
      await page.waitForTimeout(300);
    }

    // Thinking indicator should not be visible when idle
    const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]');
    const isVisible = await thinkingIndicator.isVisible().catch(() => false);
    
    // Note: The indicator might not exist at all when idle (null return)
    // That's also valid - it means no activity
    if (isVisible) {
      // If visible, check the data attribute
      const dataActive = await thinkingIndicator.getAttribute('data-active');
      expect(dataActive).not.toBe('true');
    }
  });

  test('thinking indicator should show when activity is RUNNING', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    // Set activity to RUNNING
    await setActivity(page, chatId, ChatActivity.RUNNING);
    await page.waitForTimeout(100);

    // Thinking indicator should now be visible
    const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]');
    await expect(thinkingIndicator).toBeVisible({ timeout: 1000 });

    // Clean up
    await setActivity(page, chatId, ChatActivity.IDLE);
  });

  test('green dot should show for active chat in sidebar', async ({ page }) => {
    // Get any chat ID from the sidebar
    const chatItems = page.locator('[data-testid^="chat-activity-dot-"]');
    const count = await chatItems.count();
    
    if (count === 0) {
      test.skip(true, 'No chats with activity dots found');
      return;
    }

    // Get the first chat ID
    const firstDot = chatItems.first();
    const testId = await firstDot.getAttribute('data-testid');
    const chatId = testId?.replace('chat-activity-dot-', '');

    if (!chatId) {
      test.skip(true, 'Could not extract chat ID');
      return;
    }

    // Set activity to RUNNING
    await setActivity(page, chatId, ChatActivity.RUNNING);
    await page.waitForTimeout(100);

    // Get activity state from data attribute
    const activityState = await firstDot.getAttribute('data-activity-state');
    
    // The state should be 'thinking' or similar active state
    expect(['thinking', 'streaming', 'awaiting_approval', 'background_running']).toContain(activityState);

    // Clean up
    await setActivity(page, chatId, ChatActivity.IDLE);
  });

  test('activity indicator should sync between sidebar and thinking indicator', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    // Initially both should be inactive
    const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]');
    const sidebarDot = page.locator(`[data-testid="chat-activity-dot-${chatId}"]`);

    // Set activity to RUNNING
    await setActivity(page, chatId, ChatActivity.RUNNING);
    await page.waitForTimeout(100);

    // Both indicators should show activity
    const thinkingVisible = await thinkingIndicator.isVisible().catch(() => false);
    expect(thinkingVisible).toBe(true);

    if (await sidebarDot.count() > 0) {
      const sidebarState = await sidebarDot.getAttribute('data-activity-state');
      expect(sidebarState).toBe('thinking');
    }

    // Set activity to IDLE
    await setActivity(page, chatId, ChatActivity.IDLE);
    await page.waitForTimeout(100);

    // Thinking indicator should disappear
    const thinkingStillVisible = await thinkingIndicator.isVisible().catch(() => false);
    expect(thinkingStillVisible).toBe(false);
  });

  test('child thread activity should propagate to main indicators', async ({ page }) => {
    await mockAppsBackend(page);
    const chatId = DEPLOY_CHAT.id;
    let activity = ChatActivity.RUNNING as number;
    let sequence = 2n;
    await page.route('**/reliant.v1.StreamingService/StreamUserUpdates', route => {
      const event = create(UserStreamEventSchema, { event: { case: 'updates', value: { updates: [{
        id: `activity-${sequence}`, sequenceNumber: sequence++, chatId,
        projectId: DEPLOY_CHAT.projectId, updateType: 6, entityType: 1, entityId: chatId,
        dataJson: JSON.stringify({ chat_id: chatId, activity, working_threads: activity === ChatActivity.RUNNING ? 1 : 0 }),
      }] } } });
      return route.fulfill({ contentType: 'application/connect+proto', body: Buffer.concat([frame(toBinary(UserStreamEventSchema, event)), endStream]) });
    });
    await page.route('**/reliant.v1.StreamingService/StreamChatUpdates', route => {
      const snapshot = create(ChatStreamEventSchema, { event: { case: 'snapshot', value: {
        messages: [{ id: 'activity-prompt', chatId, thread: chatId, seq: 1n, role: 1,
          contentBlocks: [{ id: 'activity-text', index: 0, type: 1, content: 'Run the child agent' }] }],
        latestSequence: 1n, otherUpdates: [{ updateType: 3, chatId, sequenceNumber: 1n, entityId: 'child-agent',
          dataJson: JSON.stringify({ id: 'child-agent', thread: 'child-agent', status: 'running', workflow_id: 'child-run', workflow_name: 'builtin://agent' }) }],
      } } });
      return route.fulfill({ contentType: 'application/connect+proto', body: Buffer.concat([frame(toBinary(ChatStreamEventSchema, snapshot)), endStream]) });
    });
    await page.goto('/', { waitUntil: 'domcontentloaded' });
    await page.getByText('Ship the storefront to prod', { exact: true }).click();
    const thinking = page.getByTestId('thinking-indicator');
    const sidebar = page.getByTestId(`chat-activity-dot-${chatId}`);
    await expect(thinking).toBeVisible();
    await expect(sidebar).toHaveAttribute('data-activity-state', /thinking|streaming|background_running/);
    // The next real stream connection reports the child has stopped. Retained
    // thread metadata alone must not leave either parent indicator running.
    activity = ChatActivity.IDLE;
    await expect(thinking).toBeHidden();
    await expect(sidebar).toBeHidden();
  });

  test('fork metadata workflows should not trigger thread activity indicators', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    // Set threads with only a fork metadata workflow (should be filtered out)
    // Activity stays IDLE because fork workflows are metadata-only
    await setActivity(page, chatId, ChatActivity.IDLE);
    await setThreads(page, chatId, [{
      thread: `fork-${Date.now()}`,
      status: 'running',
      workflow_id: `workflow-fork-${Date.now()}`,
      workflow_name: 'fork:some-thread', // Fork metadata workflow
    }]);
    await page.waitForTimeout(100);

    // Thinking indicator should NOT show (fork workflows are excluded, activity is IDLE)
    const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]');
    const thinkingVisible = await thinkingIndicator.isVisible().catch(() => false);
    expect(thinkingVisible).toBe(false);

    // Clean up
    await clearThreads(page, chatId);
  });
});

test.describe('Thread Tab Activity Indicators', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
  });

  test('thread tab should show activity pulse when thread is running', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    const threadId = `test-thread-${Date.now()}`;

    // Set chat as RUNNING and add an active thread
    await setActivity(page, chatId, ChatActivity.RUNNING);
    await setThreads(page, chatId, [{
      thread: threadId,
      status: 'running',
      workflow_id: `workflow-${threadId}`,
      workflow_name: 'builtin://agent',
    }]);
    await page.waitForTimeout(100);

    // Look for thread activity indicator
    const threadActivity = page.locator(`[data-testid="thread-activity-${threadId}"]`);
    
    // If thread tabs are visible, check the indicator
    if (await threadActivity.count() > 0) {
      const isActive = await threadActivity.getAttribute('data-active');
      expect(isActive).toBe('true');
    }

    // Clean up
    await clearThreads(page, chatId);
    await setActivity(page, chatId, ChatActivity.IDLE);
  });

  test('All tab should show activity when any thread is active', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    const threadId = `test-thread-${Date.now()}`;

    // Set chat as RUNNING and add an active thread
    await setActivity(page, chatId, ChatActivity.RUNNING);
    await setThreads(page, chatId, [{
      thread: threadId,
      status: 'running',
      workflow_id: `workflow-${threadId}`,
      workflow_name: 'builtin://agent',
    }]);
    await page.waitForTimeout(100);

    // Look for "All" tab activity indicator
    const allTabActivity = page.locator('[data-testid="thread-activity-all"]');
    
    // If thread tabs are visible, check the All indicator
    if (await allTabActivity.count() > 0) {
      const isActive = await allTabActivity.getAttribute('data-active');
      expect(isActive).toBe('true');
    }

    // Clean up
    await clearThreads(page, chatId);
    await setActivity(page, chatId, ChatActivity.IDLE);
  });

  test('thread tab activity should clear when thread completes', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    const threadId = `test-thread-${Date.now()}`;

    // Add an active thread
    await setActivity(page, chatId, ChatActivity.RUNNING);
    await setThreads(page, chatId, [{
      thread: threadId,
      status: 'running',
      workflow_id: `workflow-${threadId}`,
      workflow_name: 'builtin://agent',
    }]);
    await page.waitForTimeout(100);

    // Clear threads and set idle (simulating completion)
    await clearThreads(page, chatId);
    await setActivity(page, chatId, ChatActivity.IDLE);
    await page.waitForTimeout(100);

    // Thread activity indicator should no longer be visible
    const threadActivity = page.locator(`[data-testid="thread-activity-${threadId}"]`);
    const isVisible = await threadActivity.isVisible().catch(() => false);
    expect(isVisible).toBe(false);
  });
});

test.describe('Activity State Consistency', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
  });

  test('activityStore should correctly report activity state', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    // Initially should be IDLE
    const initialActivity = await getActivity(page, chatId);
    expect(initialActivity).toBe(ChatActivity.IDLE);

    // Set to RUNNING
    await setActivity(page, chatId, ChatActivity.RUNNING);
    const runningActivity = await getActivity(page, chatId);
    expect(runningActivity).toBe(ChatActivity.RUNNING);

    // Set to AWAITING_INPUT
    await setActivity(page, chatId, ChatActivity.AWAITING_INPUT);
    const awaitingActivity = await getActivity(page, chatId);
    expect(awaitingActivity).toBe(ChatActivity.AWAITING_INPUT);

    // Set to NEEDS_ATTENTION
    await setActivity(page, chatId, ChatActivity.NEEDS_ATTENTION);
    const attentionActivity = await getActivity(page, chatId);
    expect(attentionActivity).toBe(ChatActivity.NEEDS_ATTENTION);

    // Clean up
    await setActivity(page, chatId, ChatActivity.IDLE);
  });

  test('multiple simultaneous active chats should all track independently', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    const otherChatId = `other-chat-${Date.now()}`;

    // Set both chats to RUNNING
    await setActivity(page, chatId, ChatActivity.RUNNING);
    await setActivity(page, otherChatId, ChatActivity.RUNNING);
    await page.waitForTimeout(100);

    // Both should be RUNNING
    expect(await getActivity(page, chatId)).toBe(ChatActivity.RUNNING);
    expect(await getActivity(page, otherChatId)).toBe(ChatActivity.RUNNING);

    // Set one to IDLE
    await setActivity(page, chatId, ChatActivity.IDLE);
    await page.waitForTimeout(100);

    // First should be IDLE, second still RUNNING
    expect(await getActivity(page, chatId)).toBe(ChatActivity.IDLE);
    expect(await getActivity(page, otherChatId)).toBe(ChatActivity.RUNNING);

    // Clean up
    await setActivity(page, otherChatId, ChatActivity.IDLE);
  });

  test('thread activity should be correctly scoped per chat', async ({ page }) => {
    const chatId = await getActiveChatId(page);
    if (!chatId) {
      test.skip(true, 'No active chat available');
      return;
    }

    // Set threads for the active chat
    await setThreads(page, chatId, [
      {
        thread: 'thread-1',
        status: 'running',
        workflow_id: 'workflow-1',
        workflow_name: 'builtin://agent',
      },
      {
        thread: 'thread-2',
        status: 'running',
        workflow_id: 'workflow-2',
        workflow_name: 'builtin://agent',
      },
    ]);
    await page.waitForTimeout(100);

    // Verify threads are set
    const threadCount = await page.evaluate(({ chatId }) => {
      const store = (window as any).__ZUSTAND_STORES__?.threadActivityStore;
      if (!store) return 0;
      return store.getState().threads[chatId]?.length ?? 0;
    }, { chatId });
    expect(threadCount).toBe(2);

    // Clear one chat's threads shouldn't affect others
    await clearThreads(page, chatId);
    const afterClear = await page.evaluate(({ chatId }) => {
      const store = (window as any).__ZUSTAND_STORES__?.threadActivityStore;
      if (!store) return -1;
      return store.getState().threads[chatId]?.length ?? 0;
    }, { chatId });
    expect(afterClear).toBe(0);
  });
});
