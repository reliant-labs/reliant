import { describe, expect, it } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import {
  getMessagesFromCache,
  clearAllMessagesCache,
} from "../../hooks/message-queries";

// When a stream is cancelled, tool calls that never reached an outcome are
// preserved from the ephemeral streaming message onto the persisted assistant
// message, so a cancelled tool still shows in the transcript.
//
// That merge deduped on BLOCK ID. A streaming block is built by the
// tool_use_start delta with `id: ""` — there is no block row until the message
// is persisted — so the check compared "" against real uuids, never matched,
// and appended the ephemeral card to the persisted message. The user saw a
// duplicate tool call stuck at "Preparing…" hanging below the real ones: the
// persisted copy had its input and completed, the streaming copy had neither.
//
// tool_call_id is the identifier both copies share.
function seed() {
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

const TOOL_CALL_ID = "toolu_evaluate_script_1";

function toolUseStart(blockIndex: number): ChatUpdate {
  return {
    update_type: "streaming_delta",
    delta_type: "tool_use_start",
    block_index: blockIndex,
    message_id: "m1",
    tool_call: { id: TOOL_CALL_ID, name: "mcp__chrome-devtools__evaluate_script" },
  } as unknown as ChatUpdate;
}

function streamCancelled(): ChatUpdate {
  return {
    update_type: "streaming_delta",
    delta_type: "stream_cancelled",
    block_index: 0,
    message_id: "m1",
  } as unknown as ChatUpdate;
}

/** The persisted assistant message: same tool call, with a real block id. */
function persistedWithToolCall(chatId: string): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id: "m1",
      chatId,
      role: MessageRole.ASSISTANT,
      contentBlocks: [
        {
          id: "m1-b0",
          type: ContentBlockType.TOOL_CALL,
          index: 0,
          toolCallId: TOOL_CALL_ID,
          toolName: "mcp__chrome-devtools__evaluate_script",
          input: '{"function":"() => 1"}',
        },
      ],
      createdAt: "2026-01-01T00:00:10.000Z",
      updatedAt: "2026-01-01T00:00:10.000Z",
      streamingState: StreamingState.COMPLETE,
      seq: BigInt(2),
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

describe("cancelled tool-call preservation", () => {
  it("does not duplicate a tool call the persisted message already carries", () => {
    const chatId = "chat-cancel-dedup";
    seed();
    const store = useChatStore.getState();

    // A tool call starts streaming, then the stream is cancelled.
    store.processChatStreamUpdates(chatId, [toolUseStart(0), streamCancelled()]);

    // The persisted assistant message arrives carrying that same tool call.
    store.processChatStreamUpdates(chatId, [persistedWithToolCall(chatId)]);

    const msg = getMessagesFromCache(chatId).find((m) => m.id === "m1");
    expect(msg).toBeDefined();

    const callBlocks = (msg!.contentBlocks || []).filter(
      (b) => b.type === ContentBlockType.TOOL_CALL,
    );
    expect(callBlocks).toHaveLength(1);

    // The surviving copy must be the persisted one — it has the input, so it
    // renders as a real call rather than sitting at "Preparing…" forever.
    expect(callBlocks[0].input).toBeTruthy();
  });

  it("still preserves a cancelled tool call the persisted message does NOT carry", () => {
    const chatId = "chat-cancel-preserve";
    seed();
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart(0), streamCancelled()]);

    // Persisted message with a DIFFERENT tool call: the cancelled one was
    // never written, so it must survive or the user loses it entirely.
    const other = persistedWithToolCall(chatId) as unknown as {
      message: { contentBlocks: { toolCallId: string; id: string }[] };
    };
    other.message.contentBlocks[0].toolCallId = "toolu_other";
    other.message.contentBlocks[0].id = "m1-other";

    store.processChatStreamUpdates(chatId, [other as unknown as ChatUpdate]);

    const msg = getMessagesFromCache(chatId).find((m) => m.id === "m1");
    const callIds = (msg?.contentBlocks || [])
      .filter((b) => b.type === ContentBlockType.TOOL_CALL)
      .map((b) => b.toolCallId);
    expect(callIds).toContain("toolu_other");
    expect(callIds).toContain(TOOL_CALL_ID);
  });
});
