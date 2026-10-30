import { expect, test, type Page } from '@playwright/test';
import { create, toBinary } from '@bufbuild/protobuf';

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

async function mockWideChat(page: Page) {
  await mockAppsBackend(page);
  const chatId = DEPLOY_CHAT.id;
  await page.route('**/reliant.v1.StreamingService/StreamChatUpdates', (route) => {
    const snapshot = create(ChatStreamEventSchema, {
      event: {
        case: 'snapshot',
        value: {
          messages: [
            {
              id: 'msg_user', chatId, thread: chatId, seq: 1n, role: 1,
              contentBlocks: [{ id: 'u1', index: 0, type: 1, content: `Please look at ${LONG_URL}` }],
            },
            {
              id: 'msg_wide', chatId, thread: chatId, seq: 2n, role: 2,
              contentBlocks: [{ id: 'w1', index: 0, type: 1, content: WIDE_BODY }],
            },
          ],
          latestSequence: 2n,
        },
      },
    });
    return route.fulfill({
      contentType: 'application/connect+proto',
      body: Buffer.concat([frame(toBinary(ChatStreamEventSchema, snapshot)), endStream]),
    });
  });
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
