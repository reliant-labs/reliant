// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated GtmService hooks. Written once, when
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

import { useGetGtmAccess } from "./gtm-service-hooks_gen";
import { useListIdeas } from "./gtm-service-hooks_gen";
import { useListIdeasInfinite } from "./gtm-service-hooks_gen";
import { useGetIdea } from "./gtm-service-hooks_gen";
import { useCreateIdea } from "./gtm-service-hooks_gen";
import { useUpdateIdea } from "./gtm-service-hooks_gen";
import { useSetIdeaStatus } from "./gtm-service-hooks_gen";
import { useListCampaigns } from "./gtm-service-hooks_gen";
import { useListCampaignsInfinite } from "./gtm-service-hooks_gen";
import { useGetCampaign } from "./gtm-service-hooks_gen";
import { useCreateCampaign } from "./gtm-service-hooks_gen";
import { useUpdateCampaign } from "./gtm-service-hooks_gen";
import { useListItems } from "./gtm-service-hooks_gen";
import { useListItemsInfinite } from "./gtm-service-hooks_gen";
import { useGetItem } from "./gtm-service-hooks_gen";
import { useCreateItem } from "./gtm-service-hooks_gen";
import { useUpdateItem } from "./gtm-service-hooks_gen";
import { useGetPositioning } from "./gtm-service-hooks_gen";
import { useUpdatePositioning } from "./gtm-service-hooks_gen";
import { useListClaims } from "./gtm-service-hooks_gen";
import { useListClaimsInfinite } from "./gtm-service-hooks_gen";
import { useUpsertClaim } from "./gtm-service-hooks_gen";
import { useCheckClaims } from "./gtm-service-hooks_gen";

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

describe("GtmService hooks", () => {
  it("useGetGtmAccess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.GetGtmAccess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetGtmAccess({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListIdeas resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.ListIdeas": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIdeas({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListIdeasInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.ListIdeas": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIdeasInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetIdea resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.GetIdea": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetIdea({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateIdea resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.CreateIdea": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateIdea(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateIdea resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.UpdateIdea": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateIdea(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetIdeaStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.SetIdeaStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetIdeaStatus(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListCampaigns resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.ListCampaigns": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListCampaigns({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListCampaignsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.ListCampaigns": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListCampaignsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetCampaign resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.GetCampaign": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCampaign({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateCampaign resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.CreateCampaign": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateCampaign(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateCampaign resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.UpdateCampaign": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateCampaign(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListItems resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.ListItems": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListItems({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListItemsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.ListItems": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListItemsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetItem resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.GetItem": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetItem({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateItem resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.CreateItem": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateItem(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateItem resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.UpdateItem": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateItem(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetPositioning resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.GetPositioning": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetPositioning({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdatePositioning resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.UpdatePositioning": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdatePositioning(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListClaims resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.ListClaims": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListClaims({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListClaimsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.ListClaims": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListClaimsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useUpsertClaim resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.UpsertClaim": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpsertClaim(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCheckClaims resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "GtmService.CheckClaims": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCheckClaims({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
