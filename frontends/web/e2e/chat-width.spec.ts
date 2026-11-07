import { expect, test, type Page } from '@playwright/test';
import { create, toBinary, type MessageInitShape } from '@bufbuild/protobuf';

import type { MessageSchema } from '../src/gen/reliant/v1/chat_pb';
import { ChatStreamEventSchema } from '../src/gen/reliant/v1/streaming_pb';
import { DEPLOY_CHAT, mockAppsBackend } from './appsMocks';

// A long unbreakable token, a wide table or a long code line in a message must
// wrap, or scroll inside its own block. None of them may push the message past
// the transcript's width, which shows up as a horizontal scrollbar on the
// whole chat.

const LONG_URL = 'https://example.com/' + 'a'.repeat(400);
const LONG_PATH = 'internal/' + 'very_long_directory_name/'.repeat(12) + 'file.go';
const WIDE_TABLE =
  '| ' + Array.from({ length: 14 }, (_, i) => `column_header_${i}`).join(' | ') + ' |\n' +
  '| ' + Array.from({ length: 14 }, () => '---').join(' | ') + ' |\n' +
  '| ' + Array.from({ length: 14 }, (_, i) => `cell_value_${i}_${'x'.repeat(20)}`).join(' | ') + ' |';
const LONG_CODE = '```ts\nconst x = "' + 'y'.repeat(500) + '";\n```';

const WIDE_BODY = [
  `A bare URL: ${LONG_URL}`,
  `A bold identifier: **${'Z'.repeat(300)}**`,
  `A path: \`${LONG_PATH}\``,
  `Inline code: \`${'q'.repeat(300)}\``,
  WIDE_TABLE,
  LONG_CODE,
].join('\n\n');

function frame(bytes: Uint8Array): Buffer {
  const header = Buffer.alloc(5);
  header.writeUInt32BE(bytes.length, 1);
  return Buffer.concat([header, bytes]);
}
const endStream = Buffer.from([2, 0, 0, 0, 2, 123, 125]);

type SnapshotMessage = MessageInitShape<typeof MessageSchema>;

/** Serves the deploy chat with `messages` as its stream snapshot. */
async function mockChatSnapshot(page: Page, messages: SnapshotMessage[]) {
  await mockAppsBackend(page);
  await page.route('**/reliant.v1.StreamingService/StreamChatUpdates', (route) => {
    const snapshot = create(ChatStreamEventSchema, {
      event: {
        case: 'snapshot',
        value: { messages, latestSequence: BigInt(messages.length) },
      },
    });
    return route.fulfill({
      contentType: 'application/connect+proto',
      body: Buffer.concat([frame(toBinary(ChatStreamEventSchema, snapshot)), endStream]),
    });
  });
}

async function mockWideChat(page: Page) {
  const chatId = DEPLOY_CHAT.id;
  await mockChatSnapshot(page, [
    {
      id: 'msg_user', chatId, thread: chatId, seq: 1n, role: 1,
      contentBlocks: [{ id: 'u1', index: 0, type: 1, content: `Please look at ${LONG_URL}` }],
    },
    {
      id: 'msg_wide', chatId, thread: chatId, seq: 2n, role: 2,
      contentBlocks: [{ id: 'w1', index: 0, type: 1, content: WIDE_BODY }],
    },
  ]);
}

test('a wide message never scrolls the chat horizontally', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await mockWideChat(page);
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await page.getByText(DEPLOY_CHAT.title, { exact: true }).click();

  await expect(page.getByTestId('message-msg_wide')).toBeVisible({ timeout: 15_000 });
  // The last copy: the first is the pinned header's.
  await expect(page.getByTestId('message-msg_user').last()).toBeVisible();

  const geometry = await page.evaluate(() => {
    const transcript = document.querySelector<HTMLElement>('[data-context="transcript"]');
    const doc = document.scrollingElement as HTMLElement;
    const right = (id: string) => {
      const all = document.querySelectorAll<HTMLElement>(`[data-testid="${id}"]`);
      return Math.round(all[all.length - 1]?.getBoundingClientRect().right ?? -1);
    };
    return {
      transcriptOverflow: transcript ? transcript.scrollWidth - transcript.clientWidth : -1,
      documentOverflow: doc.scrollWidth - doc.clientWidth,
      transcriptRight: transcript ? Math.round(transcript.getBoundingClientRect().right) : -1,
      wideRight: right('message-msg_wide'),
      userRight: right('message-msg_user'),
    };
  });

  expect(geometry.transcriptOverflow, 'the transcript must not scroll sideways').toBeLessThanOrEqual(0);
  expect(geometry.documentOverflow, 'the page must not scroll sideways').toBeLessThanOrEqual(0);
  expect(geometry.wideRight).toBeLessThanOrEqual(geometry.transcriptRight);
  expect(geometry.userRight).toBeLessThanOrEqual(geometry.transcriptRight);
});

// A truncated line must truncate. Inside `.message-content` (a flex column
// with align-items: flex-start) a child sizes to fit its content, which never
// goes below the min-content of a `truncate` line: the whole line. So an agent
// message with a long subject, or a phone's tool group with a long command,
// grew past the transcript and the conversation scrolled sideways.

const LONG_SUBJECT =
  'Auth refactor done: 3 files changed, tests pass; PR #2390 ready for review, see the body for the full report';
const LONG_COMMAND = `echo ${'x'.repeat(400)}`;

async function mockAgentAndToolChat(page: Page) {
  const chatId = DEPLOY_CHAT.id;
  const createdAt = new Date().toISOString();
  await mockChatSnapshot(page, [
    {
      id: 'msg_user', chatId, thread: chatId, seq: 1n, role: 1, createdAt,
      contentBlocks: [{ id: 'u1', index: 0, type: 1, content: 'Ship it' }],
    },
    {
      id: 'msg_tools', chatId, thread: chatId, seq: 2n, role: 2, createdAt,
      contentBlocks: [
        { id: 'a1', index: 0, type: 1, content: 'Running the checks.' },
        {
          id: 'a2', index: 1, type: 2, toolName: 'shell', toolCallId: 'tc1', toolCallStatus: 3,
          input: JSON.stringify({ command: LONG_COMMAND, description: LONG_COMMAND }),
        },
        {
          id: 'a3', index: 2, type: 2, toolName: 'shell', toolCallId: 'tc2', toolCallStatus: 3,
          input: JSON.stringify({ command: 'true', description: 'Second step' }),
        },
        { id: 'a4', index: 3, type: 1, content: 'Done.' },
      ],
    },
    {
      id: 'msg_results', chatId, thread: chatId, seq: 3n, role: 4, createdAt,
      contentBlocks: [
        { id: 'r1', index: 0, type: 3, toolCallId: 'tc1', toolName: 'shell', content: JSON.stringify({ stdout: 'ok\n', stderr: '', exit_code: 0 }) },
        { id: 'r2', index: 1, type: 3, toolCallId: 'tc2', toolName: 'shell', content: JSON.stringify({ stdout: '', stderr: '', exit_code: 0 }) },
      ],
    },
    {
      // An inter-agent message: role USER, with a sender.
      id: 'msg_agent', chatId, thread: chatId, seq: 4n, role: 1, createdAt,
      sender: { threadId: 'thread_sub', title: 'Fix the timeline horizontal scroll regression', subject: LONG_SUBJECT },
      contentBlocks: [{ id: 'g1', index: 0, type: 1, content: 'The full report.' }],
    },
  ]);
}

async function sidewaysGeometry(page: Page) {
  return page.evaluate(() => {
    const transcript = document.querySelector<HTMLElement>('[data-context="transcript"]');
    const card = document.querySelector<HTMLElement>('[data-testid="agent-message-card"]');
    return {
      transcriptOverflow: transcript ? transcript.scrollWidth - transcript.clientWidth : -1,
      transcriptRight: transcript ? Math.round(transcript.getBoundingClientRect().right) : -1,
      cardRight: Math.round(card?.firstElementChild?.getBoundingClientRect().right ?? -1),
    };
  });
}

for (const mode of ['simplified', 'detailed'] as const) {
  test(`a long agent-message subject truncates instead of scrolling the chat sideways (${mode}, desktop)`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.addInitScript((m) => localStorage.setItem('appearance.chatThreadMode', m), mode);
    await mockAgentAndToolChat(page);
    await page.goto('/', { waitUntil: 'domcontentloaded' });
    await page.getByText(DEPLOY_CHAT.title, { exact: true }).click();
    await expect(page.getByTestId('agent-message-subject')).toContainText('Auth refactor done', { timeout: 15_000 });

    const geometry = await sidewaysGeometry(page);
    expect(geometry.transcriptOverflow, 'the transcript must not scroll sideways').toBeLessThanOrEqual(0);
    expect(geometry.cardRight).toBeLessThanOrEqual(geometry.transcriptRight);
  });

  test(`a long agent-message subject and tool command fit a phone (${mode})`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.addInitScript((m) => localStorage.setItem('appearance.chatThreadMode', m), mode);
    await mockAgentAndToolChat(page);
    await page.goto(`/m/chats/${DEPLOY_CHAT.id}`, { waitUntil: 'domcontentloaded' });
    await expect(page.getByTestId('agent-message-subject')).toContainText('Auth refactor done', { timeout: 15_000 });

    const geometry = await sidewaysGeometry(page);
    expect(geometry.transcriptOverflow, 'the transcript must not scroll sideways').toBeLessThanOrEqual(0);
    expect(geometry.cardRight).toBeLessThanOrEqual(geometry.transcriptRight);
  });
}

// The guard. overflow-x: hidden on the scroller is not enough: a hidden axis
// is still scrollable by scrollIntoView, focus, a drag-select, find-in-page and
// an iOS touch pan, so one row wider than the column left the conversation
// parked far to the right with no way back. The virtual container clips its
// x axis, which gives the scroller no horizontal range at all, whatever a row
// does. A deliberately oversized row stands in for the next one nobody has
// found yet.

for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
  test(`a row wider than the transcript cannot scroll the chat sideways (${viewport.width}px)`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await mockAgentAndToolChat(page);
    const path = viewport.width < 768 ? `/m/chats/${DEPLOY_CHAT.id}` : '/';
    await page.goto(path, { waitUntil: 'domcontentloaded' });
    if (path === '/') await page.getByText(DEPLOY_CHAT.title, { exact: true }).click();
    await expect(page.getByTestId('agent-message-subject')).toBeVisible({ timeout: 15_000 });

    const result = await page.evaluate(() => {
      const transcript = document.querySelector<HTMLElement>('[data-context="transcript"]')!;
      const row = transcript.querySelector<HTMLElement>('[data-index]')!;
      const wide = document.createElement('div');
      wide.style.width = '4000px';
      wide.style.height = '10px';
      row.appendChild(wide);
      // Every programmatic path that ignores overflow: hidden.
      transcript.scrollLeft = 100_000;
      wide.scrollIntoView({ block: 'nearest', inline: 'end' });
      return {
        scrollLeft: transcript.scrollLeft,
        overflow: transcript.scrollWidth - transcript.clientWidth,
      };
    });

    expect(result.overflow, 'the transcript must have no horizontal scroll range').toBeLessThanOrEqual(0);
    expect(result.scrollLeft, 'nothing can scroll the transcript sideways').toBe(0);
  });
}

// The guard clips; it does not lay out. Agent-written text in every kind of
// row (tool inputs, a question card, a system notice, markdown) must still
// wrap or scroll inside its own block, or it is cut off at the column edge.
// The transcript is virtualized, so this walks it top to bottom and checks
// every row it renders.

const UNBROKEN = 'x'.repeat(400);
const LONG_FILE = '/Users/someone/' + 'deeply_nested_directory/'.repeat(16) + 'file.ts';
const TOOL_NAMES = [
  'shell', 'view', 'edit', 'write', 'spawn', 'spawn_send', 'spawn_status', 'websearch', 'fetch', 'skill',
  'create_plan', 'update_task', 'ask_user', 'code_context', 'worktree', 'shell_wait', 'find_replace',
];

function wideToolInput(): string {
  return JSON.stringify({
    command: `echo ${UNBROKEN}`, description: UNBROKEN, file_path: LONG_FILE, path: LONG_FILE,
    old_string: UNBROKEN, new_string: UNBROKEN, content: UNBROKEN, prompt: UNBROKEN, title: UNBROKEN,
    preset: 'general', agent_id: UNBROKEN, subject: UNBROKEN, message: UNBROKEN, query: UNBROKEN,
    url: LONG_URL, symbol: UNBROKEN, process_id: UNBROKEN,
    questions: [{ question: `Use ${LONG_URL}?`, options: [{ label: UNBROKEN, description: UNBROKEN }] }],
    tasks: [{ title: UNBROKEN, description: UNBROKEN }],
  });
}

async function mockEveryRowKind(page: Page) {
  const chatId = DEPLOY_CHAT.id;
  const createdAt = new Date().toISOString();
  await mockChatSnapshot(page, [
    {
      id: 'msg_user', chatId, thread: chatId, seq: 1n, role: 1, createdAt,
      contentBlocks: [{ id: 'u1', index: 0, type: 1, content: `Look at ${LONG_URL}` }],
    },
    {
      id: 'msg_tools', chatId, thread: chatId, seq: 2n, role: 2, createdAt,
      contentBlocks: [
        { id: 'think', index: 0, type: 5, content: UNBROKEN },
        ...TOOL_NAMES.map((toolName, i) => ({
          id: `t${i}`, index: i + 1, type: 2, toolName, toolCallId: `call${i}`, toolCallStatus: 3, input: wideToolInput(),
        })),
        { id: 'text', index: 99, type: 1, content: WIDE_BODY },
      ],
    },
    {
      id: 'msg_results', chatId, thread: chatId, seq: 3n, role: 4, createdAt,
      contentBlocks: TOOL_NAMES.map((toolName, i) => ({
        id: `r${i}`, index: i, type: 3, toolCallId: `call${i}`, toolName,
        content: JSON.stringify({ stdout: UNBROKEN, stderr: '', exit_code: 0, output: UNBROKEN }),
      })),
    },
    {
      // A system notice (role SYSTEM, display style INFO).
      id: 'msg_notice', chatId, thread: chatId, seq: 4n, role: 3, createdAt, displayStyle: 1,
      contentBlocks: [{ id: 'n1', index: 0, type: 1, content: `Switched to ${LONG_FILE}` }],
    },
  ]);
}

/** Every element, across the whole virtualized transcript, that reaches past its edges unclipped. */
async function rowsPastTheEdge(page: Page): Promise<string[]> {
  return page.evaluate(async () => {
    const transcript = document.querySelector<HTMLElement>('[data-context="transcript"]')!;
    const edge = transcript.getBoundingClientRect();
    const outside = (r: DOMRect) => r.right > edge.right + 1 || r.left < edge.left - 1;
    const found = new Set<string>();
    for (let y = 0; y <= transcript.scrollHeight; y += Math.max(200, transcript.clientHeight / 2)) {
      transcript.scrollTop = y;
      await new Promise((resolve) => setTimeout(resolve, 100));
      for (const el of Array.from(transcript.querySelectorAll<HTMLElement>('[data-index] *'))) {
        const r = el.getBoundingClientRect();
        // An overflowing box, or a visible-overflow box whose text runs out of it.
        const textRunsOut = getComputedStyle(el).overflowX === 'visible' && r.left + el.scrollWidth > edge.right + 1;
        if (r.width === 0 || !(outside(r) || textRunsOut)) continue;
        // Fine when a box between it and the row clips it inside the column
        // (a code block or table scrolling in place).
        let clipped = false;
        for (let a = el.parentElement; a && !a.hasAttribute('data-index'); a = a.parentElement) {
          if (getComputedStyle(a).overflowX !== 'visible' && !outside(a.getBoundingClientRect())) {
            clipped = true;
            break;
          }
        }
        if (clipped) continue;
        const owner = el.closest('[data-testid]')?.getAttribute('data-testid') ?? '';
        found.add(`${owner} > ${el.tagName.toLowerCase()}.${String(el.className).slice(0, 80)}`);
      }
    }
    return Array.from(found);
  });
}

for (const mode of ['simplified', 'detailed'] as const) {
  for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
    test(`no row reaches past the transcript (${mode}, ${viewport.width}px)`, async ({ page }) => {
      await page.setViewportSize(viewport);
      await page.addInitScript((m) => localStorage.setItem('appearance.chatThreadMode', m), mode);
      await mockEveryRowKind(page);
      const path = viewport.width < 768 ? `/m/chats/${DEPLOY_CHAT.id}` : '/';
      await page.goto(path, { waitUntil: 'domcontentloaded' });
      if (path === '/') await page.getByText(DEPLOY_CHAT.title, { exact: true }).click();
      await expect(page.locator('[data-context="transcript"] [data-index]').first()).toBeVisible({ timeout: 15_000 });

      expect(await rowsPastTheEdge(page)).toEqual([]);
    });
  }
}
