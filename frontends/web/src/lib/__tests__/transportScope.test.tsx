// Copyright (c) 2025 Reliant Labs

/**
 * lib/transportScope.tsx, the local stand-in for forge's TransportScope: a
 * subtree reads its own transport and keeps its own cache, and everything
 * outside it stays on the module transport (lib/connect.ts).
 *
 * A port of forge#2735's web-runtime/src/transport-scope.test.tsx, over
 * reliant's own pieces: the module transport is lib/connect.ts (its test seam
 * stands in for the live backend and records every call), and the realistic
 * scope serves the shop fixture pack. forge's version drives its
 * service-hooks, whose context parameter reliant's pinned runtime does not
 * have yet (P4.1), so the reads and writes here are written the way a
 * generated hook will be once it does:
 *
 *   const clientOf = scopedClient(Svc, connectClient(Svc));
 *   queryFn: (ctx) => clientOf(ctx).method(req)
 */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { MutationObserver, QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient, type QueryFunction } from "@tanstack/react-query";
import { Code, createClient, createRouterTransport, type Transport } from "@connectrpc/connect";
import type { ReactNode } from "react";

import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { UserService, type GetCurrentUserResponse } from "@/gen/controlplane/services/user/v1/user_pb";
import { createShopTransport } from "@/fixtures/shop";
import { connectClient, setTransport, transport as moduleTransport } from "@/lib/connect";
import { createAppQueryClient, queryClient as appQueryClient } from "@/lib/query-client";
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

/** A UserService whose current user is `name`, recording every call it answers. */
function namedTransport(name: string): { transport: Transport; seen: string[] } {
  const seen: string[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(UserService, {
      getCurrentUser: () => {
        seen.push("getCurrentUser");
        return { user: { id: `u-${name}`, name } };
      },
      updateProfile: (req) => {
        seen.push(`updateProfile:${req.name ?? ""}`);
        return { user: { id: `u-${name}`, name } };
      },
    });
  });
  return { transport, seen };
}

// The live backend, behind lib/connect.ts's test seam for each test.
let live: ReturnType<typeof namedTransport>;
let restore: () => void;

beforeEach(() => {
  live = namedTransport("live");
  restore = setTransport(live.transport);
});

afterEach(() => restore());

// The generated file: the module client as the fallback, and every call
// resolving its client from the context it runs in.
const clientOf = scopedClient(UserService, connectClient(UserService));
const CURRENT_USER = ["controlplane.v1.UserService", "GetCurrentUser"] as const;
const currentUser = { queryKey: CURRENT_USER, queryFn: ((ctx) => clientOf(ctx).getCurrentUser({})) as QueryFunction<GetCurrentUserResponse> };
const useCurrentUser = () => useQuery({ ...currentUser, retry: false });
const useUpdateProfile = () => useMutation({ mutationFn: (name: string, ctx) => clientOf(ctx).updateProfile({ name }) });

const quiet = () => new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

function fixtureScope(id: string): { scope: TransportScope; seen: string[] } {
  const { transport, seen } = namedTransport(id);
  return { scope: createTransportScope({ id, transport, queryClient: quiet() }), seen };
}

const inScope = (scope: TransportScope) =>
  function Wrapper({ children }: { children: ReactNode }) {
    return <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>;
  };

describe("a generated read", () => {
  it("under a scope reads the scope's transport", async () => {
    const { scope, seen } = fixtureScope("fixture");
    const { result } = renderHook(useCurrentUser, { wrapper: inScope(scope) });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.user?.name).toBe("fixture");
    expect(seen).toEqual(["getCurrentUser"]);
    expect(live.seen).toEqual([]);
  });

  it("under a scope over the shop pack reads the pack, and never the live backend", async () => {
    const shop = createShopTransport();
    const expected = (await createClient(UserService, shop).getCurrentUser({})).user?.name;
    expect(expected).toBeTruthy();
    const scope = createTransportScope({ id: "sample", transport: shop, queryClient: createAppQueryClient(), readOnly: true });
    const { result } = renderHook(useCurrentUser, { wrapper: inScope(scope) });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.user?.name).toBe(expected);
    expect(live.seen).toEqual([]);
  });

  it("outside any scope reads the module transport (lib/connect.ts), as before", async () => {
    const app = quiet();
    const { result } = renderHook(useCurrentUser, {
      wrapper: ({ children }) => <QueryClientProvider client={app}>{children}</QueryClientProvider>,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.user?.name).toBe("live");
    expect(live.seen).toEqual(["getCurrentUser"]);
    expect(transportOf(app)).toBeUndefined();
  });

  it("fetched outside React on the scope's QueryClient reads the scope's transport too", async () => {
    const { scope, seen } = fixtureScope("fixture");
    expect((await scope.queryClient.fetchQuery(currentUser)).user?.name).toBe("fixture");
    expect(seen).toEqual(["getCurrentUser"]);
    expect((await quiet().fetchQuery(currentUser)).user?.name).toBe("live");
  });

  it("caches per scope: two scopes, one key, two entries", async () => {
    const a = fixtureScope("scope-a");
    const b = fixtureScope("scope-b");
    const ra = renderHook(useCurrentUser, { wrapper: inScope(a.scope) });
    const rb = renderHook(useCurrentUser, { wrapper: inScope(b.scope) });
    await waitFor(() => expect(ra.result.current.isSuccess && rb.result.current.isSuccess).toBe(true));
    // The key is the module constant in both: the scope id is not in it.
    expect(a.scope.queryClient.getQueryData<GetCurrentUserResponse>(CURRENT_USER)?.user?.name).toBe("scope-a");
    expect(b.scope.queryClient.getQueryData<GetCurrentUserResponse>(CURRENT_USER)?.user?.name).toBe("scope-b");
    expect(a.seen).toEqual(["getCurrentUser"]);
    expect(b.seen).toEqual(["getCurrentUser"]);

    // Invalidating the module key reaches only the cache it is called on.
    await a.scope.queryClient.invalidateQueries({ queryKey: CURRENT_USER });
    expect(a.seen).toEqual(["getCurrentUser", "getCurrentUser"]);
    expect(b.seen).toEqual(["getCurrentUser"]);
    expect(appQueryClient.getQueryData(CURRENT_USER)).toBeUndefined();
  });

  it("falls back on a TanStack Query that passes no context.client (before 5.66)", async () => {
    // What an old TanStack hands a queryFn: no `client`.
    const queryFn = currentUser.queryFn as unknown as (ctx: object) => Promise<GetCurrentUserResponse>;
    const res = await queryFn({ queryKey: [], signal: new AbortController().signal, meta: undefined });
    expect(res.user?.name).toBe("live");
  });
});

describe("a generated write", () => {
  it("from a hook under a scope goes to the scope's transport", async () => {
    const { scope, seen } = fixtureScope("fixture");
    const { result } = renderHook(useUpdateProfile, { wrapper: inScope(scope) });
    expect((await result.current.mutateAsync("Ada")).user?.name).toBe("fixture");
    expect(seen).toEqual(["updateProfile:Ada"]);
    expect(live.seen).toEqual([]);
  });

  it("imperative, bound to the scope's QueryClient, goes to the scope's transport", async () => {
    const { scope, seen } = fixtureScope("fixture");
    const write = (qc: QueryClient, name: string) =>
      new MutationObserver(qc, { mutationFn: (n: string, ctx) => clientOf(ctx).updateProfile({ name: n }) }).mutate(name);
    expect((await write(scope.queryClient, "w_2")).user?.name).toBe("fixture");
    expect(seen).toEqual(["updateProfile:w_2"]);
    expect((await write(quiet(), "w_3")).user?.name).toBe("live");
    expect(live.seen).toEqual(["updateProfile:w_3"]);
  });

  it("is the transport's to refuse: the read-only shop pack refuses it, and the scope enforces nothing", async () => {
    const scope = createTransportScope({ id: "sample", transport: createShopTransport(), queryClient: quiet(), readOnly: true });
    const { result } = renderHook(useUpdateProfile, { wrapper: inScope(scope) });
    await expect(result.current.mutateAsync("Ada")).rejects.toMatchObject({ code: Code.PermissionDenied });
    expect(live.seen).toEqual([]);

    // readOnly is advisory: a read-only scope over a transport that answers writes lets them through.
    const { transport, seen } = namedTransport("writable");
    const advisory = createTransportScope({ id: "advisory", transport, queryClient: quiet(), readOnly: true });
    const w = renderHook(useUpdateProfile, { wrapper: inScope(advisory) });
    await w.result.current.mutateAsync("Bo");
    expect(seen).toEqual(["updateProfile:Bo"]);
  });
});

describe("scopedClient", () => {
  it("returns the fallback with no context, no client, or an unbound client", () => {
    const fallback = connectClient(UserService);
    expect(clientOf()).toBe(fallback);
    expect(clientOf({})).toBe(fallback);
    expect(clientOf({ client: new QueryClient() })).toBe(fallback);
    expect(clientOf({ client: appQueryClient })).toBe(fallback);
  });

  it("returns the scope's client for the scope's QueryClient", () => {
    const { scope } = fixtureScope("fixture");
    expect(clientOf({ client: scope.queryClient })).toBe(clientFor(UserService, scope.transport));
  });
});

describe("clientFor", () => {
  it("memoizes per transport and per service", () => {
    const a = namedTransport("a").transport;
    const b = namedTransport("b").transport;
    expect(clientFor(UserService, a)).toBe(clientFor(UserService, a));
    expect(clientFor(UserService, a)).not.toBe(clientFor(UserService, b));
    expect(clientFor(OrganizationService, a)).not.toBe(clientFor(UserService, a) as unknown);
  });
});

describe("createTransportScope", () => {
  it("binds the QueryClient to the transport, defaults readOnly to false and makes a QueryClient when given none", () => {
    const { transport } = namedTransport("x");
    const scope = createTransportScope({ id: "x", transport });
    expect(scope.queryClient).toBeInstanceOf(QueryClient);
    expect(scope.queryClient).not.toBe(appQueryClient);
    expect(transportOf(scope.queryClient)).toBe(transport);
    expect(scope.readOnly).toBe(false);
    expect(createTransportScope({ id: "y", transport, readOnly: true }).readOnly).toBe(true);
  });

  it("takes the app's defaults when given createAppQueryClient()", () => {
    const scope = createTransportScope({ id: "x", transport: namedTransport("x").transport, queryClient: createAppQueryClient() });
    expect(scope.queryClient.getDefaultOptions()).toEqual(appQueryClient.getDefaultOptions());
  });

  it("refuses a QueryClient already bound to another transport", () => {
    const queryClient = new QueryClient();
    const one = namedTransport("one").transport;
    createTransportScope({ id: "one", transport: one, queryClient });
    // The same binding again is harmless.
    expect(() => createTransportScope({ id: "again", transport: one, queryClient })).not.toThrow();
    expect(() => createTransportScope({ id: "two", transport: namedTransport("two").transport, queryClient })).toThrow(
      /already bound to another transport/,
    );
    expect(transportOf(queryClient)).toBe(one);
  });
});

describe("the scope hooks", () => {
  const useProbe = () => ({
    scope: useTransportScope(),
    transport: useTransport(moduleTransport),
    client: useClient(UserService, connectClient(UserService)),
    queryClient: useQueryClient(),
  });

  it("read the nearest scope", () => {
    const outer = fixtureScope("outer").scope;
    const inner = fixtureScope("inner").scope;
    const o = renderHook(useProbe, { wrapper: inScope(outer) });
    expect(o.result.current.scope).toBe(outer);
    expect(o.result.current.transport).toBe(outer.transport);
    expect(o.result.current.client).toBe(clientFor(UserService, outer.transport));
    expect(o.result.current.queryClient).toBe(outer.queryClient);

    const i = renderHook(useProbe, {
      wrapper: ({ children }) => (
        <TransportScopeProvider scope={outer}>
          <TransportScopeProvider scope={inner}>{children}</TransportScopeProvider>
        </TransportScopeProvider>
      ),
    });
    expect(i.result.current.scope).toBe(inner);
    expect(i.result.current.client).toBe(clientFor(UserService, inner.transport));
    expect(i.result.current.queryClient).toBe(inner.queryClient);
  });

  it("read the fallback outside any: lib/connect.ts's transport and module client", () => {
    const { result } = renderHook(useProbe, {
      wrapper: ({ children }) => <QueryClientProvider client={quiet()}>{children}</QueryClientProvider>,
    });
    expect(result.current.scope).toBeNull();
    expect(result.current.transport).toBe(moduleTransport);
    expect(result.current.client).toBe(connectClient(UserService));
  });

  it("give useClient a client that is stable across renders", () => {
    const { scope } = fixtureScope("s");
    const { result, rerender } = renderHook(() => useClient(UserService, connectClient(UserService)), { wrapper: inScope(scope) });
    const first = result.current;
    rerender();
    expect(result.current).toBe(first);
  });
});

describe("a hook that builds its client at module scope (today's generated hooks, until P4.1)", () => {
  it("still calls the module transport, scope or not", async () => {
    const moduleClient = connectClient(UserService);
    const { scope, seen } = fixtureScope("fixture");
    const res = await scope.queryClient.fetchQuery({ queryKey: ["old"], queryFn: () => moduleClient.getCurrentUser({}) });
    expect(res.user?.name).toBe("live");
    expect(seen).toEqual([]);
  });
});
