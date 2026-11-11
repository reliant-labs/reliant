// The live → reload transition under stub tool bodies
// (dev-docs/design/chat-lazy-tool-bodies.md, §4). A turn watched live arrives
// whole; a reconnect's snapshot re-delivers its finished calls as stubs. The
// store keeps the bodies it holds — "the open, live window is full; history is
// stubs; a body, once held, is kept" — so rows the user has open never blank
// out, and a later read of a stub never displaces a held result.

import { beforeEach, describe, expect, it } from "vitest";
import { ContentBlockType, MessageRole, StreamingState } from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import type { Message } from "../../types/chat";
import { useChatStore } from "../chatStore";
import { useThreadActivityStore } from "../threadActivityStore";
import { getProcessedMessage } from "../../lib/messageProcessor";
import { clearAllMessagesCache, getMessagesFromCache } from "../../hooks/message-queries";

const CHAT = "c-bodies";
const FULL_INPUT = JSON.stringify({ file_path: "a.ts", content: "WHOLE ".repeat(400) });
const SKELETON = JSON.stringify({ file_path: "a.ts", content: "WHOLE WHOLE…" });
const FULL_OUTPUT = "OUTPUT ".repeat(400);

function assistant(elided: boolean): Message {
  return {
    id: "m1",
    chatId: CHAT,
    role: MessageRole.ASSISTANT,
    contentBlocks: [
      {
        id: "m1-call",
        type: ContentBlockType.TOOL_CALL,
        index: 0,
        toolCallId: "call-1",
        toolName: "write",
        input: elided ? SKELETON : FULL_INPUT,
        ...(elided ? { bodyElided: true, bodySize: BigInt(FULL_INPUT.length) } : {}),
      },
    ],
    createdAt: "2026-01-01T00:00:00.000Z",
    updatedAt: "2026-01-01T00:00:00.000Z",
    streamingState: StreamingState.COMPLETE,
    seq: 1n,
    thread: "",
    sequenceNumber: 0n,
    attachments: [],
  } as unknown as Message;
}

function toolResult(elided: boolean): Message {
  return {
    id: "t1",
    chatId: CHAT,
    role: MessageRole.TOOL,
    contentBlocks: [
      {
        id: "t1-result",
        type: ContentBlockType.TOOL_RESULT,
        index: 0,
        toolCallId: "call-1",
        toolName: "write",
        content: elided ? "OUTPUT…" : FULL_OUTPUT,
        isError: false,
        ...(elided
          ? { bodyElided: true, bodySize: BigInt(FULL_OUTPUT.length), truncated: true, originalSize: BigInt(FULL_OUTPUT.length) }
          : {}),
      },
    ],
    createdAt: "2026-01-01T00:00:01.000Z",
    updatedAt: "2026-01-01T00:00:01.000Z",
    streamingState: StreamingState.COMPLETE,
    seq: 2n,
    thread: "",
    sequenceNumber: 0n,
    attachments: [],
  } as unknown as Message;
}

const update = (message: Message): ChatUpdate => ({ update_type: "message", message }) as unknown as ChatUpdate;

function execution() {
  const state = useChatStore.getState();
  const message = getMessagesFromCache(CHAT).find((m) => m.id === "m1");
  if (!message) throw new Error("m1 not in the cache");
  return getProcessedMessage(message, state.toolResultsByCallId[CHAT] || {}).toolExecutions?.[0];
}

beforeEach(() => {
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
  useThreadActivityStore.setState({ threads: {} } as never);
});

describe("a turn watched live, then re-delivered as stubs", () => {
  it("keeps the bodies the live turn delivered when a reconnect's snapshot sends stubs", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [update(assistant(false)), update(toolResult(false))]);
    expect(execution()?.call.input).toEqual(JSON.parse(FULL_INPUT));

    store.processChatStreamUpdates(CHAT, [update(assistant(true)), update(toolResult(true))], true);

    const after = execution();
    expect(after?.call.inputElided).toBeFalsy();
    expect(after?.call.input).toEqual(JSON.parse(FULL_INPUT));
    expect(after?.result?.elided).toBeFalsy();
    expect(after?.result?.content).toBe(FULL_OUTPUT);
  });

  it("opens a cold chat with the stubs, flagged for a fetch", () => {
    useChatStore.getState().processChatStreamUpdates(CHAT, [update(assistant(true)), update(toolResult(true))], true);

    const cold = execution();
    expect(cold?.call).toMatchObject({ inputElided: true, inputSize: FULL_INPUT.length, content_block_id: "m1-call" });
    expect(cold?.result).toMatchObject({ elided: true, truncated: true, block_id: "t1-result" });
  });

  it("never lets a later read's stub displace a result it holds whole (a thread read's results)", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [update(assistant(false)), update(toolResult(false))]);

    store.ingestToolResultMessages(CHAT, [toolResult(true)]);

    expect(useChatStore.getState().toolResultsByCallId[CHAT]?.["call-1"]).toMatchObject({ content: FULL_OUTPUT });
    expect(useChatStore.getState().toolResultsByCallId[CHAT]?.["call-1"]?.elided).toBeFalsy();
  });

  it("upgrades a held stub when a read brings the body", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [update(assistant(true)), update(toolResult(true))], true);

    store.ingestToolResultMessages(CHAT, [toolResult(false)]);

    expect(useChatStore.getState().toolResultsByCallId[CHAT]?.["call-1"]).toMatchObject({ content: FULL_OUTPUT });
  });
});
