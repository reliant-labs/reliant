import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import type { Chat } from "../../types/chat";
import { queryClient } from "../../lib/query-client";
import {
  chatKeys,
  patchChatCaches,
  removeChatFromListCache,
} from "../chat-queries";

const now = "2026-01-01T00:00:00.000Z";

type ChatListEnvelope = {
  chats: Chat[];
  total?: number;
  lastUserUpdateSequence: number;
};

function buildChat(overrides: Partial<Chat>): Chat {
  return {
    id: "c1",
    userId: "user-1",
    title: "Old",
    projectId: "p1",
    worktreeId: "wt-1",
    state: ChatState.IDLE,
    createdAt: now,
    updatedAt: now,
    lastActive: now,
    selectedPresets: {},
    needsRecovery: false,
    activity: 0,
    unread: false,
    ...overrides,
  } as Chat;
}

function buildEnvelope(
  chats: Chat[],
  extra: Partial<ChatListEnvelope> = {}
): ChatListEnvelope {
  return { chats, lastUserUpdateSequence: 42, ...extra };
}

// queryClient is a module singleton — clear between tests to avoid bleed.
beforeEach(() => {
  queryClient.clear();
});

afterEach(() => {
  queryClient.clear();
});

describe("patchChatCaches", () => {
  it("patches the chat immutably inside the list envelope", () => {
    const target = buildChat({ id: "c1" });
    const other = buildChat({ id: "c2", title: "Other" });
    const envelope = buildEnvelope([target, other], { total: 2 });
    queryClient.setQueryData(chatKeys.list("p1"), envelope);

    patchChatCaches("p1", "c1", { title: "New", unread: true });

    const next = queryClient.getQueryData<ChatListEnvelope>(
      chatKeys.list("p1")
    )!;
    // New envelope, new array, new chat object — never mutated in place.
    expect(next).not.toBe(envelope);
    expect(next.chats).not.toBe(envelope.chats);
    const patched = next.chats.find((c) => c.id === "c1")!;
    expect(patched).not.toBe(target);
    expect(patched.title).toBe("New");
    expect(patched.unread).toBe(true);
    // Untouched fields survive the spread.
    expect(patched.state).toBe(ChatState.IDLE);
    // Other chats keep reference identity.
    expect(next.chats.find((c) => c.id === "c2")).toBe(other);
    // Envelope metadata is preserved.
    expect(next.total).toBe(2);
    expect(next.lastUserUpdateSequence).toBe(42);
  });

  it("patches state fields for archive-style transitions", () => {
    const envelope = buildEnvelope([buildChat({ id: "c1" })]);
    queryClient.setQueryData(chatKeys.list("p1"), envelope);

    patchChatCaches("p1", "c1", { state: ChatState.ARCHIVED });

    const next = queryClient.getQueryData<ChatListEnvelope>(
      chatKeys.list("p1")
    )!;
    expect(next.chats[0].state).toBe(ChatState.ARCHIVED);
  });

  it("never fabricates a list cache entry when the cache is empty", () => {
    patchChatCaches("p1", "c1", { title: "New" });

    expect(queryClient.getQueryData(chatKeys.list("p1"))).toBeUndefined();
  });

  it("returns the same envelope when the chat is absent from the list", () => {
    const envelope = buildEnvelope([buildChat({ id: "c2" })]);
    queryClient.setQueryData(chatKeys.list("p1"), envelope);

    patchChatCaches("p1", "missing-chat", { title: "New" });

    expect(queryClient.getQueryData(chatKeys.list("p1"))).toBe(envelope);
  });

  it("skips the list patch but still patches detail when projectId is undefined", () => {
    const envelope = buildEnvelope([buildChat({ id: "c1" })]);
    queryClient.setQueryData(chatKeys.list("p1"), envelope);
    const detail = buildChat({ id: "c1" });
    queryClient.setQueryData(chatKeys.detail("c1"), detail);

    patchChatCaches(undefined, "c1", { title: "New" });

    // List untouched (same reference), detail patched.
    expect(queryClient.getQueryData(chatKeys.list("p1"))).toBe(envelope);
    const nextDetail = queryClient.getQueryData<Chat>(chatKeys.detail("c1"))!;
    expect(nextDetail).not.toBe(detail);
    expect(nextDetail.title).toBe("New");
  });

  it("patches the detail cache alongside the list", () => {
    queryClient.setQueryData(
      chatKeys.list("p1"),
      buildEnvelope([buildChat({ id: "c1" })])
    );
    queryClient.setQueryData(chatKeys.detail("c1"), buildChat({ id: "c1" }));

    patchChatCaches("p1", "c1", { title: "New" });

    expect(
      queryClient.getQueryData<Chat>(chatKeys.detail("c1"))!.title
    ).toBe("New");
  });

  it("leaves an empty detail cache untouched", () => {
    queryClient.setQueryData(
      chatKeys.list("p1"),
      buildEnvelope([buildChat({ id: "c1" })])
    );

    patchChatCaches("p1", "c1", { title: "New" });

    expect(queryClient.getQueryData(chatKeys.detail("c1"))).toBeUndefined();
  });
});

describe("removeChatFromListCache", () => {
  it("removes the chat and decrements total", () => {
    const keep = buildChat({ id: "c2" });
    queryClient.setQueryData(
      chatKeys.list("p1"),
      buildEnvelope([buildChat({ id: "c1" }), keep], { total: 2 })
    );

    removeChatFromListCache("p1", "c1");

    const next = queryClient.getQueryData<ChatListEnvelope>(
      chatKeys.list("p1")
    )!;
    expect(next.chats).toHaveLength(1);
    // Structural sharing rebuilds the array index-wise on removal, so assert
    // deep equality rather than reference identity for the survivor.
    expect(next.chats[0]).toEqual(keep);
    expect(next.total).toBe(1);
    expect(next.lastUserUpdateSequence).toBe(42);
  });

  it("floors total at 0", () => {
    queryClient.setQueryData(
      chatKeys.list("p1"),
      buildEnvelope([buildChat({ id: "c1" })], { total: 0 })
    );

    removeChatFromListCache("p1", "c1");

    expect(
      queryClient.getQueryData<ChatListEnvelope>(chatKeys.list("p1"))!.total
    ).toBe(0);
  });

  it("keeps an absent-total envelope total-free", () => {
    queryClient.setQueryData(
      chatKeys.list("p1"),
      buildEnvelope([buildChat({ id: "c1" })])
    );

    removeChatFromListCache("p1", "c1");

    const next = queryClient.getQueryData<ChatListEnvelope>(
      chatKeys.list("p1")
    )!;
    expect(next.chats).toHaveLength(0);
    expect("total" in next).toBe(false);
  });

  it("returns the same envelope when the chat is absent", () => {
    const envelope = buildEnvelope([buildChat({ id: "c2" })], { total: 1 });
    queryClient.setQueryData(chatKeys.list("p1"), envelope);

    removeChatFromListCache("p1", "missing-chat");

    expect(queryClient.getQueryData(chatKeys.list("p1"))).toBe(envelope);
  });

  it("removes the detail cache entry", () => {
    queryClient.setQueryData(chatKeys.detail("c1"), buildChat({ id: "c1" }));

    removeChatFromListCache("p1", "c1");

    expect(queryClient.getQueryData(chatKeys.detail("c1"))).toBeUndefined();
  });

  it("still removes detail when projectId is undefined", () => {
    const envelope = buildEnvelope([buildChat({ id: "c1" })]);
    queryClient.setQueryData(chatKeys.list("p1"), envelope);
    queryClient.setQueryData(chatKeys.detail("c1"), buildChat({ id: "c1" }));

    removeChatFromListCache(undefined, "c1");

    expect(queryClient.getQueryData(chatKeys.list("p1"))).toBe(envelope);
    expect(queryClient.getQueryData(chatKeys.detail("c1"))).toBeUndefined();
  });
});