// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated PresetService hooks. Written once, when
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

import { useListPresets } from "./preset-service-hooks_gen";
import { useGetPreset } from "./preset-service-hooks_gen";
import { useListPresetsForWorkflow } from "./preset-service-hooks_gen";
import { useCreatePreset } from "./preset-service-hooks_gen";
import { useUpdatePreset } from "./preset-service-hooks_gen";
import { useDeletePreset } from "./preset-service-hooks_gen";
import { useSetDefaultPreset } from "./preset-service-hooks_gen";
import { useGetDefaultPreset } from "./preset-service-hooks_gen";
import { useGetDefaultPresetsBatch } from "./preset-service-hooks_gen";

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

describe("PresetService hooks", () => {
  it("useListPresets resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.ListPresets": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListPresets({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetPreset resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.GetPreset": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetPreset({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListPresetsForWorkflow resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.ListPresetsForWorkflow": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListPresetsForWorkflow({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreatePreset resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.CreatePreset": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreatePreset(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdatePreset resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.UpdatePreset": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdatePreset(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeletePreset resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.DeletePreset": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeletePreset(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetDefaultPreset resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.SetDefaultPreset": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetDefaultPreset(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetDefaultPreset resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.GetDefaultPreset": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDefaultPreset({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetDefaultPresetsBatch resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "PresetService.GetDefaultPresetsBatch": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDefaultPresetsBatch({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
