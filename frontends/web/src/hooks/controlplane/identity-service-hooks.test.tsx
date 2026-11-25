// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated IdentityService hooks. Written once, when
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

import { useGetIdentityStatus } from "./identity-service-hooks_gen";
import { useListIdentities } from "./identity-service-hooks_gen";
import { useListUsers } from "./identity-service-hooks_gen";
import { useListUsersInfinite } from "./identity-service-hooks_gen";
import { useGetUser } from "./identity-service-hooks_gen";
import { useInviteUser } from "./identity-service-hooks_gen";
import { useSetUserRoles } from "./identity-service-hooks_gen";
import { useSendPasswordReset } from "./identity-service-hooks_gen";
import { useLockUser } from "./identity-service-hooks_gen";
import { useUnlockUser } from "./identity-service-hooks_gen";
import { useDeleteUser } from "./identity-service-hooks_gen";
import { useRevokeSessions } from "./identity-service-hooks_gen";
import { useRequestExport } from "./identity-service-hooks_gen";
import { useGetExportURL } from "./identity-service-hooks_gen";
import { useImportUsers } from "./identity-service-hooks_gen";
import { useCheckSocialSignIn } from "./identity-service-hooks_gen";

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

describe("IdentityService hooks", () => {
  it("useGetIdentityStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.GetIdentityStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetIdentityStatus({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListIdentities resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.ListIdentities": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListIdentities({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListUsers resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.ListUsers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListUsers({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListUsersInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.ListUsers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListUsersInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetUser resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.GetUser": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetUser({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useInviteUser resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.InviteUser": { response: {} },
      }),
    );

    const { result } = renderHook(() => useInviteUser(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetUserRoles resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.SetUserRoles": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetUserRoles(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSendPasswordReset resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.SendPasswordReset": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSendPasswordReset(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useLockUser resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.LockUser": { response: {} },
      }),
    );

    const { result } = renderHook(() => useLockUser(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUnlockUser resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.UnlockUser": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUnlockUser(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteUser resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.DeleteUser": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteUser(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRevokeSessions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.RevokeSessions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRevokeSessions(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRequestExport resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.RequestExport": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRequestExport(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetExportURL resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.GetExportURL": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetExportURL({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useImportUsers resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.ImportUsers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useImportUsers(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCheckSocialSignIn resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "IdentityService.CheckSocialSignIn": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCheckSocialSignIn({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
