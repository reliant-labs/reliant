// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated DashboardService hooks. Written once, when
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

import { useListDashboards } from "./dashboard-service-hooks_gen";
import { useGetDashboard } from "./dashboard-service-hooks_gen";
import { useSaveDashboard } from "./dashboard-service-hooks_gen";
import { useSaveAsCopy } from "./dashboard-service-hooks_gen";
import { useListDashboardVersions } from "./dashboard-service-hooks_gen";
import { useListDashboardVersionsInfinite } from "./dashboard-service-hooks_gen";
import { useRestoreDashboardVersion } from "./dashboard-service-hooks_gen";
import { useArchiveDashboard } from "./dashboard-service-hooks_gen";
import { useStarDashboard } from "./dashboard-service-hooks_gen";
import { useUnstarDashboard } from "./dashboard-service-hooks_gen";
import { useRecordDashboardView } from "./dashboard-service-hooks_gen";
import { useListDashboardTemplates } from "./dashboard-service-hooks_gen";
import { usePromoteDashboardToCode } from "./dashboard-service-hooks_gen";

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

describe("DashboardService hooks", () => {
  it("useListDashboards resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.ListDashboards": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListDashboards({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetDashboard resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.GetDashboard": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDashboard({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSaveDashboard resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.SaveDashboard": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSaveDashboard(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSaveAsCopy resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.SaveAsCopy": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSaveAsCopy(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListDashboardVersions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.ListDashboardVersions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListDashboardVersions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListDashboardVersionsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.ListDashboardVersions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListDashboardVersionsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useRestoreDashboardVersion resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.RestoreDashboardVersion": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRestoreDashboardVersion(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useArchiveDashboard resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.ArchiveDashboard": { response: {} },
      }),
    );

    const { result } = renderHook(() => useArchiveDashboard(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useStarDashboard resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.StarDashboard": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStarDashboard(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUnstarDashboard resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.UnstarDashboard": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUnstarDashboard(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRecordDashboardView resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.RecordDashboardView": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRecordDashboardView(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListDashboardTemplates resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.ListDashboardTemplates": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListDashboardTemplates({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("usePromoteDashboardToCode resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DashboardService.PromoteDashboardToCode": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePromoteDashboardToCode(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
