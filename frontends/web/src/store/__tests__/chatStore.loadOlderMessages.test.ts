import { describe, expect, it, beforeEach, vi } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../api/client";
import { useChatStore } from "../chatStore";
import {
  clearAllMessagesCache,
  getMessagesFromCache,
  getMessagesMetaFromCache,
  setMessagesInCache,
} from "../../hooks/message-queries";
import { api } from "../../api/client";

// Scroll-back paging. The initial chat snapshot is BOUNDED to the newest N
// messages, so loadOlderMessages is what makes the rest of a long chat
// reachable. The behavior that most needs pinning is TERMINATION: the server's
// has_more is `len(messages) < totalCount || beforeSeq > 0`, whose second
// disjunct is permanently true for every paged request — so a loop that
// trusted it would never stop. Termination must come from the empty/short page.

const CHAT = "c-older";

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>(
    "../../api/client",
  );
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: {
        ...actual.api.chatsV2,
        listMessages: vi.fn(),
      },
    },
  };
});

const listMessages = vi.mocked(api.chatsV2.listMessages);

function msg(id: string, seq: number, overrides: Partial<Message> = {}) {
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
    ...overrides,
  } as unknown as Message;
}

function toolMessage(id: string, seq: number, toolCallId: string) {
  return msg(id, seq, {
    role: MessageRole.TOOL,
    contentBlocks: [
      {
        id: `${id}-b0`,
        type: ContentBlockType.TOOL_RESULT,
        index: 0,
        content: `result-for-${toolCallId}`,
        toolCallId,
        toolName: "bash",
      },
    ],
  } as Partial<Message>);
}

/** A page response shaped like the real RPC, including its quirky has_more. */
function page(messages: Message[], total: number) {
  return {
    messages,
    total,
    // ALWAYS true for a paged request — this is the server quirk under test.
    hasMore: true,
    oldestSeq: messages.length > 0 ? Number(messages[0].seq) : 0,
  };
}

beforeEach(() => {
  clearAllMessagesCache();
  listMessages.mockReset();
  useChatStore.setState({
    toolResultsByCallId: {},
    streamingMessages: {},
  } as never);
});

describe("loadOlderMessages", () => {
  it("prepends a fetched page and moves the cursor backward", async () => {
    setMessagesInCache(CHAT, [msg("m300", 300)], {
      total: 400,
      hasMore: true,
      oldestSeq: 300,
    });
    listMessages.mockResolvedValue(page([msg("m200", 200), msg("m299", 299)], 400));

    await useChatStore.getState().loadOlderMessages(CHAT);

    expect(listMessages).toHaveBeenCalledWith(CHAT, {
      recent: 100,
      beforeSeq: 300,
    });
    expect(getMessagesFromCache(CHAT).map((m) => m.id)).toEqual([
      "m200",
      "m299",
      "m300",
    ]);
    expect(getMessagesMetaFromCache(CHAT)?.oldestSeq).toBe(200);
  });

  it("terminates on an EMPTY page even though the server says has_more", async () => {
    setMessagesInCache(CHAT, [msg("m1", 1)], {
      total: 1,
      hasMore: true,
      oldestSeq: 1,
    });
    listMessages.mockResolvedValue(page([], 1));

    const more = await useChatStore.getState().loadOlderMessages(CHAT);

    expect(more).toBe(false);
    // hasMore must be latched off, or startReached would re-fire forever.
    expect(getMessagesMetaFromCache(CHAT)?.hasMore).toBe(false);
  });

  it("terminates on a SHORT page even though the server says has_more", async () => {
    setMessagesInCache(CHAT, [msg("m100", 100)], {
      total: 102,
      hasMore: true,
      oldestSeq: 100,
    });
    // Fewer than PAGE_SIZE (100) → we just consumed the remainder.
    listMessages.mockResolvedValue(page([msg("m1", 1), msg("m2", 2)], 102));

    const more = await useChatStore.getState().loadOlderMessages(CHAT);

    expect(more).toBe(false);
    expect(getMessagesMetaFromCache(CHAT)?.hasMore).toBe(false);
  });

  it("does not fetch when hasMore is already false", async () => {
    setMessagesInCache(CHAT, [msg("m1", 1)], {
      total: 1,
      hasMore: false,
      oldestSeq: 1,
    });

    expect(await useChatStore.getState().loadOlderMessages(CHAT)).toBe(false);
    expect(listMessages).not.toHaveBeenCalled();
  });

  it("does not fetch for a chat with no cache entry", async () => {
    expect(await useChatStore.getState().loadOlderMessages(CHAT)).toBe(false);
    expect(listMessages).not.toHaveBeenCalled();
  });

  it("does not fetch when the cursor is unknown (oldestSeq 0)", async () => {
    setMessagesInCache(CHAT, [msg("m1", 0)], {
      total: 50,
      hasMore: true,
      oldestSeq: 0,
    });

    expect(await useChatStore.getState().loadOlderMessages(CHAT)).toBe(false);
    expect(listMessages).not.toHaveBeenCalled();
  });

  it("coalesces concurrent invocations into a single fetch", async () => {
    setMessagesInCache(CHAT, [msg("m300", 300)], {
      total: 400,
      hasMore: true,
      oldestSeq: 300,
    });
    listMessages.mockResolvedValue(page([msg("m200", 200)], 400));

    const store = useChatStore.getState();
    await Promise.all([
      store.loadOlderMessages(CHAT),
      store.loadOlderMessages(CHAT),
      store.loadOlderMessages(CHAT),
    ]);

    expect(listMessages).toHaveBeenCalledTimes(1);
  });

  it("releases the in-flight guard after a failure so paging can retry", async () => {
    setMessagesInCache(CHAT, [msg("m300", 300)], {
      total: 400,
      hasMore: true,
      oldestSeq: 300,
    });
    listMessages.mockRejectedValueOnce(new Error("network"));

    // A transient failure is not end-of-history: keep hasMore so the next
    // startReached retries.
    expect(await useChatStore.getState().loadOlderMessages(CHAT)).toBe(true);
    expect(getMessagesMetaFromCache(CHAT)?.hasMore).toBe(true);

    listMessages.mockResolvedValueOnce(page([msg("m200", 200)], 400));
    await useChatStore.getState().loadOlderMessages(CHAT);
    expect(getMessagesFromCache(CHAT).map((m) => m.id)).toEqual(["m200", "m300"]);
  });

  it("MERGES the page's tool results instead of clearing the index", async () => {
    // Newer messages' results are already indexed; a prepend must not wipe them
    // or their assistant messages render tool calls with no results.
    setMessagesInCache(CHAT, [msg("m300", 300)], {
      total: 400,
      hasMore: true,
      oldestSeq: 300,
    });
    useChatStore.setState({
      toolResultsByCallId: {
        [CHAT]: {
          "call-new": { content: "newer", is_error: false, tool_name: "bash" },
        },
      },
    } as never);

    listMessages.mockResolvedValue(
      page([toolMessage("t200", 200, "call-old")], 400),
    );

    await useChatStore.getState().loadOlderMessages(CHAT);

    const index = useChatStore.getState().toolResultsByCallId[CHAT];
    expect(index["call-new"]?.content).toBe("newer");
    expect(index["call-old"]?.content).toBe("result-for-call-old");
  });
});
