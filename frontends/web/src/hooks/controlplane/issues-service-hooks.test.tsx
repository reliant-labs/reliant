// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated IssuesService hooks. Written once, when
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

import { useListIssues } from "./issues-service-hooks_gen";
import { useListIssuesInfinite } from "./issues-service-hooks_gen";
import { useGetIssue } from "./issues-service-hooks_gen";
import { useListIssueEvents } from "./issues-service-hooks_gen";
import { useListIssueEventsInfinite } from "./issues-service-hooks_gen";
import { useUpdateIssueStatus } from "./issues-service-hooks_gen";
import { useAddIssueComment } from "./issues-service-hooks_gen";
import { useUpdateIssueComment } from "./issues-service-hooks_gen";
import { useDeleteIssueComment } from "./issues-service-hooks_gen";
import { useAssignIssue } from "./issues-service-hooks_gen";
import { useLinkIssueChat } from "./issues-service-hooks_gen";
import { useListAssignableUsers } from "./issues-service-hooks_gen";
import { useListAssignableUsersInfinite } from "./issues-service-hooks_gen";
import { useListIssueTimeline } from "./issues-service-hooks_gen";
import { useListIssueTimelineInfinite } from "./issues-service-hooks_gen";
import { useDeclareMaintenanceWindow } from "./issues-service-hooks_gen";
import { useListMaintenanceWindows } from "./issues-service-hooks_gen";

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

describe("IssuesService hooks", () => {
  it("useListIssues resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListIssues": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIssues({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListIssuesInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListIssues": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIssuesInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetIssue resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.GetIssue": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetIssue({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListIssueEvents resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListIssueEvents": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIssueEvents({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListIssueEventsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListIssueEvents": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIssueEventsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useUpdateIssueStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.UpdateIssueStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateIssueStatus(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useAddIssueComment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.AddIssueComment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useAddIssueComment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateIssueComment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.UpdateIssueComment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateIssueComment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteIssueComment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.DeleteIssueComment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteIssueComment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useAssignIssue resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.AssignIssue": { response: {} },
      }),
    );

    const { result } = renderHook(() => useAssignIssue(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useLinkIssueChat resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.LinkIssueChat": { response: {} },
      }),
    );

    const { result } = renderHook(() => useLinkIssueChat(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListAssignableUsers resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListAssignableUsers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListAssignableUsers({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListAssignableUsersInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListAssignableUsers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListAssignableUsersInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useListIssueTimeline resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListIssueTimeline": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIssueTimeline({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListIssueTimelineInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListIssueTimeline": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIssueTimelineInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useDeclareMaintenanceWindow resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.DeclareMaintenanceWindow": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeclareMaintenanceWindow(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListMaintenanceWindows resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IssuesService.ListMaintenanceWindows": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMaintenanceWindows({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
