import { describe, expect, it, beforeEach } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { useThreadActivityStore } from "../threadActivityStore";
import { getProcessedMessage } from "../../lib/messageProcessor";
import {
  getMessagesFromCache,
  clearAllMessagesCache,
} from "../../hooks/message-queries";

// Characterization tests for how the store matches TOOL_RESULT blocks (which
// arrive as separate TOOL-role messages) to the TOOL_CALL blocks inside the
// assistant message. The observable contract is: after
// processChatStreamUpdates, the assistant message's ProcessedMessage (via
// processMessage → toolExecutions[].result) carries the tool result.
//
// These lock the CURRENT behavior before the normalization refactor:
//  1. result in the SAME batch as the call
//  2. result arriving in a LATER batch than the call (the "late result" case)
//  3. result arriving BEFORE its call (orphan → synthetic call synthesis)
//
// We assert against the rendered projection the UI actually reads
// (ProcessedMessage.toolExecutions), not the internal storage shape, so the
// tests survive the refactor that changes HOW the join happens.

const CHAT = "c-tools";

function seedChat(_chatId: string) {
  clearAllMessagesCache();
  useChatStore.setState({
    activeChatId: null,
    toolResultsByCallId: {},
    streamingMessages: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
  } as never);
}

function assistantWithToolCall(
  chatId: string,
  id: string,
  callId: string,
  seq: number,
  toolName = "bash",
): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId,
      role: MessageRole.ASSISTANT,
      contentBlocks: [
        {
          id: `${id}-b0`,
          type: ContentBlockType.TOOL_CALL,
          index: 0,
          toolCallId: callId,
          toolName,
          input: "{}",
        },
      ],
      createdAt: "2026-01-01T00:00:00.000Z",
      updatedAt: "2026-01-01T00:00:00.000Z",
      streamingState: StreamingState.COMPLETE,
      seq: BigInt(seq),
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

function toolResultMessage(
  chatId: string,
  id: string,
  callId: string,
  seq: number,
  content: string,
  opts: { isError?: boolean; toolName?: string } = {},
): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId,
      role: MessageRole.TOOL,
      contentBlocks: [
        {
          id: `${id}-r0`,
          type: ContentBlockType.TOOL_RESULT,
          index: 0,
          toolCallId: callId,
          toolName: opts.toolName ?? "bash",
          content,
          isError: opts.isError ?? false,
        },
      ],
      createdAt: "2026-01-01T00:00:01.000Z",
      updatedAt: "2026-01-01T00:00:01.000Z",
      streamingState: StreamingState.COMPLETE,
      seq: BigInt(seq),
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

/**
 * Read the rendered result attached to a given tool-call id for a message,
 * via exactly the read path the UI uses: getProcessedMessage over the stored
 * message and the chat's normalized tool-result index.
 */
function resultForCall(chatId: string, messageId: string, callId: string) {
  const state = useChatStore.getState();
  const message = getMessagesFromCache(chatId).find((m) => m.id === messageId);
  if (!message) return undefined;
  const processed = getProcessedMessage(
    message,
    state.toolResultsByCallId[chatId] || {},
  );
  const exec = processed.toolExecutions?.find((e) => e.call.id === callId);
  return exec?.result;
}

beforeEach(() => {
  seedChat(CHAT);
  useThreadActivityStore.setState({ threads: {} } as never);
});

describe("tool-result matching (characterization)", () => {
  it("attaches a result delivered in the SAME batch as the tool call", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      assistantWithToolCall(CHAT, "m1", "call-1", 1),
      toolResultMessage(CHAT, "t1", "call-1", 2, "same-batch output"),
    ]);

    const result = resultForCall(CHAT, "m1", "call-1");
    expect(result).toBeDefined();
    expect(result?.content).toBe("same-batch output");
    expect(result?.is_error).toBe(false);
  });

  it("attaches a result arriving in a LATER batch than the tool call", () => {
    const store = useChatStore.getState();
    // Batch 1: assistant tool call, no result yet.
    store.processChatStreamUpdates(CHAT, [
      assistantWithToolCall(CHAT, "m1", "call-1", 1),
    ]);
    expect(resultForCall(CHAT, "m1", "call-1")).toBeUndefined();

    // Batch 2: the tool result arrives afterward (the "late result" case).
    store.processChatStreamUpdates(CHAT, [
      toolResultMessage(CHAT, "t1", "call-1", 2, "late output", {
        isError: true,
      }),
    ]);

    const result = resultForCall(CHAT, "m1", "call-1");
    expect(result).toBeDefined();
    expect(result?.content).toBe("late output");
    expect(result?.is_error).toBe(true);
  });

  it("synthesizes a call for an orphan result with no matching tool call", () => {
    const store = useChatStore.getState();
    // A TOOL-role result with no assistant TOOL_CALL block anywhere. The TOOL
    // message is still processed on its own, and processMessage synthesizes a
    // call so the result renders rather than vanishing (orphan-result
    // synthesis). This is the behavior we must preserve across the refactor.
    store.processChatStreamUpdates(CHAT, [
      toolResultMessage(CHAT, "t1", "call-orphan", 1, "orphan output"),
    ]);

    const result = resultForCall(CHAT, "t1", "call-orphan");
    expect(result).toBeDefined();
    expect(result?.content).toBe("orphan output");
  });

  it("first-write-wins when two TOOL messages in the SAME batch carry the same tool_call_id", () => {
    // tool_call_results.tool_call_id is a PRIMARY KEY at rest, so a second
    // TOOL message for the same call within one batch is a re-delivery, not
    // a genuinely different result. The first copy processed must win, not
    // be silently overwritten by the duplicate.
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      assistantWithToolCall(CHAT, "m1", "call-1", 1),
      toolResultMessage(CHAT, "t1", "call-1", 2, "first output"),
      toolResultMessage(CHAT, "t2", "call-1", 3, "duplicate output", {
        isError: true,
      }),
    ]);

    const result = resultForCall(CHAT, "m1", "call-1");
    expect(result).toBeDefined();
    expect(result?.content).toBe("first output");
    expect(result?.is_error).toBe(false);
  });

  it("first-write-wins when a duplicate tool_call_id arrives in a LATER batch", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      assistantWithToolCall(CHAT, "m1", "call-1", 1),
      toolResultMessage(CHAT, "t1", "call-1", 2, "first output"),
    ]);

    // A re-delivery (retry / reconnect snapshot overlap) of the same
    // tool_call_id in a subsequent batch must not clobber the original.
    store.processChatStreamUpdates(CHAT, [
      toolResultMessage(CHAT, "t2", "call-1", 3, "redelivered output", {
        isError: true,
      }),
    ]);

    const result = resultForCall(CHAT, "m1", "call-1");
    expect(result).toBeDefined();
    expect(result?.content).toBe("first output");
    expect(result?.is_error).toBe(false);
  });
});