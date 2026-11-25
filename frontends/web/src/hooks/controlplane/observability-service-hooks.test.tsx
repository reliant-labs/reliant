// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated ObservabilityService hooks. Written once, when
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

import { useSearchLogs } from "./observability-service-hooks_gen";
import { useSearchLogsInfinite } from "./observability-service-hooks_gen";
import { useListTraces } from "./observability-service-hooks_gen";
import { useListTracesInfinite } from "./observability-service-hooks_gen";
import { useGetTrace } from "./observability-service-hooks_gen";
import { useListMetrics } from "./observability-service-hooks_gen";
import { useGetMetric } from "./observability-service-hooks_gen";
import { useListMetricExemplars } from "./observability-service-hooks_gen";
import { useGetServiceGraph } from "./observability-service-hooks_gen";
import { useGetServiceGraphCatalog } from "./observability-service-hooks_gen";
import { useGetServiceOverview } from "./observability-service-hooks_gen";
import { useGetFlamegraph } from "./observability-service-hooks_gen";
import { useExportPGOProfile } from "./observability-service-hooks_gen";
import { useQueryPanel } from "./observability-service-hooks_gen";
import { useListAttributeValues } from "./observability-service-hooks_gen";
import { useGetServiceSignals } from "./observability-service-hooks_gen";
import { useListWorkloads } from "./observability-service-hooks_gen";
import { useGetWorkload } from "./observability-service-hooks_gen";
import { useGetManagedDatabaseMetrics } from "./observability-service-hooks_gen";
import { useGetDashboards } from "./observability-service-hooks_gen";
import { useGetReleaseHealth } from "./observability-service-hooks_gen";
import { useGetUserActivity } from "./observability-service-hooks_gen";
import { useListFrontendReplay } from "./observability-service-hooks_gen";

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

describe("ObservabilityService hooks", () => {
  it("useSearchLogs resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.SearchLogs": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSearchLogs({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSearchLogsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.SearchLogs": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSearchLogsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useListTraces resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.ListTraces": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListTraces({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListTracesInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.ListTraces": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListTracesInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetTrace resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetTrace": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetTrace({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListMetrics resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.ListMetrics": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMetrics({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetMetric resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetMetric": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetMetric({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListMetricExemplars resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.ListMetricExemplars": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMetricExemplars({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetServiceGraph resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetServiceGraph": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetServiceGraph({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetServiceGraphCatalog resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetServiceGraphCatalog": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetServiceGraphCatalog({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetServiceOverview resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetServiceOverview": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetServiceOverview({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetFlamegraph resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetFlamegraph": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetFlamegraph({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useExportPGOProfile resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.ExportPGOProfile": { response: {} },
      }),
    );

    const { result } = renderHook(() => useExportPGOProfile({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useQueryPanel resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.QueryPanel": { response: {} },
      }),
    );

    const { result } = renderHook(() => useQueryPanel({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListAttributeValues resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.ListAttributeValues": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListAttributeValues({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetServiceSignals resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetServiceSignals": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetServiceSignals({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListWorkloads resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.ListWorkloads": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListWorkloads({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetWorkload resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetWorkload": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetWorkload({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetManagedDatabaseMetrics resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetManagedDatabaseMetrics": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetManagedDatabaseMetrics({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetDashboards resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetDashboards": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDashboards({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetReleaseHealth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetReleaseHealth": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetReleaseHealth({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetUserActivity resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.GetUserActivity": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetUserActivity({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListFrontendReplay resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ObservabilityService.ListFrontendReplay": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListFrontendReplay({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
