/**
 * A chat's plan and its tasks: PlanService and TaskService on the generated
 * hooks and keys, kept fresh by the chat stream's `plan_tasks` refetch.
 *
 * The cache holds what the generated hooks fetch (GetPlanByChatIdResponse,
 * ListTasksResponse); the panels read TaskItem, a view of a proto Task with
 * its status as a string, mapped from that cache entry.
 */

import { TaskStatus as ProtoTaskStatus, type ListTasksResponse, type Task } from "../gen/reliant/v1/task_pb";
import { queryClient } from "../lib/query-client";
import { planServiceKeys, useGetPlanByChatId } from "./plan-service-hooks_gen";
import { taskServiceKeys, useListTasks } from "./task-service-hooks_gen";

export type TaskStatus =
  | "pending"
  | "in_progress"
  | "completed"
  | "failed"
  | "blocked"
  | "skipped"
  | "cancelled";

export interface TaskItem {
  id: string;
  title: string;
  status: TaskStatus;
  description?: string;
  updatedAt?: string;
  createdAt?: string;
  parentId?: string;
  planId?: string;
  position?: number;
}

// ── Proto mapping ───────────────────────────────────────────────────────────

function taskStatusFromProto(status: ProtoTaskStatus): TaskStatus {
  switch (status) {
    case ProtoTaskStatus.PENDING:
      return "pending";
    case ProtoTaskStatus.IN_PROGRESS:
      return "in_progress";
    case ProtoTaskStatus.COMPLETED:
      return "completed";
    case ProtoTaskStatus.FAILED:
      return "failed";
    case ProtoTaskStatus.BLOCKED:
      return "blocked";
    case ProtoTaskStatus.SKIPPED:
      return "skipped";
    case ProtoTaskStatus.CANCELLED:
      return "cancelled";
    case ProtoTaskStatus.UNSPECIFIED:
    default:
      return "pending";
  }
}

function taskItemFromProto(task: Task): TaskItem {
  return {
    id: task.id,
    title: task.title,
    status: taskStatusFromProto(task.status),
    description: task.description || undefined,
    updatedAt: task.updatedAt,
    createdAt: task.createdAt,
    parentId: task.parentTaskId || undefined,
    planId: task.planId,
    position: task.position,
  };
}

// ── Query hooks ─────────────────────────────────────────────────────────────

/** The plan for a chat (`data.plan`, unset when the chat has none). */
export function usePlanForChat(chatId?: string | null) {
  return useGetPlanByChatId({ chatId: chatId ?? "" }, { enabled: !!chatId });
}

/**
 * Every task of a chat's plan, as TaskItems (resolves the plan first). The
 * query is the generated useListTasks; `data` is its tasks, mapped.
 */
export function useTasksForChat(chatId?: string | null) {
  const planId = usePlanForChat(chatId).data?.plan?.id;
  return useListTasks({ planId }, { enabled: !!planId, select: taskItemsOf });
}

// Module-level, so React Query re-maps only when the response changes.
const taskItemsOf = (response: ListTasksResponse) => response.tasks.map(taskItemFromProto);

/** Derived stats from tasks for a chat. */
export function useTaskStats(chatId?: string | null) {
  const { data: tasks } = useTasksForChat(chatId);

  const total = tasks?.length ?? 0;
  const completed = tasks?.filter((t) => t.status === "completed").length ?? 0;
  const inProgress = tasks?.filter((t) => t.status === "in_progress").length ?? 0;
  const pending = tasks?.filter((t) => t.status === "pending").length ?? 0;
  const percent = total > 0 ? Math.round((completed / total) * 100) : 0;

  return { total, completed, inProgress, pending, percent };
}

// ── Stream ──────────────────────────────────────────────────────────────────

/**
 * The chat stream's `plan_tasks` refetch: the server emits it whenever a
 * chat's plan or its tasks change (the plan/task tools, PlanService writes).
 * It carries no payload, so it marks both services' reads stale; only the
 * mounted ones refetch.
 */
export function invalidatePlanTasks(): void {
  void queryClient.invalidateQueries({ queryKey: planServiceKeys.all });
  void queryClient.invalidateQueries({ queryKey: taskServiceKeys.all });
}
