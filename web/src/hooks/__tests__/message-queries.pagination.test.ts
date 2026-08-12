import { beforeEach, describe, expect, it } from "vitest";
import { MessageRole, StreamingState } from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../api/client";
import { queryClient } from "../../lib/query-client";
import {
  clearAllMessagesCache,
  getMessagesFromCache,
  getMessagesMetaFromCache,
  messageKeys,
  oldestSeqOf,
  patchMessagesCache,
  prependMessagesCache,
  setMessagesInCache,
  setMessagesMetaInCache,
  type MessageListResult,
} from "../message-queries";

// The message envelope's pagination metadata is what makes a BOUNDED initial
// snapshot recoverable — without truthful hasMore/oldestSeq, older history
// is unreachable. These pin the metadata write paths.

const CHAT = "c-pagination";

function msg(id: string, seq: number): Message {
  return {
    id,
    chatId: CHAT,
    role: MessageRole.ASSISTANT,
    contentBlocks: [],
    createdAt: "2026-01-01T00:00:00.000Z",
    updatedAt: "2026-01-01T00:00:00.000Z",
    streamingState: StreamingState.COMPLETE,
    seq: BigInt(seq),
    thread: "",
    sequenceNumber: 0n,
    attachments: [],
  } as unknown as Message;
}

function envelope(): MessageListResult | undefined {
  return queryClient.getQueryData<MessageListResult>(messageKeys.list(CHAT));
}

beforeEach(() => {
  clearAllMessagesCache();
});

describe("oldestSeqOf", () => {
  it("returns the MINIMUM seq, not the first element", () => {
    // The list is deliberately unsorted: the server used to read messages[0]
    // off a map-ordered slice, which picked an arbitrary element.
    expect(oldestSeqOf([msg("b", 40), msg("a", 12), msg("c", 88)])).toBe(12);
  });

  it("returns 0 for an empty list (the 'unknown' sentinel)", () => {
    expect(oldestSeqOf([])).toBe(0);
  });
});

describe("setMessagesInCache", () => {
  it("derives oldestSeq instead of defaulting it to 0", () => {
    setMessagesInCache(CHAT, [msg("m2", 20), msg("m3", 30)]);
    expect(envelope()?.oldestSeq).toBe(20);
  });

  it("writes asserted metadata verbatim", () => {
    setMessagesInCache(CHAT, [msg("m2", 20)], {
      total: 500,
      hasMore: true,
      oldestSeq: 20,
    });
    expect(getMessagesMetaFromCache(CHAT)).toEqual({
      total: 500,
      hasMore: true,
      oldestSeq: 20,
    });
  });

  it("preserves metadata across message-only rewrites", () => {
    setMessagesInCache(CHAT, [msg("m2", 20)], { total: 500, hasMore: true });
    setMessagesInCache(CHAT, [msg("m2", 20), msg("m3", 30)]);
    expect(envelope()?.total).toBe(500);
    expect(envelope()?.hasMore).toBe(true);
  });
});

describe("setMessagesMetaInCache", () => {
  it("updates metadata without touching messages", () => {
    setMessagesInCache(CHAT, [msg("m2", 20), msg("m3", 30)]);
    setMessagesMetaInCache(CHAT, { total: 900, hasMore: true, oldestSeq: 20 });

    expect(getMessagesFromCache(CHAT).map((m) => m.id)).toEqual(["m2", "m3"]);
    expect(getMessagesMetaFromCache(CHAT)).toEqual({
      total: 900,
      hasMore: true,
      oldestSeq: 20,
    });
  });

  it("clamps oldestSeq so it can only move backward", () => {
    // Cache holds scroll-back history down to seq 10. A reconnect snapshot
    // describes only its bounded newest window (oldest 300) — adopting that
    // would push the paging cursor forward past history we already hold.
    setMessagesInCache(CHAT, [msg("m1", 10), msg("m300", 300)]);
    setMessagesMetaInCache(CHAT, { total: 400, hasMore: true, oldestSeq: 300 });

    expect(getMessagesMetaFromCache(CHAT)?.oldestSeq).toBe(10);
  });

  it("is a no-op for a chat with no cache entry", () => {
    // Must not seed a message-less envelope — that would falsely trip the
    // hasMessagesCache "already initialized" marker.
    setMessagesMetaInCache(CHAT, { total: 10, hasMore: true });
    expect(envelope()).toBeUndefined();
  });
});

describe("prependMessagesCache", () => {
  it("concatenates older messages onto the front", () => {
    setMessagesInCache(CHAT, [msg("m3", 30), msg("m4", 40)]);
    prependMessagesCache(CHAT, [msg("m1", 10), msg("m2", 20)]);

    expect(getMessagesFromCache(CHAT).map((m) => m.id)).toEqual([
      "m1",
      "m2",
      "m3",
      "m4",
    ]);
  });

  it("moves oldestSeq backward to the new minimum", () => {
    setMessagesInCache(CHAT, [msg("m3", 30)], { oldestSeq: 30 });
    prependMessagesCache(CHAT, [msg("m1", 10), msg("m2", 20)]);
    expect(envelope()?.oldestSeq).toBe(10);
  });

  it("does not duplicate messages already present", () => {
    setMessagesInCache(CHAT, [msg("m2", 20), msg("m3", 30)]);
    prependMessagesCache(CHAT, [msg("m1", 10), msg("m2", 20)]);
    expect(getMessagesFromCache(CHAT).map((m) => m.id)).toEqual([
      "m1",
      "m2",
      "m3",
    ]);
  });

  it("keeps messages the stream appended while the page was in flight", () => {
    setMessagesInCache(CHAT, [msg("m3", 30)]);
    // Live message lands mid-fetch.
    patchMessagesCache(CHAT, (msgs) => [...msgs, msg("m4", 40)]);
    prependMessagesCache(CHAT, [msg("m1", 10)]);

    expect(getMessagesFromCache(CHAT).map((m) => m.id)).toEqual([
      "m1",
      "m3",
      "m4",
    ]);
  });

  it("records hasMore from the caller", () => {
    setMessagesInCache(CHAT, [msg("m3", 30)], { hasMore: true });
    prependMessagesCache(CHAT, [msg("m1", 10)], { hasMore: false });
    expect(envelope()?.hasMore).toBe(false);
  });
});
