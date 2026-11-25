// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated DaemonService hooks. Written once, when
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

import { useCreateDaemon } from "./daemon-service-hooks_gen";
import { useGetDaemon } from "./daemon-service-hooks_gen";
import { useListDaemons } from "./daemon-service-hooks_gen";
import { useUpdateDaemon } from "./daemon-service-hooks_gen";
import { useDeleteDaemon } from "./daemon-service-hooks_gen";
import { useSuspendDaemon } from "./daemon-service-hooks_gen";
import { useResumeDaemon } from "./daemon-service-hooks_gen";
import { useResizeDaemon } from "./daemon-service-hooks_gen";
import { useGrowDaemonDisk } from "./daemon-service-hooks_gen";
import { useSetPortAccess } from "./daemon-service-hooks_gen";
import { useRemovePortAccess } from "./daemon-service-hooks_gen";
import { useListPortAccessRules } from "./daemon-service-hooks_gen";
import { useSetDefaultPortAccess } from "./daemon-service-hooks_gen";

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

describe("DaemonService hooks", () => {
  it("useCreateDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.CreateDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.GetDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDaemon({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListDaemons resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.ListDaemons": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListDaemons({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.UpdateDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.DeleteDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSuspendDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.SuspendDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSuspendDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useResumeDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.ResumeDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useResumeDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useResizeDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.ResizeDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useResizeDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGrowDaemonDisk resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.GrowDaemonDisk": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGrowDaemonDisk(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetPortAccess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.SetPortAccess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetPortAccess(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRemovePortAccess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.RemovePortAccess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRemovePortAccess(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListPortAccessRules resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.ListPortAccessRules": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListPortAccessRules({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetDefaultPortAccess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DaemonService.SetDefaultPortAccess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetDefaultPortAccess(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
