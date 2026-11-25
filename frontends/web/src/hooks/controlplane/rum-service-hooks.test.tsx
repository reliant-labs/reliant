// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated RumService hooks. Written once, when
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

import { useListSessions } from "./rum-service-hooks_gen";
import { useListSessionsInfinite } from "./rum-service-hooks_gen";
import { useGetSession } from "./rum-service-hooks_gen";
import { useGetSessionTimeline } from "./rum-service-hooks_gen";
import { useGetSessionTimelineInfinite } from "./rum-service-hooks_gen";
import { useGetSessionReplay } from "./rum-service-hooks_gen";
import { useGetSessionReplayInfinite } from "./rum-service-hooks_gen";

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

describe("RumService hooks", () => {
  it("useListSessions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "RumService.ListSessions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListSessions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListSessionsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "RumService.ListSessions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListSessionsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetSession resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "RumService.GetSession": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetSession({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetSessionTimeline resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "RumService.GetSessionTimeline": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetSessionTimeline({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetSessionTimelineInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "RumService.GetSessionTimeline": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetSessionTimelineInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetSessionReplay resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "RumService.GetSessionReplay": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetSessionReplay({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetSessionReplayInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "RumService.GetSessionReplay": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetSessionReplayInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });
});
