// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated AccessTokenService hooks. Written once, when
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

import { useCreateToken } from "./access-token-service-hooks_gen";
import { useListTokens } from "./access-token-service-hooks_gen";
import { useRevokeToken } from "./access-token-service-hooks_gen";
import { useCreateMyToken } from "./access-token-service-hooks_gen";
import { useListMyTokens } from "./access-token-service-hooks_gen";
import { useRevokeMyToken } from "./access-token-service-hooks_gen";
import { useUpdateMyToken } from "./access-token-service-hooks_gen";
import { useResetDaemonScopePolicy } from "./access-token-service-hooks_gen";
import { useListMyCLILogins } from "./access-token-service-hooks_gen";
import { useRevokeMyCLILogin } from "./access-token-service-hooks_gen";
import { useExchangeToken } from "./access-token-service-hooks_gen";
import { useListOrgMemberGrants } from "./access-token-service-hooks_gen";
import { useUpdateOrgMemberGrants } from "./access-token-service-hooks_gen";

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

describe("AccessTokenService hooks", () => {
  it("useCreateToken resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.CreateToken": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateToken(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListTokens resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListTokens": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListTokens({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRevokeToken resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.RevokeToken": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRevokeToken(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateMyToken resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.CreateMyToken": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateMyToken(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListMyTokens resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMyTokens({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRevokeMyToken resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.RevokeMyToken": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRevokeMyToken(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateMyToken resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.UpdateMyToken": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateMyToken(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useResetDaemonScopePolicy resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ResetDaemonScopePolicy": { response: {} },
      }),
    );

    const { result } = renderHook(() => useResetDaemonScopePolicy(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListMyCLILogins resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyCLILogins": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMyCLILogins({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRevokeMyCLILogin resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.RevokeMyCLILogin": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRevokeMyCLILogin(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useExchangeToken resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ExchangeToken": { response: {} },
      }),
    );

    const { result } = renderHook(() => useExchangeToken(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListOrgMemberGrants resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListOrgMemberGrants": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListOrgMemberGrants({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateOrgMemberGrants resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.UpdateOrgMemberGrants": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateOrgMemberGrants(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
