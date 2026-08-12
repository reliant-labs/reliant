import { describe, expect, it } from "vitest";
import {
  compareMessagesWithinThread,
  sortMessagesForDisplay,
  type OrderableMessage,
} from "../messageOrder";

const CHAT = "chat-1";

function msg(id: string, seq: number, thread = ""): OrderableMessage {
  return { id, seq: BigInt(seq), thread };
}

describe("compareMessagesWithinThread", () => {
  it("orders by seq", () => {
    const a = msg("a", 1);
    const b = msg("b", 2);
    expect(compareMessagesWithinThread(a, b)).toBeLessThan(0);
    expect(compareMessagesWithinThread(b, a)).toBeGreaterThan(0);
  });

  it("breaks seq ties by id", () => {
    const a = msg("a", 0);
    const c = msg("c", 0);
    expect(compareMessagesWithinThread(a, c)).toBeLessThan(0); // "a" < "c"
    expect(compareMessagesWithinThread(c, a)).toBeGreaterThan(0);
  });
});

describe("sortMessagesForDisplay", () => {
  it("uses seq as the canonical order within a thread", () => {
    const out = sortMessagesForDisplay(
      [msg("m2", 2), msg("m3", 3), msg("m1", 1)],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual(["m1", "m2", "m3"]);
  });

  it("treats '', '0', and chatId as the same (main) thread", () => {
    const out = sortMessagesForDisplay(
      [msg("b", 2, "0"), msg("c", 3, CHAT), msg("a", 1, "")],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual(["a", "b", "c"]);
  });

  // THE PHASE-3 PROOF.
  //
  // Two threads whose messages genuinely interleave. Under the old per-thread
  // ordinals this was unrepresentable: main and spawn each counted 1,2,3 from
  // zero, so the client had to GUESS the interleaving from timestamps (and
  // clamp them, because timestamps were not trustworthy either). seq is a
  // chat-global total order assigned at persistence time, so the true
  // interleaving is read straight off the data — exactly, not heuristically.
  it("renders the true cross-thread interleaving from chat-global seq", () => {
    const out = sortMessagesForDisplay(
      [
        msg("spawn-b", 4, "spawn"),
        msg("main-a", 1, ""),
        msg("spawn-c", 6, "spawn"),
        msg("main-b", 3, ""),
        msg("spawn-a", 2, "spawn"),
        msg("main-c", 5, ""),
      ],
      CHAT,
    );
    // Interleaved main/spawn/main/spawn/main/spawn — each thread's ordinals
    // would have been 1,2,3 and 1,2,3, which carries none of this.
    expect(out.map((m) => m.id)).toEqual([
      "main-a",
      "spawn-a",
      "main-b",
      "spawn-b",
      "main-c",
      "spawn-c",
    ]);
  });

  it("keeps a thread's own messages in seq order inside an interleaving", () => {
    const out = sortMessagesForDisplay(
      [
        msg("spawn-1", 10, "spawn"),
        msg("spawn-2", 20, "spawn"),
        msg("main-1", 15, ""),
      ],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual(["spawn-1", "main-1", "spawn-2"]);
  });

  it("keeps client-side placeholders (seq 999998/999999) at the end", () => {
    const out = sortMessagesForDisplay(
      [
        { id: "streaming-temp", seq: BigInt(999999), thread: "" },
        msg("m1", 1),
        { id: "optimistic-user-1", seq: BigInt(999998), thread: "" },
        msg("m2", 2),
        msg("spawn-1", 3, "spawn"),
      ],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual([
      "m1",
      "m2",
      "spawn-1",
      "optimistic-user-1",
      "streaming-temp",
    ]);
  });

  it("is deterministic for identical seqs", () => {
    const a = msg("a", 0);
    const b = msg("b", 0);
    expect(sortMessagesForDisplay([b, a], CHAT).map((m) => m.id)).toEqual([
      "a",
      "b",
    ]);
    expect(sortMessagesForDisplay([a, b], CHAT).map((m) => m.id)).toEqual([
      "a",
      "b",
    ]);
  });

  // A live message that reaches the client without a seq must not be hoisted to
  // the top of the conversation. It is the NEWEST thing in the chat, and
  // sorting it first both misplaces it and breaks the interleaved timeline's
  // pinned user-message header, which walks the sorted list and treats each
  // user message as the head of the section beneath it — a user message sitting
  // above the whole history makes every later message appear to belong to it.
  it("orders a message with no seq last, not first", () => {
    const withoutSeq = { id: "no-seq" } as Parameters<
      typeof sortMessagesForDisplay
    >[0][number];

    const sorted = sortMessagesForDisplay(
      [msg("m1", 10), withoutSeq, msg("m2", 11)],
      CHAT,
    ).map((m) => m.id);

    expect(sorted[0]).not.toBe("no-seq");
    expect(sorted).toEqual(["m1", "m2", "no-seq"]);
  });

  // A genuine optimistic echo still sorts after one that merely lost its seq,
  // so the placeholder stays at the bottom where the composer expects it.
  it("keeps placeholder sentinels below a seq-less message", () => {
    const withoutSeq = { id: "no-seq" } as Parameters<
      typeof sortMessagesForDisplay
    >[0][number];

    const sorted = sortMessagesForDisplay(
      [msg("optimistic-user-1", 999998), withoutSeq, msg("m1", 10)],
      CHAT,
    ).map((m) => m.id);

    expect(sorted).toEqual(["m1", "no-seq", "optimistic-user-1"]);
  });

  // seq 0 is a real value for the first message in a chat, and that message
  // genuinely sorts first — the missing-seq handling must not disturb it.
  it("still orders a real seq-0 first message first", () => {
    expect(
      sortMessagesForDisplay([msg("second", 1), msg("first", 0)], CHAT).map(
        (m) => m.id,
      ),
    ).toEqual(["first", "second"]);
  });
});
