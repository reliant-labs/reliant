// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated TriggerService hooks. Written once, when
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

import { useCreateTrigger } from "./trigger-service-hooks_gen";
import { useGetTrigger } from "./trigger-service-hooks_gen";
import { useListTriggers } from "./trigger-service-hooks_gen";
import { useUpdateTrigger } from "./trigger-service-hooks_gen";
import { useDeleteTrigger } from "./trigger-service-hooks_gen";
import { useSetTriggerEnabled } from "./trigger-service-hooks_gen";
import { useFireTrigger } from "./trigger-service-hooks_gen";
import { useListTriggerEvents } from "./trigger-service-hooks_gen";
import { useGetLaunchEvent } from "./trigger-service-hooks_gen";
import { useRotateWebhookToken } from "./trigger-service-hooks_gen";
import { useEnableWorkflow } from "./trigger-service-hooks_gen";
import { useDisableWorkflow } from "./trigger-service-hooks_gen";
import { useResolveTriggerSenders } from "./trigger-service-hooks_gen";
import { useListReplayEvents } from "./trigger-service-hooks_gen";
import { usePreviewReplay } from "./trigger-service-hooks_gen";

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

describe("TriggerService hooks", () => {
  it("useCreateTrigger resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.CreateTrigger": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateTrigger(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetTrigger resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.GetTrigger": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetTrigger({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListTriggers resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.ListTriggers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListTriggers({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateTrigger resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.UpdateTrigger": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateTrigger(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteTrigger resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.DeleteTrigger": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteTrigger(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetTriggerEnabled resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.SetTriggerEnabled": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetTriggerEnabled(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useFireTrigger resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.FireTrigger": { response: {} },
      }),
    );

    const { result } = renderHook(() => useFireTrigger(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListTriggerEvents resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.ListTriggerEvents": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListTriggerEvents({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetLaunchEvent resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.GetLaunchEvent": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetLaunchEvent({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRotateWebhookToken resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.RotateWebhookToken": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRotateWebhookToken(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useEnableWorkflow resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.EnableWorkflow": { response: {} },
      }),
    );

    const { result } = renderHook(() => useEnableWorkflow(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDisableWorkflow resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.DisableWorkflow": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDisableWorkflow(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useResolveTriggerSenders resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.ResolveTriggerSenders": { response: {} },
      }),
    );

    const { result } = renderHook(() => useResolveTriggerSenders({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListReplayEvents resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.ListReplayEvents": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListReplayEvents({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("usePreviewReplay resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "TriggerService.PreviewReplay": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePreviewReplay({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
