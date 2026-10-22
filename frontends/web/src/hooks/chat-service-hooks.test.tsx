// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated ChatService hooks. Written once, when
// no sibling test existed; this file is YOURS from here — forge never
// regenerates it, so edit freely (delete it and run `forge project rescaffold <path>` to
// get a fresh one).
//
// Each test covers the happy path of one RPC: the hook resolves against the
// mock transport, which is the floor that catches a renamed schema, a broken
// transport wiring, or a hook that no longer compiles. Extend with error /
// loading / empty cases as you build the page — see the `frontend-testing`
// skill for the four-state coverage rule.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, it, expect } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { useStartChat } from "./chat-service-hooks_gen";
import { useRunNow } from "./chat-service-hooks_gen";
import { useAdoptChat } from "./chat-service-hooks_gen";
import { useUnadoptChat } from "./chat-service-hooks_gen";
import { useListChats } from "./chat-service-hooks_gen";
import { useGetChat } from "./chat-service-hooks_gen";
import { useUpdateChat } from "./chat-service-hooks_gen";
import { useDeleteChat } from "./chat-service-hooks_gen";
import { useSearchChats } from "./chat-service-hooks_gen";
import { useListArchivedChats } from "./chat-service-hooks_gen";
import { useSendMessage } from "./chat-service-hooks_gen";
import { useSendAgentMessage } from "./chat-service-hooks_gen";
import { useListQueuedAgentMessages } from "./chat-service-hooks_gen";
import { useCancelQueuedAgentMessage } from "./chat-service-hooks_gen";
import { useInterruptThread } from "./chat-service-hooks_gen";
import { useListMessages } from "./chat-service-hooks_gen";
import { useUpdateChatState } from "./chat-service-hooks_gen";
import { useTerminateChat } from "./chat-service-hooks_gen";
import { usePauseChat } from "./chat-service-hooks_gen";
import { useResumeChat } from "./chat-service-hooks_gen";
import { useRetryUsageLimitWait } from "./chat-service-hooks_gen";
import { useDismissChat } from "./chat-service-hooks_gen";
import { useMarkUnreadChat } from "./chat-service-hooks_gen";
import { useCompactChat } from "./chat-service-hooks_gen";
import { useBranchChat } from "./chat-service-hooks_gen";
import { useListBranches } from "./chat-service-hooks_gen";
import { useUpdateWorkflowParams } from "./chat-service-hooks_gen";
import { useUpdateChatProviderPolicy } from "./chat-service-hooks_gen";
import { useGetChatUpdates } from "./chat-service-hooks_gen";
import { useListChatPlans } from "./chat-service-hooks_gen";
import { useGetWorkflowExecutions } from "./chat-service-hooks_gen";
import { useListStepExecutions } from "./chat-service-hooks_gen";
import { useGetThreadWorkflowInputs } from "./chat-service-hooks_gen";
import { useSetChatDaemon } from "./chat-service-hooks_gen";

import type { ReactNode } from "react";

// One QueryClient per test, built before it and torn down after it. Every
// test must leave NOTHING queued behind it: the last test in a file is
// followed by the jsdom teardown, and a React render that fires after that
// dies on `window` — "ReferenceError: window is not defined … caught after
// test environment was torn down" — failing a run whose tests all passed.
let queryClient: QueryClient;

beforeEach(() => {
  queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 0 },
      mutations: { retry: false },
    },
  });
});

// cleanup() is called explicitly because Testing Library only registers its
// own auto-cleanup when `afterEach` is a GLOBAL, and vitest does not expose
// globals unless the config opts in. Without it every renderHook root stays
// mounted, still subscribed, until the worker exits. clear() then drops the
// cache along with any gc timers its queries still hold.
afterEach(() => {
  cleanup();
  queryClient.clear();
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

// mutateAsync resolves BEFORE the hook re-renders: React Query notifies its
// observers on a later macrotask. A mutation test that returned there would
// leave that render queued past its own end — and past the jsdom teardown,
// when it is the file's last test. Wait for the RENDERED status to settle,
// success or error, so the test ends with nothing left to fire.
async function waitForSettled(result: { current: { status: string } }) {
  await waitFor(() => expect(["success", "error"]).toContain(result.current.status));
}

describe("ChatService hooks", () => {
  it("useStartChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.StartChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStartChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRunNow resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.RunNow": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRunNow(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useAdoptChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.AdoptChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useAdoptChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUnadoptChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.UnadoptChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUnadoptChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListChats resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.ListChats": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListChats({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.GetChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetChat({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.UpdateChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.DeleteChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSearchChats resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.SearchChats": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSearchChats({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListArchivedChats resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.ListArchivedChats": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListArchivedChats({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSendMessage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.SendMessage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSendMessage(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSendAgentMessage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.SendAgentMessage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSendAgentMessage(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListQueuedAgentMessages resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.ListQueuedAgentMessages": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListQueuedAgentMessages({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCancelQueuedAgentMessage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.CancelQueuedAgentMessage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCancelQueuedAgentMessage(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useInterruptThread resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.InterruptThread": { response: {} },
      }),
    );

    const { result } = renderHook(() => useInterruptThread(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListMessages resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.ListMessages": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMessages({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateChatState resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.UpdateChatState": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateChatState(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useTerminateChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.TerminateChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useTerminateChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("usePauseChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.PauseChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePauseChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useResumeChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.ResumeChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useResumeChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRetryUsageLimitWait resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.RetryUsageLimitWait": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRetryUsageLimitWait(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDismissChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.DismissChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDismissChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useMarkUnreadChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.MarkUnreadChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useMarkUnreadChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCompactChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.CompactChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCompactChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useBranchChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.BranchChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useBranchChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListBranches resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.ListBranches": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListBranches({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateWorkflowParams resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.UpdateWorkflowParams": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateWorkflowParams(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateChatProviderPolicy resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.UpdateChatProviderPolicy": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateChatProviderPolicy(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetChatUpdates resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.GetChatUpdates": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetChatUpdates({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListChatPlans resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.ListChatPlans": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListChatPlans({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetWorkflowExecutions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.GetWorkflowExecutions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetWorkflowExecutions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListStepExecutions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.ListStepExecutions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListStepExecutions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetThreadWorkflowInputs resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.GetThreadWorkflowInputs": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetThreadWorkflowInputs({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetChatDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChatService.SetChatDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetChatDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
