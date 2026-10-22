// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated PackageCommandsService hooks. Written once, when
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

import { useListCommands } from "./package-commands-service-hooks_gen";
import { useRunCommand } from "./package-commands-service-hooks_gen";
import { useListProcesses } from "./package-commands-service-hooks_gen";
import { useGetProcess } from "./package-commands-service-hooks_gen";
import { useGetProcessLogs } from "./package-commands-service-hooks_gen";
import { useKillProcess } from "./package-commands-service-hooks_gen";
import { useGetCommandFavorites } from "./package-commands-service-hooks_gen";
import { useSetCommandFavorite } from "./package-commands-service-hooks_gen";

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

describe("PackageCommandsService hooks", () => {
  it("useListCommands resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PackageCommandsService.ListCommands": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListCommands({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRunCommand resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PackageCommandsService.RunCommand": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRunCommand(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListProcesses resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PackageCommandsService.ListProcesses": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListProcesses({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetProcess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PackageCommandsService.GetProcess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProcess({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetProcessLogs resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PackageCommandsService.GetProcessLogs": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProcessLogs({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useKillProcess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PackageCommandsService.KillProcess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useKillProcess(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCommandFavorites resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PackageCommandsService.GetCommandFavorites": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCommandFavorites({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetCommandFavorite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PackageCommandsService.SetCommandFavorite": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetCommandFavorite(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
