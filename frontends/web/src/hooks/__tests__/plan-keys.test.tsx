/**
 * PlanService reads and writes share the generated key scope, so a chat's
 * plan (usePlanForChat — the tasks panel and sidebar resolve it first) is
 * never left stale:
 *
 *  - a PlanService write (the generated mutations) refetches it;
 *  - the chat stream's `plan_tasks` refetch — what the server emits when the
 *    plan tools or a PlanService write change a plan — invalidates
 *    planServiceKeys.all, driven here through the chat store's real stream
 *    reducer.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useChatStore } from "@/store/chatStore";
import type { ChatUpdate } from "@/types/streaming";
import { usePlanForChat } from "../task-queries";
import { planServiceKeys, useUpdatePlan } from "../plan-service-hooks_gen";

const CHAT_ID = "chat-plan";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

let title: string;
let planCalls: number;

beforeEach(() => {
  title = "Ship it";
  planCalls = 0;
  setTransport(
    mockTransport({
      "PlanService.GetPlanByChatId": () => {
        planCalls++;
        return { plan: { id: "plan-1", chatId: CHAT_ID, title } };
      },
      "PlanService.UpdatePlan": (req) => {
        title = (req as { title: string }).title;
        return { plan: { id: "plan-1", chatId: CHAT_ID, title } };
      },
      // The task list the plan resolves to; unrelated to these assertions.
      "TaskService.ListTasks": { response: { tasks: [] } },
    }),
  );
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

function planTasksRefetch(): ChatUpdate {
  // What streaming-grpc hands the store for a REFETCH chat update: the JSON
  // payload spread onto the update.
  return { update_type: "refetch", id: `refetch-${Math.random()}`, type: "plan_tasks" } as unknown as ChatUpdate;
}

describe("PlanService keys", () => {
  it("a plan write refetches the chat's plan", async () => {
    const plan = renderHook(() => usePlanForChat(CHAT_ID), { wrapper });
    await waitFor(() => expect(plan.result.current.data?.plan?.title).toBe("Ship it"));

    const update = renderHook(() => useUpdatePlan(), { wrapper });
    await update.result.current.mutateAsync({ planId: "plan-1", title: "Ship it twice" });

    await waitFor(() => expect(plan.result.current.data?.plan?.title).toBe("Ship it twice"));
    expect(planCalls).toBe(2);
  });

  it("the chat stream's plan_tasks refetch invalidates planServiceKeys.all", async () => {
    const plan = renderHook(() => usePlanForChat(CHAT_ID), { wrapper });
    await waitFor(() => expect(plan.result.current.data?.plan?.title).toBe("Ship it"));

    title = "Changed by the plan tool";
    useChatStore.getState().processChatStreamUpdates(CHAT_ID, [planTasksRefetch()]);

    await waitFor(() => expect(plan.result.current.data?.plan?.title).toBe("Changed by the plan tool"));
    expect(queryClient.getQueryCache().findAll({ queryKey: planServiceKeys.all })).toHaveLength(1);
  });
});
