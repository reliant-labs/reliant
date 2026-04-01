import { create } from "zustand";
import {
  taskGrpc,
  type Task as V2Task,
  TaskStatus as ProtoTaskStatus,
} from "../api/task-grpc";
import {
  planGrpc,
  type Plan,
  PlanStatus,
  PlanComplexity,
} from "../api/plan-grpc";
import { logger } from "../lib/logger";

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

interface TasksState {
  tasksByChat: Record<string, Record<string, TaskItem>>; // chatId -> taskId -> task
  planIdByChat: Record<string, string>; // chatId -> planId (for API calls)
  plansByChat: Record<string, Plan>; // chatId -> Plan (cached plan data)
  planLoadedForChat: Record<string, boolean>; // chatId -> whether we've attempted to load plan (even if null)
  upsertTask: (chatId: string, task: TaskItem) => void;
  setPlanId: (chatId: string, planId: string) => void;
  setPlan: (chatId: string, plan: Plan | null) => void;
  getPlanForChat: (chatId: string) => Plan | undefined;
  hasPlanBeenLoaded: (chatId: string) => boolean;
  invalidatePlanCache: (chatId: string) => void; // Invalidate cache to force reload

  // API methods
  loadPlanAndTasks: (chatId: string) => Promise<void>; // New: load plan and tasks with caching
  fetchTasks: (chatId: string, planId: string) => Promise<void>;
  createTask: (chatId: string, planId: string, title: string, description?: string) => Promise<void>;
  updateTask: (chatId: string, taskId: string, updates: { title?: string; description?: string; status?: TaskStatus; position?: number }) => Promise<void>;
  deleteTask: (chatId: string, taskId: string) => Promise<void>;

  // Tool parsing methods (legacy support)
  processUpdateTaskContent: (chatId: string, content: string) => void;
  processAddTaskContent: (chatId: string, content: string) => void;
  processCreateSubtaskContent: (chatId: string, content: string) => void;
  processListTasksContent: (chatId: string, content: string) => void;
  processCreatePlanContent: (chatId: string, content: string) => void; // Process create_plan tool result

  getTasksForChat: (chatId?: string | null) => TaskItem[];
  getStatsForChat: (
    chatId?: string | null
  ) => { total: number; completed: number; pending: number; percent: number };
  resetChat: (chatId: string) => void;
  reset: () => void;
}

function normalizeTaskStatus(status?: string): TaskStatus | undefined {
  if (!status) return undefined;
  switch (status.toLowerCase()) {
    case "pending":
      return "pending";
    case "in_progress":
    case "in-progress":
      return "in_progress";
    case "completed":
      return "completed";
    case "failed":
      return "failed";
    case "blocked":
      return "blocked";
    case "skipped":
      return "skipped";
    case "cancelled":
    case "canceled":
      return "cancelled";
    default:
      return undefined;
  }
}

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

function planStatusFromUnknown(status: unknown): PlanStatus {
  if (typeof status === "number") {
    return status as PlanStatus;
  }
  if (typeof status === "string") {
    switch (status.toLowerCase()) {
      case "in_progress":
      case "in-progress":
        return PlanStatus.IN_PROGRESS;
      case "completed":
        return PlanStatus.COMPLETED;
      case "cancelled":
      case "canceled":
        return PlanStatus.CANCELLED;
      case "pending":
      default:
        return PlanStatus.PENDING;
    }
  }
  return PlanStatus.PENDING;
}

function planComplexityFromUnknown(
  complexity: unknown,
): PlanComplexity | undefined {
  if (typeof complexity === "number") {
    return complexity as PlanComplexity;
  }
  if (typeof complexity === "string") {
    switch (complexity.toLowerCase()) {
      case "simple":
        return PlanComplexity.SIMPLE;
      case "moderate":
        return PlanComplexity.MODERATE;
      case "complex":
        return PlanComplexity.COMPLEX;
      default:
        return undefined;
    }
  }
  return undefined;
}

// Convert V2Task to TaskItem
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

// Try to parse a plain-text update_task output like in the example
function parseUpdateTaskPlain(content: string): Partial<TaskItem> | null {
  const lines = content.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  if (lines.length === 0) return null;

  const result: Partial<TaskItem> = {};

  for (const line of lines) {
    // ID: <uuid>
    let m = line.match(/^ID\s*:\s*(.+)$/i);
    if (m) {
      result.id = m[1].trim();
      continue;
    }
    // Title: <text>
    m = line.match(/^Title\s*:\s*(.+)$/i);
    if (m) {
      result.title = m[1].trim();
      continue;
    }
    // Status: completed|in_progress|pending|...
    m = line.match(/^Status\s*:\s*(.+)$/i);
    if (m) {
      result.status = normalizeTaskStatus(m[1].trim()) || "pending";
      continue;
    }
    // Description: <text>
    m = line.match(/^Description\s*:\s*(.+)$/i);
    if (m) {
      result.description = m[1].trim();
      continue;
    }
  }

  // Fallbacks - don't default status, let caller preserve existing
  if (!result.title && lines[0]) {
    // Try to derive a title from the first non-meta line
    const firstContent = lines.find(
      (l) => !/^\w+\s*:\s*/.test(l) && !/success/i.test(l)
    );
    if (firstContent) result.title = firstContent.slice(0, 80);
  }

  return result.id ? result : null;
}

// Parse AddTaskTool (and CreateSubtaskTool) plain responses
function parseKeyValuePlain(content: string): Record<string, string> {
  const map: Record<string, string> = {};
  const lines = content.split(/\r?\n/);
  for (const line of lines) {
    const m = line.match(/^([A-Za-z ]+)\s*:\s*(.+)$/);
    if (m) {
      const key = m[1].trim().toLowerCase().replace(/\s+/g, "_");
      map[key] = m[2].trim();
    }
  }
  return map;
}

// Parse list_tasks output into a set of tasks
function parseListTasks(content: string): TaskItem[] {
  const tasks: TaskItem[] = [];
  const lines = content.split(/\r?\n/);
  let lastTask: TaskItem | null = null;

  for (const raw of lines) {
    const line = raw.trim();
    // Match lines like: "1. [<id>] 🔄 Title here [in_progress]"
    const m = line.match(/^\d+\.\s*\[([^\]]+)\]\s+\S+\s+(.+?)\s*\[([^\]]+)\]$/);
    if (m) {
      const id = m[1];
      const title = m[2];
      const status = normalizeTaskStatus(m[3]) || "pending";
      const task: TaskItem = { id, title, status };
      tasks.push(task);
      lastTask = task;
      continue;
    }

    // Description lines: "Description: <text>"
    const d = line.match(/^Description\s*:\s*(.+)$/i);
    if (d && lastTask) {
      lastTask.description = d[1];
    }
  }
  return tasks;
}

// In-flight promise dedup for loadPlanAndTasks
const loadPlanInflight = new Map<string, Promise<void>>();

export const useTasksStore = create<TasksState>((set, get) => ({
  tasksByChat: {},
  planIdByChat: {},
  plansByChat: {},
  planLoadedForChat: {},

  upsertTask: (chatId: string, task: TaskItem) => {
    set((state) => {
      const byChat = state.tasksByChat[chatId] || {};
      const existingTask = byChat[task.id];
      const now = new Date().toISOString();

      const updatedTask = {
        ...existingTask,
        ...task,
        // Use provided updatedAt or fall back to now
        updatedAt: task.updatedAt || now,
        // Set createdAt only if this is a new task
        createdAt: existingTask?.createdAt || task.createdAt || now
      };

      const updatedByChat = {
        ...byChat,
        [task.id]: updatedTask
      };
      return { tasksByChat: { ...state.tasksByChat, [chatId]: updatedByChat } };
    });
  },

  setPlanId: (chatId: string, planId: string) => {
    set((state) => ({
      planIdByChat: { ...state.planIdByChat, [chatId]: planId }
    }));
  },

  setPlan: (chatId: string, plan: Plan | null) => {
    set((state) => {
      const newState: Partial<TasksState> = {
        planLoadedForChat: { ...state.planLoadedForChat, [chatId]: true },
      };
      if (plan) {
        newState.plansByChat = { ...state.plansByChat, [chatId]: plan };
        newState.planIdByChat = { ...state.planIdByChat, [chatId]: plan.id };
      }
      return newState;
    });
  },

  getPlanForChat: (chatId: string) => {
    return get().plansByChat[chatId];
  },

  hasPlanBeenLoaded: (chatId: string) => {
    return !!get().planLoadedForChat[chatId];
  },

  invalidatePlanCache: (chatId: string) => {
    set((state) => {
      const nextPlanLoaded = { ...state.planLoadedForChat };
      delete nextPlanLoaded[chatId];
      return { planLoadedForChat: nextPlanLoaded };
    });
  },

  // Load plan and tasks with caching - only fetches if not already loaded
  loadPlanAndTasks: async (chatId: string) => {
    // Check if we've already loaded the plan for this chat
    if (get().hasPlanBeenLoaded(chatId)) {
      return;
    }

    // Deduplicate concurrent calls for the same chatId
    const inflight = loadPlanInflight.get(chatId);
    if (inflight) {
      return inflight;
    }

    const promise = (async () => {
      try {
        // Fetch plan from API
        const plan = await planGrpc.getByChatId(chatId);
        
        // Store the plan (or mark as loaded with null)
        get().setPlan(chatId, plan);

        if (plan) {
          // Fetch tasks for the plan
          await get().fetchTasks(chatId, plan.id);
        }
      } catch (error) {
        logger.error("[TasksStore] Failed to load plan/tasks:", error);
        // Mark as loaded even on error to prevent infinite retries
        set((state) => ({
          planLoadedForChat: { ...state.planLoadedForChat, [chatId]: true },
        }));
        throw error;
      } finally {
        loadPlanInflight.delete(chatId);
      }
    })();

    loadPlanInflight.set(chatId, promise);
    return promise;
  },

  // Fetch all tasks for a plan from the gRPC API
  fetchTasks: async (chatId: string, planId: string) => {
    try {
      const response = await taskGrpc.list(planId);

      // Store plan ID for this chat
      get().setPlanId(chatId, planId);

      // Clear existing tasks for this chat and add new ones
      set((state) => ({
        tasksByChat: {
          ...state.tasksByChat,
          [chatId]: response.tasks.reduce((acc, task) => {
            acc[task.id] = v2TaskToTaskItem(task);
            return acc;
          }, {} as Record<string, TaskItem>)
        }
      }));
    } catch (error) {
      logger.error("[TasksStore] Failed to fetch tasks:", error);
      throw error;
    }
  },

  // Create a new task via gRPC API
  createTask: async (chatId: string, planId: string, title: string, description?: string) => {
    try {
      const task = await taskGrpc.create({
        plan_id: planId,
        title,
        description,
        status: ProtoTaskStatus.PENDING,
      });

      // Add to local store
      get().upsertTask(chatId, v2TaskToTaskItem(task));
    } catch (error) {
      logger.error("[TasksStore] Failed to create task:", error);
      throw error;
    }
  },

  // Update a task via gRPC API
  updateTask: async (chatId: string, taskId: string, updates: { title?: string; description?: string; status?: TaskStatus; position?: number }) => {
    try {

      // Convert local string status to proto enum for gRPC API
      const apiUpdates = {
        ...updates,
        status: taskStatusToProto(updates.status),
      };

      const task = await taskGrpc.update(taskId, apiUpdates);

      // Update local store
      get().upsertTask(chatId, v2TaskToTaskItem(task));
    } catch (error) {
      logger.error("[TasksStore] Failed to update task:", error);
      throw error;
    }
  },

  // Delete a task via gRPC API
  deleteTask: async (chatId: string, taskId: string) => {
    try {
      await taskGrpc.delete(taskId);

      // Remove from local store
      set((state) => {
        const byChat = { ...state.tasksByChat[chatId] };
        delete byChat[taskId];
        return {
          tasksByChat: {
            ...state.tasksByChat,
            [chatId]: byChat
          }
        };
      });
    } catch (error) {
      logger.error("[TasksStore] Failed to delete task:", error);
      throw error;
    }
  },

  processUpdateTaskContent: (chatId: string, content: string) => {
    if (!content || !chatId) return;

    // Try JSON first
    try {
      const data = JSON.parse(content);
      if (data && typeof data === "object") {
        const id = (data.id || data.task_id) as string | undefined;
        if (id) {
          const title = (data.title || data.name) as string | undefined;
          // Only set status if explicitly provided in the result
          // Don't default to "in_progress" as this would overwrite the intended status
          const status = normalizeTaskStatus(
            data.status ? String(data.status) : undefined,
          );
          const description = (data.description || data.details) as
            | string
            | undefined;
          
          // Build update object - only include fields that are present
          const update: TaskItem = {
            id,
            title: title || id,
            status: status || get().tasksByChat[chatId]?.[id]?.status || "pending",
            updatedAt: new Date().toISOString(),
          };
          if (description !== undefined) update.description = description;
          
          get().upsertTask(chatId, update);
          return;
        }
      }
    } catch {
      // not JSON; fall back to plain text
    }

    const parsed = parseUpdateTaskPlain(content);
    if (parsed && parsed.id) {
      // Only use parsed status if actually found in the content
      const existingTask = get().tasksByChat[chatId]?.[parsed.id];
      get().upsertTask(chatId, {
        id: parsed.id,
        title: parsed.title || existingTask?.title || parsed.id,
        status: parsed.status || existingTask?.status || "pending",
        description: parsed.description,
        updatedAt: new Date().toISOString(),
      });
    }
  },

  processAddTaskContent: (chatId: string, content: string) => {
    if (!content || !chatId) return;
    try {
      const data = JSON.parse(content);
      if (data && typeof data === "object") {
        const id = (data.id || data.task_id) as string | undefined;
        if (id) {
          get().upsertTask(chatId, {
            id,
            title: (data.title || id) as string,
            status: normalizeTaskStatus(String(data.status || "pending")) || "pending",
            description: (data.description || "") as string,
            parentId: (data.parent_id || data.parent_task_id) as string | undefined,
            planId: (data.plan_id || "") as string | undefined,
            updatedAt: new Date().toISOString(),
          });
          return;
        }
      }
    } catch {
      // Failed to parse JSON, continue with plain text parsing
    }

    const kv = parseKeyValuePlain(content);
    if (kv.id) {
      get().upsertTask(chatId, {
        id: kv.id,
        title: kv.title || kv.name || kv.id,
        status: normalizeTaskStatus(kv.status) || "pending",
        description: kv.description,
        parentId: kv.parent_id,
        planId: kv.plan,
        updatedAt: new Date().toISOString(),
      });
    }
  },

  processCreateSubtaskContent: (chatId: string, content: string) => {
    if (!content || !chatId) return;
    const kv = parseKeyValuePlain(content);
    if (kv.id) {
      get().upsertTask(chatId, {
        id: kv.id,
        title: kv.title || kv.id,
        status: normalizeTaskStatus(kv.status) || "pending",
        description: kv.description,
        // Parent line in response is parent title; we don't have parent ID, so omit parentId here
        updatedAt: new Date().toISOString(),
      });
    }
  },

  processListTasksContent: (chatId: string, content: string) => {
    if (!content || !chatId) return;
    // If no tasks found, clear for this chat
    if (/^No tasks found for this plan/i.test(content.trim())) {
      get().resetChat(chatId);
      return;
    }
    const tasks = parseListTasks(content);
    if (tasks.length > 0) {
      tasks.forEach((t) => get().upsertTask(chatId, { ...t, updatedAt: new Date().toISOString() }));
    }
  },

  // Process create_plan tool result - parse and cache the plan
  processCreatePlanContent: (chatId: string, content: string) => {
    if (!content || !chatId) return;
    
    let planId: string | undefined;
    
    try {
      // Try to parse JSON response
      const data = JSON.parse(content);
      if (data && typeof data === "object") {
        planId = (data.id || data.plan_id) as string | undefined;
        if (planId) {
          const plan: Plan = {
            id: planId,
            chat_id: chatId,
            title: (data.title || "") as string,
            description: (data.description || undefined) as string | undefined,
            status: planStatusFromUnknown(data.status),
            complexity: planComplexityFromUnknown(data.complexity),
            created_at: (data.created_at || new Date().toISOString()) as string,
            updated_at: (data.updated_at || new Date().toISOString()) as string,
          };
          
          get().setPlan(chatId, plan);
        }
      }
    } catch {
      // Not JSON - try key-value parsing
    }

    // Try key-value parsing as fallback (for plain text responses)
    if (!planId) {
      const kv = parseKeyValuePlain(content);
      if (kv.id) {
        planId = kv.id;
        const plan: Plan = {
          id: kv.id,
          chat_id: chatId,
          title: kv.title || "",
          description: kv.description,
          status: planStatusFromUnknown(kv.status),
          complexity: planComplexityFromUnknown(kv.complexity),
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        
        get().setPlan(chatId, plan);
      }
    }

    // Also parse inline tasks from the create_plan response
    // The response format is: "N. [task_id] Task Title (Status: pending)"
    if (planId) {
      const taskRegex = /^\d+\.\s*\[([^\]]+)\]\s+(.+?)\s*\(Status:\s*(\w+)\)/gm;
      let match;
      while ((match = taskRegex.exec(content)) !== null) {
        const [, taskId, title, status] = match;
        if (taskId && title) {
          get().upsertTask(chatId, {
            id: taskId,
            title: title.trim(),
            status: normalizeTaskStatus(status) || "pending",
            planId,
            updatedAt: new Date().toISOString(),
          });
        }
      }
    }
  },

  getTasksForChat: (chatId?: string | null) => {
    if (!chatId) return [];
    const byChat = get().tasksByChat[chatId];
    return byChat ? Object.values(byChat) : [];
  },

  getStatsForChat: (chatId?: string | null) => {
    const tasks = get().getTasksForChat(chatId);
    const total = tasks.length;
    const completed = tasks.filter((t) => t.status === "completed").length;
    const pending = tasks.filter((t) => t.status === "pending").length;
    const percent = total > 0 ? Math.round((completed / total) * 100) : 0;
    return { total, completed, pending, percent };
  },

  resetChat: (chatId: string) => {
    set((state) => {
      const nextTasks = { ...state.tasksByChat };
      const nextPlans = { ...state.plansByChat };
      const nextPlanIds = { ...state.planIdByChat };
      const nextPlanLoaded = { ...state.planLoadedForChat };
      delete nextTasks[chatId];
      delete nextPlans[chatId];
      delete nextPlanIds[chatId];
      delete nextPlanLoaded[chatId];
      return { 
        tasksByChat: nextTasks,
        plansByChat: nextPlans,
        planIdByChat: nextPlanIds,
        planLoadedForChat: nextPlanLoaded,
      };
    });
  },

  reset: () => {
    set({ 
      tasksByChat: {},
      plansByChat: {},
      planIdByChat: {},
      planLoadedForChat: {},
    });
  },
}));
