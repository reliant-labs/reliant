/**
 * Presentation-layer collapse of concurrent identical failures.
 *
 * Background (verified against a real database): one chat produced three
 * near-identical "Claude session expired" banners within 15 seconds — one on
 * the main thread (activity 11133, attempt 4) and one on a spawn thread
 * (activity 11114, attempt 5). Another chat produced FIVE, one per thread.
 *
 * Those are genuinely DIFFERENT events on DIFFERENT threads; they are not
 * duplicates in the data model and must not be merged in the store or the DB.
 * But when the user is looking at several threads at once, N identical
 * concurrent failures should read as ONE line carrying a count.
 *
 * Everything below tests the derived render state only — `errorEvents` is
 * never mutated.
 */
import { describe, expect, it } from "vitest";
import type { ErrorUpdate } from "../../../../types/streaming";
import {
  CONCURRENT_ERROR_WINDOW_MS,
  createThreadVisibilityCheck,
  groupVisibleErrors,
} from "../InterleavedTimeline";

const CHAT_ID = "d8165a13-0850-4bde-87b4-9378bec343f2";
// The main thread's id EQUALS the chat id; spawns carry distinct uuids.
const MAIN_THREAD = CHAT_ID;
const SPAWN_THREAD = "dd777fa2-9780-54bb-ae67-0735945483cf";

const BASE_TIME = Date.parse("2026-02-11T17:24:40.000Z");

function makeError(overrides: Partial<ErrorUpdate> & { id: string }): ErrorUpdate {
  return {
    update_type: "error",
    chat_id: CHAT_ID,
    activity_type: "CallLLM",
    activity_id: "11133",
    error_message: "activity error: Claude session expired",
    error_summary: "Claude session expired",
    timestamp: new Date(BASE_TIME).toISOString(),
    sequence_number: 1,
    ...overrides,
  };
}

/** Offset from BASE_TIME, in seconds, as an ISO timestamp. */
function at(seconds: number): string {
  return new Date(BASE_TIME + seconds * 1000).toISOString();
}

/** Visibility check for "no thread filter" — every thread is visible. */
const showAll = createThreadVisibilityCheck(CHAT_ID, null);

describe("groupVisibleErrors", () => {
  it("collapses five concurrent identical failures across five threads into one row with a count", () => {
    // The observed five-thread case: identical summary, spanning ~15s.
    const threads = [
      MAIN_THREAD,
      SPAWN_THREAD,
      "11111111-1111-4111-8111-111111111111",
      "22222222-2222-4222-8222-222222222222",
      "33333333-3333-4333-8333-333333333333",
    ];
    const errors = threads.map((thread, i) =>
      makeError({
        id: `activity-error-${CHAT_ID}-${11110 + i}`,
        activity_id: String(11110 + i),
        thread,
        attempt_number: 4,
        timestamp: at(i * 3),
      }),
    );

    const groups = groupVisibleErrors(errors, {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });

    expect(groups).toHaveLength(1);
    expect(groups[0].errors).toHaveLength(5);
    // The representative is the earliest error, so the collapsed row lands at
    // the same point in the timeline the first failure would have.
    expect(groups[0].error.id).toBe(errors[0].id);
    // Every affected thread is carried, so the row can expand to useful detail.
    expect(groups[0].errors.map((e) => e.thread)).toEqual(threads);
  });

  it("collapses the observed two-thread case (main + spawn) from chat d8165a13", () => {
    const mainErr = makeError({
      id: `activity-error-${CHAT_ID}-11133`,
      activity_id: "11133",
      thread: MAIN_THREAD,
      attempt_number: 4,
      timestamp: at(0),
    });
    const spawnErr = makeError({
      id: `activity-error-${CHAT_ID}-11114`,
      activity_id: "11114",
      thread: SPAWN_THREAD,
      attempt_number: 5,
      timestamp: at(12),
    });

    const groups = groupVisibleErrors([mainErr, spawnErr], {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });

    expect(groups).toHaveLength(1);
    expect(groups[0].errors).toHaveLength(2);
  });

  it("does not mutate the input array or its entries", () => {
    const errors = [
      makeError({ id: "a", thread: MAIN_THREAD, timestamp: at(0) }),
      makeError({ id: "b", thread: SPAWN_THREAD, timestamp: at(5) }),
    ];
    const snapshot = JSON.parse(JSON.stringify(errors));

    groupVisibleErrors(errors, { isVisible: showAll, collapseAcrossThreads: true });

    expect(errors).toEqual(snapshot);
  });

  it("does not collapse across threads when only one thread is visible", () => {
    // The user is looking at one thread and wants that thread's own errors.
    const selected = new Set([SPAWN_THREAD]);
    const errors = [
      makeError({ id: "a", thread: SPAWN_THREAD, timestamp: at(0) }),
      makeError({ id: "b", thread: SPAWN_THREAD, timestamp: at(5) }),
    ];

    const groups = groupVisibleErrors(errors, {
      isVisible: createThreadVisibilityCheck(CHAT_ID, selected),
      collapseAcrossThreads: false,
    });

    expect(groups).toHaveLength(2);
    expect(groups.every((g) => g.errors.length === 1)).toBe(true);
  });

  it("does not collapse two failures that share a thread even when several threads are visible", () => {
    // Same thread means the user really did hit the failure twice there; the
    // retry badge (store-level dedup by id) is what folds a retry series.
    const errors = [
      makeError({ id: "a", activity_id: "1", thread: MAIN_THREAD, timestamp: at(0) }),
      makeError({ id: "b", activity_id: "2", thread: MAIN_THREAD, timestamp: at(5) }),
    ];

    const groups = groupVisibleErrors(errors, {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });

    expect(groups).toHaveLength(2);
  });

  it("does not collapse errors far apart in time even when the text matches", () => {
    const errors = [
      makeError({ id: "a", thread: MAIN_THREAD, timestamp: at(0) }),
      makeError({
        id: "b",
        thread: SPAWN_THREAD,
        timestamp: new Date(BASE_TIME + CONCURRENT_ERROR_WINDOW_MS + 1000).toISOString(),
      }),
    ];

    const groups = groupVisibleErrors(errors, {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });

    expect(groups).toHaveLength(2);
  });

  it("collapses errors inside the window and starts a new group outside it", () => {
    const errors = [
      makeError({ id: "a", thread: MAIN_THREAD, timestamp: at(0) }),
      makeError({ id: "b", thread: SPAWN_THREAD, timestamp: at(12) }),
      makeError({
        id: "c",
        thread: SPAWN_THREAD,
        timestamp: new Date(BASE_TIME + CONCURRENT_ERROR_WINDOW_MS + 5000).toISOString(),
      }),
      makeError({
        id: "d",
        thread: MAIN_THREAD,
        timestamp: new Date(BASE_TIME + CONCURRENT_ERROR_WINDOW_MS + 7000).toISOString(),
      }),
    ];

    const groups = groupVisibleErrors(errors, {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });

    expect(groups).toHaveLength(2);
    expect(groups.map((g) => g.errors.length)).toEqual([2, 2]);
  });

  it("never collapses different summaries", () => {
    const errors = [
      makeError({ id: "a", thread: MAIN_THREAD, timestamp: at(0) }),
      makeError({
        id: "b",
        thread: SPAWN_THREAD,
        timestamp: at(3),
        error_summary: "Rate limited by the AI provider",
      }),
    ];

    const groups = groupVisibleErrors(errors, {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });

    expect(groups).toHaveLength(2);
  });

  it("never merges errors from different chats", () => {
    const errors = [
      makeError({ id: "a", thread: MAIN_THREAD, timestamp: at(0) }),
      makeError({
        id: "b",
        thread: SPAWN_THREAD,
        timestamp: at(3),
        chat_id: "99999999-9999-4999-8999-999999999999",
      }),
    ];

    const groups = groupVisibleErrors(errors, {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });

    expect(groups).toHaveLength(2);
  });

  it("falls back to the cleaned error message when no summary is present", () => {
    const errors = [
      makeError({
        id: "a",
        thread: MAIN_THREAD,
        timestamp: at(0),
        error_summary: undefined,
        error_message: "dial tcp: lookup api.anthropic.com: no such host",
      }),
      makeError({
        id: "b",
        thread: SPAWN_THREAD,
        timestamp: at(4),
        error_summary: undefined,
        error_message: "dial tcp: lookup api.anthropic.com: no such host",
      }),
    ];

    const groups = groupVisibleErrors(errors, {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });

    expect(groups).toHaveLength(1);
    expect(groups[0].errors).toHaveLength(2);
  });

  it("keeps a thread-less legacy error standalone and visible in every view", () => {
    // An error with no thread predates thread scoping. It stays visible
    // everywhere rather than being guessed into a thread — and it must not be
    // swallowed into another thread's collapsed row either.
    const legacy = makeError({ id: "legacy", thread: undefined, timestamp: at(1) });
    const scoped = makeError({ id: "scoped", thread: SPAWN_THREAD, timestamp: at(2) });

    const allVisible = groupVisibleErrors([legacy, scoped], {
      isVisible: showAll,
      collapseAcrossThreads: true,
    });
    expect(allVisible).toHaveLength(2);
    expect(allVisible.map((g) => g.error.id).sort()).toEqual(["legacy", "scoped"]);

    // Visible in the main-thread view, which the scoped spawn error is not.
    const mainOnly = groupVisibleErrors([legacy, scoped], {
      isVisible: createThreadVisibilityCheck(CHAT_ID, new Set([MAIN_THREAD])),
      collapseAcrossThreads: false,
    });
    expect(mainOnly.map((g) => g.error.id)).toEqual(["legacy"]);

    // ...and in an unrelated spawn's view too.
    const otherSpawn = groupVisibleErrors([legacy, scoped], {
      isVisible: createThreadVisibilityCheck(
        CHAT_ID,
        new Set(["44444444-4444-4444-8444-444444444444"]),
      ),
      collapseAcrossThreads: false,
    });
    expect(otherSpawn.map((g) => g.error.id)).toEqual(["legacy"]);
  });

  it("drops errors scoped to a thread that is not visible", () => {
    const errors = [
      makeError({ id: "main", thread: MAIN_THREAD, timestamp: at(0) }),
      makeError({ id: "spawn", thread: SPAWN_THREAD, timestamp: at(2) }),
    ];

    const groups = groupVisibleErrors(errors, {
      isVisible: createThreadVisibilityCheck(CHAT_ID, new Set([SPAWN_THREAD])),
      collapseAcrossThreads: false,
    });

    expect(groups.map((g) => g.error.id)).toEqual(["spawn"]);
  });
});

describe("createThreadVisibilityCheck", () => {
  it("treats the main thread's id as the chat id", () => {
    // This is the case the data makes easy to get wrong: the main thread's id
    // EQUALS the chat id, while spawns carry distinct uuids.
    const mainSelected = createThreadVisibilityCheck(CHAT_ID, new Set([MAIN_THREAD]));
    expect(mainSelected(MAIN_THREAD)).toBe(true);
    expect(mainSelected(CHAT_ID)).toBe(true);
    expect(mainSelected(SPAWN_THREAD)).toBe(false);
  });

  it("accepts every encoding the main thread ships in", () => {
    // "", "0" and the chat id are all the main thread.
    for (const selected of [CHAT_ID, "0", ""]) {
      const isVisible = createThreadVisibilityCheck(CHAT_ID, new Set([selected]));
      expect(isVisible(CHAT_ID)).toBe(true);
      expect(isVisible("0")).toBe(true);
      expect(isVisible("")).toBe(true);
      expect(isVisible(undefined)).toBe(true);
      expect(isVisible(SPAWN_THREAD)).toBe(false);
    }
  });

  it("shows every thread when nothing is selected", () => {
    for (const selection of [null, undefined, new Set<string>()]) {
      const isVisible = createThreadVisibilityCheck(CHAT_ID, selection);
      expect(isVisible(MAIN_THREAD)).toBe(true);
      expect(isVisible(SPAWN_THREAD)).toBe(true);
      expect(isVisible(undefined)).toBe(true);
    }
  });

  it("does not treat a spawn thread as main", () => {
    const spawnSelected = createThreadVisibilityCheck(CHAT_ID, new Set([SPAWN_THREAD]));
    expect(spawnSelected(SPAWN_THREAD)).toBe(true);
    expect(spawnSelected(CHAT_ID)).toBe(false);
    expect(spawnSelected("0")).toBe(false);
    // An undefined thread resolves to main, which is NOT selected here — the
    // legacy "visible everywhere" rule lives in groupVisibleErrors, not here.
    expect(spawnSelected(undefined)).toBe(false);
  });
});
