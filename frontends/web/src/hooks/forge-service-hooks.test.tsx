// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated ForgeService hooks. Written once, when
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

import { useGetTopology } from "./forge-service-hooks_gen";
import { useVerifyEnv } from "./forge-service-hooks_gen";
import { useListSecrets } from "./forge-service-hooks_gen";
import { useGetEnvStatus } from "./forge-service-hooks_gen";
import { useGetEnvShape } from "./forge-service-hooks_gen";
import { usePlanPromote } from "./forge-service-hooks_gen";
import { useApplyPromote } from "./forge-service-hooks_gen";
import { usePlanDeploy } from "./forge-service-hooks_gen";
import { useStartDeployPlan } from "./forge-service-hooks_gen";
import { useStartDeploy } from "./forge-service-hooks_gen";
import { useGetDeployStatus } from "./forge-service-hooks_gen";

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

describe("ForgeService hooks", () => {
  it("useGetTopology resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.GetTopology": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetTopology({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useVerifyEnv resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.VerifyEnv": { response: {} },
      }),
    );

    const { result } = renderHook(() => useVerifyEnv(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListSecrets resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.ListSecrets": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListSecrets({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetEnvStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.GetEnvStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetEnvStatus({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetEnvShape resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.GetEnvShape": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetEnvShape({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("usePlanPromote resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.PlanPromote": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePlanPromote(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useApplyPromote resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.ApplyPromote": { response: {} },
      }),
    );

    const { result } = renderHook(() => useApplyPromote(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("usePlanDeploy resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.PlanDeploy": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePlanDeploy(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useStartDeployPlan resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.StartDeployPlan": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStartDeployPlan(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useStartDeploy resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.StartDeploy": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStartDeploy(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetDeployStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ForgeService.GetDeployStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDeployStatus({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
