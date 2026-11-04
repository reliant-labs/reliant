/**
 * Thread navigation derived from a chat's thread list: which thread is in
 * view, whether there is more than one to pick from, and where "back" goes.
 *
 * Shared by the desktop `ChatHeader` (thread tabs + back arrow) and the mobile
 * top bar and Chat sheet, so both shells agree on what "back to the parent
 * thread" means.
 */

import { useMemo } from "react";
import type { ThreadInfo } from "./thread-views";

export interface ThreadNavigation {
  /** Threads a picker offers: spawned sub-agents are reached from their spawn row, not a tab. */
  pickableThreads: ThreadInfo[];
  /** More than the main thread to pick from. */
  hasMultipleThreads: boolean;
  /** The thread in view — may be a spawn thread, which is not pickable. */
  selectedThread: ThreadInfo | undefined;
  /** Viewing a thread other than main (or the all-threads view). */
  isInSubThread: boolean;
  /**
   * Where back goes from a sub-thread: the parent when it is a known non-main
   * thread, else the default chat view (null).
   */
  backTarget: ThreadInfo | undefined;
  backTargetId: string | null;
}

export function useThreadNavigation(
  allThreads: ThreadInfo[],
  selectedThreadId: string | null | undefined,
): ThreadNavigation {
  return useMemo(() => {
    const pickableThreads = allThreads.filter((t) => !t.isSpawn);
    const selectedThread = selectedThreadId ? allThreads.find((t) => t.id === selectedThreadId) : undefined;
    const backTarget = selectedThread?.parentThread
      ? allThreads.find((t) => t.id === selectedThread.parentThread && !t.isMain)
      : undefined;
    return {
      pickableThreads,
      hasMultipleThreads: pickableThreads.some((t) => !t.isMain),
      selectedThread,
      isInSubThread: Boolean(selectedThread && !selectedThread.isMain),
      backTarget,
      backTargetId: backTarget?.id ?? null,
    };
  }, [allThreads, selectedThreadId]);
}
