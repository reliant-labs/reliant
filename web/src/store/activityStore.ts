/**
 * Activity Store — single source of truth for chat activity state.
 *
 * Each chat has exactly one ChatActivity value (from proto).
 * The sidebar, thinking indicator, and all other consumers read from here.
 *
 * Populated from three channels, ordered by user-update sequence number
 * (per-user monotonic counter assigned transactionally on the server):
 *   1. CHAT_ACTIVITY_CHANGED streaming events — carry their exact sequence
 *      number (applyStreamActivity).
 *   2. ListChats responses — carry lastUserUpdateSequence, a freshness
 *      watermark for the whole snapshot (applyListActivities).
 *   3. GetChat responses — carry no sequence; the caller passes the client's
 *      maxSeenSeq captured before the request as a freshness lower bound
 *      (applyChatSnapshot).
 * Plus local optimistic writes (setActivity) for instant UI feedback on
 * user actions (send/cancel/pause/resume).
 *
 * Precedence is decided purely by sequence ordering — there is no
 * wall-clock freshness window. Each entry records the sequence that set it;
 * a write only lands if its sequence claim is at least as new:
 *   - stream event seq E:   applies if E >= entry.seq (an event at exactly
 *     maxSeenSeq+1 must be able to confirm an optimistic write)
 *   - list watermark L:     applies if L > entry.seq (equality means no
 *     server events since the entry was set — nothing to correct — and
 *     strict > shields the snapshot-read race inside the server handler)
 *   - GetChat baseline B:   applies if B >= entry.seq (the response is at
 *     least as fresh as everything the client has processed; this is the
 *     stuck-busy recovery path used by checkChatStatus)
 *   - optimistic:           always applies, tagged maxSeenSeq + 1 so it
 *     beats all current server knowledge but yields to anything newer
 */

import { create } from "zustand";
import { ChatActivity } from "../gen/reliant/v1/chat_pb";

// Re-export the proto enum so consumers don't need to import from gen/
export { ChatActivity } from "../gen/reliant/v1/chat_pb";

// Map activity enum to dot states for the sidebar ActivityDot component
export type DotState = "idle" | "thinking" | "awaiting_approval" | "error" | "paused";

export function activityToDotState(activity: ChatActivity): DotState {
  switch (activity) {
    case ChatActivity.RUNNING:
      return "thinking";
    case ChatActivity.AWAITING_INPUT:
      return "awaiting_approval";
    case ChatActivity.ERROR:
      return "error";
    case ChatActivity.PAUSED:
      return "paused";
    default:
      return "idle";
  }
}

interface ActivityEntry {
  activity: ChatActivity;
  /** User-update sequence that set this value (see module doc for rules). */
  seq: number;
}

interface ActivityState {
  entries: Map<string, ActivityEntry>;
  /** Convenience projection — just the activity values (for consumers). */
  activities: Map<string, ChatActivity>;
  /**
   * Highest server-derived sequence observed (stream event seqs and list
   * watermarks). Optimistic writes are tagged maxSeenSeq + 1 but do NOT
   * advance it — only real server knowledge does, so repeated optimistic
   * writes can never inflate entry seqs past what the server will assign.
   */
  maxSeenSeq: number;
  /** Local optimistic write for instant feedback on a user action. */
  setActivity: (chatId: string, activity: ChatActivity) => void;
  /** Write from a CHAT_ACTIVITY_CHANGED stream event carrying its seq. */
  applyStreamActivity: (
    chatId: string,
    activity: ChatActivity,
    seq: number,
  ) => void;
  /** Bulk write from a ListChats snapshot with its watermark. */
  applyListActivities: (
    serverActivities: Map<string, ChatActivity>,
    watermark: number,
  ) => void;
  /** Write from a GetChat snapshot; baselineSeq = maxSeenSeq before fetch. */
  applyChatSnapshot: (
    chatId: string,
    activity: ChatActivity,
    baselineSeq: number,
  ) => void;
  removeActivity: (chatId: string) => void;
}

function buildActivitiesProjection(
  entries: Map<string, ActivityEntry>,
): Map<string, ChatActivity> {
  const m = new Map<string, ChatActivity>();
  for (const [id, e] of entries) m.set(id, e.activity);
  return m;
}

export const useActivityStore = create<ActivityState>((set) => ({
  entries: new Map(),
  activities: new Map(),
  maxSeenSeq: 0,

  setActivity: (chatId, activity) =>
    set((state) => {
      const existing = state.entries.get(chatId);
      const seq = state.maxSeenSeq + 1;
      // Skip if nothing changes — prevents unnecessary re-renders
      if (existing?.activity === activity && existing.seq === seq) return state;
      const next = new Map(state.entries);
      next.set(chatId, { activity, seq });
      return { entries: next, activities: buildActivitiesProjection(next) };
    }),

  applyStreamActivity: (chatId, activity, seq) =>
    set((state) => {
      const existing = state.entries.get(chatId);
      if (seq > 0 && existing && seq < existing.seq) {
        // Stale event (e.g. replay of an update older than a list snapshot
        // we already applied) — sequence precedence rejects it.
        return state;
      }
      const maxSeenSeq = Math.max(state.maxSeenSeq, seq);
      // seq <= 0 means a malformed/ephemeral event — apply defensively
      // (matches pre-sequence behavior) but preserve the entry's seq.
      const entrySeq = seq > 0 ? seq : (existing?.seq ?? 0);
      if (existing?.activity === activity && existing.seq === entrySeq) {
        return maxSeenSeq === state.maxSeenSeq ? state : { maxSeenSeq };
      }
      const next = new Map(state.entries);
      next.set(chatId, { activity, seq: entrySeq });
      return {
        entries: next,
        activities: buildActivitiesProjection(next),
        maxSeenSeq,
      };
    }),

  applyListActivities: (serverActivities, watermark) =>
    set((state) => {
      let changed = false;
      const next = new Map(state.entries);
      for (const [chatId, activity] of serverActivities) {
        const existing = next.get(chatId);
        if (existing && watermark <= existing.seq) continue;
        if (existing?.activity === activity && existing.seq === watermark) {
          continue;
        }
        next.set(chatId, { activity, seq: watermark });
        changed = true;
      }
      const maxSeenSeq = Math.max(state.maxSeenSeq, watermark);
      if (!changed) {
        return maxSeenSeq === state.maxSeenSeq ? state : { maxSeenSeq };
      }
      return {
        entries: next,
        activities: buildActivitiesProjection(next),
        maxSeenSeq,
      };
    }),

  applyChatSnapshot: (chatId, activity, baselineSeq) =>
    set((state) => {
      const existing = state.entries.get(chatId);
      if (existing && baselineSeq < existing.seq) return state;
      if (existing?.activity === activity && existing.seq === baselineSeq) {
        return state;
      }
      const next = new Map(state.entries);
      next.set(chatId, { activity, seq: baselineSeq });
      return { entries: next, activities: buildActivitiesProjection(next) };
    }),

  removeActivity: (chatId) =>
    set((state) => {
      if (!state.entries.has(chatId)) return state;
      const next = new Map(state.entries);
      next.delete(chatId);
      return { entries: next, activities: buildActivitiesProjection(next) };
    }),
}));

// ============================================================================
// Selector hooks
// ============================================================================

/** Get the activity enum value for a chat. Defaults to IDLE. */
export function useChatActivity(chatId: string): ChatActivity {
  return useActivityStore(
    (s) => s.activities.get(chatId) ?? ChatActivity.IDLE,
  );
}

/** True when a chat is actively working (RUNNING or AWAITING_INPUT). */
export function useIsChatRunning(chatId: string): boolean {
  return useActivityStore(
    (s) => {
      const activity = s.activities.get(chatId) ?? ChatActivity.IDLE;
      return activity === ChatActivity.RUNNING || activity === ChatActivity.AWAITING_INPUT;
    },
  );
}
