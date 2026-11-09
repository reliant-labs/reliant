// @vitest-environment node
//
// The web opens chats with stub tool bodies (lib/toolBodies.ts): every
// transcript read it makes says so — ListMessages (the seed, scroll-back,
// thread reads, ChatSearch's current-chat search) and the chat stream's
// snapshot and replay. One constant decides it, so these assert that every
// read carries that constant (a revert to "full" is one line and keeps them
// green).

import { afterEach, describe, expect, it, vi } from "vitest";
import { ToolBodyMode } from "../../gen/reliant/v1/chat_pb";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { TRANSCRIPT_TOOL_BODIES, TRANSCRIPT_TOOL_BODY_MODE } from "../../lib/toolBodies";
import { listMessages } from "../chat-actions";
import { ChatStreamingService } from "../../api/chat-streaming-grpc";

afterEach(() => setTransport(null));

describe("the stub tool-bodies opt-in", () => {
  it("asks for the mode it names", () => {
    expect(TRANSCRIPT_TOOL_BODY_MODE).toBe(TRANSCRIPT_TOOL_BODIES === "stub" ? ToolBodyMode.STUB : ToolBodyMode.FULL);
  });

  it("is what every ListMessages the web makes asks for", async () => {
    const listMessagesRpc = vi.fn(async () => ({ messages: [], total: 0, hasMore: false, oldestSeq: 0n, errors: [] }));
    setTransport(mockTransport({ "ChatService.ListMessages": listMessagesRpc }));

    await listMessages("chat-1", { recent: 50 });
    await listMessages("chat-1", { beforeSeq: 10 });
    await listMessages("chat-1", { threadId: "thread-1" });
    // ChatSearch's current-chat search: the unbounded read.
    await listMessages("chat-1");

    expect(listMessagesRpc).toHaveBeenCalledTimes(4);
    for (const [request] of listMessagesRpc.mock.calls as unknown as Array<[{ toolBodies?: ToolBodyMode }]>) {
      expect(request.toolBodies).toBe(TRANSCRIPT_TOOL_BODY_MODE);
    }
  });

  it("is what the chat stream asks its snapshot and replay for", () => {
    const service = new ChatStreamingService({ onChatUpdate: vi.fn(), onChatSnapshot: vi.fn() });
    expect((service as unknown as { toolBodies: string | undefined }).toolBodies).toBe(TRANSCRIPT_TOOL_BODIES);
  });
});
