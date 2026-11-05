// Guard for moving processChatStreamUpdates' fold onto @reliantlabs/sdk's
// reduceChat. These pin what the store did with a batch BEFORE the fold moved,
// at the places a different reducer could differ silently: what a wrong-chat
// frame, a refetch signal, an error marker, an approval / question / thread do
// beyond the chat's own slices, the order of info and warning notices, and
// which slices keep their identity. They name what the stores and caches hold,
// never how the batch was folded, so they run unchanged on both sides.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ContentBlockType, MessageRole, StreamingState } from "../../gen/reliant/v1/chat_pb";
import { ApprovalStatus } from "../../gen/reliant/v1/approval_pb";
import type { ChatUpdate } from "../../types/streaming";
import { clearAllMessagesCache, getMessagesFromCache } from "../../hooks/message-queries";
import { approvalsFromCache } from "../../hooks/approval-queries";
import { queryClient } from "../../lib/query-client";
import { logger } from "../../lib/logger";
import { questionServiceKeys } from "../../hooks/question-service-hooks_gen";
import { useChatStore } from "../chatStore";
import { useModalStore } from "../modalStore";
import { useThreadActivityStore } from "../threadActivityStore";

const refetch = vi.hoisted(() => ({ trigger: vi.fn(), plan: vi.fn() }));
vi.mock("../refetchStore", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../refetchStore")>()),
  triggerRefetch: refetch.trigger,
}));
vi.mock("../../hooks/task-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/task-queries")>()),
  invalidatePlanTasks: refetch.plan,
}));

const CHAT = "c-guard";

function seed() {
  clearAllMessagesCache();
  queryClient.clear();
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
  useModalStore.getState().closeModal();
  refetch.trigger.mockClear();
  refetch.plan.mockClear();
}

const text = (chatId: string, id: string, seq: number, role = MessageRole.ASSISTANT): ChatUpdate =>
  ({
    update_type: "message",
    message: {
      id,
      chatId,
      role,
      contentBlocks: [{ id: `${id}-b0`, type: ContentBlockType.TEXT, index: 0, content: id }],
      createdAt: "2026-01-01T00:00:00.000Z",
      updatedAt: "2026-01-01T00:00:00.000Z",
      streamingState: StreamingState.COMPLETE,
      seq: BigInt(seq),
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  }) as unknown as ChatUpdate;

const info = (id: string, type: "info" | "warning", message: string): ChatUpdate =>
  ({ update_type: type, id, chat_id: CHAT, message, timestamp: "2026-01-01T00:00:00.000Z" }) as unknown as ChatUpdate;

const error = (id: string, message: string): ChatUpdate =>
  ({
    update_type: "error",
    id,
    chat_id: CHAT,
    activity_id: "act",
    error_message: message,
    timestamp: "2026-01-01T00:00:00.000Z",
    sequence_number: 0,
  }) as unknown as ChatUpdate;

beforeEach(seed);
afterEach(() => vi.restoreAllMocks());

describe("a batch's effects beyond the chat's own slices", () => {
  it("drops a message of another chat and says so", () => {
    const warn = vi.spyOn(logger, "warn").mockImplementation(() => {});
    useChatStore.getState().processChatStreamUpdates(CHAT, [text(CHAT, "mine", 1), text("some-other-chat", "theirs", 2)]);
    expect(getMessagesFromCache(CHAT).map((m) => m.id)).toEqual(["mine"]);
    expect(warn.mock.calls.some((c) => /wrong chat/i.test(String(c[0])))).toBe(true);
  });

  it("keeps info and warning notices in the order they arrived", () => {
    useChatStore
      .getState()
      .processChatStreamUpdates(CHAT, [info("w1", "warning", "first"), info("i1", "info", "second"), info("w2", "warning", "third")]);
    expect((useChatStore.getState().infoEvents[CHAT] ?? []).map((e) => e.id)).toEqual(["w1", "i1", "w2"]);
  });

  it("forwards a refetch signal, and marks the plan stale for plan_tasks", () => {
    useChatStore.getState().processChatStreamUpdates(CHAT, [
      { update_type: "refetch", type: "file_tree" },
      { update_type: "refetch", type: "plan_tasks" },
    ] as unknown as ChatUpdate[]);
    expect(refetch.trigger.mock.calls).toEqual([
      ["file_tree", CHAT],
      ["plan_tasks", CHAT],
    ]);
    expect(refetch.plan).toHaveBeenCalledTimes(1);
  });

  it("strips an error marker, opens the upgrade modal live, and only strips on a snapshot", () => {
    const marked = "Out of credits [RELIANT_MANAGED_QUOTA_EXHAUSTED:x]";
    useChatStore.getState().processChatStreamUpdates(CHAT, [error("e1", marked)], true);
    expect(useChatStore.getState().errorEvents[CHAT]?.[0]?.error_message).toBe("Out of credits");
    expect(useModalStore.getState().activeModal).toBeNull();

    useChatStore.getState().processChatStreamUpdates(CHAT, [error("e2", marked)]);
    // Distinct ids are distinct rows; each reads clean.
    expect(useChatStore.getState().errorEvents[CHAT]?.map((e) => e.error_message)).toEqual(["Out of credits", "Out of credits"]);
    expect(useModalStore.getState().activeModal).toBe("upgrade-required");
  });

  it("puts approvals, questions and threads where the app reads them", () => {
    useChatStore.getState().processChatStreamUpdates(CHAT, [
      {
        update_type: "approval",
        id: "a1",
        chat_id: CHAT,
        approval_type: "tool_call",
        entity_id: "tc-1",
        title: "Run it?",
        status: "pending",
        sequence_number: 1,
        created_at: "2026-01-01T00:00:00.000Z",
      },
      {
        update_type: "question",
        question_id: "q1",
        chat_id: CHAT,
        workflow_id: "w",
        thread_id: CHAT,
        step_id: "s",
        status: "pending",
      },
      { update_type: "thread", id: "t1", chat_id: CHAT, thread: "t1", status: "running", title: "Sub", is_planning_mode: false },
    ] as unknown as ChatUpdate[]);

    expect(approvalsFromCache(CHAT).map((a) => [a.id, a.status, a.title])).toEqual([["a1", ApprovalStatus.PENDING, "Run it?"]]);
    const questions = queryClient.getQueryData<{ questions: { questionId: string }[] }>(questionServiceKeys.listPendingQuestions({ chatId: CHAT }));
    expect(questions?.questions.map((q) => q.questionId)).toEqual(["q1"]);
    expect((useThreadActivityStore.getState().threads[CHAT] ?? []).map((t) => t.thread)).toEqual(["t1"]);

    // A resolution merges over the approval it names, keeping what it asked.
    useChatStore.getState().processChatStreamUpdates(CHAT, [
      { update_type: "approval", id: "a1", chat_id: CHAT, status: "approved", sequence_number: 2, created_at: "" },
    ] as unknown as ChatUpdate[]);
    expect(approvalsFromCache(CHAT).map((a) => [a.id, a.status, a.title])).toEqual([["a1", ApprovalStatus.APPROVED, "Run it?"]]);
  });

  it("a snapshot does not clear the threads the app already knows", () => {
    useChatStore.getState().processChatStreamUpdates(CHAT, [
      { update_type: "thread", id: "t1", chat_id: CHAT, thread: "t1", status: "running", is_planning_mode: false },
    ] as unknown as ChatUpdate[]);
    useChatStore.getState().processChatStreamUpdates(CHAT, [text(CHAT, "m1", 1)], true);
    expect((useThreadActivityStore.getState().threads[CHAT] ?? []).map((t) => t.thread)).toEqual(["t1"]);
  });
});

describe("what keeps its identity", () => {
  it("a batch with no streaming change leaves the streaming slice as it was", () => {
    const delta = (d: Record<string, unknown>) =>
      ({ update_type: "streaming_delta", message_id: "m-live", thread: "", block_index: 0, ...d }) as unknown as ChatUpdate;
    useChatStore.getState().processChatStreamUpdates(CHAT, [delta({ delta_type: "message_start" }), delta({ delta_type: "content_block_start" })]);
    const before = useChatStore.getState().streamingMessages;
    expect(before[CHAT]).toBeDefined();

    useChatStore.getState().processChatStreamUpdates(CHAT, [info("i1", "info", "fyi")]);
    expect(useChatStore.getState().streamingMessages).toBe(before);
  });

  it("the finalized-id set keeps its identity across a batch that finalizes nothing", () => {
    useChatStore.getState().processChatStreamUpdates(CHAT, [text(CHAT, "m1", 1)]);
    const before = useChatStore.getState().finalizedStreamIds;
    expect(before[CHAT]?.has("m1")).toBe(true);
    useChatStore.getState().processChatStreamUpdates(CHAT, [info("i1", "info", "fyi")]);
    expect(useChatStore.getState().finalizedStreamIds).toBe(before);
  });
});
