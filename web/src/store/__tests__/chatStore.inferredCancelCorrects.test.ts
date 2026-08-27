import { beforeEach, describe, expect, it } from "vitest";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { clearAllMessagesCache } from "../../hooks/message-queries";

// Reproduces the surviving "cancelled" card in chat a268f489.
//
// The DB for that chat is completely clean: 17 tool_call blocks, 17 matching
// tool_result blocks, every tool_calls row status=3 (completed), and not one
// synthesized "interrupted" stub. Server-side cleanup had nothing to repair,
// so the cancelled card the user is looking at is purely a client artifact.
//
// The turn at 02:44:27 streamed under message id b87245b5 and was interrupted;
// `stream_finalized{reason:"cancelled"}` landed at 02:44:43 and no row for
// b87245b5 was ever written. The abort pass then paints every tool block in
// that stream "cancelled" — both on the placeholder and, crucially, on the
// per-chat `toolCallStates` map that ToolExecution reads BEFORE anything
// durable (its currentStatus resolution returns liveStatus first).
//
// The placeholder itself is cleaned up correctly. `toolCallStates` is not.
// It is a per-chat Map that is only ever merged into, never reconciled against
// the durable rows, and applyToolCallStateUpdates treats "cancelled" as
// TERMINAL: a later "completed" for the same tool call is dropped on the floor
// to stop a stale completion resurrecting a cancelled tool.
//
// That guard is right when the cancel is the truth. It is wrong when the tool
// was still running as the stream ended and went on to finish, because the
// cancel was a client-side inference and the completion is the server fact.
// The tool's real result is sitting in the transcript while its card reads
// "cancelled" — and because the map has no TTL and no durable reconciliation,
// nothing short of switching chats or reloading clears it.

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

const toolUseStart = (messageId: string, callId: string): ChatUpdate =>
  ({
    update_type: "streaming_delta",
    delta_type: "tool_use_start",
    block_index: 0,
    message_id: messageId,
    thread: "",
    tool_call: { id: callId, name: "bash" },
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

describe("a tool that outlives the stream that started it", () => {
  beforeEach(seed);

  it("lets a real completion correct a cancel the abort only inferred", () => {
    const chatId = "cancel-then-real-completion";
    const store = useChatStore.getState();

    // A tool is dispatched and running when the user interrupts the turn.
    store.processChatStreamUpdates(chatId, [toolUseStart("msg-dead", "call-1")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-1", "executing")]);
    store.processChatStreamUpdates(chatId, [finalized("msg-dead", "cancelled")]);

    expect(
      liveStatus(chatId, "call-1"),
      "precondition: the abort infers a cancel, since nothing yet says otherwise",
    ).toBe("cancelled");

    // The tool was already past the point of no return and finishes anyway.
    // The server records the completion and emits it — this is a fact about
    // what the tool DID, arriving after the guess about what it did.
    store.processChatStreamUpdates(chatId, [toolStatus("call-1", "completed")]);

    expect(
      liveStatus(chatId, "call-1"),
      "the tool ran to a result the user can see in the transcript; its card must " +
        "not keep reading 'cancelled' against a completion the server reported",
    ).toBe("completed");
  });

  it("still refuses a stale completion for a tool the user really cancelled", () => {
    // The guard's real case, which must survive: an explicit cancel followed by
    // a completion that was already in flight. Here the cancel is the truth and
    // the completion is the stale one.
    const chatId = "explicit-cancel-wins";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-1", "call-2")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-2", "executing")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-2", "cancelled")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-2", "completed")]);

    expect(
      liveStatus(chatId, "call-2"),
      "a tool the server itself reported cancelled must not be resurrected",
    ).toBe("cancelled");
  });
});
