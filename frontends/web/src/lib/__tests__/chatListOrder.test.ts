/**
 * "Stop the sidebar reshuffling itself."
 *
 * The complaint these cover is that the chat list reorders while you are
 * looking at it, even with an explicit sort selected. Two separate causes:
 * an approval float that ran in every sort mode, and ties that fell through
 * to the server's `updated_at DESC` array order, which changes on every
 * message. Both are ordering bugs, so they are pinned here on the comparator
 * rather than through a rendered sidebar.
 */

import { describe, expect, it } from "vitest";
import { sortChats, type ChatOrderFields } from "../chatListOrder";

const NEVER_NEEDS_ATTENTION = () => false;

function makeChat(
  id: string,
  overrides: Partial<ChatOrderFields> = {}
): ChatOrderFields {
  return {
    id,
    title: id,
    createdAt: "2026-01-01T00:00:00Z",
    lastMessageAt: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("newest_first", () => {
  it("orders by created_at descending", () => {
    const chats = [
      makeChat("middle", { createdAt: "2026-01-02T00:00:00Z" }),
      makeChat("oldest", { createdAt: "2026-01-01T00:00:00Z" }),
      makeChat("newest", { createdAt: "2026-01-03T00:00:00Z" }),
    ];

    expect(sortChats(chats, "newest_first", NEVER_NEEDS_ATTENTION).map((c) => c.id)).toEqual([
      "newest",
      "middle",
      "oldest",
    ]);
  });

  it("does not float a chat awaiting approval to the top", () => {
    // The chat that starts asking for approval is the OLDEST one, so any
    // movement is the approval float rather than a legitimate re-sort.
    const chats = [
      makeChat("newest", { createdAt: "2026-01-03T00:00:00Z" }),
      makeChat("middle", { createdAt: "2026-01-02T00:00:00Z" }),
      makeChat("waiting", { createdAt: "2026-01-01T00:00:00Z" }),
    ];

    const before = sortChats(chats, "newest_first", NEVER_NEEDS_ATTENTION);
    const after = sortChats(chats, "newest_first", (chat) => chat.id === "waiting");

    expect(after.map((c) => c.id)).toEqual(before.map((c) => c.id));
    expect(after.map((c) => c.id)).toEqual(["newest", "middle", "waiting"]);
  });

  it("holds its order when a message arrives in an older chat", () => {
    // The list arrives from the server ordered by `updated_at DESC`, so a new
    // message moves that chat to the front of the INPUT array. Under
    // newest_first that must not change what renders.
    const chats = [
      makeChat("newest", { createdAt: "2026-01-03T00:00:00Z" }),
      makeChat("middle", { createdAt: "2026-01-02T00:00:00Z" }),
      makeChat("oldest", { createdAt: "2026-01-01T00:00:00Z" }),
    ];
    const afterServerReorder = [chats[2], chats[0], chats[1]];

    expect(
      sortChats(afterServerReorder, "newest_first", NEVER_NEEDS_ATTENTION).map((c) => c.id)
    ).toEqual(["newest", "middle", "oldest"]);
  });

  it("keeps chats created in the same second in a fixed order", () => {
    // Chats created by one action can share a timestamp to the millisecond.
    // Comparing only on time leaves them tied, and a stable sort then renders
    // them in whatever order the server last returned.
    const sameInstant = "2026-01-01T00:00:00Z";
    const chats = [
      makeChat("chat-a", { createdAt: sameInstant }),
      makeChat("chat-b", { createdAt: sameInstant }),
      makeChat("chat-c", { createdAt: sameInstant }),
    ];

    const order = sortChats(chats, "newest_first", NEVER_NEEDS_ATTENTION).map((c) => c.id);
    const reversedInput = sortChats([...chats].reverse(), "newest_first", NEVER_NEEDS_ATTENTION);

    expect(reversedInput.map((c) => c.id)).toEqual(order);
  });
});

describe("needs_attention_first", () => {
  it("still floats chats blocked on the user", () => {
    const chats = [
      makeChat("newest", { createdAt: "2026-01-03T00:00:00Z" }),
      makeChat("waiting", { createdAt: "2026-01-01T00:00:00Z" }),
    ];

    expect(
      sortChats(chats, "needs_attention_first", (chat) => chat.id === "waiting").map((c) => c.id)
    ).toEqual(["waiting", "newest"]);
  });
});

describe("recent_activity", () => {
  it("orders by last_message_at, falling back to created_at", () => {
    const chats = [
      makeChat("quiet", {
        createdAt: "2026-01-03T00:00:00Z",
        lastMessageAt: "2026-01-03T00:00:00Z",
      }),
      makeChat("chatty", {
        createdAt: "2026-01-01T00:00:00Z",
        lastMessageAt: "2026-01-04T00:00:00Z",
      }),
      makeChat("never-messaged", {
        createdAt: "2026-01-02T00:00:00Z",
        lastMessageAt: undefined,
      }),
    ];

    expect(sortChats(chats, "recent_activity", NEVER_NEEDS_ATTENTION).map((c) => c.id)).toEqual([
      "chatty",
      "quiet",
      "never-messaged",
    ]);
  });
});

describe("alphabetical", () => {
  it("sorts by title, treating an untitled chat as \"New chat\"", () => {
    const chats = [
      makeChat("z", { title: "Zebra" }),
      makeChat("untitled", { title: undefined }),
      makeChat("a", { title: "Apple" }),
    ];

    expect(sortChats(chats, "alphabetical_asc", NEVER_NEEDS_ATTENTION).map((c) => c.id)).toEqual([
      "a",
      "untitled",
      "z",
    ]);
  });
});
