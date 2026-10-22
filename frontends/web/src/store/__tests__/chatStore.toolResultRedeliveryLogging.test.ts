import { describe, expect, it, beforeEach, vi, afterEach } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { useThreadActivityStore } from "../threadActivityStore";
import { logger } from "../../lib/logger";
import {
  getMessagesFromCache,
  clearAllMessagesCache,
} from "../../hooks/message-queries";

// Re-delivery of an already-recorded tool result is the STEADY STATE, not an
// anomaly: every stream gap-resync replays a window of up to 200 messages, so
// each already-known result is re-offered on every resync. Warning per
// occurrence produced measured bursts of ~290 synchronous console.warn +
// ipcRenderer.send calls inside ~2ms, on the main thread, inside the store
// update React renders from — a multi-hundred-call IPC storm landing mid-frame
// against a 16.7ms budget.
//
// These tests lock in the shape that replaced it:
//   - identical re-delivery  → NO warn, at most one aggregate debug per batch
//   - differing re-delivery  → ONE aggregate warn (the genuine clobber signal)
// while first-write-wins remains intact in both cases.

const CHAT = "c-redelivery";

function seedChat() {
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
}

function assistantWithToolCalls(
  chatId: string,
  id: string,
  callIds: string[],
  seq: number,
): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId,
      role: MessageRole.ASSISTANT,
      contentBlocks: callIds.map((callId, index) => ({
        id: `${id}-b${index}`,
        type: ContentBlockType.TOOL_CALL,
        index,
        toolCallId: callId,
        toolName: "bash",
        input: "{}",
      })),
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
  opts: { isError?: boolean } = {},
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
          toolName: "bash",
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

function storedResult(chatId: string, callId: string) {
  return useChatStore.getState().toolResultsByCallId[chatId]?.[callId];
}

/** Warn calls whose message mentions tool results — ignores unrelated warnings. */
function toolResultWarnCalls(spy: { mock: { calls: unknown[][] } }) {
  return spy.mock.calls.filter(
    (call) => typeof call[0] === "string" && /tool result/i.test(call[0]),
  );
}

const CALL_COUNT = 50;
const callIds = Array.from({ length: CALL_COUNT }, (_, i) => `call-${i}`);

/** A resync-style batch: the whole window of already-known results, re-sent. */
function replayBatch(startSeq: number, contentFor: (i: number) => string) {
  return callIds.map((callId, i) =>
    toolResultMessage(
      CHAT,
      `replay-${startSeq}-${i}`,
      callId,
      startSeq + i,
      contentFor(i),
    ),
  );
}

describe("tool-result re-delivery logging", () => {
  let warnSpy: ReturnType<typeof vi.spyOn>;
  let debugSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    seedChat();
    warnSpy = vi.spyOn(logger, "warn").mockImplementation(() => {});
    debugSpy = vi.spyOn(logger, "debug").mockImplementation(() => {});
  });

  afterEach(() => {
    warnSpy.mockRestore();
    debugSpy.mockRestore();
  });

  /** Seed the index with one result per call, then clear the spies. */
  function seedResults() {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      assistantWithToolCalls(CHAT, "m1", callIds, 1),
      ...callIds.map((callId, i) =>
        toolResultMessage(CHAT, `t-${i}`, callId, 100 + i, `output-${i}`),
      ),
    ]);
    warnSpy.mockClear();
    debugSpy.mockClear();
  }

  it("does not warn per-occurrence when a resync replays already-recorded results", () => {
    seedResults();
    const store = useChatStore.getState();

    // Three resyncs replaying the identical window — the production pattern.
    for (let round = 0; round < 3; round++) {
      store.processChatStreamUpdates(
        CHAT,
        replayBatch(1000 + round * 100, (i) => `output-${i}`),
      );
    }

    // This is the assertion that fails against the old code, which emitted one
    // warn per re-delivered id: 50 ids x 3 rounds = 150 warns.
    expect(toolResultWarnCalls(warnSpy)).toHaveLength(0);

    // Observability is preserved as an aggregate: exactly one line per batch
    // (3 resyncs → 3 lines) carrying the suppressed count, rather than the 150
    // individual lines the old code emitted. Other debug logging on this path
    // is unrelated, so count only the aggregate.
    const aggregates = debugSpy.mock.calls.filter(
      (call) =>
        typeof call[0] === "string" && /re-delivered tool results/i.test(call[0]),
    );
    expect(aggregates).toHaveLength(3);
    expect(
      (aggregates[0][1] as { alreadyRecorded: number }).alreadyRecorded,
    ).toBe(CALL_COUNT);
  });

  it("keeps the first result when an identical copy is re-delivered", () => {
    seedResults();
    useChatStore
      .getState()
      .processChatStreamUpdates(CHAT, replayBatch(2000, (i) => `output-${i}`));

    for (let i = 0; i < CALL_COUNT; i++) {
      expect(storedResult(CHAT, callIds[i])?.content).toBe(`output-${i}`);
    }
  });

  it("warns ONCE, in aggregate, when a re-delivery carries DIFFERENT content", () => {
    seedResults();

    // The genuinely suspicious case the original warning existed to catch: a
    // re-delivered error stub that would clobber real output.
    useChatStore.getState().processChatStreamUpdates(CHAT, [
      toolResultMessage(CHAT, "conflict-1", callIds[0], 3000, "ERROR: stub", {
        isError: true,
      }),
      toolResultMessage(CHAT, "conflict-2", callIds[1], 3001, "ERROR: stub", {
        isError: true,
      }),
      // ...alongside a large window of identical re-deliveries, which must not
      // add any warnings of their own.
      ...replayBatch(3100, (i) => `output-${i}`),
    ]);

    const warns = toolResultWarnCalls(warnSpy);
    expect(warns).toHaveLength(1);
    expect((warns[0][1] as { count: number }).count).toBe(2);

    // First-write-wins still holds: the stub did NOT clobber real output.
    expect(storedResult(CHAT, callIds[0])?.content).toBe("output-0");
    expect(storedResult(CHAT, callIds[0])?.is_error).toBe(false);
    expect(storedResult(CHAT, callIds[1])?.content).toBe("output-1");
  });

  it("records results normally on the first delivery without warning", () => {
    seedChat();
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      assistantWithToolCalls(CHAT, "m1", ["call-a"], 1),
      toolResultMessage(CHAT, "t-a", "call-a", 2, "fresh output"),
    ]);

    expect(toolResultWarnCalls(warnSpy)).toHaveLength(0);
    expect(storedResult(CHAT, "call-a")?.content).toBe("fresh output");
  });

  it("re-records after a snapshot that REPLACES the message list", () => {
    seedResults();
    const store = useChatStore.getState();

    // snapshotReplaces resets the index, so the same ids are new again — they
    // must be recorded, not dropped as re-deliveries.
    store.processChatStreamUpdates(
      CHAT,
      [
        assistantWithToolCalls(CHAT, "m2", [callIds[0]], 9000),
        toolResultMessage(CHAT, "snap-t", callIds[0], 9001, "post-snapshot"),
      ],
      true,
    );

    expect(getMessagesFromCache(CHAT).length).toBeGreaterThan(0);
    expect(storedResult(CHAT, callIds[0])?.content).toBe("post-snapshot");
  });
});
