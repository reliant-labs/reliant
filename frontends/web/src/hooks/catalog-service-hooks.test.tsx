// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated CatalogService hooks. Written once, when
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

import { useListModels } from "./catalog-service-hooks_gen";
import { useListModelsByProvider } from "./catalog-service-hooks_gen";
import { useListAvailableModels } from "./catalog-service-hooks_gen";
import { useListTools } from "./catalog-service-hooks_gen";
import { useListNodes } from "./catalog-service-hooks_gen";
import { useGetCELCompletions } from "./catalog-service-hooks_gen";
import { useSearchCatalog } from "./catalog-service-hooks_gen";
import { useGetCatalogEntry } from "./catalog-service-hooks_gen";
import { useListCatalogIntegrations } from "./catalog-service-hooks_gen";
import { useListInputOptions } from "./catalog-service-hooks_gen";

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

describe("CatalogService hooks", () => {
  it("useListModels resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.ListModels": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListModels({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListModelsByProvider resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.ListModelsByProvider": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListModelsByProvider({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListAvailableModels resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.ListAvailableModels": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListAvailableModels({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListTools resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.ListTools": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListTools({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListNodes resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.ListNodes": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListNodes({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetCELCompletions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.GetCELCompletions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCELCompletions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSearchCatalog resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.SearchCatalog": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSearchCatalog({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetCatalogEntry resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.GetCatalogEntry": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCatalogEntry({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListCatalogIntegrations resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.ListCatalogIntegrations": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListCatalogIntegrations({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListInputOptions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "CatalogService.ListInputOptions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListInputOptions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
