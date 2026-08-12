/**
 * Derives the "background work" summary shown in BackgroundWorkPill.
 *
 * Two independent sources, deliberately kept separate all the way to the UI
 * because they behave differently:
 *
 *   - Spawns are chat-scoped and arrive live on the chat stream
 *     (threadActivityStore), so they need no fetch and no polling.
 *   - Background bash processes are worktree-scoped and polled through
 *     processStore, and a single worktree is shared by every chat open
 *     against it.
 *
 * The blocked attribution is the reason this hook exists rather than a pair
 * of counters. A spawned agent that is waiting on a question or a tool
 * approval has stopped making progress and needs the user, but the spawn
 * itself is often scrolled far up the timeline by the time that happens.
 * Approvals do not carry a thread id on the wire, so they cannot be pinned
 * to a specific spawn; pending questions carry workflow_id, which the thread
 * records also carry, so those we can attribute exactly.
 */

import { useMemo } from "react";
import { useActiveThreads } from "../../store/threadActivityStore";
import { useProcessStore } from "../../store/processStore";
import { BackgroundProcessStatus } from "../../api/background-grpc";
import { usePendingQuestion } from "../../hooks/approval-queries";
import { isSpawnOrigin } from "./thread-views/threadUtils";
import { getActivityDisplayText } from "../../store/threadActivityStore";

export interface ActiveSpawn {
  threadId: string;
  title: string;
  /** Human-readable current step, e.g. "Thinking" / "Running tools". */
  activity: string | null;
  startedAt: number | null;
  /** True when this spawn is waiting on the user and has stopped progressing. */
  isBlocked: boolean;
  blockReason?: string;
}

export interface ActiveCommand {
  id: string;
  command: string;
  startedAt: number | null;
}

export interface BackgroundWork {
  spawns: ActiveSpawn[];
  commands: ActiveCommand[];
  /** Spawns waiting on the user — the subset worth interrupting for. */
  blockedSpawns: ActiveSpawn[];
  hasWork: boolean;
}

const EMPTY: BackgroundWork = {
  spawns: [],
  commands: [],
  blockedSpawns: [],
  hasWork: false,
};

function parseTime(value: string | undefined): number | null {
  if (!value) return null;
  const parsed = new Date(value).getTime();
  return Number.isNaN(parsed) ? null : parsed;
}

export function useBackgroundWork(
  chatId: string | undefined,
  worktreeId: string | undefined,
): BackgroundWork {
  const activeThreads = useActiveThreads(chatId ?? "");
  const processes = useProcessStore((state) => state.processes);
  const { data: pendingQuestion } = usePendingQuestion(chatId);

  const questionWorkflowId = pendingQuestion?.workflow_id;

  return useMemo(() => {
    if (!chatId) return EMPTY;

    const spawns: ActiveSpawn[] = [];
    for (const thread of activeThreads) {
      if (!isSpawnOrigin(thread.origin)) continue;
      if (thread.status !== "running" && thread.status !== "active") continue;
      if (!thread.thread) continue;

      // A pending question names the workflow that asked it, and thread
      // records carry the same workflow id — so the block attributes to one
      // exact spawn rather than lighting up every running child.
      const isBlocked = Boolean(
        questionWorkflowId && thread.workflow_id === questionWorkflowId,
      );

      spawns.push({
        threadId: thread.thread,
        title: thread.thread_title || thread.title || thread.agent_name || "Agent",
        activity: getActivityDisplayText(thread.current_activity ?? null),
        startedAt: parseTime(thread.created_at),
        isBlocked,
        blockReason: isBlocked ? "Waiting on your answer" : undefined,
      });
    }

    // Each spawn's own status is the authority — NOT the chat's.
    //
    // This deliberately differs from useActiveThreadIds, which gates thread
    // activity on the chat being RUNNING. That gate is right for threads whose
    // work is the chat's current turn, and wrong for async spawns, which exist
    // precisely to outlive the turn that launched them: the parent finishes
    // its message and goes idle while the children keep working. Gating these
    // on the chat would hide exactly the case the pill was built for.
    //
    // A spawn's record going stale is bounded by the thread status itself —
    // a child that ends for any reason (success, failure, cancellation)
    // reports a terminal status, which drops it from this list.
    const liveSpawns = spawns;

    const commands: ActiveCommand[] = [];
    for (const process of processes) {
      if (process.status !== BackgroundProcessStatus.RUNNING) continue;
      // Processes are worktree-scoped and a worktree is shared across chats.
      // Prefer the process's own chat_id when the daemon set one; fall back to
      // the worktree so commands started before chat attribution still show.
      if (process.chat_id && process.chat_id !== chatId) continue;
      if (!process.chat_id && worktreeId && process.worktree_id !== worktreeId) {
        continue;
      }

      commands.push({
        id: process.id,
        command: process.command,
        startedAt: parseTime(process.start_time),
      });
    }

    const blockedSpawns = liveSpawns.filter((s) => s.isBlocked);

    return {
      spawns: liveSpawns,
      commands,
      blockedSpawns,
      hasWork: liveSpawns.length > 0 || commands.length > 0,
    };
  }, [
    chatId,
    worktreeId,
    activeThreads,
    processes,
    questionWorkflowId,
  ]);
}
