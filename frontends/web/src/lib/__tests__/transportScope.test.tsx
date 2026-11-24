// Copyright (c) 2025 Reliant Labs

/**
 * lib/transportScope.tsx, the local stand-in for forge's TransportScope: a
 * subtree reads its own transport and keeps its own cache, and everything
 * outside it stays on the module transport (lib/connect.ts).
 *
 * The queries here are written the way forge F4b's generated hooks will be:
 * `scopedClient(Svc, connectClient(Svc))` at module scope, resolved per call
 * from the query function's context.
 */

import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError, createClient, createRouterTransport, type Transport } from "@connectrpc/connect";

import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { createShopTransport } from "@/fixtures/shop";
import { connectClient, setTransport, transport as moduleTransport } from "@/lib/connect";
import { queryClient as appQueryClient } from "@/lib/query-client";
import {
  clientFor,
  createTransportScope,
  scopedClient,
  transportOf,
  TransportScopeProvider,
  useClient,
  useTransport,
  useTransportScope,
  type TransportScope,
} from "@/lib/transportScope";

/** A UserService whose current user is `name`, counting the calls it answers. */
function userTransport(name: string): Transport & { calls: string[] } {
  const calls: string[] = [];
  const t = createRouterTransport(({ service }) => {
    service(UserService, {
      getCurrentUser: () => {
        calls.push("getCurrentUser");
        return { user: { id: `u-${name}`, name } };
      },
      updateProfile: (req) => {
        calls.push(`updateProfile:${req.name ?? ""}`);
        return {};
      },
    });
  });
  return Object.assign(t, { calls });
}

/** The module transport's stand-in: records and refuses, so a call that escapes a scope fails the test. */
function liveTransport(): Transport & { calls: string[] } {
  const calls: string[] = [];
  const t = createRouterTransport(({ service }) => {
    service(UserService, {
      getCurrentUser: () => {
        calls.push("getCurrentUser");
        throw new ConnectError("the module transport was called", Code.FailedPrecondition);
      },
    });
  });
  return Object.assign(t, { calls });
}

// The shape a forge F4b generated hook takes: the module client is the fallback.
const userClientOf = scopedClient(UserService, connectClient(UserService));
const CURRENT_USER = ["controlplane.v1.UserService", "GetCurrentUser"] as const;

function useCurrentUser() {
  return useQuery({ queryKey: CURRENT_USER, queryFn: (ctx) => userClientOf(ctx).getCurrentUser({}), retry: false });
}

const testClient = () => new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

const inScope = (scope: TransportScope) =>
  function Wrapper({ children }: { children: ReactNode }) {
    return <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>;
  };

let live: ReturnType<typeof liveTransport>;
let restore: () => void;

beforeEach(() => {
  live = liveTransport();
  restore = setTransport(live);
});

afterEach(() => {
  restore();
});

describe("inside a scope", () => {
  it("a hook reads the shop pack through the scope's transport, and the module transport is never called", async () => {
    const shop = createShopTransport();
    const expected = await createClient(UserService, shop).getCurrentUser({});
    expect(expected.user?.name).toBeTruthy();

    const scope = createTransportScope({ id: "sample", transport: shop, readOnly: true });
    const { result } = renderHook(() => ({ query: useCurrentUser(), scope: useTransportScope() }), { wrapper: inScope(scope) });

    await waitFor(() => expect(result.current.query.isSuccess).toBe(true));
    expect(result.current.query.data?.user?.name).toBe(expected.user?.name);
    expect(result.current.scope).toBe(scope);
    expect(live.calls).toEqual([]);
  });

  it("useTransport and useClient return the scope's", async () => {
    const t = userTransport("scoped");
    const scope = createTransportScope({ id: "s", transport: t, queryClient: testClient() });
    const { result } = renderHook(
      () => ({ transport: useTransport(moduleTransport), client: useClient(UserService, connectClient(UserService)), qc: useQueryClient() }),
      { wrapper: inScope(scope) },
    );
    expect(result.current.transport).toBe(t);
    expect(result.current.client).toBe(clientFor(UserService, t));
    expect(result.current.qc).toBe(scope.queryClient);
    expect((await result.current.client.getCurrentUser({})).user?.name).toBe("scoped");
    expect(live.calls).toEqual([]);
  });

  it("a mutation runs on the scope's transport, and the read-only shop pack refuses it", async () => {
    const t = userTransport("scoped");
    const scope = createTransportScope({ id: "s", transport: t, queryClient: testClient() });
    const useUpdate = () => useMutation({ mutationFn: (name: string, ctx) => userClientOf(ctx).updateProfile({ name }) });
    const { result } = renderHook(useUpdate, { wrapper: inScope(scope) });
    await result.current.mutateAsync("Ada");
    expect(t.calls).toEqual(["updateProfile:Ada"]);

    const shopScope = createTransportScope({ id: "sample", transport: createShopTransport(), queryClient: testClient(), readOnly: true });
    const shop = renderHook(useUpdate, { wrapper: inScope(shopScope) });
    await expect(shop.result.current.mutateAsync("Ada")).rejects.toMatchObject({ code: Code.PermissionDenied });
    expect(live.calls).toEqual([]);
  });

  it("the nearest scope wins", async () => {
    const outer = createTransportScope({ id: "outer", transport: userTransport("outer"), queryClient: testClient() });
    const inner = createTransportScope({ id: "inner", transport: userTransport("inner"), queryClient: testClient() });
    const { result } = renderHook(useCurrentUser, {
      wrapper: ({ children }) => (
        <TransportScopeProvider scope={outer}>
          <TransportScopeProvider scope={inner}>{children}</TransportScopeProvider>
        </TransportScopeProvider>
      ),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.user?.name).toBe("inner");
  });
});

describe("outside React", () => {
  it("fetchQuery on the scope's QueryClient reads the scope's transport", async () => {
    const shop = createShopTransport();
    const scope = createTransportScope({ id: "sample", transport: shop });
    const res = await scope.queryClient.fetchQuery({ queryKey: CURRENT_USER, queryFn: (ctx) => userClientOf(ctx).getCurrentUser({}) });
    expect(res.user?.name).toBe((await createClient(UserService, shop).getCurrentUser({})).user?.name);
    expect(transportOf(scope.queryClient)).toBe(shop);
    expect(live.calls).toEqual([]);
  });

  it("a context with no QueryClient, or one no scope made, gets the fallback", () => {
    const fallback = connectClient(UserService);
    expect(userClientOf()).toBe(fallback);
    expect(userClientOf({})).toBe(fallback);
    expect(userClientOf({ client: appQueryClient })).toBe(fallback);
    expect(transportOf(appQueryClient)).toBeUndefined();
  });
});

describe("outside every scope", () => {
  it("hooks fall back to the module transport (lib/connect.ts, its test seam included)", async () => {
    const t = userTransport("live");
    setTransport(t);
    const qc = testClient();
    const { result } = renderHook(
      () => ({ query: useCurrentUser(), scope: useTransportScope(), transport: useTransport(moduleTransport), client: useClient(UserService, connectClient(UserService)) }),
      { wrapper: ({ children }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider> },
    );
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true));
    expect(result.current.query.data?.user?.name).toBe("live");
    expect(result.current.scope).toBeNull();
    expect(result.current.transport).toBe(moduleTransport);
    expect(result.current.client).toBe(connectClient(UserService));
    expect(t.calls).toEqual(["getCurrentUser"]);
  });
});

describe("cache isolation", () => {
  it("two scopes answer the same query key from their own transports and caches", async () => {
    const a = createTransportScope({ id: "a", transport: userTransport("Ada"), queryClient: testClient() });
    const b = createTransportScope({ id: "b", transport: userTransport("Bo"), queryClient: testClient() });
    const ra = renderHook(useCurrentUser, { wrapper: inScope(a) });
    const rb = renderHook(useCurrentUser, { wrapper: inScope(b) });
    await waitFor(() => expect(ra.result.current.isSuccess && rb.result.current.isSuccess).toBe(true));
    expect(ra.result.current.data?.user?.name).toBe("Ada");
    expect(rb.result.current.data?.user?.name).toBe("Bo");
    expect(a.queryClient.getQueryData(CURRENT_USER)).not.toBe(b.queryClient.getQueryData(CURRENT_USER));

    // The key is the module constant; invalidating it reaches only the cache it is called on.
    await a.queryClient.invalidateQueries({ queryKey: CURRENT_USER });
    expect(a.queryClient.getQueryState(CURRENT_USER)?.dataUpdateCount).toBe(2);
    expect(b.queryClient.getQueryState(CURRENT_USER)?.dataUpdateCount).toBe(1);
    expect(appQueryClient.getQueryData(CURRENT_USER)).toBeUndefined();
  });

  it("refuses a QueryClient that already carries another scope's transport", () => {
    const qc = testClient();
    const t = userTransport("a");
    createTransportScope({ id: "a", transport: t, queryClient: qc });
    expect(() => createTransportScope({ id: "a-again", transport: t, queryClient: qc })).not.toThrow();
    expect(() => createTransportScope({ id: "b", transport: userTransport("b"), queryClient: qc })).toThrow(/another scope's transport/);
  });
});

describe("createTransportScope", () => {
  it("builds its own QueryClient with the app's defaults, and is not read-only unless asked", () => {
    const t = userTransport("x");
    const scope = createTransportScope({ id: "x", transport: t });
    expect(scope.queryClient).not.toBe(appQueryClient);
    expect(scope.queryClient.getDefaultOptions()).toEqual(appQueryClient.getDefaultOptions());
    expect(scope.readOnly).toBe(false);
    expect(scope.id).toBe("x");
    expect(createTransportScope({ id: "y", transport: t, readOnly: true }).readOnly).toBe(true);
  });
});

describe("clientFor", () => {
  it("is memoized per transport and service", () => {
    const t1 = userTransport("1");
    const t2 = userTransport("2");
    expect(clientFor(UserService, t1)).toBe(clientFor(UserService, t1));
    expect(clientFor(UserService, t1)).not.toBe(clientFor(UserService, t2));
  });

  it("gives useClient a client that is stable across renders", () => {
    const scope = createTransportScope({ id: "s", transport: userTransport("s"), queryClient: testClient() });
    const { result, rerender } = renderHook(() => useClient(UserService, connectClient(UserService)), { wrapper: inScope(scope) });
    const first = result.current;
    rerender();
    expect(result.current).toBe(first);
  });
});
