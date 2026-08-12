import { describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";
import type { Message } from "../../../../api/client";
import { sortMessagesForDisplay } from "../../../../lib/messageOrder";
import { useMessagesByThread } from "../useThreads";

// Thread presentation after the ordinal → seq flip.
//
// Two things used to live in lib/messageOrder.ts and no longer do:
//   1. Cross-thread interleaving, which was reconstructed at read time from
//      (clamped) timestamps because per-thread ordinals carried no chat-wide
//      order. It is now read straight off `seq`, a chat-global total order.
//   2. `threadKeyOf`, which normalized the main thread's several encodings
//      ("", "0", or the chatId itself) to a single key. Ordering no longer
//      consults thread identity at all, so that normalization moved entirely
//      to the grouping layer (useThreads.ts / InterleavedTimeline.tsx).
//
// These tests pin both: the timeline interleaves correctly from seq, each
// thread keeps its own internal order, and the main thread is still identified
// under every encoding it ships in.

const CHAT = "chat-presentation";

function msg(
  id: string,
  seq: number,
  thread: string,
  createdAt = "2026-01-01T00:00:00.000Z",
): Message {
  return {
    id,
    chatId: CHAT,
    role: 2,
    contentBlocks: [],
    createdAt,
    updatedAt: createdAt,
    seq: BigInt(seq),
    thread,
    sequenceNumber: 0n,
    attachments: [],
  } as unknown as Message;
}

describe("timeline interleaving across a main thread and multiple spawns", () => {
  // The rendering-layer twin of the messageOrder proof: a chat with a main
  // thread plus TWO spawn threads, all interleaved. Timestamps are
  // deliberately wrong (every spawn message claims an hour earlier than it
  // happened) so a timestamp-based ordering would visibly scramble the
  // transcript; seq gets it exactly right.
  // Ids are deliberately REVERSE-alphabetical within each thread (main-c has
  // the lowest seq, main-a the highest). compareMessagesWithinThread falls
  // back to id when seqs tie, so ids that happened to sort the same way as
  // seq would let a broken seq comparison still produce the right answer.
  // These ids make that impossible: only real seq ordering passes.
  const messages: Message[] = [
    msg("main-c", 1, "", "2026-01-01T10:00:00.000Z"),
    msg("spawnA-c", 2, "spawn-a", "2026-01-01T09:00:00.000Z"),
    msg("main-b", 3, "", "2026-01-01T10:00:02.000Z"),
    msg("spawnB-b", 4, "spawn-b", "2026-01-01T09:00:01.000Z"),
    msg("spawnA-b", 5, "spawn-a", "2026-01-01T09:00:02.000Z"),
    msg("main-a", 6, "", "2026-01-01T10:00:06.000Z"),
    msg("spawnB-a", 7, "spawn-b", "2026-01-01T09:00:03.000Z"),
    msg("spawnA-a", 8, "spawn-a", "2026-01-01T09:00:04.000Z"),
  ];

  it("renders true chronological interleaving from chat-global seq", () => {
    // Shuffled input — display order must come from seq, not arrival order.
    const shuffled = [
      messages[5],
      messages[0],
      messages[7],
      messages[3],
      messages[1],
      messages[6],
      messages[2],
      messages[4],
    ];

    expect(sortMessagesForDisplay(shuffled, CHAT).map((m) => m.id)).toEqual([
      "main-c",
      "spawnA-c",
      "main-b",
      "spawnB-b",
      "spawnA-b",
      "main-a",
      "spawnB-a",
      "spawnA-a",
    ]);
  });

  it("keeps each thread's own messages in that thread's order", () => {
    const { result } = renderHook(() => useMessagesByThread(messages, CHAT));
    const groups = result.current;

    // Grouping normalizes the main thread's "" encoding onto the chatId.
    expect(groups.get(CHAT)?.map((m) => m.id)).toEqual([
      "main-c",
      "main-b",
      "main-a",
    ]);
    expect(groups.get("spawn-a")?.map((m) => m.id)).toEqual([
      "spawnA-c",
      "spawnA-b",
      "spawnA-a",
    ]);
    expect(groups.get("spawn-b")?.map((m) => m.id)).toEqual([
      "spawnB-b",
      "spawnB-a",
    ]);
  });

  it("keeps each thread's own order even when its messages arrive shuffled", () => {
    const shuffled = [
      messages[7], // spawnA-a (seq 8)
      messages[1], // spawnA-c (seq 2)
      messages[4], // spawnA-b (seq 5)
      messages[5], // main-a   (seq 6)
      messages[0], // main-c   (seq 1)
      messages[2], // main-b   (seq 3)
    ];

    const { result } = renderHook(() => useMessagesByThread(shuffled, CHAT));
    expect(result.current.get("spawn-a")?.map((m) => m.id)).toEqual([
      "spawnA-c",
      "spawnA-b",
      "spawnA-a",
    ]);
    expect(result.current.get(CHAT)?.map((m) => m.id)).toEqual([
      "main-c",
      "main-b",
      "main-a",
    ]);
  });
});

// `threadKeyOf` used to do this normalization inside messageOrder.ts. It was
// removed with the flip; grouping owns it now. These pin that the main thread
// is still recognized under each encoding the server/client can produce, so a
// main-thread message never gets stranded in a phantom thread of its own.
describe("main-thread identification after threadKeyOf removal", () => {
  it("treats an empty thread as the main thread", () => {
    const { result } = renderHook(() =>
      useMessagesByThread([msg("m1", 1, "")], CHAT),
    );
    expect(result.current.get(CHAT)?.map((m) => m.id)).toEqual(["m1"]);
    expect(result.current.has("")).toBe(false);
  });

  it("treats a thread equal to the chatId as the main thread", () => {
    const { result } = renderHook(() =>
      useMessagesByThread([msg("m1", 1, CHAT)], CHAT),
    );
    expect(result.current.get(CHAT)?.map((m) => m.id)).toEqual(["m1"]);
  });

  it("groups '' and chatId encodings into one main thread, in seq order", () => {
    // The same conversation can carry both encodings across a session; they
    // must collapse into one thread rather than render as two.
    // Reverse-alphabetical ids again, so only real seq ordering passes.
    const { result } = renderHook(() =>
      useMessagesByThread(
        [msg("m-b", 2, CHAT), msg("m-c", 1, ""), msg("m-a", 3, CHAT)],
        CHAT,
      ),
    );
    expect(result.current.size).toBe(1);
    expect(result.current.get(CHAT)?.map((m) => m.id)).toEqual([
      "m-c",
      "m-b",
      "m-a",
    ]);
  });

  it("keeps a real spawn thread separate from the main thread", () => {
    const { result } = renderHook(() =>
      useMessagesByThread([msg("m1", 1, ""), msg("s1", 2, "spawn-a")], CHAT),
    );
    expect([...result.current.keys()].sort()).toEqual([CHAT, "spawn-a"]);
  });

  // The literal "0" thread encoding is the one main-thread spelling that
  // grouping does NOT fold into the chatId — `msg.thread || chatId` only
  // catches the empty/undefined case, and this was true before the seq flip
  // too (useThreads.ts is unchanged by it). It is safe only because "0" is no
  // longer reachable for a message: messages.thread_id is NOT NULL with a FK
  // to threads(id) (20260801000000_conversation_integrity_constraints.sql),
  // and no code path ever creates a thread row with id "0" — the "no more
  // '0' default" comments in call_llm.go / compact.go / workflow_status.go
  // mark where that default was removed.
  //
  // This test pins the CURRENT behavior deliberately. If a "0" thread ever
  // becomes reachable again, it fails and says what to do — rather than
  // silently rendering a main-thread message under a phantom "0" thread.
  it("does NOT fold a literal '0' thread into main (unreachable via the thread_id FK)", () => {
    const { result } = renderHook(() =>
      useMessagesByThread([msg("m1", 1, ""), msg("m0", 2, "0")], CHAT),
    );

    expect([...result.current.keys()].sort()).toEqual(["0", CHAT]);
    expect(result.current.get(CHAT)?.map((m) => m.id)).toEqual(["m1"]);

    // If this assertion ever fails because a real message arrived on thread
    // "0", the fix is to normalize "0" in useMessagesByThread/useThreads
    // (as chatStore.normalizeThreadKey and InterleavedTimeline already do),
    // NOT to relax this test.
  });
});
