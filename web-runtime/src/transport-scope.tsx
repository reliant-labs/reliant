"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// TransportScope: a subtree of the app with its own Connect transport and its
// own React Query cache.
//
// WHY. A forge frontend has one transport, the module singleton behind
// `connectClient` in the owned src/lib/connect.ts, and its generated hooks
// build their clients at module scope. That is right for an app, and it
// leaves no way to point PART of the tree somewhere else: a sample app in a
// product tour that reads fixture data while the person's own chats and
// projects beside it stay live, a preview panel, a storybook-like gallery
// inside the app. Routing by request content (an interceptor that recognises
// "sample" ids) is the alternative, and it fails open: the first request it
// does not recognise goes to the real backend.
//
// HOW. The QueryClient carries the transport. React context cannot be read
// inside a queryFn or a mutationFn, but TanStack Query passes the QueryClient
// that runs them as `context.client` (queries from 5.66.0, mutations from
// 5.89.0). So a scope is a transport bound to a QueryClient of its own:
// createTransportScope records the binding (transportOf), and
// TransportScopeProvider mounts that QueryClient over the subtree. Every read
// the subtree makes — a hook, a `fetchQuery`/`prefetchQuery` on the scope's
// client outside React, a `<service>Mutations(scope.queryClient)` write —
// resolves its client from the QueryClient it runs on, through scopedClient.
//
// The scope's id is NOT part of any query key. A scope always owns its
// QueryClient, so its cache is isolated by construction, and a generated
// `<service>Keys.*` stays a module constant: an app's hand-written
// `invalidateQueries({ queryKey: orderServiceKeys.all })` keeps matching under
// a scope and outside it.
//
// Outside a scope — or on a TanStack Query too old to pass `context.client` —
// scopedClient returns the fallback, the module client, which is exactly what
// a generated hook used before this module existed.
//
// WHY A SUBPATH AND NOT THE BARREL. It imports @tanstack/react-query, an
// OPTIONAL peer, for the same reason as ./service-hooks.
import type { DescService } from "@bufbuild/protobuf";
import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createContext,
  createElement,
  useContext,
  type ReactElement,
  type ReactNode,
} from "react";

/**
 * A subtree's transport and the QueryClient that caches what it reads.
 *
 * `readOnly` is a flag for the app's own UI to read (hide write controls,
 * show a "sample data" note). The runtime enforces nothing with it: what a
 * scope's transport refuses is the transport's business.
 */
export interface TransportScope {
  /** A name for the scope, for logs and debugging. Never part of a query key. */
  readonly id: string;
  /** Where every rpc made under this scope goes. */
  readonly transport: Transport;
  /** The scope's own cache. Its binding to `transport` is what scopedClient reads. */
  readonly queryClient: QueryClient;
  /** Whether the app should treat the subtree as read-only. Advisory. */
  readonly readOnly: boolean;
}

/** What createTransportScope takes. */
export interface TransportScopeOptions {
  /** A name for the scope, for logs and debugging. */
  id: string;
  /** Where every rpc made under the scope goes: a fixture or mock transport, a second backend. */
  transport: Transport;
  /**
   * The scope's cache. Defaults to a new QueryClient with React Query's
   * defaults. Pass one to set defaultOptions; never pass the app's own: its
   * reads would then all go to this transport.
   */
  queryClient?: QueryClient;
  /** Whether the app should treat the subtree as read-only. Default false. */
  readOnly?: boolean;
}

/**
 * What scopedClient reads from: React Query's query or mutation function
 * context, or anything carrying the QueryClient a call runs on.
 */
export interface ScopedClientContext {
  readonly client?: QueryClient;
}

// QueryClient → the transport its scope was created with. A WeakMap, so a
// scope's client and its transport are collected with the scope.
const transports = new WeakMap<QueryClient, Transport>();

// Transport → (service → client). Clients are cheap but not free, and a hook
// that called createClient on every render would hand its consumers a new
// identity each time.
const clients = new WeakMap<Transport, WeakMap<DescService, unknown>>();

/**
 * createTransportScope binds a transport to a QueryClient: every generated
 * read and write that runs on that QueryClient goes to the transport.
 *
 * A QueryClient belongs to one transport. Binding one that is already bound
 * to a different transport throws, because the second binding would silently
 * move every read of the first scope.
 */
export function createTransportScope(
  options: TransportScopeOptions,
): TransportScope {
  const queryClient = options.queryClient ?? new QueryClient();
  const bound = transports.get(queryClient);
  if (bound !== undefined && bound !== options.transport) {
    throw new Error(
      `@reliantlabs/forge-web-runtime: createTransportScope("${options.id}") was given a ` +
        "QueryClient already bound to another transport. Each scope needs a " +
        "QueryClient of its own.",
    );
  }
  transports.set(queryClient, options.transport);
  return {
    id: options.id,
    transport: options.transport,
    queryClient,
    readOnly: options.readOnly ?? false,
  };
}

/**
 * transportOf returns the transport a QueryClient was bound to by
 * createTransportScope, or undefined for any other QueryClient (the app's
 * own).
 */
export function transportOf(queryClient: QueryClient): Transport | undefined {
  return transports.get(queryClient);
}

/**
 * clientFor returns a Connect client for `service` over `transport`. It is
 * memoized per transport and service, so two calls return the same object
 * and a hook may call it on every render.
 */
export function clientFor<S extends DescService>(
  service: S,
  transport: Transport,
): Client<S> {
  let byService = clients.get(transport);
  if (byService === undefined) {
    byService = new WeakMap();
    clients.set(transport, byService);
  }
  let client = byService.get(service) as Client<S> | undefined;
  if (client === undefined) {
    client = createClient(service, transport);
    byService.set(service, client);
  }
  return client;
}

/**
 * scopedClient is what a generated hook file calls: a function of React
 * Query's query or mutation function context that returns the client to
 * use. On a QueryClient bound by createTransportScope it is that scope's
 * client; on any other, or with no context at all, it is `fallback`.
 *
 *   const clientOf = scopedClient(OrderService, connectClient(OrderService));
 *   queryFn: (ctx) => clientOf(ctx).getOrder(req, { signal: ctx.signal })
 */
export function scopedClient<S extends DescService>(
  service: S,
  fallback: Client<S>,
): (context?: ScopedClientContext) => Client<S> {
  return (context) => {
    const transport =
      context?.client === undefined
        ? undefined
        : transports.get(context.client);
    return transport === undefined ? fallback : clientFor(service, transport);
  };
}

const ScopeContext = createContext<TransportScope | null>(null);

/**
 * TransportScopeProvider mounts a scope over its children: the scope's
 * QueryClient (a QueryClientProvider) and the scope itself (useTransportScope).
 * A scope nested in another wins for its subtree.
 *
 * Do not mount another QueryClientProvider inside it: generated hooks below
 * that one would run on its client and leave the scope.
 */
export function TransportScopeProvider({
  scope,
  children,
}: {
  scope: TransportScope;
  children?: ReactNode;
}): ReactElement {
  return createElement(
    QueryClientProvider,
    { client: scope.queryClient },
    createElement(ScopeContext.Provider, { value: scope }, children),
  );
}

/** The nearest TransportScope above this component, or null outside any. */
export function useTransportScope(): TransportScope | null {
  return useContext(ScopeContext);
}

/** The nearest scope's transport, or `fallback` (the app's) outside any. */
export function useTransport(fallback: Transport): Transport {
  return useContext(ScopeContext)?.transport ?? fallback;
}

/**
 * useClient is a hand-written hook's way to call an rpc: the client for
 * `service` over the nearest scope's transport, or `fallback` (the app's
 * module client) outside any. Memoized, so its identity is stable across
 * renders.
 */
export function useClient<S extends DescService>(
  service: S,
  fallback: Client<S>,
): Client<S> {
  const scope = useContext(ScopeContext);
  return scope === null ? fallback : clientFor(service, scope.transport);
}
