// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ContextValues } from "@connectrpc/connect";

// The chat open's `stream_requested` phase is marked when the stream's
// request is handed to fetch (the transport's request-sent stage), not when
// the stream was asked for: the gap between the two is the time the request
// spent in the page.

type Request = { chatId: string; sinceSeq: bigint };
type Options = { contextValues?: ContextValues };

const calls: { request: Request; options: Options }[] = [];

vi.mock("../../lib/connect", () => ({
  connectClient: () => ({
    streamChatUpdates: (request: Request, options: Options) => {
      calls.push({ request, options });
      return {
        async *[Symbol.asyncIterator]() {
          await new Promise(() => undefined);
          yield undefined;
        },
      };
    },
  }),
}));

const markChatOpen = vi.fn();
vi.mock("../../lib/chatOpenInteraction", () => ({
  markChatOpen: (...args: unknown[]) => markChatOpen(...args),
  noteChatOpenSync: vi.fn(),
}));

import { requestSentKey } from "../transport";
import { ChatStreamingService } from "../chat-streaming-grpc";

const CHAT = "aaaaaaaa-1111-2222-3333-444444444444";

afterEach(() => {
  calls.length = 0;
  markChatOpen.mockClear();
});

describe("the chat stream's request", () => {
  it("marks stream_requested for its chat when the transport sends it, and not before", () => {
    const service = new ChatStreamingService({ onChatUpdate: vi.fn(), onChatSnapshot: vi.fn() });
    service.subscribeToChat(CHAT);

    expect(calls).toHaveLength(1);
    expect(markChatOpen).not.toHaveBeenCalledWith(CHAT, "stream_requested", expect.anything());

    const onSent = calls[0].options.contextValues?.get(requestSentKey);
    expect(onSent).toBeTypeOf("function");
    onSent?.();

    expect(markChatOpen).toHaveBeenCalledWith(CHAT, "stream_requested", { since_sequence: 0 });
    service.stop();
  });
});
