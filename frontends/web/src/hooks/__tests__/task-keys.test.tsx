/**
 * TaskService reads and writes share the generated key scope, so a chat's
 * task list (useTasksForChat — the tasks panel, the sidebar's stats, a task
 * tool's card) is never left stale:
 *
 *  - a TaskService write (the generated mutations) refetches it;
 *  - the chat stream's `plan_tasks` refetch invalidates taskServiceKeys.all
 *    (and planServiceKeys.all), driven through the chat store's real stream
 *    reducer. Before this the signal reached no subscriber at all, and the
 *    panel caught up only once its 30s staleTime ran out and something
 *    remounted it.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { TaskStatus } from "@/gen/reliant/v1/task_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useChatStore } from "@/store/chatStore";
import type { ChatUpdate } from "@/types/streaming";
import { useTasksForChat } from "../task-queries";
import { taskServiceKeys, useUpdateTask } from "../task-service-hooks_gen";

const CHAT_ID = "chat-tasks";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

type Row = { id: string; planId: string; title: string; status: TaskStatus };
let tasks: Row[];
let listCalls: number;

beforeEach(() => {
  tasks = [
    { id: "t1", planId: "plan-1", title: "Write it", status: TaskStatus.IN_PROGRESS },
    { id: "t2", planId: "plan-1", title: "Test it", status: TaskStatus.PENDING },
  ];
  listCalls = 0;
  setTransport(
    mockTransport({
      "PlanService.GetPlanByChatId": { response: { plan: { id: "plan-1", chatId: CHAT_ID, title: "Ship" } } },
      "TaskService.ListTasks": (req) => {
        listCalls++;
        const { planId } = req as { planId?: string };
        return { tasks: tasks.filter((t) => t.planId === planId) };
      },
      "TaskService.UpdateTask": (req) => {
        const { taskId, status } = req as { taskId: string; status?: TaskStatus };
        tasks = tasks.map((t) => (t.id === taskId && status !== undefined ? { ...t, status } : t));
        return { task: tasks.find((t) => t.id === taskId) };
      },
    }),
  );
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

const statuses = (data: { id: string; status: string }[] | undefined) => data?.map((t) => [t.id, t.status]);

function planTasksRefetch(): ChatUpdate {
  return { update_type: "refetch", id: `refetch-${Math.random()}`, type: "plan_tasks" } as unknown as ChatUpdate;
}

describe("TaskService keys", () => {
  it("resolves the chat's plan, then maps its tasks", async () => {
    const list = renderHook(() => useTasksForChat(CHAT_ID), { wrapper });
    await waitFor(() =>
      expect(statuses(list.result.current.data)).toEqual([
        ["t1", "in_progress"],
        ["t2", "pending"],
      ]),
    );
  });

  it("a task write refetches the chat's tasks", async () => {
    const list = renderHook(() => useTasksForChat(CHAT_ID), { wrapper });
    await waitFor(() => expect(list.result.current.data).toHaveLength(2));

    const update = renderHook(() => useUpdateTask(), { wrapper });
    await update.result.current.mutateAsync({ taskId: "t1", status: TaskStatus.COMPLETED });

    await waitFor(() => expect(statuses(list.result.current.data)?.[0]).toEqual(["t1", "completed"]));
    expect(listCalls).toBe(2);
  });

  it("the chat stream's plan_tasks refetch invalidates taskServiceKeys.all", async () => {
    const list = renderHook(() => useTasksForChat(CHAT_ID), { wrapper });
    await waitFor(() => expect(list.result.current.data).toHaveLength(2));

    // The add_task tool wrote a row; the server announces plan_tasks.
    tasks = [...tasks, { id: "t3", planId: "plan-1", title: "Ship it", status: TaskStatus.PENDING }];
    useChatStore.getState().processChatStreamUpdates(CHAT_ID, [planTasksRefetch()]);

    await waitFor(() => expect(list.result.current.data?.map((t) => t.id)).toEqual(["t1", "t2", "t3"]));
    expect(queryClient.getQueryCache().findAll({ queryKey: taskServiceKeys.all }).length).toBeGreaterThan(0);
  });
});
