import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import type { Chat } from "../../types/chat";
import { queryClient } from "../../lib/query-client";
import {
  chatKeys,
  getCachedChatList,
  getCachedMainWorktree,
  patchChatCaches,
  removeChatFromListCache,
  upsertChatInListCache,
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

describe("upsertChatInListCache", () => {
  it("creates a list envelope for the first chat when the cache is absent", () => {
    const chat = buildChat({ id: "c1" });

    upsertChatInListCache("p1", chat);

    const next = queryClient.getQueryData<ChatListEnvelope>(
      chatKeys.list("p1")
    )!;
    expect(next.chats).toEqual([chat]);
    expect(next.total).toBe(1);
    expect(next.lastUserUpdateSequence).toBe(0);
  });

  it("prepends a new chat to an existing empty list envelope", () => {
    const chat = buildChat({ id: "c1" });
    queryClient.setQueryData(chatKeys.list("p1"), buildEnvelope([], { total: 0 }));

    upsertChatInListCache("p1", chat);

    const next = queryClient.getQueryData<ChatListEnvelope>(
      chatKeys.list("p1")
    )!;
    expect(next.chats).toEqual([chat]);
    expect(next.total).toBe(1);
    expect(next.lastUserUpdateSequence).toBe(42);
  });

  it("replaces an existing chat without duplicating it", () => {
    const existing = buildChat({ id: "c1", title: "Old" });
    const other = buildChat({ id: "c2", title: "Other" });
    const envelope = buildEnvelope([existing, other], { total: 2 });
    queryClient.setQueryData(chatKeys.list("p1"), envelope);

    upsertChatInListCache("p1", buildChat({ id: "c1", title: "New" }));

    const next = queryClient.getQueryData<ChatListEnvelope>(
      chatKeys.list("p1")
    )!;
    expect(next.chats).toHaveLength(2);
    expect(next.chats[0].title).toBe("New");
    expect(next.chats[1]).toBe(other);
    expect(next.total).toBe(2);
  });
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

  it("patches the list and the detail even when projectId is undefined", () => {
    queryClient.setQueryData(chatKeys.list("p1"), buildEnvelope([buildChat({ id: "c1" })]));
    const detail = buildChat({ id: "c1" });
    queryClient.setQueryData(chatKeys.detail("c1"), detail);

    patchChatCaches(undefined, "c1", { title: "New" });

    // A chat is patched wherever it is listed: the caller's project id is
    // not needed to find it.
    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list("p1"))!.chats[0]!.title).toBe("New");
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

  it("removes the chat from every list even when projectId is undefined", () => {
    queryClient.setQueryData(chatKeys.list("p1"), buildEnvelope([buildChat({ id: "c1" })]));
    queryClient.setQueryData(chatKeys.detail("c1"), buildChat({ id: "c1" }));

    removeChatFromListCache(undefined, "c1");

    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list("p1"))!.chats).toEqual([]);
    expect(queryClient.getQueryData(chatKeys.detail("c1"))).toBeUndefined();
  });
});
// The sidebar lists every project (chatKeys.list(null)) or one; both lists can
// hold the same chat, and every write must reach both.
describe("all-projects and project lists together", () => {
  it("keys the all-projects list apart from every project's", () => {
    expect(chatKeys.list(null)).toEqual(chatKeys.list(undefined));
    expect(chatKeys.list(null)).not.toEqual(chatKeys.list("p1"));
  });

  it("patches and removes a chat in every list that holds it", () => {
    const chat = buildChat({ id: "c1", projectId: "p1" });
    queryClient.setQueryData(chatKeys.list(null), buildEnvelope([chat]));
    queryClient.setQueryData(chatKeys.list("p1"), buildEnvelope([chat]));

    patchChatCaches(undefined, "c1", { title: "New" });
    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list(null))!.chats[0]!.title).toBe("New");
    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list("p1"))!.chats[0]!.title).toBe("New");

    removeChatFromListCache(undefined, "c1");
    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list(null))!.chats).toEqual([]);
    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list("p1"))!.chats).toEqual([]);
  });

  it("upserts into a loaded all-projects list but never invents one", () => {
    const chat = buildChat({ id: "c2", projectId: "p2" });
    upsertChatInListCache("p2", chat);
    expect(queryClient.getQueryData(chatKeys.list(null))).toBeUndefined();

    queryClient.setQueryData(chatKeys.list(null), buildEnvelope([buildChat({ id: "c1" })]));
    upsertChatInListCache("p2", buildChat({ id: "c3", projectId: "p2" }));
    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list(null))!.chats.map((c) => c.id)).toEqual([
      "c3",
      "c1",
    ]);
  });

  it("reads one project's chats and main workspace from the all-projects list when its own is not loaded", () => {
    queryClient.setQueryData(chatKeys.list(null), {
      ...buildEnvelope([buildChat({ id: "a", projectId: "p1" }), buildChat({ id: "b", projectId: "p2" })]),
      mainWorktrees: [{ id: "main-p2", projectId: "p2", isMain: true }],
    });
    expect(getCachedChatList("p2").map((c) => c.id)).toEqual(["b"]);
    expect(getCachedChatList(null).map((c) => c.id)).toEqual(["a", "b"]);
    expect(getCachedMainWorktree("p2")?.id).toBe("main-p2");
    expect(getCachedMainWorktree("p1")).toBeUndefined();
  });
});
