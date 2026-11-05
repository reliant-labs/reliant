import { describe, expect, it } from "vitest";
import { createChatState, reduceChat } from "./chat-reducer.js";
import type { ChatState } from "./chat-state.js";
import { decodeChatUpdate } from "./decode.js";
import type { ChatUpdate } from "./events.js";
import {
  approvalData,
  CHAT,
  deltaData,
  finalizedData,
  messageData,
  textMessage,
  toolCallData,
  updateData,
} from "../test/scripted-server.js";
import { ChatUpdateType, type ChatUpdateData } from "../gen/reliant/v1/streaming_pb.js";
import { create } from "@bufbuild/protobuf";
import { ChatUpdateDataSchema } from "../gen/reliant/v1/streaming_pb.js";

const NOW = "2026-02-02T00:00:00.000Z";
const env = { now: () => NOW };

const decode = (...data: unknown[]): ChatUpdate[] =>
  data.map((d) => decodeChatUpdate(create(ChatUpdateDataSchema, d as never) as ChatUpdateData)!).filter(Boolean);

const apply = (state: ChatState, ...data: unknown[]) =>
  reduceChat(state, { kind: "updates", updates: decode(...data), cursor: state.cursor + 1n }, env);
const snapshot = (state: ChatState, ...data: unknown[]) =>
  reduceChat(state, { kind: "snapshot", updates: decode(...data), cursor: 10n }, env);

const texts = (s: ChatState) => s.messages.map((m) => m.contentBlocks.map((b) => b.content).join(""));
const streamingText = (s: ChatState) =>
  (s.streaming[CHAT]?.contentBlocks ?? []).map((b) => b.content).join("");

const fresh = () => createChatState(CHAT);

describe("reduceChat is pure", () => {
  it("never mutates the state it is given", () => {
    const before = snapshot(fresh(), messageData(1, "m-1", "one"), approvalData(2, "pending"));
    const frozen = JSON.stringify(before, (_k, v) => (typeof v === "bigint" ? v.toString() : v instanceof Map ? [...v] : v instanceof Set ? [...v] : v));
    apply(before, messageData(3, "m-2", "two"), deltaData("content_block_start"), toolCallData(4, "t1", "executing"));
    const after = JSON.stringify(before, (_k, v) => (typeof v === "bigint" ? v.toString() : v instanceof Map ? [...v] : v instanceof Set ? [...v] : v));
    expect(after).toBe(frozen);
  });

  it("gives the same answer for the same input, with an injected clock", () => {
    const a = apply(fresh(), deltaData("content_block_start"));
    const b = apply(fresh(), deltaData("content_block_start"));
    expect(a.streaming[CHAT]).toEqual(b.streaming[CHAT]);
    expect(a.streaming[CHAT]?.createdAt).toBe(NOW);
  });
});

describe("messages", () => {
  it("a snapshot replaces, a replay merges and keeps canonical order", () => {
    let s = snapshot(fresh(), messageData(1, "m-1", "one"), messageData(2, "m-2", "two"));
    expect(texts(s)).toEqual(["one", "two"]);
    s = apply(s, messageData(4, "m-4", "four"), messageData(3, "m-3", "three"));
    expect(texts(s)).toEqual(["one", "two", "three", "four"]);
    s = snapshot(s, messageData(9, "m-9", "nine"));
    expect(texts(s)).toEqual(["nine"]);
  });

  it("a reconnect snapshot that overlaps and would lose loaded history merges instead", () => {
    let s = snapshot(fresh(), messageData(1, "m-1", "one"), messageData(2, "m-2", "two"), messageData(3, "m-3", "three"));
    s = snapshot(s, messageData(3, "m-3", "three"), messageData(4, "m-4", "four"));
    expect(texts(s)).toEqual(["one", "two", "three", "four"]);
  });

  it("drops a message that belongs to another chat", () => {
    const other = {
      ...messageData(5, "m-x", "wrong"),
      dataJson: JSON.stringify({ message: { id: "m-x", chat_id: "someone-else", role: 2, seq: 5, content_blocks: [{ id: "b", type: 1, index: 0, content: "wrong" }] } }),
    };
    expect(texts(apply(fresh(), other))).toEqual([]);
  });

  it("an arriving user message retires the optimistic one; a failed send survives", () => {
    let s = reduceChat(fresh(), { kind: "sendStarted", clientMessageId: "c1", text: "hello", createdAt: NOW }, env);
    expect(s.messages.map((m) => m.id)).toEqual(["optimistic-user-c1"]);
    s = reduceChat(s, { kind: "sendFailed", clientMessageId: "c1" }, env);
    expect(s.messages.map((m) => m.id)).toEqual(["optimistic-failed-c1"]);
    s = snapshot(s, messageData(1, "m-1", "one"));
    expect(s.messages.map((m) => m.id)).toEqual(["m-1", "optimistic-failed-c1"]);

    let t = reduceChat(fresh(), { kind: "sendStarted", clientMessageId: "c2", text: "hi", createdAt: NOW }, env);
    t = apply(t, messageData(1, "m-1", "hi", 1));
    expect(t.messages.map((m) => m.id)).toEqual(["m-1"]);
  });
});

describe("streaming placeholder", () => {
  it("builds under the pre-allocated id, and a retry restarts it", () => {
    let s = apply(fresh(), deltaData("message_start"), deltaData("content_block_start"), deltaData("content_block_delta", { delta: "Hel" }));
    expect(s.streaming[CHAT]?.id).toBe("m-live");
    expect(streamingText(s)).toBe("Hel");
    s = apply(s, deltaData("content_block_delta", { delta: "lo" }));
    expect(streamingText(s)).toBe("Hello");
    s = apply(s, deltaData("message_start"), deltaData("content_block_start"), deltaData("content_block_delta", { delta: "Again" }));
    expect(streamingText(s)).toBe("Again");
  });

  it("a tail of deltas with nothing to build on fabricates no placeholder", () => {
    expect(apply(fresh(), deltaData("content_block_delta", { delta: "x" })).streaming).toEqual({});
  });

  it("the persisted message retires the placeholder", () => {
    let s = apply(fresh(), deltaData("content_block_start"), deltaData("content_block_delta", { delta: "Hi" }));
    expect(Object.keys(s.streaming)).toEqual([CHAT]);
    s = apply(s, messageData(1, "m-live", "Hi"));
    expect(s.streaming).toEqual({});
    expect(texts(s)).toEqual(["Hi"]);
  });

  it("a delta naming a finalized id is a stale tail and is dropped", () => {
    let s = apply(fresh(), messageData(1, "m-live", "done"));
    s = apply(s, deltaData("content_block_start"), deltaData("content_block_delta", { delta: "late" }));
    expect(s.streaming).toEqual({});

    // Same through the marker, in one batch with the tail.
    const t = apply(fresh(), finalizedData(1, "m-9", "completed"), deltaData("content_block_start", {}, "m-9"));
    expect(t.streaming).toEqual({});
    expect(t.finalizedStreamIds.has("m-9")).toBe(true);
  });

  it("addresses blocks by block_index across batches, past a thinking block that takes no slot", () => {
    let s = apply(
      fresh(),
      deltaData("message_start"),
      deltaData("thinking_block_start"),
      deltaData("thinking_block_delta", { delta: "hmm" }),
      deltaData("content_block_start", { block_index: 1 }),
      deltaData("content_block_delta", { block_index: 1, delta: "Hel" }),
    );
    expect(streamingText(s)).toBe("Hel");
    s = apply(s, deltaData("content_block_delta", { block_index: 1, delta: "lo" }));
    expect(streamingText(s)).toBe("Hello");
    s = apply(s, deltaData("tool_use_start", { block_index: 3, tool_call: { id: "t1", name: "shell" } }));
    s = apply(s, deltaData("content_block_start", { block_index: 4 }), deltaData("content_block_delta", { block_index: 4, delta: "!" }));
    s = apply(s, deltaData("content_block_delta", { block_index: 1, delta: " there" }));
    const blocks = s.streaming[CHAT]!.contentBlocks;
    expect(blocks.map((b) => b.index)).toEqual([1, 3, 4]);
    expect(blocks.map((b) => b.content ?? "")).toEqual(["Hello there", "", "!"]);
    expect(blocks[1]!.toolCallId).toBe("t1");
  });

  it("keeps threads apart", () => {
    const s = apply(
      fresh(),
      deltaData("content_block_start", { thread: "t-a" }, "m-a"),
      deltaData("content_block_delta", { thread: "t-a", delta: "A" }, "m-a"),
      deltaData("content_block_start", {}, "m-main"),
    );
    expect(Object.keys(s.streaming).sort()).toEqual([CHAT, "t-a"].sort());
    expect(s.streaming["t-a"]?.contentBlocks[0]?.content).toBe("A");
  });
});

describe("tools", () => {
  it("never regresses a settled tool, and a late completion does not undo a cancel", () => {
    let s = apply(fresh(), toolCallData(1, "t1", "completed"));
    s = apply(s, toolCallData(2, "t1", "executing"));
    expect(s.toolCalls.get("t1")?.status).toBe("executing"); // not terminal for the status fold: latest wins
    s = apply(fresh(), toolCallData(1, "t2", "completed"));
    s = apply(s, updateData(ChatUpdateType.TOOL_CALL, 2, "t2", { tool_call_id: "t2", tool_name: "shell", status: "failed" }));
    expect(s.toolCalls.get("t2")?.status).toBe("failed");
  });

  it("results join their call by id, first write wins", () => {
    const result = (id: string, text: string) =>
      updateData(ChatUpdateType.MESSAGE, 5, id, {
        message: { id, chat_id: CHAT, role: 4, seq: 5, content_blocks: [{ id: `${id}-b`, type: 3, index: 0, tool_call_id: "t1", content: text }] },
      });
    let s = apply(fresh(), result("r1", "first"));
    expect(s.toolResults.t1?.content).toBe("first");
    s = apply(s, result("r2", "redelivered-different"));
    expect(s.toolResults.t1?.content).toBe("first");
  });

  it("a stream that stopped settles its unfinished tools to cancelled, inferred, and keeps the placeholder", () => {
    let s = apply(
      fresh(),
      deltaData("tool_use_start", { tool_call: { id: "t1", name: "shell" } }),
      deltaData("tool_use_start", { block_index: 1, tool_call: { id: "t2", name: "shell" } }),
      toolCallData(1, "t2", "completed"),
    );
    s = apply(s, finalizedData(2, "m-live", "cancelled"));
    expect(s.toolCalls.get("t1")).toMatchObject({ status: "cancelled", inferred: true });
    expect(s.toolCalls.get("t2")?.status).toBe("completed");
    expect(s.streaming[CHAT]).toBeDefined();
    expect((s.streaming[CHAT]!.contentBlocks as Array<{ status?: string }>).find((b) => b)?.status).toBe("cancelled");
  });

  it("an inferred cancel yields to the tool's real outcome arriving afterwards", () => {
    let s = apply(fresh(), deltaData("tool_use_start", { tool_call: { id: "t1", name: "shell" } }));
    s = apply(s, finalizedData(1, "m-live", "aborted"));
    expect(s.toolCalls.get("t1")?.status).toBe("cancelled");
    s = apply(s, toolCallData(2, "t1", "completed"));
    expect(s.toolCalls.get("t1")).toMatchObject({ status: "completed", inferred: false });
  });
});

describe("approvals, questions, logs", () => {
  it("merges an approval's resolution over the pending one without losing its title", () => {
    let s = apply(fresh(), approvalData(1, "pending"));
    s = apply(s, updateData(ChatUpdateType.APPROVAL, 2, "appr-1", { id: "appr-1", chat_id: CHAT, status: "denied", denial_reason: "no" }));
    expect(s.approvals).toHaveLength(1);
    expect(s.approvals[0]).toMatchObject({ status: "denied", title: "Run the migration", denial_reason: "no" });
  });

  it("a blocking question is pending until resolved; an async one never takes the pending list", () => {
    const q = (status: string, extra = {}) =>
      updateData(ChatUpdateType.QUESTION, 1, "q1", { question_id: "q1", chat_id: CHAT, workflow_id: "w", step_id: "s", status, ...extra });
    let s = apply(fresh(), q("pending"));
    expect(s.pendingQuestions.map((x) => x.question_id)).toEqual(["q1"]);
    s = apply(s, q("resolved"));
    expect(s.pendingQuestions).toEqual([]);
    s = apply(s, q("pending", { question_id: "q2", async: true }));
    expect(s.pendingQuestions).toEqual([]);
    expect(s.asyncQuestions.map((x) => x.question_id)).toEqual(["q2"]);
  });

  it("a replacement snapshot reconciles gates, threads, workflows and placeholders against itself", () => {
    const question = updateData(ChatUpdateType.QUESTION, 2, "q1", { question_id: "q1", chat_id: CHAT, workflow_id: "w", step_id: "s", status: "pending" });
    const asyncQuestion = updateData(ChatUpdateType.QUESTION, 3, "q2", { question_id: "q2", chat_id: CHAT, workflow_id: "w", step_id: "s", status: "pending", async: true });
    const thread = updateData(ChatUpdateType.THREAD, 4, "t-a", { id: "t-a", chat_id: CHAT, thread_id: "t-a", status: "active" });
    const workflow = updateData(ChatUpdateType.WORKFLOW_STATUS, 5, "w", { workflow_id: "w", chat_id: CHAT, status: "running" });
    let s = snapshot(fresh(), messageData(1, "m-1", "one"), approvalData(1, "pending"), question, asyncQuestion, thread, workflow);
    s = apply(s, deltaData("content_block_start"), deltaData("content_block_delta", { delta: "half" }));
    expect(s.approvals).toHaveLength(1);
    expect(s.pendingQuestions).toHaveLength(1);
    expect(s.asyncQuestions).toHaveLength(1);
    expect(s.threads).toHaveLength(1);
    expect(Object.keys(s.workflowStatus)).toEqual(["w"]);
    expect(streamingText(s)).toBe("half");

    // The reconnect could not replay the cursor; the server's snapshot no longer
    // has any of it, and the stream's finalize was missed.
    s = snapshot(s, messageData(9, "m-9", "fresh start"));
    expect(s.approvals).toEqual([]);
    expect(s.pendingQuestions).toEqual([]);
    expect(s.asyncQuestions).toEqual([]);
    expect(s.threads).toEqual([]);
    expect(s.workflowStatus).toEqual({});
    expect(s.streaming).toEqual({});

    // What the snapshot does report is held.
    s = snapshot(s, messageData(9, "m-9", "fresh start"), approvalData(9, "pending"));
    expect(s.approvals.map((a) => a.id)).toEqual(["appr-1"]);
  });

  it("errors dedupe by id and a snapshot starts the log afresh", () => {
    const err = (id: string, message: string) =>
      updateData(ChatUpdateType.ERROR, 1, id, { id, chat_id: CHAT, activity_type: "a", activity_id: "x", error_message: message, timestamp: NOW, sequence_number: 1 });
    let s = apply(fresh(), err("e1", "try 1"));
    s = apply(s, err("e1", "try 2"), err("e2", "other"));
    expect(s.errors.map((e) => e.error_message)).toEqual(["try 2", "other"]);
    s = snapshot(s, err("e3", "fresh"));
    expect(s.errors.map((e) => e.id)).toEqual(["e3"]);
  });

  it("an unknown update type is ignored, not fatal", () => {
    const s = apply(fresh(), { updateType: 9999 as ChatUpdateType, sequenceNumber: 1n, entityId: "x", chatId: CHAT, dataJson: "{}" });
    expect({ ...s, cursor: 0n }).toEqual(fresh());
  });
});

describe("cursor and sync", () => {
  it("tracks the cursor the actions carry and clears syncPending on caught_up", () => {
    let s = fresh();
    expect(s.syncPending).toBe(true);
    s = snapshot(s, messageData(1, "m-1", "one"));
    expect(s.cursor).toBe(10n);
    expect(s.syncPending).toBe(true);
    s = reduceChat(s, { kind: "caughtUp", cursor: 10n }, env);
    expect(s.syncPending).toBe(false);
    s = reduceChat(s, { kind: "caughtUp", cursor: 3n }, env);
    expect(s.cursor).toBe(10n);
  });
});

describe("decodeChatUpdate", () => {
  it("wraps a message payload and spreads the others with their discriminator and sequence", () => {
    const [m] = decode(messageData(7, "m-7", "seven"));
    expect(m).toMatchObject({ update_type: "message", message: { id: "m-7" } });
    const [a] = decode(approvalData(8, "pending"));
    expect(a).toMatchObject({ update_type: "approval", id: "appr-1", sequence_number: 8 });
  });

  it("returns null for a kind it does not know or a payload it cannot parse", () => {
    expect(decodeChatUpdate(create(ChatUpdateDataSchema, { updateType: 9999 as ChatUpdateType, dataJson: "{}" }))).toBeNull();
    expect(decodeChatUpdate(create(ChatUpdateDataSchema, { updateType: ChatUpdateType.APPROVAL, dataJson: "{nope" }))).toBeNull();
  });

  it("ignores the integer update_type the Go side also writes into the payload", () => {
    const [a] = decode(updateData(ChatUpdateType.APPROVAL, 1, "x", { id: "x", status: "pending", update_type: 2 }));
    expect(a!.update_type).toBe("approval");
  });
});

describe("textMessage fixture", () => {
  it("is a complete message", () => {
    expect(textMessage("m", 2, 1, "t").streamingState).toBe(2);
  });
});
