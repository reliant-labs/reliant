import { beforeEach, describe, expect, it } from "vitest";
import { act, render } from "@testing-library/react";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import {
  useErrorEvents,
  useInfoEvents,
  useRunOutputs,
  useToolCallStates,
  useToolResultsByCallId,
} from "../chatStoreHooks";
import { useThreadActivityStore } from "../threadActivityStore";
import { clearAllMessagesCache } from "../../hooks/message-queries";

// A stream batch that carries nothing for a slice must leave that slice's
// identity alone.
//
// processChatStreamUpdates folds EVERY batch through every per-chat slice —
// errorEvents, infoEvents, runOutputs, nodeExecutions, toolCallStates,
// toolResultsByCallId — and each fold used to return a fresh array / Map /
// object even when the batch had no entries for it. A streaming delta is the
// common batch while an agent works, so every network message replaced all six
// slices: ChatContainer re-rendered on the event logs and busted the memo'd
// ChatPresenter (the timeline then re-sorted the whole transcript), and every
// rendered ChatMessage re-parsed its content on the fresh tool-result index and
// re-rendered its tool cards on the fresh status Map. On a phone that render
// storm, once per incoming batch, is the lag in an active chat.

const CHAT = "c-identity";

function seedChat() {
  clearAllMessagesCache();
  useChatStore.setState({
    activeChatId: null,
    toolResultsByCallId: {},
    streamingMessages: {},
    finalizedStreamIds: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
  } as never);
  useThreadActivityStore.setState({ threads: {} } as never);
}

function message(id: string, role: MessageRole, seq: number, block: Record<string, unknown>): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId: CHAT,
      role,
      contentBlocks: [{ id: `${id}-b0`, index: 0, ...block }],
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

/** One batch that populates every slice, so each has an identity to keep. */
function everySlice(): ChatUpdate[] {
  return [
    message("m-call", MessageRole.ASSISTANT, 1, {
      type: ContentBlockType.TOOL_CALL,
      toolCallId: "call-1",
      toolName: "bash",
      input: "{}",
    }),
    message("m-result", MessageRole.TOOL, 2, {
      type: ContentBlockType.TOOL_RESULT,
      toolCallId: "call-1",
      toolName: "bash",
      content: "ok",
      isError: false,
    }),
    {
      update_type: "tool_call",
      tool_call_id: "call-1",
      tool_name: "bash",
      status: "completed",
      node_id: "",
      sequence_number: 0,
    },
    {
      update_type: "error",
      id: "e1",
      chat_id: CHAT,
      activity_id: "act",
      error_message: "boom",
      timestamp: "2026-01-01T00:00:01.000Z",
      sequence_number: 0,
    },
    { update_type: "info", id: "i1", chat_id: CHAT, message: "fyi", timestamp: "2026-01-01T00:00:01.000Z" },
    {
      update_type: "run_output",
      id: "ro-a",
      step_id: "s1",
      unique_activity_id: "a",
      sequence_number: 1,
      timestamp: "2026-01-01T00:00:00.000Z",
      content: "out",
    },
    { update_type: "node_execution", node_id: "n1", event_type: "started", status: 0, sequence_number: 1 },
  ] as unknown as ChatUpdate[];
}

/** A streaming delta: the batch an active chat receives many times a second. */
function delta(n: number): ChatUpdate[] {
  return [
    {
      update_type: "streaming_delta",
      delta_type: "content_block_start",
      block_index: n,
      message_id: "m-streaming",
      thread: "",
    },
  ] as unknown as ChatUpdate[];
}

function slices() {
  const s = useChatStore.getState();
  return {
    errorEvents: s.errorEvents[CHAT],
    infoEvents: s.infoEvents[CHAT],
    runOutputs: s.runOutputs[CHAT],
    nodeExecutions: s.nodeExecutions[CHAT],
    toolCallStates: s.toolCallStates[CHAT],
    toolResultsByCallId: s.toolResultsByCallId[CHAT],
  };
}

beforeEach(seedChat);

describe("stream batch slice identity", () => {
  it("keeps every per-chat slice's identity across batches that carry none of it", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, everySlice());
    const before = slices();
    // Sanity: each slice exists and holds what the first batch put there.
    expect(before.errorEvents).toHaveLength(1);
    expect(before.toolCallStates?.get("call-1")?.status).toBe("completed");
    expect(before.toolResultsByCallId?.["call-1"]).toBeDefined();

    for (let i = 0; i < 5; i++) store.processChatStreamUpdates(CHAT, delta(i));

    const after = slices();
    for (const key of Object.keys(before) as Array<keyof typeof before>) {
      expect(after[key], key).toBe(before[key]);
    }
    // The delta itself still landed.
    expect(useChatStore.getState().streamingMessages[CHAT]).toBeDefined();
  });

  it("still replaces a slice that a batch does carry", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, everySlice());
    const before = slices();

    store.processChatStreamUpdates(CHAT, [
      {
        update_type: "error",
        id: "e2",
        chat_id: CHAT,
        activity_id: "act",
        error_message: "again",
        timestamp: "2026-01-01T00:00:02.000Z",
        sequence_number: 0,
      },
    ] as unknown as ChatUpdate[]);

    const after = slices();
    expect(after.errorEvents).not.toBe(before.errorEvents);
    expect(after.errorEvents?.map((e) => e.id)).toEqual(["e1", "e2"]);
    expect(after.infoEvents).toBe(before.infoEvents);
    expect(after.toolCallStates).toBe(before.toolCallStates);
  });

  it("a snapshot with no entries still clears a slice that had some", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, everySlice());
    store.processChatStreamUpdates(CHAT, [], true);
    expect(useChatStore.getState().errorEvents[CHAT]).toEqual([]);
    expect(useChatStore.getState().runOutputs[CHAT]).toEqual([]);
  });

  it("does not re-render a subscriber of those slices on streaming deltas", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, everySlice());

    let renders = 0;
    function Subscriber() {
      // The subscriptions ChatContainer and every ChatMessage make.
      useErrorEvents(CHAT);
      useInfoEvents(CHAT);
      useRunOutputs(CHAT);
      useToolCallStates(CHAT);
      useToolResultsByCallId(CHAT);
      renders += 1;
      return null;
    }
    render(<Subscriber />);
    const initial = renders;

    act(() => {
      for (let i = 0; i < 20; i++) store.processChatStreamUpdates(CHAT, delta(i));
    });
    // Separate acts so React cannot batch a storm into one commit.
    for (let i = 20; i < 25; i++) {
      act(() => store.processChatStreamUpdates(CHAT, delta(i)));
    }

    expect(renders).toBe(initial);
  });
});
