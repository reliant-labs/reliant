// Copyright (c) 2025 Reliant Labs

/**
 * A transport SCOPE: a subtree of the app that reads its own Connect
 * transport and keeps its own query cache, while everything outside it stays
 * on the app's transport (lib/connect.ts). The case it exists for is the
 * forge tour's sample app: its console pages and the shell nav's console
 * model read the shop fixture pack, and the person's chats, projects and org
 * stay live.
 *
 * Local stand-in for `@reliantlabs/forge-web-runtime/transport-scope`
 * (forge#2726, F4a; not yet released), with the same names and signatures,
 * so reliant's call sites can move onto it before the forge release ships.
 * Once a forge release publishes it, this file becomes a re-export and no
 * caller changes, just as src/fixtures/transport.ts stands in for forge#2716.
 * Matched to F4a's web-runtime/src/transport-scope.tsx (forge branch
 * feat/web-runtime-transport-scope, before its PR opened), including its two
 * exported types, TransportScopeOptions and ScopedClientContext. Keep it in
 * step with forge's; anything reliant adds is marked
 * "RELIANT EXTENSION" and listed here, so the swap knows what to keep:
 *
 *   1. `createTransportScope` without a `queryClient` builds one with the
 *      app's defaults (lib/query-client.ts `createAppQueryClient`: staleTime,
 *      gcTime, the retry policy), not a bare `new QueryClient()`. The
 *      signature is forge's; only the default differs. On the swap this file
 *      keeps a one-line `createTransportScope` wrapper that fills
 *      `queryClient: createAppQueryClient()` and re-exports the rest.
 *
 * ── How a call finds its transport ──
 *
 * React context cannot be read inside a `queryFn` or `mutationFn`, and
 * forge's generated hooks build their client at module scope. TanStack Query 5
 * passes the executing QueryClient to both functions as `context.client`, so
 * THE SCOPE'S QUERYCLIENT CARRIES ITS TRANSPORT: `createTransportScope` binds
 * the two (`transportOf`), and `scopedClient(Svc, fallback)(ctx)` resolves the
 * client from whichever QueryClient is running the call. One mechanism covers
 * hooks, `fetchQuery`/`prefetchQuery` on `scope.queryClient` outside React,
 * and `<service>Mutations(scope.queryClient)`. Inside a component,
 * `useClient` / `useTransport` read the nearest scope from context.
 *
 * A scope ALWAYS has its own QueryClient, so its cache is isolated by
 * construction and the scope id never joins a query key: every
 * `xServiceKeys.*` stays a module constant, and `invalidateQueries(xKeys.all)`
 * invalidates the cache of whichever client it is called on.
 *
 * Outside a scope every lookup returns its `fallback`, which in reliant is the
 * module transport (`transport` / `connectClient(Svc)` from lib/connect.ts):
 * today's behaviour, test seam and routing included.
 */

import { createContext, useContext, type ReactElement, type ReactNode } from "react";
import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import { createClient, type Client, type Transport } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";

import { createAppQueryClient } from "@/lib/query-client";

/** A subtree's own transport and query cache. */
export interface TransportScope {
  /** Names the scope (logs, tests). Never part of a query key. */
  readonly id: string;
  /** What every call made in the scope is sent over. */
  readonly transport: Transport;
  /** The scope's own cache; it carries `transport` (see `transportOf`). */
  readonly queryClient: QueryClient;
  /** The UI's capability inside the scope: offer no write controls when true. */
  readonly readOnly: boolean;
}

/** What createTransportScope takes. */
export interface TransportScopeOptions {
  /** Names the scope (logs, tests). */
  id: string;
  /** Where every rpc made under the scope goes: a fixture pack's transport, a second backend. */
  transport: Transport;
  /**
   * The scope's cache. Defaults to a new one with the app's defaults
   * (RELIANT EXTENSION 1). Never pass the app's own: its reads would all go
   * to this transport.
   */
  queryClient?: QueryClient;
  /** Whether the UI treats the subtree as read-only. Default false. */
  readOnly?: boolean;
}

/** What scopedClient reads: a query or mutation function's context, or anything carrying the QueryClient a call runs on. */
export interface ScopedClientContext {
  readonly client?: QueryClient;
}

// The transport each scope's QueryClient carries. A WeakMap, so a dropped
// scope's client and transport are collected with it.
const transports = new WeakMap<QueryClient, Transport>();

/**
 * A scope over `transport`, with its own QueryClient. Throws when
 * `queryClient` already carries a different transport: the second binding
 * would silently move every read of the first scope.
 */
export function createTransportScope(options: TransportScopeOptions): TransportScope {
  const queryClient = options.queryClient ?? createAppQueryClient();
  const bound = transports.get(queryClient);
  if (bound !== undefined && bound !== options.transport) {
    throw new Error(`createTransportScope(${JSON.stringify(options.id)}): this QueryClient already carries another scope's transport; give each scope its own`);
  }
  transports.set(queryClient, options.transport);
  return { id: options.id, transport: options.transport, queryClient, readOnly: options.readOnly ?? false };
}

const ScopeContext = createContext<TransportScope | null>(null);

/**
 * Mounts `scope` over `children`: its QueryClient (QueryClientProvider) and
 * the scope itself (`useTransportScope`). The nearest scope wins. Mount no
 * other QueryClientProvider inside it: hooks below that one would run on its
 * client and leave the scope.
 */
export function TransportScopeProvider({ scope, children }: { scope: TransportScope; children?: ReactNode }): ReactElement {
  return (
    <QueryClientProvider client={scope.queryClient}>
      <ScopeContext.Provider value={scope}>{children}</ScopeContext.Provider>
    </QueryClientProvider>
  );
}

/** The nearest scope, or null outside every scope. */
export function useTransportScope(): TransportScope | null {
  return useContext(ScopeContext);
}

/** The nearest scope's transport, else `fallback` (reliant: lib/connect.ts `transport`). */
export function useTransport(fallback: Transport): Transport {
  return useTransportScope()?.transport ?? fallback;
}

/**
 * A client for `service` on the nearest scope's transport, else `fallback`
 * (reliant: `connectClient(service)`). Stable across renders (`clientFor`).
 */
export function useClient<S extends DescService>(service: S, fallback: Client<S>): Client<S> {
  const scope = useTransportScope();
  return scope ? clientFor(service, scope.transport) : fallback;
}

const clients = new WeakMap<Transport, WeakMap<DescService, Client<DescService>>>();

/** A client for `service` on `transport`, memoized per (transport, service). */
export function clientFor<S extends DescService>(service: S, transport: Transport): Client<S> {
  let byService = clients.get(transport);
  if (!byService) {
    byService = new WeakMap();
    clients.set(transport, byService);
  }
  let client = byService.get(service) as Client<S> | undefined;
  if (!client) {
    client = createClient(service, transport);
    byService.set(service, client as Client<DescService>);
  }
  return client;
}

/** The transport `queryClient` carries, set by `createTransportScope`; undefined for any other client. */
export function transportOf(queryClient: QueryClient): Transport | undefined {
  return transports.get(queryClient);
}

/**
 * What a generated hook calls (forge F4b): given the query or mutation
 * function's context, the client on the transport its QueryClient carries,
 * else `fallback`. A context with no `client` (an older TanStack, a direct
 * call) gets `fallback`, which is today's behaviour.
 *
 *   const clientOf = scopedClient(OrderService, connectClient(OrderService));
 *   queryFn: (ctx) => clientOf(ctx).getOrder(req)
 */
export function scopedClient<S extends DescService>(service: S, fallback: Client<S>): (ctx?: ScopedClientContext) => Client<S> {
  return (ctx) => {
    const transport = ctx?.client ? transportOf(ctx.client) : undefined;
    return transport ? clientFor(service, transport) : fallback;
  };
}
