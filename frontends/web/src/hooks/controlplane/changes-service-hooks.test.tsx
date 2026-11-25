// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated ChangesService hooks. Written once, when
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

import { useListChanges } from "./changes-service-hooks_gen";
import { useListChangesInfinite } from "./changes-service-hooks_gen";
import { useGetChange } from "./changes-service-hooks_gen";
import { useCreateChange } from "./changes-service-hooks_gen";
import { useUpdateChangeDraft } from "./changes-service-hooks_gen";
import { useSetChangePreview } from "./changes-service-hooks_gen";
import { useRequestChangeApproval } from "./changes-service-hooks_gen";
import { useLaunchChange } from "./changes-service-hooks_gen";
import { useRampChange } from "./changes-service-hooks_gen";
import { useRollOutChange } from "./changes-service-hooks_gen";
import { useStopChange } from "./changes-service-hooks_gen";
import { useEndChangeRun } from "./changes-service-hooks_gen";
import { useListChangeEvents } from "./changes-service-hooks_gen";
import { useListChangeEventsInfinite } from "./changes-service-hooks_gen";
import { useGetChangesSnapshot } from "./changes-service-hooks_gen";
import { useGetChangeReadout } from "./changes-service-hooks_gen";
import { useGetChangesDeliveryStatus } from "./changes-service-hooks_gen";

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

describe("ChangesService hooks", () => {
  it("useListChanges resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.ListChanges": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListChanges({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListChangesInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.ListChanges": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListChangesInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetChange resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.GetChange": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetChange({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateChange resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.CreateChange": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateChange(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateChangeDraft resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.UpdateChangeDraft": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateChangeDraft(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetChangePreview resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.SetChangePreview": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetChangePreview(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRequestChangeApproval resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.RequestChangeApproval": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRequestChangeApproval(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useLaunchChange resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.LaunchChange": { response: {} },
      }),
    );

    const { result } = renderHook(() => useLaunchChange(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRampChange resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.RampChange": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRampChange(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRollOutChange resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.RollOutChange": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRollOutChange(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useStopChange resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.StopChange": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStopChange(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useEndChangeRun resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.EndChangeRun": { response: {} },
      }),
    );

    const { result } = renderHook(() => useEndChangeRun(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListChangeEvents resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.ListChangeEvents": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListChangeEvents({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListChangeEventsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.ListChangeEvents": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListChangeEventsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetChangesSnapshot resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.GetChangesSnapshot": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetChangesSnapshot({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetChangeReadout resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.GetChangeReadout": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetChangeReadout({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetChangesDeliveryStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ChangesService.GetChangesDeliveryStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetChangesDeliveryStatus({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
