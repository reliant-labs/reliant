/**
 * Sequence-precedence tests for activityStore.
 *
 * Replaces the old wall-clock "isFreshNonIdle / 10s anti-downgrade window"
 * characterization: activity writes are now ordered purely by user-update
 * sequence numbers.
 *
 * Rules (see activityStore.ts module doc):
 *  - stream event seq E applies iff E >= entry.seq
 *  - ListChats watermark L applies iff L > entry.seq
 *  - GetChat baseline B applies iff B >= entry.seq
 *  - optimistic setActivity always applies, tagged maxSeenSeq + 1
 */
import { beforeEach, describe, expect, it } from "vitest";
import { useActivityStore, ChatActivity } from "../activityStore";

function reset() {
  useActivityStore.setState({
    entries: new Map(),
    activities: new Map(),
    maxSeenSeq: 0,
  });
}

const activityOf = (chatId: string) =>
  useActivityStore.getState().activities.get(chatId);

describe("activityStore sequence precedence", () => {
  beforeEach(reset);

  // ---- stream events -------------------------------------------------------

  describe("applyStreamActivity", () => {
    it("applies events in order and advances maxSeenSeq", () => {
      const s = useActivityStore.getState();
      s.applyStreamActivity("c1", ChatActivity.RUNNING, 5);
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);
      expect(useActivityStore.getState().maxSeenSeq).toBe(5);

      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.IDLE, 6);
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
      expect(useActivityStore.getState().maxSeenSeq).toBe(6);
    });

    it("rejects an event older than the entry's seq (stale replay)", () => {
      const s = useActivityStore.getState();
      s.applyStreamActivity("c1", ChatActivity.IDLE, 10);
      s.applyStreamActivity("c1", ChatActivity.RUNNING, 4);
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
    });

    it("an equal-seq event applies (idempotent redelivery, optimistic confirm)", () => {
      const s = useActivityStore.getState();
      s.applyStreamActivity("c1", ChatActivity.RUNNING, 7);
      s.applyStreamActivity("c1", ChatActivity.AWAITING_INPUT, 7);
      expect(activityOf("c1")).toBe(ChatActivity.AWAITING_INPUT);
    });

    it("confirms an optimistic write at exactly maxSeenSeq+1", () => {
      const s = useActivityStore.getState();
      s.applyStreamActivity("c1", ChatActivity.IDLE, 3); // maxSeenSeq=3
      useActivityStore.getState().setActivity("c1", ChatActivity.RUNNING); // seq=4
      // The real server event lands with seq 4 — must not be rejected.
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 4);
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);
      // ...and its follow-up IDLE at seq 5 must apply.
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.IDLE, 5);
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
    });

    it("seq<=0 (malformed/ephemeral) applies defensively without corrupting seq", () => {
      const s = useActivityStore.getState();
      s.applyStreamActivity("c1", ChatActivity.RUNNING, 8);
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.IDLE, 0);
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
      // Entry keeps seq 8: a list watermark of 8 still can't overwrite it.
      useActivityStore.getState().applyListActivities(
        new Map([["c1", ChatActivity.RUNNING]]),
        8,
      );
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
    });

    it("no-op redelivery returns the same state reference", () => {
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 5);
      const before = useActivityStore.getState();
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 5);
      expect(useActivityStore.getState()).toBe(before);
      expect(useActivityStore.getState().activities).toBe(before.activities);
    });
  });

  // ---- optimistic local writes ---------------------------------------------

  describe("setActivity (optimistic)", () => {
    it("always applies and outranks the current watermark", () => {
      useActivityStore.getState().applyListActivities(
        new Map([["c1", ChatActivity.IDLE]]),
        20,
      );
      useActivityStore.getState().setActivity("c1", ChatActivity.RUNNING);
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);

      // A re-fetch with the SAME watermark cannot downgrade it...
      useActivityStore.getState().applyListActivities(
        new Map([["c1", ChatActivity.IDLE]]),
        20,
      );
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);

      // ...but any newer server knowledge wins.
      useActivityStore.getState().applyListActivities(
        new Map([["c1", ChatActivity.IDLE]]),
        22,
      );
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
    });

    it("does not advance maxSeenSeq (repeated optimism can't inflate)", () => {
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.IDLE, 9);
      useActivityStore.getState().setActivity("c1", ChatActivity.RUNNING);
      useActivityStore.getState().setActivity("c2", ChatActivity.RUNNING);
      expect(useActivityStore.getState().maxSeenSeq).toBe(9);
      // Both optimistic entries claim seq 10; the next real event (seq 10)
      // can still confirm/override them.
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.IDLE, 10);
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
    });
  });

  // ---- list snapshots ------------------------------------------------------

  describe("applyListActivities", () => {
    it("seeds unknown chats and advances maxSeenSeq to the watermark", () => {
      useActivityStore.getState().applyListActivities(
        new Map([
          ["c1", ChatActivity.RUNNING],
          ["c2", ChatActivity.IDLE],
        ]),
        15,
      );
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);
      expect(activityOf("c2")).toBe(ChatActivity.IDLE);
      expect(useActivityStore.getState().maxSeenSeq).toBe(15);
    });

    it("does not downgrade an entry set by a newer stream event", () => {
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 30);
      useActivityStore.getState().applyListActivities(
        new Map([["c1", ChatActivity.IDLE]]),
        25,
      );
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);
    });

    it("equal watermark does not overwrite (no events since entry was set)", () => {
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 30);
      useActivityStore.getState().applyListActivities(
        new Map([["c1", ChatActivity.IDLE]]),
        30,
      );
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);
    });

    it("newer watermark overwrites stale entries (stuck-dot recovery)", () => {
      // Missed IDLE event scenario: entry stuck RUNNING at seq 30, the
      // missed event was seq 31, a later list fetch has watermark >= 31.
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 30);
      useActivityStore.getState().applyListActivities(
        new Map([["c1", ChatActivity.IDLE]]),
        31,
      );
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
    });

    it("watermark 0 (server fallback) never overwrites existing entries", () => {
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 5);
      useActivityStore.getState().applyListActivities(
        new Map([["c1", ChatActivity.IDLE]]),
        0,
      );
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);
    });
  });

  // ---- GetChat snapshots ---------------------------------------------------

  describe("applyChatSnapshot", () => {
    it("baseline >= entry seq reconciles a stale RUNNING to IDLE", () => {
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 12);
      // Client has seen up to 12; GetChat issued now says IDLE.
      useActivityStore.getState().applyChatSnapshot("c1", ChatActivity.IDLE, 12);
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
    });

    it("yields to a stream event that arrived after the baseline was captured", () => {
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.IDLE, 12);
      const baseline = useActivityStore.getState().maxSeenSeq; // 12
      // Mid-flight: workflow starts, event seq 13 arrives before the response.
      useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 13);
      useActivityStore.getState().applyChatSnapshot("c1", ChatActivity.IDLE, baseline);
      expect(activityOf("c1")).toBe(ChatActivity.RUNNING);
    });

    it("overwrites an optimistic write made before the baseline was captured", () => {
      // Recovery path: optimistic RUNNING (seq = maxSeen+1 = 1), then
      // checkChatStatus captures baseline AFTER later server knowledge.
      useActivityStore.getState().setActivity("c1", ChatActivity.RUNNING); // seq 1
      useActivityStore.getState().applyStreamActivity("c2", ChatActivity.IDLE, 40);
      const baseline = useActivityStore.getState().maxSeenSeq; // 40
      useActivityStore.getState().applyChatSnapshot("c1", ChatActivity.IDLE, baseline);
      expect(activityOf("c1")).toBe(ChatActivity.IDLE);
    });
  });

  // ---- removeActivity ------------------------------------------------------

  it("removeActivity drops the entry", () => {
    useActivityStore.getState().applyStreamActivity("c1", ChatActivity.RUNNING, 3);
    useActivityStore.getState().removeActivity("c1");
    expect(activityOf("c1")).toBeUndefined();
  });
});
