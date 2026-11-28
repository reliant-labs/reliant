// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";

// The app's chat stream asks the server not to count the chat's messages for
// the snapshot (StreamChatUpdatesRequest.skip_total): nothing reads the
// total, and on a long chat the count was the slowest read of the open.

type Request = { chatId: string; sinceSeq: bigint; skipTotal?: boolean };

const requests: Request[] = [];

vi.mock("../../lib/connect", () => ({
  connectClient: () => ({
    streamChatUpdates: (request: Request) => {
      requests.push(request);
      return {
        async *[Symbol.asyncIterator]() {
          await new Promise(() => undefined);
          yield undefined;
        },
      };
    },
  }),
}));

vi.mock("../../lib/chatOpenInteraction", () => ({
  markChatOpen: vi.fn(),
  recordingChatOpen: vi.fn(),
  noteChatOpenSync: vi.fn(),
}));

import { ChatStreamingService } from "../chat-streaming-grpc";

const CHAT = "aaaaaaaa-1111-2222-3333-444444444444";
const OTHER = "bbbbbbbb-1111-2222-3333-444444444444";

afterEach(() => {
  requests.length = 0;
});

describe("the chat stream's request", () => {
  it("sets skip_total on every open, a chat switch included", () => {
    const service = new ChatStreamingService({ onChatUpdate: vi.fn(), onChatSnapshot: vi.fn() });
    service.subscribeToChat(CHAT);
    service.subscribeToChat(OTHER);

    expect(requests.map((r) => [r.chatId, r.skipTotal])).toEqual([
      [CHAT, true],
      [OTHER, true],
    ]);
    service.stop();
  });
});
