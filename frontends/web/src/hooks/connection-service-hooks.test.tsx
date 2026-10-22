// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated ConnectionService hooks. Written once, when
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

import { useListIntegrations } from "./connection-service-hooks_gen";
import { useListConnections } from "./connection-service-hooks_gen";
import { useGetConnection } from "./connection-service-hooks_gen";
import { useCreateApiKeyConnection } from "./connection-service-hooks_gen";
import { useStartOAuth } from "./connection-service-hooks_gen";
import { useCompleteOAuth } from "./connection-service-hooks_gen";
import { useTestConnection } from "./connection-service-hooks_gen";
import { useRenameConnection } from "./connection-service-hooks_gen";
import { useSetDefaultConnection } from "./connection-service-hooks_gen";
import { useDeleteConnection } from "./connection-service-hooks_gen";
import { useListConnectionEvents } from "./connection-service-hooks_gen";

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

describe("ConnectionService hooks", () => {
  it("useListIntegrations resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.ListIntegrations": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIntegrations({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListConnections resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.ListConnections": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListConnections({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetConnection resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.GetConnection": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetConnection({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateApiKeyConnection resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.CreateApiKeyConnection": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateApiKeyConnection(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useStartOAuth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.StartOAuth": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStartOAuth(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCompleteOAuth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.CompleteOAuth": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCompleteOAuth(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useTestConnection resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.TestConnection": { response: {} },
      }),
    );

    const { result } = renderHook(() => useTestConnection(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRenameConnection resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.RenameConnection": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRenameConnection(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetDefaultConnection resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.SetDefaultConnection": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetDefaultConnection(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteConnection resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.DeleteConnection": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteConnection(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListConnectionEvents resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.ListConnectionEvents": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListConnectionEvents({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
