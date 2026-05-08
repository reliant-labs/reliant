import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { planGrpc } from "../api/plan-grpc";
import {
  taskGrpc,
  type Task as V2Task,
  TaskStatus as ProtoTaskStatus,
} from "../api/task-grpc";

// Re-export types that consumers need
export type { Plan } from "../api/plan-grpc";

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

// ── Key factory ─────────────────────────────────────────────────────────────

export const taskKeys = {
  all: ["tasks"] as const,
  plans: () => [...taskKeys.all, "plans"] as const,
  plan: (chatId: string) => [...taskKeys.plans(), chatId] as const,
  lists: () => [...taskKeys.all, "list"] as const,
  list: (planId: string) => [...taskKeys.lists(), planId] as const,
};

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

function taskStatusToProto(status?: TaskStatus): ProtoTaskStatus | undefined {
  if (!status) return undefined;
  switch (status) {
    case "pending":
      return ProtoTaskStatus.PENDING;
    case "in_progress":
      return ProtoTaskStatus.IN_PROGRESS;
    case "completed":
      return ProtoTaskStatus.COMPLETED;
    case "failed":
      return ProtoTaskStatus.FAILED;
    case "blocked":
      return ProtoTaskStatus.BLOCKED;
    case "skipped":
      return ProtoTaskStatus.SKIPPED;
    case "cancelled":
      return ProtoTaskStatus.CANCELLED;
    default:
      return ProtoTaskStatus.PENDING;
  }
}

function v2TaskToTaskItem(task: V2Task): TaskItem {
  return {
    id: task.id,
    title: task.title,
    status: taskStatusFromProto(task.status),
    description: task.description || undefined,
    updatedAt: task.updated_at,
    createdAt: task.created_at,
    parentId: task.parent_task_id || undefined,
    planId: task.plan_id,
    position: task.position,
  };
}

// ── Query hooks ─────────────────────────────────────────────────────────────

/** Fetch the plan for a chat. Returns null when no plan exists. */
export function usePlanForChat(chatId?: string | null) {
  return useQuery({
    queryKey: taskKeys.plan(chatId!),
    queryFn: () => planGrpc.getByChatId(chatId!),
    enabled: !!chatId,
    staleTime: 30_000,
  });
}

/** Fetch all tasks for a chat (resolves plan automatically). */
export function useTasksForChat(chatId?: string | null) {
  const planQuery = usePlanForChat(chatId);
  const planId = planQuery.data?.id;

  return useQuery({
    queryKey: taskKeys.list(planId!),
    queryFn: async () => {
      const { tasks } = await taskGrpc.list(planId!);
      return tasks.map(v2TaskToTaskItem);
    },
    enabled: !!planId,
    staleTime: 30_000,
  });
}

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

// ── Mutation hooks ──────────────────────────────────────────────────────────

export function useCreateTask() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (vars: {
      plan_id: string;
      title: string;
      description?: string;
    }) => taskGrpc.create(vars),
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: taskKeys.list(vars.plan_id) });
    },
  });
}

export function useUpdateTask() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (vars: {
      taskId: string;
      planId: string;
      updates: {
        title?: string;
        description?: string;
        status?: TaskStatus;
        position?: number;
      };
    }) =>
      taskGrpc.update(vars.taskId, {
        ...vars.updates,
        status: taskStatusToProto(vars.updates.status),
      }),
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: taskKeys.list(vars.planId) });
    },
  });
}

export function useDeleteTask() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (vars: { taskId: string; planId: string }) =>
      taskGrpc.delete(vars.taskId),
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: taskKeys.list(vars.planId) });
    },
  });
}
