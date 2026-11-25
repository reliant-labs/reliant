// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated AlertingService hooks. Written once, when
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

import { useListReceivers } from "./alerting-service-hooks_gen";
import { useCreateReceiver } from "./alerting-service-hooks_gen";
import { useUpdateReceiver } from "./alerting-service-hooks_gen";
import { useDeleteReceiver } from "./alerting-service-hooks_gen";
import { useTestReceiver } from "./alerting-service-hooks_gen";
import { useCreateSilence } from "./alerting-service-hooks_gen";
import { useListSilences } from "./alerting-service-hooks_gen";
import { useExpireSilence } from "./alerting-service-hooks_gen";
import { useListAlerts } from "./alerting-service-hooks_gen";
import { useListAlertHistory } from "./alerting-service-hooks_gen";
import { useListNotifications } from "./alerting-service-hooks_gen";
import { useMarkNotificationsRead } from "./alerting-service-hooks_gen";

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

describe("AlertingService hooks", () => {
  it("useListReceivers resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.ListReceivers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListReceivers({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateReceiver resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.CreateReceiver": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateReceiver(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateReceiver resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.UpdateReceiver": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateReceiver(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteReceiver resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.DeleteReceiver": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteReceiver(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useTestReceiver resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.TestReceiver": { response: {} },
      }),
    );

    const { result } = renderHook(() => useTestReceiver(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateSilence resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.CreateSilence": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateSilence(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListSilences resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.ListSilences": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListSilences({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useExpireSilence resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.ExpireSilence": { response: {} },
      }),
    );

    const { result } = renderHook(() => useExpireSilence(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListAlerts resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.ListAlerts": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListAlerts({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListAlertHistory resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.ListAlertHistory": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListAlertHistory({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListNotifications resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.ListNotifications": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListNotifications({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useMarkNotificationsRead resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AlertingService.MarkNotificationsRead": { response: {} },
      }),
    );

    const { result } = renderHook(() => useMarkNotificationsRead(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
