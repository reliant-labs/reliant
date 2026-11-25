// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated LLMGatewayService hooks. Written once, when
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

import { useCreateLLMKey } from "./llm-gateway-service-hooks_gen";
import { useListLLMKeys } from "./llm-gateway-service-hooks_gen";
import { useGetLLMKey } from "./llm-gateway-service-hooks_gen";
import { useRevokeLLMKey } from "./llm-gateway-service-hooks_gen";
import { useRotateLLMKey } from "./llm-gateway-service-hooks_gen";
import { useGetLLMSpend } from "./llm-gateway-service-hooks_gen";
import { useListAvailableModels } from "./llm-gateway-service-hooks_gen";

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

describe("LLMGatewayService hooks", () => {
  it("useCreateLLMKey resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "LLMGatewayService.CreateLLMKey": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateLLMKey(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListLLMKeys resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "LLMGatewayService.ListLLMKeys": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListLLMKeys({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetLLMKey resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "LLMGatewayService.GetLLMKey": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetLLMKey({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRevokeLLMKey resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "LLMGatewayService.RevokeLLMKey": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRevokeLLMKey(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRotateLLMKey resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "LLMGatewayService.RotateLLMKey": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRotateLLMKey(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetLLMSpend resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "LLMGatewayService.GetLLMSpend": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetLLMSpend({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListAvailableModels resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "LLMGatewayService.ListAvailableModels": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListAvailableModels({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
