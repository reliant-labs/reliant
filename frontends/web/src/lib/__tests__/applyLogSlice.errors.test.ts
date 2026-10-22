/**
 * Store-level dedup of error events (applyErrorUpdates → applyLogSlice, keyed
 * by e.id).
 *
 * This is load-bearing for the stacked-banner fix: one activity's retry series
 * shares a single id, so attempts 1..N must update ONE row IN PLACE and let
 * the badge advance, instead of stacking N rows for one failure. If this
 * regressed, no amount of presentation-layer grouping would save the timeline.
 */
import { describe, expect, it } from "vitest";
import { applyLogSlice } from "../chatStreamReducers";
import type { ErrorUpdate } from "../../types/streaming";

const CHAT_ID = "d8165a13-0850-4bde-87b4-9378bec343f2";
const MAIN_THREAD = CHAT_ID;
const SPAWN_THREAD = "dd777fa2-9780-54bb-ae67-0735945483cf";

function makeError(overrides: Partial<ErrorUpdate> & { id: string }): ErrorUpdate {
  return {
    update_type: "error",
    chat_id: CHAT_ID,
    activity_type: "CallLLM",
    activity_id: "11133",
    error_message: "activity error: Claude session expired",
    error_summary: "Claude session expired",
    timestamp: "2026-02-11T17:24:40.000Z",
    sequence_number: 1,
    ...overrides,
  };
}

// Mirrors chatStore's applyErrorUpdates exactly.
const applyErrorUpdates = (
  existing: ErrorUpdate[],
  updates: ErrorUpdate[],
  isSnapshot: boolean,
): ErrorUpdate[] => applyLogSlice(existing, updates, isSnapshot, { key: (e) => e.id });

describe("applyErrorUpdates (applyLogSlice keyed by id)", () => {
  it("replaces a later attempt in place rather than stacking a second row", () => {
    const attempt1 = makeError({
      id: `activity-error-${CHAT_ID}-11133`,
      thread: MAIN_THREAD,
      attempt_number: 1,
      is_retrying: true,
    });
    const attempt4 = { ...attempt1, attempt_number: 4, timestamp: "2026-02-11T17:24:55.000Z" };

    const afterFirst = applyErrorUpdates([], [attempt1], false);
    const afterFourth = applyErrorUpdates(afterFirst, [attempt4], false);

    expect(afterFourth).toHaveLength(1);
    expect(afterFourth[0].attempt_number).toBe(4);
    expect(afterFourth[0].timestamp).toBe("2026-02-11T17:24:55.000Z");
  });

  it("keeps a replaced entry at its original index when a newer timestamp arrives", () => {
    const first = makeError({ id: "first", timestamp: "2026-02-11T17:24:00.000Z" });
    const second = makeError({ id: "second", timestamp: "2026-02-11T17:24:10.000Z" });
    const firstRetried = { ...first, attempt_number: 5, timestamp: "2026-02-11T17:25:00.000Z" };

    const next = applyErrorUpdates([first, second], [firstRetried], false);

    expect(next.map((e) => e.id)).toEqual(["first", "second"]);
    expect(next[0].attempt_number).toBe(5);
  });

  it("keeps concurrent failures on different threads as distinct rows", () => {
    // These are genuinely different events; the store must not merge them.
    const main = makeError({
      id: `activity-error-${CHAT_ID}-11133`,
      activity_id: "11133",
      thread: MAIN_THREAD,
      attempt_number: 4,
    });
    const spawn = makeError({
      id: `activity-error-${CHAT_ID}-11114`,
      activity_id: "11114",
      thread: SPAWN_THREAD,
      attempt_number: 5,
    });

    const next = applyErrorUpdates([], [main, spawn], false);

    expect(next).toHaveLength(2);
    expect(next.map((e) => e.thread)).toEqual([MAIN_THREAD, SPAWN_THREAD]);
  });

  it("does not resurrect superseded rows on the snapshot path", () => {
    // A snapshot REPLACES the list, so a row the live path already folded into
    // its successor cannot come back as a second entry.
    const attempt1 = makeError({ id: "retry-series", attempt_number: 1 });
    const attempt4 = { ...attempt1, attempt_number: 4 };
    const live = applyErrorUpdates([attempt1], [attempt4], false);

    const afterSnapshot = applyErrorUpdates(live, [attempt4], true);

    expect(afterSnapshot).toHaveLength(1);
    expect(afterSnapshot[0].attempt_number).toBe(4);
  });

  it("collapses a repeated id delivered twice within one snapshot batch", () => {
    const attempt1 = makeError({ id: "retry-series", attempt_number: 1 });
    const attempt4 = { ...attempt1, attempt_number: 4 };

    const next = applyErrorUpdates([], [attempt1, attempt4], true);

    expect(next).toHaveLength(1);
    expect(next[0].attempt_number).toBe(4);
  });

  it("leaves the existing array untouched", () => {
    const existing = [makeError({ id: "a", attempt_number: 1 })];
    const before = JSON.parse(JSON.stringify(existing));

    applyErrorUpdates(existing, [makeError({ id: "a", attempt_number: 2 })], false);

    expect(existing).toEqual(before);
  });
});
