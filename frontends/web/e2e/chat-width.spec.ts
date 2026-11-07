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
