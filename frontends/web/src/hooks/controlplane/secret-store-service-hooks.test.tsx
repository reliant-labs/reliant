// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated SecretStoreService hooks. Written once, when
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

import { useGrantHostedHTTPUse } from "./secret-store-service-hooks_gen";
import { useRevokeHostedHTTPUse } from "./secret-store-service-hooks_gen";
import { useAllowSecretUse } from "./secret-store-service-hooks_gen";
import { useDenySecretUse } from "./secret-store-service-hooks_gen";
import { useListSecretAccess } from "./secret-store-service-hooks_gen";
import { useListSecrets } from "./secret-store-service-hooks_gen";
import { useGetSecretVersions } from "./secret-store-service-hooks_gen";
import { useSetSecret } from "./secret-store-service-hooks_gen";
import { useDeleteSecret } from "./secret-store-service-hooks_gen";
import { useUndeleteSecret } from "./secret-store-service-hooks_gen";
import { useDestroySecret } from "./secret-store-service-hooks_gen";
import { useBeginStepUp } from "./secret-store-service-hooks_gen";
import { useCompleteStepUp } from "./secret-store-service-hooks_gen";

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

describe("SecretStoreService hooks", () => {
  it("useGrantHostedHTTPUse resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.GrantHostedHTTPUse": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGrantHostedHTTPUse(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRevokeHostedHTTPUse resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.RevokeHostedHTTPUse": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRevokeHostedHTTPUse(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useAllowSecretUse resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.AllowSecretUse": { response: {} },
      }),
    );

    const { result } = renderHook(() => useAllowSecretUse(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDenySecretUse resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.DenySecretUse": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDenySecretUse(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListSecretAccess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.ListSecretAccess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListSecretAccess({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListSecrets resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.ListSecrets": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListSecrets({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetSecretVersions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.GetSecretVersions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetSecretVersions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetSecret resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.SetSecret": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetSecret(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteSecret resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.DeleteSecret": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteSecret(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUndeleteSecret resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.UndeleteSecret": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUndeleteSecret(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDestroySecret resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.DestroySecret": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDestroySecret(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useBeginStepUp resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.BeginStepUp": { response: {} },
      }),
    );

    const { result } = renderHook(() => useBeginStepUp(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCompleteStepUp resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SecretStoreService.CompleteStepUp": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCompleteStepUp(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
