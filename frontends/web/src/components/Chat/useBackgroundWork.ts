/**
 * Derives the "background work" summary shown in BackgroundWorkPill.
 *
 * Two independent sources, deliberately kept separate all the way to the UI
 * because they behave differently:
 *
 *   - Spawns are chat-scoped. The live chat stream supplies immediate rows,
 *     while the workflow execution tree is the durable title/lifecycle source.
 *   - Background bash processes are worktree-scoped and read from
 *     BackgroundService (useBackgroundProcesses, kept live by the stream),
 *     and a single worktree is shared by every chat open against it.
 *
 * Failed spawns come from the workflow tree alone. A sub-agent whose step
 * fails stops on its own — the rest of the chat keeps running — and reports to
 * its parent, so the failure belongs here, on the agent, rather than in a
 * chat-wide banner. It is stopped, not finished: a message resumes it where it
 * stopped, even once the chat has gone idle.
 *
 * The blocked attribution is the reason this hook exists rather than a pair
 * of counters. A spawned agent that is waiting on a question or a tool
 * approval has stopped making progress and needs the user, but the spawn
 * itself is often scrolled far up the timeline by the time that happens.
 * Approvals do not carry a thread id on the wire, so they cannot be pinned
 * to a specific spawn; pending questions carry their thread_id (and
 * workflow_id), so those we attribute exactly.
 *
 * Every pending question in the chat is considered, not "the chat's
 * question": each thread can park on its own, and the composer answers only
 * the one for the thread in view. A question on any OTHER thread that is not
 * already a spawn row here (the main thread while a spawn is in view, or a
 * thread with no live spawn record) is listed as a waiting question, so the
 * user can always get to it.
 */

import { useMemo } from "react";
import { useActiveThreads } from "../../store/threadActivityStore";
import { BackgroundProcessStatus, useBackgroundProcesses } from "../../hooks/background-queries";
import { usePendingQuestions } from "../../hooks/question-queries";
import { questionThreadId, viewedThreadId } from "../../lib/pendingQuestions";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { isWorkflowLive, workflowStoppedBecause } from "../../lib/workflowLifecycle";
import { WorkflowStopReason } from "../../gen/reliant/v1/chat_pb";
import type { WorkflowExecutionData } from "../../types/chat";
import type { ActiveThreadUpdate } from "../../types/streaming";
import { isSpawnOrigin } from "./thread-views/threadUtils";
import { getActivityDisplayText } from "../../store/threadActivityStore";

export interface ActiveSpawn {
  threadId: string;
  title: string;
  /** Tool call that launched this spawn, used for cancellation. */
  toolCallId?: string;
  /** Human-readable current step, e.g. "Thinking" / "Running tools". */
  activity: string | null;
  startedAt: number | null;
  /** True when this spawn is waiting on the user and has stopped progressing. */
  isBlocked: boolean;
  blockReason?: string;
}

/**
 * A spawned agent whose latest run failed and has not been resumed: it
 * stopped on an error, and a message to it resumes it where it stopped.
 *
 * Only that agent stopped: its parent was told (an agent_result with
 * status="failed") and decides what happens next. Retrying from here asks the
 * parent to resume it, so the parent's plan never has an agent restarted
 * behind its back.
 */
export interface FailedSpawn {
  /** The failed run's workflow row. A resumption is a new row on the same thread. */
  workflowId: string;
  threadId: string;
  title: string;
  /** The thread that spawned this agent, which is the one asked to resume it. */
  parentThreadId: string | null;
  failedAt: number | null;
}

/**
 * A question waiting on the user on a thread that is neither in view (the
 * composer already shows that one) nor listed as a live spawn row.
 */
export interface WaitingQuestion {
  questionId: string;
  /** The asking thread; the chat id for the main thread. */
  threadId: string;
  isMainThread: boolean;
  title: string;
}

export interface ActiveCommand {
  id: string;
  command: string;
  startedAt: number | null;
}

export interface BackgroundWork {
  spawns: ActiveSpawn[];
  /** Agents that failed recently and are waiting on a decision; see FailedSpawn. */
  failedSpawns: FailedSpawn[];
  commands: ActiveCommand[];
  /** Spawns waiting on the user — the subset worth interrupting for. */
  blockedSpawns: ActiveSpawn[];
  /** Questions on other threads that no spawn row above already shows. */
  waitingQuestions: WaitingQuestion[];
  hasWork: boolean;
}

const EMPTY: BackgroundWork = {
  spawns: [],
  failedSpawns: [],
  commands: [],
  blockedSpawns: [],
  waitingQuestions: [],
  hasWork: false,
};

function parseTime(value: string | undefined): number | null {
  if (!value) return null;
  const parsed = new Date(value).getTime();
  return Number.isNaN(parsed) ? null : parsed;
}

function nonGenericTitle(value: string | undefined): string | undefined {
  if (!value?.trim()) return undefined;
  const title = value.trim();
  const normalized = title.toLowerCase();
  if (normalized === "agent" || normalized === "builtin://agent" || normalized === "spawn_tool") {
    return undefined;
  }
  return title;
}

function workflowTitle(workflow: WorkflowExecutionData | undefined): string | undefined {
  if (!workflow) return undefined;
  return (
    nonGenericTitle(workflow.threadTitle) ||
    nonGenericTitle(workflow.spawnedByNodeId) ||
    nonGenericTitle(workflow.workflowName)
  );
}

function threadTitle(thread: ActiveThreadUpdate): string {
  return (
    nonGenericTitle(thread.thread_title) ||
    nonGenericTitle(thread.title) ||
    nonGenericTitle(thread.agent_name) ||
    "Agent"
  );
}

function indexSpawnWorkflows(workflows: WorkflowExecutionData[]): {
  byId: Map<string, WorkflowExecutionData>;
  byThread: Map<string, WorkflowExecutionData>;
} {
  const byId = new Map<string, WorkflowExecutionData>();
  const byThread = new Map<string, WorkflowExecutionData>();

  const visit = (workflow: WorkflowExecutionData) => {
    if (isSpawnOrigin(workflow.origin)) {
      byId.set(workflow.id, workflow);
      if (workflow.thread) byThread.set(workflow.thread, workflow);
    }
    for (const child of workflow.children || []) visit(child);
  };

  for (const workflow of workflows) visit(workflow);
  return { byId, byThread };
}

/**
 * How long a failed agent stays in the strip. A failure is a call for a
 * decision about the current work, not a permanent record — the spawn card in
 * the transcript keeps that — so one from yesterday is not "background work".
 */
export const FAILED_SPAWN_VISIBLE_MS = 24 * 60 * 60 * 1000;

/**
 * The spawned agents whose latest run failed. A thread's newest spawn run
 * decides it: spawn(agent_id=...) resumes an agent as a new run on the same
 * thread, so a failure that was resumed is superseded and no longer shown.
 */
function failedSpawnsOf(workflows: WorkflowExecutionData[], now: number): FailedSpawn[] {
  const latestByThread = new Map<string, { workflow: WorkflowExecutionData; rootThread: string }>();

  const visit = (workflow: WorkflowExecutionData, rootThread: string) => {
    if (isSpawnOrigin(workflow.origin) && workflow.thread) {
      const current = latestByThread.get(workflow.thread);
      if (!current || (parseTime(workflow.createdAt) ?? 0) >= (parseTime(current.workflow.createdAt) ?? 0)) {
        latestByThread.set(workflow.thread, { workflow, rootThread });
      }
    }
    for (const child of workflow.children || []) visit(child, rootThread);
  };
  for (const root of workflows) visit(root, root.thread);

  const failed: FailedSpawn[] = [];
  for (const { workflow, rootThread } of latestByThread.values()) {
    if (!workflowStoppedBecause(workflow.state, workflow.stopReason, WorkflowStopReason.FAILED)) continue;
    const failedAt = parseTime(workflow.completedAt) ?? parseTime(workflow.createdAt);
    if (failedAt !== null && now - failedAt > FAILED_SPAWN_VISIBLE_MS) continue;
    failed.push({
      workflowId: workflow.id,
      threadId: workflow.thread,
      title: workflowTitle(workflow) || "Agent",
      parentThreadId: workflow.parentThread || rootThread || null,
      failedAt,
    });
  }
  return failed.sort((a, b) => (b.failedAt ?? 0) - (a.failedAt ?? 0));
}

/**
 * What the strip's Retry sends to a failed agent's parent. It names the
 * resume handle the parent's failed agent_result already carries, so the
 * request maps onto one tool call rather than leaving the parent to guess.
 */
export function failedAgentRetryMessage(spawn: Pick<FailedSpawn, "title" | "threadId">): string {
  return (
    `Retry the agent "${spawn.title}" that stopped on an error: resume it where it stopped with ` +
    `spawn(agent_id="${spawn.threadId}", prompt=...), then carry on.`
  );
}

function workflowForThread(
  thread: ActiveThreadUpdate,
  workflows: ReturnType<typeof indexSpawnWorkflows>,
): WorkflowExecutionData | undefined {
  if (thread.workflow_id) {
    const byId = workflows.byId.get(thread.workflow_id);
    if (byId) return byId;
  }
  return workflows.byThread.get(thread.thread);
}

/** Find a thread's workflow anywhere in the tree (spawned or not). */
function workflowOfThread(
  workflows: WorkflowExecutionData[],
  threadId: string,
): WorkflowExecutionData | undefined {
  for (const workflow of workflows) {
    if (workflow.thread === threadId) return workflow;
    const nested = workflowOfThread(workflow.children || [], threadId);
    if (nested) return nested;
  }
  return undefined;
}

export function useBackgroundWork(
  chatId: string | undefined,
  worktreeId: string | undefined,
  /** The thread in view (null/undefined = main): its question is the composer's. */
  selectedThreadId?: string | null,
  /** The chat's main thread id (its root workflow id), when known. */
  mainThreadId?: string | null,
): BackgroundWork {
  const activeThreads = useActiveThreads(chatId ?? "");
  const { data: processes } = useBackgroundProcesses();
  const pendingQuestions = usePendingQuestions(chatId).data?.questions;
  const { allWorkflows } = useWorkflowExecutions(chatId ?? null);

  return useMemo(() => {
    if (!chatId) return EMPTY;

    const questions = pendingQuestions ?? [];
    const questionThreads = new Set(questions.map((q) => questionThreadId(q, chatId, mainThreadId)));
    const questionWorkflows = new Set(questions.map((q) => q.workflowId).filter(Boolean));

    const spawnWorkflows = indexSpawnWorkflows(allWorkflows);
    const spawns: ActiveSpawn[] = [];
    for (const thread of activeThreads) {
      if (!isSpawnOrigin(thread.origin)) continue;
      if (thread.status !== "running" && thread.status !== "active") continue;
      if (!thread.thread) continue;

      const workflow = workflowForThread(thread, spawnWorkflows);
      if (workflow && !isWorkflowLive(workflow.state, workflow.stopReason)) {
        continue;
      }

      // A pending question names the thread (and workflow) that asked it, so
      // the block attributes to one exact spawn rather than lighting up every
      // running child.
      const isBlocked =
        questionThreads.has(thread.thread) ||
        Boolean(thread.workflow_id && questionWorkflows.has(thread.workflow_id)) ||
        Boolean(workflow?.id && questionWorkflows.has(workflow.id));

      spawns.push({
        threadId: thread.thread,
        title: workflowTitle(workflow) || threadTitle(thread),
        toolCallId: thread.spawned_by_tool_call_id,
        activity: getActivityDisplayText(thread.current_activity ?? null),
        startedAt: parseTime(thread.created_at || workflow?.createdAt),
        isBlocked,
        blockReason: isBlocked ? "Waiting on your answer" : undefined,
      });
    }

    // Each spawn's own lifecycle is the authority — NOT the chat's.
    //
    // This deliberately differs from useActiveThreadIds, which gates thread
    // activity on the chat being RUNNING. That gate is right for threads whose
    // work is the chat's current turn, and wrong for async spawns, which exist
    // precisely to outlive the turn that launched them: the parent finishes
    // its message and goes idle while the children keep working. Gating these
    // on the chat would hide exactly the case the pill was built for.
    //
    // Prefer the workflow row when it exists, because it is durable and gets
    // refetched after terminal child-workflow events. Keep stream-only rows
    // too, because an async spawn can be launched and visible before the
    // workflow tree refetch catches up.
    const liveSpawns = spawns;

    // Questions no spawn row speaks for, minus the one in the composer. One
    // entry per thread: a thread waits on one question at a time.
    const inView = viewedThreadId(chatId, selectedThreadId, mainThreadId);
    const spawnRows = new Set(liveSpawns.map((spawn) => spawn.threadId));
    const waitingByThread = new Map<string, WaitingQuestion>();
    for (const question of questions) {
      const threadId = questionThreadId(question, chatId, mainThreadId);
      if (threadId === inView || spawnRows.has(threadId)) continue;
      const isMainThread = threadId === chatId;
      waitingByThread.set(threadId, {
        questionId: question.questionId,
        threadId,
        isMainThread,
        title: isMainThread
          ? "Main thread"
          : workflowTitle(workflowOfThread(allWorkflows, threadId)) || "Agent",
      });
    }
    const waitingQuestions = [...waitingByThread.values()];

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
    const failedSpawns = failedSpawnsOf(allWorkflows, Date.now());

    return {
      spawns: liveSpawns,
      failedSpawns,
      commands,
      blockedSpawns,
      waitingQuestions,
      hasWork: liveSpawns.length > 0 || commands.length > 0,
    };
  }, [
    chatId,
    worktreeId,
    selectedThreadId,
    mainThreadId,
    activeThreads,
    processes,
    pendingQuestions,
    allWorkflows,
  ]);
}
