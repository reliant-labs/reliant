import { beforeEach, describe, expect, it } from "vitest";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { clearAllMessagesCache } from "../../hooks/message-queries";

// A stream cut short by a pause or an interrupt emits `stream_finalized` for an
// assistant message id that NEVER becomes a row: the cancelled step persists
// nothing and is re-dispatched, so the blocks streamed under that id are the
// only record of it there will ever be.
//
// Nothing durable arrives to say what the tool calls inside it did, so unless
// the client settles them itself they keep the last live status they were given
// — "executing" — and ToolExecution's status resolution reads that live status
// before anything else. The card spins against a chat that has already stopped.
//
// Verified live on chat 13400985: `stream_finalized{reason:"cancelled"}` for
// message 098853e9, which is absent from the messages table.

function seed() {
  clearAllMessagesCache();
  useChatStore.setState({
    activeChatId: null,
    streamingMessages: {},
    toolResultsByCallId: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
    finalizedStreamIds: {},
  } as never);
}

const toolUseStart = (
  messageId: string | undefined,
  callId: string,
): ChatUpdate =>
  ({
    update_type: "streaming_delta",
    delta_type: "tool_use_start",
    block_index: 0,
    tool_call: { id: callId, name: "bash" },
    ...(messageId ? { message_id: messageId } : {}),
  }) as unknown as ChatUpdate;

const toolStatus = (callId: string, status: string): ChatUpdate =>
  ({
    update_type: "tool_call",
    tool_call_id: callId,
    tool_name: "bash",
    status,
  }) as unknown as ChatUpdate;

const finalized = (
  messageId: string,
  reason: "completed" | "aborted" | "cancelled",
): ChatUpdate =>
  ({
    update_type: "stream_finalized",
    message_id: messageId,
    reason,
    thread: "",
  }) as unknown as ChatUpdate;

/** The live status a tool card resolves before it consults anything durable. */
function liveStatus(chatId: string, callId: string): string | undefined {
  return useChatStore.getState().toolCallStates[chatId]?.get(callId)?.status;
}

/** The status painted on the streamed block itself, keyed by tool-call id. */
function blockStatus(chatId: string, callId: string): string | undefined {
  const slice = useChatStore.getState().streamingMessages[chatId] || {};
  for (const message of Object.values(slice)) {
    for (const block of message?.contentBlocks || []) {
      if (block.toolCallId === callId) {
        return (block as { status?: string }).status;
      }
    }
  }
  return undefined;
}

describe("orphaned stream settles its unfinished tool calls", () => {
  beforeEach(seed);

  it("settles a tool left executing when its stream is finalized as cancelled", () => {
    const chatId = "orphan-cancelled";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-1", "call-1")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-1", "executing")]);
    expect(
      liveStatus(chatId, "call-1"),
      "precondition: the tool is live-executing before the pause lands",
    ).toBe("executing");

    store.processChatStreamUpdates(chatId, [finalized("msg-1", "cancelled")]);

    expect(
      liveStatus(chatId, "call-1"),
      "the live status a tool card reads first must settle — no durable row is coming",
    ).toBe("cancelled");
    expect(blockStatus(chatId, "call-1")).toBe("cancelled");
  });

  it("settles an aborted stream reported without a message id (legacy placeholder)", () => {
    const chatId = "orphan-legacy";
    const store = useChatStore.getState();

    // Deltas with no message_id build a fabricated `streaming-temp-*`
    // placeholder, so the marker's id matches nothing — the thread is the only
    // handle onto the stream it ended.
    store.processChatStreamUpdates(chatId, [toolUseStart(undefined, "call-2")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-2", "executing")]);

    store.processChatStreamUpdates(chatId, [finalized("msg-2", "aborted")]);

    expect(liveStatus(chatId, "call-2")).toBe("cancelled");
    expect(blockStatus(chatId, "call-2")).toBe("cancelled");
  });

  it("settles a tool whose deltas arrive in the SAME batch as the aborting marker", () => {
    // The marker's id is finalized by this very batch, so the deltas carrying
    // it are dropped as a stale tail and no placeholder is ever built. The
    // live tool status has no placeholder to be settled through, and used to
    // sit at "executing" forever.
    const chatId = "orphan-same-batch";

    useChatStore.getState().processChatStreamUpdates(chatId, [
      toolUseStart("msg-3", "call-3"),
      toolStatus("call-3", "executing"),
      finalized("msg-3", "cancelled"),
    ]);

    expect(
      liveStatus(chatId, "call-3"),
      "a tool whose stream stopped in the same batch must not stay executing",
    ).toBe("cancelled");
  });

  it("does not repaint a tool that genuinely completed", () => {
    // The regression documented in chatStreamReducers: a completed tool
    // repainted as cancelled is a lie about work the user already has the
    // results of. Whichever terminal status lands first describes the tool.
    const chatId = "orphan-completed";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-4", "call-4")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-4", "completed")]);

    store.processChatStreamUpdates(chatId, [finalized("msg-4", "cancelled")]);

    expect(liveStatus(chatId, "call-4")).toBe("completed");
    expect(
      blockStatus(chatId, "call-4"),
      "a completed tool's block must not be stamped cancelled",
    ).not.toBe("cancelled");
  });

  it("protects a completion that rides the same batch as the aborting marker", () => {
    const chatId = "orphan-completed-same-batch";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-5", "call-5")]);
    store.processChatStreamUpdates(chatId, [
      toolStatus("call-5", "completed"),
      finalized("msg-5", "cancelled"),
    ]);

    expect(liveStatus(chatId, "call-5")).toBe("completed");
  });

  it("leaves a normally completed stream alone", () => {
    // reason "completed" means the assistant message WILL be persisted, so the
    // placeholder is retired the usual way and nothing is painted cancelled.
    const chatId = "orphan-none";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-6", "call-6")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-6", "executing")]);

    store.processChatStreamUpdates(chatId, [finalized("msg-6", "completed")]);

    expect(liveStatus(chatId, "call-6")).toBe("executing");
    expect(
      useChatStore.getState().streamingMessages[chatId]?.[chatId],
      "a normally finalized stream retires its placeholder for the persisted row",
    ).toBeFalsy();
  });
});
