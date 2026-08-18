/**
 * Identity-stability regression for mergeActiveThreads.
 *
 * InterleavedTimeline depends on the array useActiveThreads returns, and
 * mergeActiveThreads is called on every thread update batch — including
 * repeated status pings with identical payloads. Without identity stability,
 * every such repeat produces a fresh array reference and invalidates the
 * whole timeline memo. These tests pin that a genuine no-op merge returns
 * the SAME reference (existing array, and every untouched entry object),
 * while any real change still produces a new one.
 */
import { describe, expect, it } from "vitest";
import { mergeActiveThreads } from "../chatStreamReducers";
import type { ActiveThreadUpdate } from "../../types/streaming";

function thread(overrides: Partial<ActiveThreadUpdate>): ActiveThreadUpdate {
  return {
    update_type: "thread",
    id: "wf-1",
    chat_id: "chat-1",
    thread: "thread-1",
    is_planning_mode: false,
    status: "running",
    created_at: "2026-01-01T00:00:00.000Z",
    ...overrides,
  } as ActiveThreadUpdate;
}

describe("mergeActiveThreads identity stability", () => {
  it("returns the SAME array reference when an update is a true no-op", () => {
    const existing = [thread({})];
    const identicalUpdate = thread({});

    const merged = mergeActiveThreads(existing, [identicalUpdate]);

    expect(merged).toBe(existing);
  });

  it("returns the same array reference across a batch of all-no-op updates", () => {
    const existing = [thread({}), thread({ id: "wf-2", thread: "thread-2" })];
    const updates = [thread({}), thread({ id: "wf-2", thread: "thread-2" })];

    const merged = mergeActiveThreads(existing, updates);

    expect(merged).toBe(existing);
  });

  it("returns a NEW array when a real change occurs, but leaves untouched entries reference-equal", () => {
    const untouched = thread({ id: "wf-2", thread: "thread-2", status: "running" });
    const changing = thread({ id: "wf-1", thread: "thread-1", status: "running" });
    const existing = [changing, untouched];

    const merged = mergeActiveThreads(existing, [
      thread({ id: "wf-1", thread: "thread-1", status: "completed" }),
    ]);

    expect(merged).not.toBe(existing);
    expect(merged[0]).not.toBe(changing);
    expect(merged[0].status).toBe("completed");
    // The other entry was never touched by this update batch — same object.
    expect(merged[1]).toBe(untouched);
  });

  it("appending a new thread produces a new array and leaves existing entries reference-equal", () => {
    const existingEntry = thread({});
    const existing = [existingEntry];

    const merged = mergeActiveThreads(existing, [
      thread({ id: "wf-2", thread: "thread-2" }),
    ]);

    expect(merged).not.toBe(existing);
    expect(merged).toHaveLength(2);
    expect(merged[0]).toBe(existingEntry);
  });

  it("identity-field fallback still applies: an update omitting thread_title keeps prev's value, and is reported unchanged", () => {
    const existing = [thread({ thread_title: "Fix the bug" })];
    // Update omits thread_title entirely (as a real completion event would).
    const update = thread({ status: "running", thread_title: undefined });

    const merged = mergeActiveThreads(existing, [update]);

    // Nothing actually changed once the fallback is applied (status was
    // already "running"), so identity must be preserved.
    expect(merged).toBe(existing);
    expect(merged[0].thread_title).toBe("Fix the bug");
  });

  it("does NOT falsely report unchanged when a field genuinely changes to undefined", () => {
    const existing = [thread({ current_activity: "CallLLM" })];
    // current_activity is not one of the identity-fallback fields, so an
    // update that omits it is a genuine transition to undefined.
    const update = thread({ current_activity: undefined });

    const merged = mergeActiveThreads(existing, [update]);

    expect(merged).not.toBe(existing);
    expect(merged[0].current_activity).toBeUndefined();
  });

  it("does not falsely report unchanged when a fallback field changes from one real value to another", () => {
    const existing = [thread({ thread_title: "Old title" })];
    const update = thread({ thread_title: "New title" });

    const merged = mergeActiveThreads(existing, [update]);

    expect(merged).not.toBe(existing);
    expect(merged[0].thread_title).toBe("New title");
  });

  it("router_decision: identical nested object contents are treated as unchanged", () => {
    const existing = [
      thread({ router_decision: { workflow: "wf", preset: "p1" } }),
    ];
    const update = thread({ router_decision: { workflow: "wf", preset: "p1" } });

    const merged = mergeActiveThreads(existing, [update]);

    expect(merged).toBe(existing);
  });

  it("router_decision: a genuinely different nested value is reported changed", () => {
    const existing = [
      thread({ router_decision: { workflow: "wf", preset: "p1" } }),
    ];
    const update = thread({ router_decision: { workflow: "wf", preset: "p2" } });

    const merged = mergeActiveThreads(existing, [update]);

    expect(merged).not.toBe(existing);
    expect(merged[0].router_decision).toEqual({ workflow: "wf", preset: "p2" });
  });

  it("merges compact activity rows into the canonical thread row", () => {
    const existing = [
      thread({
        id: "chat-1-thread-1-compact",
        thread: "thread-1",
        origin: "spawn",
        thread_title: "Fix generic pill title",
        workflow_id: "wf-1",
        current_activity: "Compact",
      }),
    ];

    const merged = mergeActiveThreads(existing, [
      thread({
        id: "wf-1",
        thread: "thread-1",
        status: "running",
      }),
    ]);

    expect(merged).toHaveLength(1);
    expect(merged[0].id).toBe("wf-1");
    expect(merged[0].thread_title).toBe("Fix generic pill title");
    expect(merged[0].origin).toBe("spawn");
    expect(merged[0].workflow_id).toBe("wf-1");
  });
});
