import { beforeEach, describe, expect, it } from "vitest";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import {
  clearAllMessagesCache,
  getMessagesFromCache,
} from "../../hooks/message-queries";

// An interrupted turn DOES get a persisted row, and the client used to assume
// it never would.
//
// CallLLM.executeCore persists the partial assistant message on a detached
// context when its own context is cancelled (persistInterruptedTurn,
// call_llm.go:266), reusing output.MessageId — the SAME id the streaming
// deltas were stamped with — and SaveMessage emits a `message` chat_update for
// it. CleanupActivity independently writes a real TOOL-role message with
// tool_result blocks for every orphaned call (createRepairToolMessage,
// cleanup.go:357).
//
// The store nonetheless exempted settled-abort threads from the retire-by-id
// pass on the premise that "no message row is coming", so the placeholder
// became immortal. ChatContainer merges the streaming slice over the persisted
// list BY ID and lets the placeholder win, so the stale cancelled turn was
// re-rendered on every subsequent turn until a reload dropped the in-memory
// slice.
//
// The placeholder must therefore retire when the persisted row actually
// ARRIVES, and only then — an interrupt that lands before any text or tool
// call really does produce no row (persistInterruptedTurn returns early on an
// empty turn), and there the placeholder is still the only record.

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
    tool_call: { id: callId, name: "bash" },
  }) as unknown as ChatUpdate;

/**
 * The delta that opens every stream: CallLLM emits `message_start` carrying the
 * pre-allocated id before any content (call_llm.go:927).
 */
const messageStart = (messageId: string): ChatUpdate =>
  ({
    update_type: "streaming_delta",
    delta_type: "message_start",
    block_index: 0,
    message_id: messageId,
    thread: "",
    role: "assistant",
  }) as unknown as ChatUpdate;

/** The first visible content of the new turn. */
const contentBlockStart = (messageId: string, content: string): ChatUpdate =>
  ({
    update_type: "streaming_delta",
    delta_type: "content_block_start",
    block_index: 0,
    message_id: messageId,
    thread: "",
    content,
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

/**
 * The persisted partial that persistInterruptedTurn writes: same id as the
 * deltas, a real tool_call block carrying its input (so the message reads as
 * complete), delivered as an ordinary `message` update.
 */
const persistedInterruptedTurn = (
  chatId: string,
  messageId: string,
  callId: string,
): ChatUpdate =>
  ({
    update_type: "message",
    message: {
      id: messageId,
      chatId,
      thread: "",
      role: 2, // MessageRole.ASSISTANT
      contentBlocks: [
        {
          id: "block-persisted-1",
          index: 0,
          type: 2, // ContentBlockType.TOOL_CALL
          toolName: "bash",
          toolCallId: callId,
          // Input present ⇒ the block is not streaming ⇒ the message reads as
          // complete (isProtoMessageComplete), which is what makes it a
          // finalized id the retire pass can act on.
          input: '{"command":"ls"}',
        },
      ],
    },
  }) as unknown as ChatUpdate;

/** An ordinary completed assistant turn: one text block, no tools. */
const persistedTextTurn = (
  chatId: string,
  messageId: string,
  content: string,
): ChatUpdate =>
  ({
    update_type: "message",
    message: {
      id: messageId,
      chatId,
      thread: "",
      role: 2, // MessageRole.ASSISTANT
      contentBlocks: [
        {
          id: `block-${messageId}`,
          index: 0,
          type: 1, // ContentBlockType.TEXT
          content,
        },
      ],
    },
  }) as unknown as ChatUpdate;

/** Placeholders currently held in the ephemeral streaming slice. */
function streamingPlaceholderIds(chatId: string): string[] {
  const slice = useChatStore.getState().streamingMessages[chatId] || {};
  return Object.values(slice)
    .filter((message): message is NonNullable<typeof message> => !!message)
    .map((message) => message.id);
}

describe("an interrupted turn retires its placeholder once the row arrives", () => {
  beforeEach(seed);

  it("retires the settled placeholder when the persisted partial lands", () => {
    const chatId = "interrupted-persisted";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-1", "call-1")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-1", "executing")]);
    store.processChatStreamUpdates(chatId, [finalized("msg-1", "cancelled")]);

    expect(
      streamingPlaceholderIds(chatId),
      "precondition: the abort settles the placeholder and keeps it on screen",
    ).toContain("msg-1");

    // CallLLM's detached write lands a moment later, under the same id.
    store.processChatStreamUpdates(chatId, [
      persistedInterruptedTurn(chatId, "msg-1", "call-1"),
    ]);

    expect(
      streamingPlaceholderIds(chatId),
      "the persisted row is now the record of this turn — the placeholder must go, " +
        "or ChatContainer's merge-by-id keeps painting the stale copy over it forever",
    ).not.toContain("msg-1");

    const persisted = getMessagesFromCache(chatId).find((m) => m.id === "msg-1");
    expect(persisted, "the persisted row must survive the retirement").toBeTruthy();
  });

  it("keeps the placeholder while no row has arrived", () => {
    // This is the case the whole exemption exists for, and it is REAL:
    // streamProcessingState.toolCalls is populated only in handleComplete,
    // from event.Response.ToolCalls (call_llm.go:1930), because a tool call's
    // Input is still empty at tool_use_start. An interrupt mid-tool-input
    // never delivers that completion event, so a turn that had streamed only
    // a tool call reaches persistInterruptedTurn with empty ResponseText AND
    // empty ToolCalls — and it returns early (call_llm.go:289) without
    // writing anything.
    //
    // Nothing durable is coming, so the settled placeholder really is the only
    // record of what the user watched, and it must stay.
    const chatId = "interrupted-no-row";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-2", "call-2")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-2", "executing")]);
    store.processChatStreamUpdates(chatId, [finalized("msg-2", "cancelled")]);

    expect(
      streamingPlaceholderIds(chatId),
      "nothing durable arrived, so the settled placeholder is still the only record",
    ).toContain("msg-2");
  });

  it("retires a stale settled placeholder when a NEW stream opens on its thread", () => {
    // The leak the user actually saw: one cancelled tool call re-rendered on
    // every subsequent turn, cured by a browser reload.
    //
    // When the interrupted turn produced no persisted row (the case above),
    // nothing carrying msg-2's id ever arrives, so neither the by-id retire
    // pass nor the completedThreads pass can ever fire for it. The placeholder
    // is immortal in a slice that only a reload clears, and ChatContainer
    // merges it into the transcript on every render.
    //
    // A new stream opening on that thread is proof the cancelled turn is over:
    // whatever it was showing is now history, and the new stream owns the
    // thread's live placeholder.
    const chatId = "interrupted-stale-across-turns";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-2", "call-2")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-2", "executing")]);
    store.processChatStreamUpdates(chatId, [finalized("msg-2", "cancelled")]);
    expect(streamingPlaceholderIds(chatId)).toContain("msg-2");

    // The user sends another message; a fresh turn opens on the same thread.
    store.processChatStreamUpdates(chatId, [
      messageStart("msg-3"),
      contentBlockStart("msg-3", "Sure — picking that back up."),
    ]);

    expect(
      streamingPlaceholderIds(chatId),
      "the cancelled turn's placeholder must not ride along on every later turn",
    ).not.toContain("msg-2");
    expect(
      streamingPlaceholderIds(chatId),
      "the new stream's own placeholder is live and must be kept",
    ).toContain("msg-3");
  });

  it("retires the placeholder when the row rides the same batch as the abort", () => {
    const chatId = "interrupted-same-batch";

    useChatStore.getState().processChatStreamUpdates(chatId, [
      toolUseStart("msg-3", "call-3"),
      toolStatus("call-3", "executing"),
      finalized("msg-3", "cancelled"),
      persistedInterruptedTurn(chatId, "msg-3", "call-3"),
    ]);

    expect(
      streamingPlaceholderIds(chatId),
      "a persisted row in the aborting batch retires the placeholder just the same",
    ).not.toContain("msg-3");
  });

  it("grafts a cancelled tool onto ONE message, not every later turn", () => {
    // The exact symptom reported: after an interrupt, one cancelled tool call
    // rendered again on every subsequent turn, and a browser reload cured it.
    //
    // When a stream is cut short, the completedThreads pass preserves the
    // cancelled tool_call blocks from the dying placeholder by appending them
    // onto a persisted assistant message. But its filter is
    // `role === ASSISTANT && isProtoMessageComplete(msg) && thread matches` —
    // it never restricts to the message the blocks came FROM. Every complete
    // assistant message on that thread matches, including ones written turns
    // later, so the cancelled card is grafted onto each new turn as it lands.
    //
    // It is a pure client-side graft, which is why a reload (which refetches
    // the untouched server rows) made it disappear.
    const chatId = "interrupted-graft";
    const store = useChatStore.getState();

    // Turn 1 is interrupted mid tool call.
    store.processChatStreamUpdates(chatId, [toolUseStart("msg-1", "call-1")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-1", "executing")]);
    store.processChatStreamUpdates(chatId, [finalized("msg-1", "cancelled")]);

    // Turn 2: a completely unrelated, successful assistant message.
    store.processChatStreamUpdates(chatId, [
      persistedTextTurn(chatId, "msg-2", "First reply after the interrupt."),
    ]);
    // Turn 3: another one.
    store.processChatStreamUpdates(chatId, [
      persistedTextTurn(chatId, "msg-3", "Second reply after the interrupt."),
    ]);

    const carriers = getMessagesFromCache(chatId)
      .filter((m) =>
        (m.contentBlocks || []).some((b) => b.toolCallId === "call-1"),
      )
      .map((m) => m.id);

    // The blocks were streamed under msg-1. They may legitimately be preserved
    // onto msg-1 (delta identity gives the persisted partial the same id), or
    // dropped — but they must NEVER be attached to msg-2 or msg-3, which are
    // different turns the user asked for after the interrupt.
    expect(
      carriers.filter((id) => id !== "msg-1"),
      `the interrupted turn's cancelled tool call was grafted onto ${carriers.join(", ")}; ` +
        "it must never appear on a later turn's message",
    ).toEqual([]);
  });

  it("still settles the tool status when the row arrives", () => {
    // Retiring the placeholder must not undo the cancellation the abort pass
    // painted: the card reads the live tool status before anything durable.
    const chatId = "interrupted-status-kept";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [toolUseStart("msg-4", "call-4")]);
    store.processChatStreamUpdates(chatId, [toolStatus("call-4", "executing")]);
    store.processChatStreamUpdates(chatId, [finalized("msg-4", "cancelled")]);
    store.processChatStreamUpdates(chatId, [
      persistedInterruptedTurn(chatId, "msg-4", "call-4"),
    ]);

    expect(
      useChatStore.getState().toolCallStates[chatId]?.get("call-4")?.status,
      "the tool was cancelled and must stay cancelled after the row lands",
    ).toBe("cancelled");
  });
});
