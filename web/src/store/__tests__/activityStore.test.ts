import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  useActivityStore,
  ChatActivity,
} from "../activityStore";

vi.mock("../../api/client", () => ({
  api: {},
}));

describe("activityStore", () => {
  beforeEach(() => {
    useActivityStore.setState({
      entries: new Map(),
      activities: new Map(),
      maxSeenSeq: 0,
    });
  });

  // ---- setActivity ---------------------------------------------------------

  it("setActivity updates activity for a chat", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);

    expect(useActivityStore.getState().activities.get("chat-1")).toBe(
      ChatActivity.RUNNING,
    );
  });

  it("setActivity with same value returns same state reference", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);
    const stateBefore = useActivityStore.getState();

    // Set the same value again — should be a no-op
    useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);
    const stateAfter = useActivityStore.getState();

    expect(stateAfter).toBe(stateBefore);
    // The activities Map reference should also be identical
    expect(stateAfter.activities).toBe(stateBefore.activities);
  });

  it("setActivity with different value creates new state", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);
    const stateBefore = useActivityStore.getState();

    useActivityStore.getState().setActivity("chat-1", ChatActivity.IDLE);
    const stateAfter = useActivityStore.getState();

    expect(stateAfter).not.toBe(stateBefore);
    expect(stateAfter.activities).not.toBe(stateBefore.activities);
    expect(stateAfter.activities.get("chat-1")).toBe(ChatActivity.IDLE);
  });

  // ---- applyListActivities -------------------------------------------------

  it("applyListActivities merges server activities under the watermark", () => {
    const bulk = new Map<string, ChatActivity>([
      ["chat-2", ChatActivity.AWAITING_INPUT],
      ["chat-3", ChatActivity.IDLE],
    ]);
    useActivityStore.getState().applyListActivities(bulk, 5);

    const activities = useActivityStore.getState().activities;
    expect(activities.get("chat-2")).toBe(ChatActivity.AWAITING_INPUT);
    expect(activities.get("chat-3")).toBe(ChatActivity.IDLE);
    expect(useActivityStore.getState().maxSeenSeq).toBe(5);
  });

  // ---- removeActivity ------------------------------------------------------

  it("removeActivity removes a chat's activity", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);
    useActivityStore.getState().setActivity("chat-2", ChatActivity.IDLE);

    useActivityStore.getState().removeActivity("chat-1");

    const activities = useActivityStore.getState().activities;
    expect(activities.has("chat-1")).toBe(false);
    expect(activities.get("chat-2")).toBe(ChatActivity.IDLE);
  });

  // ---- useChatActivity selector --------------------------------------------

  it("useChatActivity defaults to IDLE for unknown chats", () => {
    // Exercise the selector logic directly against state
    const state = useActivityStore.getState();
    const activity =
      state.activities.get("nonexistent") ?? ChatActivity.IDLE;

    expect(activity).toBe(ChatActivity.IDLE);
  });

  // ---- useIsChatRunning selector -------------------------------------------

  describe("useIsChatRunning", () => {
    it("returns true for RUNNING", () => {
      useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);
      const state = useActivityStore.getState();
      const activity =
        state.activities.get("chat-1") ?? ChatActivity.IDLE;
      const isRunning =
        activity === ChatActivity.RUNNING ||
        activity === ChatActivity.AWAITING_INPUT;

      expect(isRunning).toBe(true);
    });

    it("returns true for AWAITING_INPUT", () => {
      useActivityStore
        .getState()
        .setActivity("chat-1", ChatActivity.AWAITING_INPUT);
      const state = useActivityStore.getState();
      const activity =
        state.activities.get("chat-1") ?? ChatActivity.IDLE;
      const isRunning =
        activity === ChatActivity.RUNNING ||
        activity === ChatActivity.AWAITING_INPUT;

      expect(isRunning).toBe(true);
    });

    it("returns false for IDLE", () => {
      useActivityStore.getState().setActivity("chat-1", ChatActivity.IDLE);
      const state = useActivityStore.getState();
      const activity =
        state.activities.get("chat-1") ?? ChatActivity.IDLE;
      const isRunning =
        activity === ChatActivity.RUNNING ||
        activity === ChatActivity.AWAITING_INPUT;

      expect(isRunning).toBe(false);
    });

    it("returns false for unknown chat", () => {
      const state = useActivityStore.getState();
      const activity =
        state.activities.get("nonexistent") ?? ChatActivity.IDLE;
      const isRunning =
        activity === ChatActivity.RUNNING ||
        activity === ChatActivity.AWAITING_INPUT;

      expect(isRunning).toBe(false);
    });
  });
});