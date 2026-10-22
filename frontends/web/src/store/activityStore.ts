/**
 * Activity Store — single source of truth for chat activity state.
 *
 * Each chat has exactly one ChatActivity value (from proto), plus the count
 * of its threads still working (Chat.working_threads): an AWAITING_INPUT chat
 * can need the user while other agents keep running, which one enum value
 * cannot say. Both travel together on every channel below and share one
 * sequence claim. The sidebar, thinking indicator, and all other consumers
 * read from here.
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

// Coarse activity classes the sidebar and keyboard navigation sort, group and
// filter by. What a user READS for an activity (label, color) is not here: it
// is lib/runStatus's runStatusFromActivity.
export type DotState = "idle" | "thinking" | "awaiting_input" | "error" | "paused";

export function activityToDotState(activity: ChatActivity): DotState {
  switch (activity) {
    case ChatActivity.RUNNING:
      return "thinking";
    case ChatActivity.AWAITING_INPUT:
      return "awaiting_input";
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
  /** Threads still working (Chat.working_threads); 0 when unknown. */
  workingThreads: number;
  /** User-update sequence that set this value (see module doc for rules). */
  seq: number;
}

interface ActivityState {
  entries: Map<string, ActivityEntry>;
  /** Convenience projection — just the activity values (for consumers). */
  activities: Map<string, ChatActivity>;
  /** Convenience projection — working-thread counts, for chats that have any. */
  workingThreads: Map<string, number>;
  /**
   * Highest server-derived sequence observed (stream event seqs and list
   * watermarks). Optimistic writes are tagged maxSeenSeq + 1 but do NOT
   * advance it — only real server knowledge does, so repeated optimistic
   * writes can never inflate entry seqs past what the server will assign.
   */
  maxSeenSeq: number;
  /**
   * Local optimistic write for instant feedback on a user action. Keeps the
   * known working-thread count, except that IDLE has none.
   */
  setActivity: (chatId: string, activity: ChatActivity) => void;
  /** Write from a CHAT_ACTIVITY_CHANGED stream event carrying its seq. */
  applyStreamActivity: (
    chatId: string,
    activity: ChatActivity,
    seq: number,
    workingThreads?: number,
  ) => void;
  /**
   * Bulk write from a ListChats snapshot with its watermark. A chat missing
   * from serverWorkingThreads has none working.
   */
  applyListActivities: (
    serverActivities: Map<string, ChatActivity>,
    watermark: number,
    serverWorkingThreads?: Map<string, number>,
  ) => void;
  /** Write from a GetChat snapshot; baselineSeq = maxSeenSeq before fetch. */
  applyChatSnapshot: (
    chatId: string,
    activity: ChatActivity,
    baselineSeq: number,
    workingThreads?: number,
  ) => void;
  removeActivity: (chatId: string) => void;
}

function buildProjections(entries: Map<string, ActivityEntry>): {
  activities: Map<string, ChatActivity>;
  workingThreads: Map<string, number>;
} {
  const activities = new Map<string, ChatActivity>();
  const workingThreads = new Map<string, number>();
  for (const [id, e] of entries) {
    activities.set(id, e.activity);
    if (e.workingThreads > 0) workingThreads.set(id, e.workingThreads);
  }
  return { activities, workingThreads };
}

function sameEntry(a: ActivityEntry | undefined, b: ActivityEntry): boolean {
  return (
    a !== undefined &&
    a.activity === b.activity &&
    a.workingThreads === b.workingThreads &&
    a.seq === b.seq
  );
}

export const useActivityStore = create<ActivityState>((set) => ({
  entries: new Map(),
  activities: new Map(),
  workingThreads: new Map(),
  maxSeenSeq: 0,

  setActivity: (chatId, activity) =>
    set((state) => {
      const existing = state.entries.get(chatId);
      const entry: ActivityEntry = {
        activity,
        workingThreads: activity === ChatActivity.IDLE ? 0 : (existing?.workingThreads ?? 0),
        seq: state.maxSeenSeq + 1,
      };
      // Skip if nothing changes — prevents unnecessary re-renders
      if (sameEntry(existing, entry)) return state;
      const next = new Map(state.entries);
      next.set(chatId, entry);
      return { entries: next, ...buildProjections(next) };
    }),

  applyStreamActivity: (chatId, activity, seq, workingThreads = 0) =>
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
      const entry: ActivityEntry = {
        activity,
        workingThreads,
        seq: seq > 0 ? seq : (existing?.seq ?? 0),
      };
      if (sameEntry(existing, entry)) {
        return maxSeenSeq === state.maxSeenSeq ? state : { maxSeenSeq };
      }
      const next = new Map(state.entries);
      next.set(chatId, entry);
      return {
        entries: next,
        ...buildProjections(next),
        maxSeenSeq,
      };
    }),

  applyListActivities: (serverActivities, watermark, serverWorkingThreads) =>
    set((state) => {
      let changed = false;
      const next = new Map(state.entries);
      for (const [chatId, activity] of serverActivities) {
        const existing = next.get(chatId);
        if (existing && watermark <= existing.seq) continue;
        const entry: ActivityEntry = {
          activity,
          workingThreads: serverWorkingThreads?.get(chatId) ?? 0,
          seq: watermark,
        };
        if (sameEntry(existing, entry)) continue;
        next.set(chatId, entry);
        changed = true;
      }
      const maxSeenSeq = Math.max(state.maxSeenSeq, watermark);
      if (!changed) {
        return maxSeenSeq === state.maxSeenSeq ? state : { maxSeenSeq };
      }
      return {
        entries: next,
        ...buildProjections(next),
        maxSeenSeq,
      };
    }),

  applyChatSnapshot: (chatId, activity, baselineSeq, workingThreads = 0) =>
    set((state) => {
      const existing = state.entries.get(chatId);
      if (existing && baselineSeq < existing.seq) return state;
      const entry: ActivityEntry = { activity, workingThreads, seq: baselineSeq };
      if (sameEntry(existing, entry)) return state;
      const next = new Map(state.entries);
      next.set(chatId, entry);
      return { entries: next, ...buildProjections(next) };
    }),

  removeActivity: (chatId) =>
    set((state) => {
      if (!state.entries.has(chatId)) return state;
      const next = new Map(state.entries);
      next.delete(chatId);
      return { entries: next, ...buildProjections(next) };
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

/** How many of a chat's threads are still working. Defaults to 0. */
export function useChatWorkingThreads(chatId: string): number {
  return useActivityStore((s) => s.workingThreads.get(chatId) ?? 0);
}

/**
 * The coarse "something is in progress" test the ESC handler, the app's
 * active-workflow count and the streaming cleanup use: every activity from
 * RUNNING up — except QUEUED_FOR_MACHINE. That one ranks highest in the enum
 * only because it was added last: its run has ENDED (the machine never came
 * up), and the server starts a fresh one when the machine connects. Treating it
 * as busy would pause a run that is not there.
 */
export function isBusyActivity(activity: ChatActivity | undefined): boolean {
  return activity !== undefined && activity >= ChatActivity.RUNNING && activity !== ChatActivity.QUEUED_FOR_MACHINE;
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
