import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import {
  getMessagesFromCache,
  clearAllMessagesCache,
} from "../../hooks/message-queries";

// Streaming content deltas are coalesced onto ONE animation frame.
//
// Every commit to the streamingMessages slice re-renders the whole timeline
// and makes Virtuoso re-measure, so the commit RATE is what the user perceives
// as scroll jitter. This suite pins the rate: no matter how many content
// deltas arrive between frames, the thread commits once, with all of the text,
// in order.
//
// It replaced a newline-triggered synchronous flush, which tied the repaint
// rate to the shape of the prose — a markdown list committed on every line
// while an unbroken paragraph waited out the full fallback delay. Several
// assertions below fail against that behavior by construction: they count
// commits for deltas that all end in "\n".

// A hand-driven requestAnimationFrame. The real one is unusable here: the
// point of every test is WHEN a commit happens, and jsdom's rAF fires on its
// own schedule.
const pendingFrames = new Map<number, () => void>();
let nextFrameId = 1;

function installManualRaf() {
  pendingFrames.clear();
  nextFrameId = 1;
  vi.stubGlobal("requestAnimationFrame", (cb: () => void) => {
    const id = nextFrameId++;
    pendingFrames.set(id, cb);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    pendingFrames.delete(id);
  });
}

/** Run every frame callback armed so far, as the browser would. */
function runFrame() {
  const callbacks = [...pendingFrames.values()];
  pendingFrames.clear();
  for (const cb of callbacks) cb();
}

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
}

function contentStart(
  messageId: string,
  thread = "",
  blockIndex = 0,
): ChatUpdate {
  return {
    update_type: "streaming_delta",
    delta_type: "content_block_start",
    block_index: blockIndex,
    message_id: messageId,
    thread,
  } as unknown as ChatUpdate;
}

function contentDelta(
  messageId: string,
  text: string,
  thread = "",
  blockIndex = 0,
): ChatUpdate {
  return {
    update_type: "streaming_delta",
    delta_type: "content_block_delta",
    block_index: blockIndex,
    delta: text,
    message_id: messageId,
    thread,
  } as unknown as ChatUpdate;
}

function streamFinalized(messageId: string, thread = ""): ChatUpdate {
  return {
    update_type: "stream_finalized",
    message_id: messageId,
    reason: "aborted",
    thread,
  } as unknown as ChatUpdate;
}

function completeAssistantMessage(
  chatId: string,
  id: string,
  thread = "",
): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId,
      role: MessageRole.ASSISTANT,
      contentBlocks: [
        {
          id: `${id}-b0`,
          type: ContentBlockType.TEXT,
          index: 0,
          content: "the whole persisted answer",
        },
      ],
      createdAt: "2026-01-01T00:00:10.000Z",
      updatedAt: "2026-01-01T00:00:10.000Z",
      streamingState: StreamingState.COMPLETE,
      seq: BigInt(2),
      thread,
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

/**
 * Count commits to a chat's streamingMessages slice. This is the quantity the
 * workstream is about: one commit is one timeline render plus one round of
 * Virtuoso re-measures.
 */
function countStreamingCommits(chatId: string) {
  let commits = 0;
  let previous = useChatStore.getState().streamingMessages[chatId];
  const unsubscribe = useChatStore.subscribe((state) => {
    const next = state.streamingMessages[chatId];
    if (next !== previous) {
      previous = next;
      commits += 1;
    }
  });
  return {
    get count() {
      return commits;
    },
    reset() {
      commits = 0;
    },
    unsubscribe,
  };
}

function placeholders(chatId: string) {
  return Object.values(
    useChatStore.getState().streamingMessages[chatId] || {},
  ).filter((m): m is NonNullable<typeof m> => !!m);
}

function textOf(chatId: string, messageId: string): string {
  const message = placeholders(chatId).find((m) => m.id === messageId);
  return (message?.contentBlocks || []).map((b) => b.content || "").join("");
}

describe("streaming delta coalescing", () => {
  beforeEach(() => {
    seedChat();
    installManualRaf();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    pendingFrames.clear();
  });

  it("commits once per frame no matter how many content deltas arrive", () => {
    const chatId = "chat-coalesce-one-commit";
    const store = useChatStore.getState();

    // The block start is a non-content delta and commits synchronously; the
    // counter starts after it so only content deltas are measured.
    store.processChatStreamUpdates(chatId, [contentStart("assistant-1")]);
    const commits = countStreamingCommits(chatId);

    // Every one of these ends in a newline — under the old newline-flush this
    // was six separate commits.
    const lines = [
      "- first\n",
      "- second\n",
      "- third\n",
      "```go\n",
      "func main() {}\n",
      "```\n",
    ];
    for (const line of lines) {
      store.processChatStreamUpdates(chatId, [
        contentDelta("assistant-1", line),
      ]);
    }

    expect(
      commits.count,
      "nothing may commit before the frame runs",
    ).toBe(0);

    runFrame();

    expect(commits.count).toBe(1);
    expect(textOf(chatId, "assistant-1")).toBe(lines.join(""));

    commits.unsubscribe();
  });

  it("preserves delta order within a frame, including sub-token fragments", () => {
    const chatId = "chat-coalesce-order";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [contentStart("assistant-order")]);

    // Deltas split mid-word and mid-line: reordering or de-duplicating any of
    // them corrupts the message rather than merely delaying it.
    const fragments = ["Th", "e qu", "ick\nbr", "own", " fox\n"];
    for (const fragment of fragments) {
      store.processChatStreamUpdates(chatId, [
        contentDelta("assistant-order", fragment),
      ]);
    }
    runFrame();

    expect(textOf(chatId, "assistant-order")).toBe("The quick\nbrown fox\n");
  });

  it("lands every batch when deltas span several frames", () => {
    const chatId = "chat-coalesce-multi-frame";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [contentStart("assistant-multi")]);
    const commits = countStreamingCommits(chatId);

    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-multi", "frame one\n"),
      contentDelta("assistant-multi", "still one\n"),
    ]);
    runFrame();

    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-multi", "frame two\n"),
      contentDelta("assistant-multi", "still two\n"),
    ]);
    runFrame();

    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-multi", "frame three\n"),
    ]);
    runFrame();

    // Coalescing must not swallow anything: three frames, three commits, and
    // no batch lost to the frame that superseded it.
    expect(commits.count).toBe(3);
    expect(textOf(chatId, "assistant-multi")).toBe(
      "frame one\nstill one\nframe two\nstill two\nframe three\n",
    );

    commits.unsubscribe();
  });

  it("keeps each thread's buffer and commit independent", () => {
    const chatId = "chat-coalesce-threads";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [
      contentStart("assistant-main", ""),
      contentStart("assistant-spawn", "spawn"),
    ]);

    // Interleaved so a shared buffer would splice the two transcripts together.
    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-main", "main one\n", ""),
      contentDelta("assistant-spawn", "spawn one\n", "spawn"),
      contentDelta("assistant-main", "main two\n", ""),
      contentDelta("assistant-spawn", "spawn two\n", "spawn"),
    ]);
    runFrame();

    expect(textOf(chatId, "assistant-main")).toBe("main one\nmain two\n");
    expect(textOf(chatId, "assistant-spawn")).toBe("spawn one\nspawn two\n");
  });

  it("re-applies the finalized-id drop rule at flush time", () => {
    const chatId = "chat-coalesce-finalized";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [contentStart("assistant-fin")]);
    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-fin", "text that will never render\n"),
    ]);

    // Finalization lands in the window between buffering and the frame.
    store.processChatStreamUpdates(chatId, [streamFinalized("assistant-fin")]);
    expect(
      placeholders(chatId),
      "finalization retires the placeholder immediately",
    ).toHaveLength(0);

    // The already-armed frame must not resurrect it.
    runFrame();
    expect(placeholders(chatId)).toHaveLength(0);
  });

  it("drops a pending frame when a complete message ends the stream", () => {
    const chatId = "chat-coalesce-complete";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [contentStart("assistant-done")]);
    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-done", "partial ta"),
    ]);

    // The persisted message supersedes the buffered tail — it already contains
    // that text — and clearing the buffer is what stops the stale flush.
    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "assistant-done"),
    ]);

    runFrame();

    expect(placeholders(chatId)).toHaveLength(0);

    // Nothing is lost by dropping the buffered tail: the persisted message is
    // the authoritative copy and already contains the text the tail carried.
    const persisted = getMessagesFromCache(chatId);
    expect(persisted.map((m) => m.id)).toEqual(["assistant-done"]);
    expect(
      (persisted[0].contentBlocks || []).map((b) => b.content || "").join(""),
    ).toBe("the whole persisted answer");
  });

  it("commits a trailing partial buffer when the stream goes quiet", () => {
    const chatId = "chat-coalesce-trailing";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [contentStart("assistant-trail")]);
    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-trail", "the last "),
      contentDelta("assistant-trail", "few tokens"),
    ]);

    // No newline and no further deltas — under the old newline-flush these sat
    // in the buffer for the full timeout. They must reach the UI on the next
    // frame like any other batch.
    runFrame();

    expect(textOf(chatId, "assistant-trail")).toBe("the last few tokens");
  });

  it("cancels a pending frame when streaming state is torn down", () => {
    const chatId = "chat-coalesce-teardown";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [contentStart("assistant-teardown")]);
    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-teardown", "in flight\n"),
    ]);
    expect(pendingFrames.size).toBe(1);

    // Chat switch / cancel / idle all route through clearStreamingState.
    useChatStore.getState().clearStreamingState(chatId);

    expect(
      pendingFrames.size,
      "the armed frame must be cancelled, not merely ignored",
    ).toBe(0);

    runFrame();
    expect(useChatStore.getState().streamingMessages[chatId]).toBeUndefined();
  });

  it("drains buffered content ahead of a non-content delta so order holds", () => {
    const chatId = "chat-coalesce-drain";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [contentStart("assistant-drain")]);
    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-drain", "before the tool\n"),
    ]);

    // A tool_use_start is handled immediately; the text that preceded it must
    // go with it, or the tool card would render above its own preamble.
    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "tool_use_start",
        block_index: 1,
        tool_call: { id: "tool-1", name: "bash" },
        message_id: "assistant-drain",
        thread: "",
      } as unknown as ChatUpdate,
    ]);

    const blocks = placeholders(chatId)[0]?.contentBlocks || [];
    expect(blocks[0]?.content).toBe("before the tool\n");
    expect(blocks[1]?.type).toBe(ContentBlockType.TOOL_CALL);
    // The drain consumed the buffer, so the frame it armed is disarmed and no
    // second commit can replay the same text.
    expect(pendingFrames.size).toBe(0);
  });
});

// Without requestAnimationFrame there is no frame to coalesce onto, so the
// watchdog timer becomes the only scheduler. A backgrounded tab is the same
// situation with a frame that may never be reached, and a stream that stalls
// invisibly there is worse than one that repaints a little late.
describe("streaming delta coalescing without requestAnimationFrame", () => {
  beforeEach(() => {
    seedChat();
    vi.useFakeTimers();
    vi.stubGlobal("requestAnimationFrame", undefined);
    vi.stubGlobal("cancelAnimationFrame", undefined);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("still commits via the fallback timer", () => {
    const chatId = "chat-coalesce-no-raf";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [contentStart("assistant-norat")]);
    store.processChatStreamUpdates(chatId, [
      contentDelta("assistant-norat", "one\n"),
      contentDelta("assistant-norat", "two\n"),
    ]);

    expect(textOf(chatId, "assistant-norat")).toBe("");

    vi.runAllTimers();

    expect(textOf(chatId, "assistant-norat")).toBe("one\ntwo\n");
  });
});
