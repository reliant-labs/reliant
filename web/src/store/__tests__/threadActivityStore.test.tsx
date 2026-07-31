/**
 * Tests for threadActivityStore's derivation hooks.
 *
 * All activity-answering selectors are gated internally on the chat-level
 * authority (activityStore): when the chat is not RUNNING/AWAITING_INPUT,
 * no thread is active and no activity text is produced, regardless of what
 * per-thread records say. Thread records themselves persist (they carry
 * names/titles for the timeline), but every consumer — thinking indicator,
 * thread tabs, sidebar dot — derives from the same chat-level gate, so the
 * two stores can never transiently disagree in what they display.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { renderHook, cleanup } from "@testing-library/react";
import { useActivityStore, ChatActivity } from "../activityStore";
import {
  useThreadActivityStore,
  useIsThreadActive,
  useActiveThreadIds,
  useChatCurrentActivity,
  getActivityDisplayText,
} from "../threadActivityStore";
import type { ActiveThreadUpdate } from "../../types/streaming";

const CHAT = "chat-1";
const CHILD = "thread-a";

function buildThread(overrides: Partial<ActiveThreadUpdate>): ActiveThreadUpdate {
  return {
    update_type: "thread",
    id: "wf-1",
    chat_id: CHAT,
    thread: CHILD,
    workflow_name: "Agent",
    is_planning_mode: false,
    status: "running",
    created_at: "2026-01-01T00:00:00.000Z",
    ...overrides,
  };
}

beforeEach(() => {
  useActivityStore.setState({ entries: new Map(), activities: new Map() });
  useThreadActivityStore.setState({ threads: {} });
});

afterEach(() => {
  cleanup();
});

describe("useIsThreadActive (characterization)", () => {
  it("chat RUNNING + thread record running → child thread active", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.RUNNING);
    useThreadActivityStore.getState().setThreads(CHAT, [buildThread({})]);

    const { result } = renderHook(() => useIsThreadActive(CHAT, CHILD));
    expect(result.current).toBe(true);
  });

  it("chat IDLE + stale running thread record → NOT active (chat gates)", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.IDLE);
    useThreadActivityStore.getState().setThreads(CHAT, [buildThread({})]);

    const { result } = renderHook(() => useIsThreadActive(CHAT, CHILD));
    expect(result.current).toBe(false);
  });

  it("chat RUNNING + no thread record → child NOT active, but null/main ARE", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.RUNNING);

    const child = renderHook(() => useIsThreadActive(CHAT, CHILD));
    expect(child.result.current).toBe(false);

    const allView = renderHook(() => useIsThreadActive(CHAT, null));
    expect(allView.result.current).toBe(true);

    const main = renderHook(() => useIsThreadActive(CHAT, CHAT));
    expect(main.result.current).toBe(true);
  });

  it("chat AWAITING_INPUT counts as running for gating", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.AWAITING_INPUT);
    useThreadActivityStore.getState().setThreads(CHAT, [buildThread({})]);

    const { result } = renderHook(() => useIsThreadActive(CHAT, CHILD));
    expect(result.current).toBe(true);
  });

  it("thread metadata records (thread:/fork: names) are ignored", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.RUNNING);
    useThreadActivityStore
      .getState()
      .setThreads(CHAT, [
        buildThread({ workflow_name: "thread:child" }),
        buildThread({ id: "wf-2", workflow_name: "fork:other", thread: "t2" }),
      ]);

    const { result } = renderHook(() => useIsThreadActive(CHAT, CHILD));
    expect(result.current).toBe(false);
  });

  it("completed thread record → NOT active even when chat RUNNING", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.RUNNING);
    useThreadActivityStore
      .getState()
      .setThreads(CHAT, [buildThread({ status: "completed" })]);

    const { result } = renderHook(() => useIsThreadActive(CHAT, CHILD));
    expect(result.current).toBe(false);
  });
});

describe("useActiveThreadIds", () => {
  it("collects running/active non-metadata thread ids when chat is running", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.RUNNING);
    useThreadActivityStore.getState().setThreads(CHAT, [
      buildThread({}),
      buildThread({ id: "wf-2", thread: "t2", status: "active" }),
      buildThread({ id: "wf-3", thread: "t3", status: "completed" }),
      buildThread({ id: "wf-4", thread: "t4", workflow_name: "thread:x" }),
    ]);

    const { result } = renderHook(() => useActiveThreadIds(CHAT));
    expect(result.current).toEqual(new Set([CHILD, "t2"]));
  });

  it("returns empty set when the chat is IDLE, even with running records", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.IDLE);
    useThreadActivityStore.getState().setThreads(CHAT, [buildThread({})]);

    const { result } = renderHook(() => useActiveThreadIds(CHAT));
    expect(result.current).toEqual(new Set());
  });
});

describe("useChatCurrentActivity", () => {
  it("returns the first running thread's current_activity when chat is running", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.RUNNING);
    useThreadActivityStore
      .getState()
      .setThreads(CHAT, [buildThread({ current_activity: "V2_CallLLM" })]);

    const { result } = renderHook(() => useChatCurrentActivity(CHAT));
    expect(result.current).toBe("V2_CallLLM");
  });

  it("returns null when no running thread has an activity", () => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.RUNNING);
    useThreadActivityStore
      .getState()
      .setThreads(CHAT, [buildThread({ status: "completed", current_activity: "CallLLM" })]);

    const { result } = renderHook(() => useChatCurrentActivity(CHAT));
    expect(result.current).toBeNull();
  });

  it("returns null when the chat is IDLE, even with a stale running record", () => {
    // The flicker scenario: a thread event with current_activity arrives
    // after (or survives past) the chat-level IDLE — the gate must win so
    // the indicator's visibility and text can never disagree.
    useActivityStore.getState().setActivity(CHAT, ChatActivity.IDLE);
    useThreadActivityStore
      .getState()
      .setThreads(CHAT, [buildThread({ current_activity: "V2_CallLLM" })]);

    const { result } = renderHook(() => useChatCurrentActivity(CHAT));
    expect(result.current).toBeNull();
  });
});

describe("getActivityDisplayText (characterization)", () => {
  it("maps known activity names", () => {
    expect(getActivityDisplayText("Compact")).toBe("Summarizing conversation");
    expect(getActivityDisplayText("ExecuteTools")).toBe("Running tools");
    expect(getActivityDisplayText("CallLLM")).toBe("Thinking");
  });

  it("maps V2_-prefixed names identically to unprefixed ones", () => {
    expect(getActivityDisplayText("V2_CallLLM")).toBe("Thinking");
    expect(getActivityDisplayText("V2_Compact")).toBe("Summarizing conversation");
    expect(getActivityDisplayText("V2_ExecuteTools")).toBe("Running tools");
  });

  it("falls back to 'Thinking' for unmapped names", () => {
    expect(getActivityDisplayText("SomeBrandNewActivity")).toBe("Thinking");
  });

  it("returns null for null/empty input", () => {
    expect(getActivityDisplayText(null)).toBeNull();
    expect(getActivityDisplayText("")).toBeNull();
  });
});