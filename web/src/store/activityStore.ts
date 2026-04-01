/**
 * Activity Store — single source of truth for chat activity state.
 *
 * Each chat has exactly one ChatActivity value (from proto).
 * The sidebar, thinking indicator, and all other consumers read from here.
 *
 * Populated from:
 *   1. ListChats response (chat.activity field)
 *   2. chat_activity_changed streaming events (globalUpdatesStore)
 */

import { create } from "zustand";
import { ChatActivity } from "../gen/reliant/v1/chat_pb";

// Re-export the proto enum so consumers don't need to import from gen/
export { ChatActivity } from "../gen/reliant/v1/chat_pb";

// Map activity enum to dot states for the sidebar ActivityDot component
export type DotState = "idle" | "thinking" | "awaiting_approval" | "error";

export function activityToDotState(activity: ChatActivity): DotState {
  switch (activity) {
    case ChatActivity.RUNNING:
      return "thinking";
    case ChatActivity.AWAITING_INPUT:
      return "awaiting_approval";
    case ChatActivity.ERROR:
      return "error";
    default:
      return "idle";
  }
}

/**
 * How long (ms) an optimistic non-IDLE value is protected from being
 * downgraded to IDLE by a server response.  After this window the server
 * value is considered authoritative — this prevents permanently-stuck
 * RUNNING states when a CHAT_ACTIVITY_CHANGED WebSocket event is missed.
 */
const ANTI_DOWNGRADE_WINDOW_MS = 10_000;

interface ActivityEntry {
  activity: ChatActivity;
  /** Epoch-ms when this value was written. */
  setAt: number;
}

interface ActivityState {
  entries: Map<string, ActivityEntry>;
  /** Convenience projection — just the activity values (for consumers). */
  activities: Map<string, ChatActivity>;
  setActivity: (chatId: string, activity: ChatActivity) => void;
  setActivities: (activities: Map<string, ChatActivity>) => void;
  removeActivity: (chatId: string) => void;
  /**
   * Returns true if `chatId` currently has a non-IDLE value that was set
   * within the anti-downgrade window (i.e. it's "fresh" and should be
   * protected from a server-side IDLE overwrite).
   */
  isFreshNonIdle: (chatId: string) => boolean;
}

function buildActivitiesProjection(
  entries: Map<string, ActivityEntry>,
): Map<string, ChatActivity> {
  const m = new Map<string, ChatActivity>();
  for (const [id, e] of entries) m.set(id, e.activity);
  return m;
}

export const useActivityStore = create<ActivityState>((set, get) => ({
  entries: new Map(),
  activities: new Map(),

  setActivity: (chatId, activity) =>
    set((state) => {
      // Skip if value hasn't changed — prevents unnecessary re-renders
      if (state.entries.get(chatId)?.activity === activity) return state;
      const next = new Map(state.entries);
      next.set(chatId, { activity, setAt: Date.now() });
      return { entries: next, activities: buildActivitiesProjection(next) };
    }),

  setActivities: (activities) => {
    const now = Date.now();
    const entries = new Map<string, ActivityEntry>();
    for (const [id, a] of activities) entries.set(id, { activity: a, setAt: now });
    set({ entries, activities });
  },

  removeActivity: (chatId) =>
    set((state) => {
      const next = new Map(state.entries);
      next.delete(chatId);
      return { entries: next, activities: buildActivitiesProjection(next) };
    }),

  isFreshNonIdle: (chatId) => {
    const entry = get().entries.get(chatId);
    if (!entry || entry.activity === ChatActivity.IDLE) return false;
    return Date.now() - entry.setAt < ANTI_DOWNGRADE_WINDOW_MS;
  },
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
