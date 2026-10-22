import { beforeEach, describe, expect, it } from "vitest";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { clearAllMessagesCache } from "../../hooks/message-queries";

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

const toolUseStart = (messageId: string | undefined, callId: string): ChatUpdate =>
  ({
    update_type: "streaming_delta",
    delta_type: "tool_use_start",
    block_index: 0,
    tool_call: { id: callId, name: "bash" },
    ...(messageId ? { message_id: messageId } : {}),
  }) as unknown as ChatUpdate;

const toolCallUpdate = (callId: string, status: string): ChatUpdate =>
  ({
    update_type: "tool_call",
    tool_call_id: callId,
    tool_name: "bash",
    status,
  }) as unknown as ChatUpdate;

const finalized = (messageId: string, reason: string): ChatUpdate =>
  ({
    update_type: "stream_finalized",
    message_id: messageId,
    reason,
    thread: "",
  }) as unknown as ChatUpdate;

function dump(chatId: string, label: string) {
  const s = useChatStore.getState();
  const slice = s.streamingMessages[chatId];
  const states = s.toolCallStates[chatId];
  // eslint-disable-next-line no-console
  console.log(`### ${label}`, JSON.stringify({
    placeholders: slice
      ? Object.entries(slice).map(([k, m]) => ({
          key: k,
          id: m?.id,
          blocks: (m?.contentBlocks || []).map((b) => ({
            type: b.type,
            toolCallId: b.toolCallId,
            status: (b as { status?: string }).status,
          })),
        }))
      : null,
    toolCallStates: states ? [...states.entries()].map(([k, v]) => [k, v.status]) : null,
  }, null, 2));
}

describe("probe", () => {
  beforeEach(seed);

  it("A: orphan stream, deltas carry message_id, marker matches", () => {
    const chatId = "probe-a";
    const st = useChatStore.getState();
    st.processChatStreamUpdates(chatId, [toolUseStart("msg-1", "call-1")]);
    st.processChatStreamUpdates(chatId, [toolCallUpdate("call-1", "executing")]);
    dump(chatId, "A before finalize");
    st.processChatStreamUpdates(chatId, [finalized("msg-1", "cancelled")]);
    dump(chatId, "A after finalize");
    expect(true).toBe(true);
  });

  it("B: orphan stream, deltas have NO message_id (temp id), marker has real id", () => {
    const chatId = "probe-b";
    const st = useChatStore.getState();
    st.processChatStreamUpdates(chatId, [toolUseStart(undefined, "call-2")]);
    st.processChatStreamUpdates(chatId, [toolCallUpdate("call-2", "executing")]);
    dump(chatId, "B before finalize");
    st.processChatStreamUpdates(chatId, [finalized("msg-2", "cancelled")]);
    dump(chatId, "B after finalize");
    expect(true).toBe(true);
  });

  it("C: same batch marker + deltas", () => {
    const chatId = "probe-c";
    const st = useChatStore.getState();
    st.processChatStreamUpdates(chatId, [
      toolUseStart("msg-3", "call-3"),
      toolCallUpdate("call-3", "executing"),
      finalized("msg-3", "cancelled"),
    ]);
    dump(chatId, "C after batch");
    expect(true).toBe(true);
  });
});
