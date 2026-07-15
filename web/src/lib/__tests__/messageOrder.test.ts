import { describe, expect, it } from "vitest";
import {
  compareMessagesWithinThread,
  sortMessagesForDisplay,
  type OrderableMessage,
} from "../messageOrder";

const CHAT = "chat-1";

function msg(
  id: string,
  ordinal: number,
  createdAt: string,
  thread = "",
): OrderableMessage {
  return { id, ordinal: BigInt(ordinal), createdAt, thread };
}

describe("compareMessagesWithinThread", () => {
  it("orders by ordinal before createdAt", () => {
    const a = msg("a", 1, "2026-01-01T10:00:00.000Z");
    const b = msg("b", 2, "2026-01-01T09:00:00.000Z"); // earlier clock, later ordinal
    expect(compareMessagesWithinThread(a, b)).toBeLessThan(0);
    expect(compareMessagesWithinThread(b, a)).toBeGreaterThan(0);
  });

  it("breaks ordinal ties by createdAt then id", () => {
    const a = msg("a", 0, "2026-01-01T10:00:00.000Z");
    const b = msg("b", 0, "2026-01-01T10:00:01.000Z");
    expect(compareMessagesWithinThread(a, b)).toBeLessThan(0);

    const c = msg("c", 0, "2026-01-01T10:00:00.000Z");
    expect(compareMessagesWithinThread(a, c)).toBeLessThan(0); // "a" < "c"
  });
});

describe("sortMessagesForDisplay", () => {
  it("uses ordinal as the canonical order within a thread (repair-message case)", () => {
    // m3 was persisted with a bogus earlier timestamp (local-vs-UTC bug).
    const out = sortMessagesForDisplay(
      [
        msg("m2", 2, "2026-01-01T10:00:20.000Z"),
        msg("m3", 3, "2026-01-01T09:00:00.000Z"),
        msg("m1", 1, "2026-01-01T10:00:00.000Z"),
      ],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual(["m1", "m2", "m3"]);
  });

  it("treats '', '0', and chatId as the same (main) thread", () => {
    const out = sortMessagesForDisplay(
      [
        msg("b", 2, "2026-01-01T10:00:00.000Z", "0"),
        msg("c", 3, "2026-01-01T10:00:00.000Z", CHAT),
        msg("a", 1, "2026-01-01T10:00:00.000Z", ""),
      ],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual(["a", "b", "c"]);
  });

  it("interleaves threads by time while keeping each thread in ordinal order", () => {
    const out = sortMessagesForDisplay(
      [
        msg("main-1", 1, "2026-01-01T10:00:00.000Z"),
        msg("spawn-1", 1, "2026-01-01T10:00:05.000Z", "spawn"),
        msg("main-2", 2, "2026-01-01T10:00:10.000Z"),
        msg("spawn-2", 2, "2026-01-01T10:00:15.000Z", "spawn"),
      ],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual([
      "main-1",
      "spawn-1",
      "main-2",
      "spawn-2",
    ]);
  });

  it("a bad timestamp cannot reorder a thread against itself when interleaving", () => {
    // spawn-2 has an earlier clock than spawn-1; the clamp keeps it after
    // spawn-1 in the merged output.
    const out = sortMessagesForDisplay(
      [
        msg("spawn-1", 1, "2026-01-01T10:00:10.000Z", "spawn"),
        msg("spawn-2", 2, "2026-01-01T09:00:00.000Z", "spawn"),
        msg("main-1", 1, "2026-01-01T10:00:05.000Z"),
      ],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual(["main-1", "spawn-1", "spawn-2"]);
  });

  it("keeps client-side placeholders (ordinal 999998/999999) at the end of their thread", () => {
    const out = sortMessagesForDisplay(
      [
        { id: "streaming-temp", ordinal: BigInt(999999), createdAt: "2026-01-01T10:00:02.000Z", thread: "" },
        msg("m1", 1, "2026-01-01T10:00:00.000Z"),
        { id: "optimistic-user-1", ordinal: BigInt(999998), createdAt: "2026-01-01T10:00:01.000Z", thread: "" },
        msg("m2", 2, "2026-01-01T10:00:00.500Z"),
      ],
      CHAT,
    );
    expect(out.map((m) => m.id)).toEqual([
      "m1",
      "m2",
      "optimistic-user-1",
      "streaming-temp",
    ]);
  });

  it("is deterministic for identical timestamps and ordinals", () => {
    const a = msg("a", 0, "2026-01-01T10:00:00.000Z");
    const b = msg("b", 0, "2026-01-01T10:00:00.000Z");
    expect(sortMessagesForDisplay([b, a], CHAT).map((m) => m.id)).toEqual([
      "a",
      "b",
    ]);
    expect(sortMessagesForDisplay([a, b], CHAT).map((m) => m.id)).toEqual([
      "a",
      "b",
    ]);
  });
});
