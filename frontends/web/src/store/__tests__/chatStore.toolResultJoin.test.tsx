import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../types/chat";
import { useChatStore } from "../chatStore";
import { getProcessedMessage, processMessage } from "../../lib/messageProcessor";
import { queryClient } from "../../lib/query-client";
import {
  clearAllMessagesCache,
  prefetchChatMessages,
  useThreadMessages,
} from "../../hooks/message-queries";

// The server no longer copies a tool result onto its tool_call block (that
// put every output on the wire twice). A call finds its result only by
// tool_call_id, through the store's toolResultsByCallId index — so every read
// that brings TOOL messages to the client must fold them into it, including
// the React Query reads (the chat's cold seed, a spawn thread's read) that do
// not otherwise touch the store.

const listMessages = vi.fn();

vi.mock("../../hooks/chat-actions", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../hooks/chat-actions")>();
  return { ...actual, listMessages: (...args: unknown[]) => listMessages(...args) };
});

const CHAT = "c-join";

function message(id: string, role: MessageRole, seq: number, contentBlocks: Message["contentBlocks"], thread = CHAT): Message {
  return {
    id,
    chatId: CHAT,
    role,
    contentBlocks,
    createdAt: "2026-01-01T00:00:00.000Z",
    updatedAt: "2026-01-01T00:00:00.000Z",
    streamingState: StreamingState.COMPLETE,
    seq: BigInt(seq),
    thread,
    sequenceNumber: 0n,
    attachments: [],
  } as unknown as Message;
}

function assistantCalling(id: string, callId: string, seq: number, thread?: string): Message {
  return message(id, MessageRole.ASSISTANT, seq, [
    { id: `${id}-c`, index: 0, type: ContentBlockType.TOOL_CALL, toolCallId: callId, toolName: "shell", input: "{}" },
  ], thread);
}

function toolResult(id: string, callId: string, seq: number, content: string, extra: Record<string, unknown> = {}, thread?: string): Message {
  return message(id, MessageRole.TOOL, seq, [
    { id: `${id}-r`, index: 0, type: ContentBlockType.TOOL_RESULT, toolCallId: callId, toolName: "shell", content, isError: false, ...extra },
  ], thread);
}

beforeEach(() => {
  clearAllMessagesCache();
  queryClient.clear();
  listMessages.mockReset();
  useChatStore.setState({ toolResultsByCallId: {} } as never);
});

describe("joining a tool call to its result by tool_call_id", () => {
  it("resolves the result from the index; the call block carries none", () => {
    const call = assistantCalling("a1", "call-1", 1);
    expect(processMessage(call).toolExecutions?.[0].result).toBeUndefined();

    const processed = processMessage(call, [], { "call-1": { content: "ok", is_error: false } });
    expect(processed.toolExecutions?.[0].result).toMatchObject({ tool_call_id: "call-1", content: "ok", is_error: false });
  });

  it("carries a preview's truncated flag and full size through the join", () => {
    const store = useChatStore.getState();
    store.ingestToolResultMessages(CHAT, [
      toolResult("t1", "call-1", 2, "head … 90 KB omitted … tail", { truncated: true, originalSize: 92160n }),
    ]);
    const result = getProcessedMessage(
      assistantCalling("a1", "call-1", 1),
      useChatStore.getState().toolResultsByCallId[CHAT],
    ).toolExecutions?.[0].result;
    expect(result).toMatchObject({ content: "head … 90 KB omitted … tail", truncated: true, original_size: 92160 });
  });

  it("keeps a result already known: a result is written once", () => {
    const store = useChatStore.getState();
    store.ingestToolResultMessages(CHAT, [toolResult("t1", "call-1", 2, "first")]);
    const before = useChatStore.getState().toolResultsByCallId[CHAT];
    store.ingestToolResultMessages(CHAT, [toolResult("t1-again", "call-1", 2, "second")]);
    expect(useChatStore.getState().toolResultsByCallId[CHAT]["call-1"].content).toBe("first");
    expect(useChatStore.getState().toolResultsByCallId[CHAT]).toBe(before);
  });

  it("the chat's cold seed read folds its results into the index", async () => {
    listMessages.mockResolvedValue({
      messages: [assistantCalling("a1", "call-1", 1), toolResult("t1", "call-1", 2, "seeded output")],
      total: 2,
      hasMore: false,
      oldestSeq: 1,
    });
    prefetchChatMessages(CHAT);

    await waitFor(() =>
      expect(useChatStore.getState().toolResultsByCallId[CHAT]?.["call-1"]?.content).toBe("seeded output"),
    );
  });

  it("a spawn thread's read folds its results into the index", async () => {
    const thread = "wf-child";
    listMessages.mockResolvedValue({
      messages: [assistantCalling("a1", "call-child", 1, thread), toolResult("t1", "call-child", 2, "child output", {}, thread)],
      total: 2,
      hasMore: false,
      oldestSeq: 1,
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    renderHook(() => useThreadMessages(CHAT, thread, { recent: 10 }), { wrapper });

    await waitFor(() =>
      expect(useChatStore.getState().toolResultsByCallId[CHAT]?.["call-child"]?.content).toBe("child output"),
    );
    expect(listMessages).toHaveBeenCalledWith(CHAT, { threadId: thread, recent: 10 });
  });
});
