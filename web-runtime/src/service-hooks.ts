"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Every export below is a React hook or builds one, so this module belongs in
// the CLIENT graph. Without the directive the Next.js App Router compiles it
// into the SERVER graph, where it binds a SECOND @tanstack/react-query
// instance and every generated hook throws "No QueryClient set" at runtime —
// invisible to tsc, eslint AND `next build`. published-surface.test.ts asserts
// the directive survives into dist/. It is NOT applied package-wide: the
// ./interceptors subpath exists to be importable without React.
//
// The React Query wrapper machinery behind forge's generated per-service hook
// files (`src/hooks/<svc>-service-hooks_gen.ts`).
//
// WHAT WAS HERE BEFORE. Each generated file re-emitted the same three things
// per RPC: a query-key entry, a useQuery or useMutation wrapper, and — for
// mutations — the entity-scoped invalidateQueries plus the onSuccess
// composition, with the reasoning for each re-inlined as comments. A
// five-service project rendered five copies of machinery that differed only in
// RPC names and message types. This module IS that machinery, written once.
// The generated file keeps exactly what varies per RPC: the name, the request
// schema, the response type, query-vs-mutation, and the entity scope.
//
// WHY A SUBPATH AND NOT THE BARREL. This module imports @tanstack/react-query.
// Every browser frontend forge scaffolds depends on it — but the barrel is
// also what a consumer imports for interceptors and errors alone, and
// anchoring React Query there would make a non-React consumer resolve a
// package it never installed. Same reasoning as ./mock-transport and ./otel:
// an engine with its own dependency footprint gets its own entry point, and
// React Query is an OPTIONAL peer.
//
// WHAT IT DELIBERATELY DOES NOT DO. It does not emit error toasts — they
// belong to one app-wide chokepoint, a MutationCache onError on the app's
// QueryClient (a forge scaffold installs it in src/lib/query-client.ts, and
// pages that render errors inline pass `meta: { silenceErrorToast: true }`);
// an app without one handles errors at the call site. It never defaults
// success-side UX either: "CreateOrder succeeded" leaks RPC names into the
// UI, so the toast / redirect / inline confirmation stays the call site's via
// options.onSuccess.
import {
  create,
  toJson,
  type DescMessage,
  type MessageInitShape,
} from "@bufbuild/protobuf";
import {
  infiniteQueryOptions,
  queryOptions,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type DataTag,
  type InfiniteData,
  type UseInfiniteQueryOptions,
  type UseInfiniteQueryResult,
  type UseMutationOptions,
  type UseMutationResult,
  type UseQueryOptions,
  type UseQueryResult,
} from "@tanstack/react-query";

import type { ConnectClientError } from "./errors.js";

/**
 * A generated query key. The tuple is [service, entity?, method,
 * protojson(req)] for a method key and [service] / [service, entity] for the
 * broader scopes.
 */
export type ServiceQueryKey = readonly unknown[];

/**
 * requestKey projects a request init into protojson for use inside a query
 * key. Two reasons this is not the raw request object:
 *
 *   - React Query's default hasher is JSON.stringify, which THROWS on bigint
 *     fields (any proto int64) — the raw message crashes the cache.
 *   - protojson normalizes representations, so { id: 1 } and { id: "1" } land
 *     in ONE cache entry instead of silently splitting the cache.
 *
 * Exported because a hand-written hook that wants to share a generated hook's
 * cache entry has to hash its request the identical way.
 */
export function requestKey<Schema extends DescMessage>(
  schema: Schema,
  req: MessageInitShape<Schema>,
): unknown {
  return toJson(schema, create(schema, req));
}

/**
 * One read's key entry in a generated factory: `orderServiceKeys.getOrder`.
 *
 * Called, it is that request's key, [service, entity?, method,
 * protojson(req)]. Its `.all` is the rpc's scope, [service, entity?,
 * method]: a prefix of every key the rpc caches under — every request, the
 * plain query and the `<method>Infinite` read alike — and of no other rpc's.
 *
 *   queryClient.invalidateQueries({ queryKey: orderServiceKeys.listOrders.all })
 *   queryClient.setQueriesData({ queryKey: orderServiceKeys.getOrder.all }, patch)
 *
 * It exists so code never names a key's tuple positions. A predicate like
 * `query.queryKey[1] === "listOrders"` breaks silently when a
 * `// forge:query-scope` marker inserts the entity at index 1; `.all` is
 * built by the same function that builds the keys, so it moves with them.
 *
 * `.all` is not data-tagged. It names a SET of entries, and for a paged
 * read that set holds two shapes (the response, and the infinite read's
 * `{ pages, pageParams }`), so a `setQueriesData` updater over it must
 * handle both.
 */
export type ServiceMethodKey<Schema extends DescMessage> = ((
  req: MessageInitShape<Schema>,
) => ServiceQueryKey) & {
  /** Every cached request of this rpc: [service, entity?, method]. */
  readonly all: ServiceQueryKey;
};

/** The three key-scope builders a generated key factory is assembled from. */
export interface ServiceKeyBuilders {
  /** Every query on this service: [service]. */
  readonly all: readonly [string];
  /** Every query for one CRUD entity: [service, entity]. Mutations invalidate this. */
  entity(name: string): readonly [string, string];
  /**
   * One query: [service, entity?, method, protojson(req)]. Returns the
   * per-request key FUNCTION, which is what both the query hook and any
   * call-site prefetch/setQueryData use, with the rpc's scope
   * [service, entity?, method] on it as `.all` (ServiceMethodKey).
   */
  query<Schema extends DescMessage>(
    method: string,
    schema: Schema,
    entityScope?: string,
  ): ServiceMethodKey<Schema>;
  /**
   * One paged read: [service, entity?, method, protojson(req), "infinite"],
   * where req is the request WITHOUT its page token — the hook owns the
   * token, so every page of one listing shares one cache entry.
   *
   * It nests under the same scopes as `query`, so an entity or service-wide
   * invalidation refetches it. The "infinite" suffix keeps it OFF the plain
   * query's entry for the same request: an infinite query caches
   * `{ pages, pageParams }`, not one response, and a plain hook reading that
   * entry would get the wrong shape.
   */
  infinite<Schema extends DescMessage>(
    method: string,
    schema: Schema,
    entityScope?: string,
  ): (req: PageRequest<Schema>) => ServiceQueryKey;
}

/**
 * Omit distributed over a union. MessageInitShape is `Message | init
 * object`, and a plain Omit over a union keeps only the keys both members
 * share — which would silently drop every field from the init side.
 */
type DistributiveOmit<T, K extends PropertyKey> = T extends unknown
  ? Omit<T, K>
  : never;

/**
 * The request a generated infinite hook takes: the rpc's request without
 * `pageToken`, which the hook supplies page by page.
 */
export type PageRequest<Schema extends DescMessage> = DistributiveOmit<
  MessageInitShape<Schema>,
  "pageToken"
>;

/**
 * The response shape forge's list-pagination convention (AIP-158) gives a
 * paged read: `string next_page_token`, empty on the last page. Optional so
 * an `optional string next_page_token` response satisfies it too.
 */
export interface PagedResponse {
  nextPageToken?: string;
}

/**
 * pageRequestKey is requestKey with the page token dropped, so every page of
 * one listing hashes to one key even when a caller (JS, or a cast) passes a
 * token anyway. It is dropped from the protojson rather than from the init
 * object: a spread Message keeps its $typeName, and create() would hand that
 * token-less copy back as if it were a complete message.
 */
function pageRequestKey<Schema extends DescMessage>(
  schema: Schema,
  req: PageRequest<Schema>,
): unknown {
  const json = requestKey(schema, req as unknown as MessageInitShape<Schema>);
  if (json === null || typeof json !== "object" || Array.isArray(json)) {
    return json;
  }
  const { pageToken: _pageToken, ...rest } = json as Record<string, unknown>;
  return rest;
}

/**
 * serviceKeys returns the builders for one service's query-key factory.
 *
 * Cache-key semantics are a compatibility contract, not an implementation
 * detail: an existing app's
 * `invalidateQueries({ queryKey: orderServiceKeys.order })` has to keep
 * matching the keys its queries were registered under across a version bump.
 * So the tuple order and contents produced here are fixed, and identical to
 * what forge previously inlined into every generated hook file.
 */
export function serviceKeys(service: string): ServiceKeyBuilders {
  // The rpc scope every key below starts with. Both builders derive their
  // keys from it, so `.all` is a prefix of them by construction, not by a
  // second copy of the tuple layout.
  const rpcScope = (method: string, entityScope?: string): ServiceQueryKey =>
    entityScope === undefined
      ? ([service, method] as const)
      : ([service, entityScope, method] as const);
  return {
    all: [service] as const,
    entity: (name: string) => [service, name] as const,
    query: <Schema extends DescMessage>(
      method: string,
      schema: Schema,
      entityScope?: string,
    ): ServiceMethodKey<Schema> => {
      const all = rpcScope(method, entityScope);
      return Object.assign(
        (req: MessageInitShape<Schema>): ServiceQueryKey => [
          ...all,
          requestKey(schema, req),
        ],
        { all },
      );
    },
    infinite:
      <Schema extends DescMessage>(
        method: string,
        schema: Schema,
        entityScope?: string,
      ) =>
      (req: PageRequest<Schema>): ServiceQueryKey => [
        ...rpcScope(method, entityScope),
        pageRequestKey(schema, req),
        "infinite",
      ],
  };
}

/**
 * How a generated file calls one rpc. `signal` is React Query's: a query
 * that is unmounted or superseded (a new key while typing) aborts its
 * request instead of letting it land on a cache entry nobody reads.
 */
export type ServiceCall<Schema extends DescMessage, Response> = (
  req: MessageInitShape<Schema>,
  signal?: AbortSignal,
) => Promise<Response>;

/**
 * The options a generated read takes — its hook (`useGetOrder(req,
 * options?)`) and its query-options builder
 * (`orderServiceQueries.getOrder(req, options?)`) alike: React Query's own,
 * minus the two the read owns, the key and the fetch. Everything else
 * (`select`, `enabled`, `staleTime`, `placeholderData`, …) passes through.
 *
 * TData is what `data` is, exactly as in React Query's own useQuery: the
 * response by default, or whatever `select` returns. A call site mapping the
 * proto response to a view model passes `select: (r) => toView(r)` and gets
 * `data: View` — no useMemo over `data`, and React Query re-runs the mapping
 * only when the response changes (or when `select` itself does, so hoist it
 * or wrap it in useCallback when it is expensive).
 */
export type ServiceQueryInput<Response, TData = Response> = Partial<
  Omit<
    UseQueryOptions<Response, ConnectClientError, TData, ServiceQueryKey>,
    "queryKey" | "queryFn"
  >
>;

/**
 * What a query-options builder returns: TanStack's `queryOptions(...)`
 * result, so it goes anywhere React Query takes one —
 * `queryClient.fetchQuery`, `ensureQueryData`, `prefetchQuery`,
 * `useQueries({ queries })`, `useQuery`, `useSuspenseQuery`. Its `queryKey`
 * is data-tagged, so `queryClient.getQueryData(opts.queryKey)` and
 * `setQueryData` are typed as the response with no annotation.
 *
 * fetchQuery / ensureQueryData resolve to the RESPONSE, not to `select`'s
 * TData: they return what the cache holds, and `select` is an observer
 * option that only a hook or useQueries applies.
 */
export type ServiceQueryOptions<Response, TData = Response> = UseQueryOptions<
  Response,
  ConnectClientError,
  TData,
  ServiceQueryKey
> & {
  queryKey: DataTag<ServiceQueryKey, Response, ConnectClientError>;
};

/**
 * A generated query-options builder: `orderServiceQueries.getOrder(req,
 * options?)`. It is the read's single definition — the generated hook
 * `useGetOrder` is `useQuery` over exactly this — so code outside React (an
 * imperative action, a stream handler, a router loader, a prefetch) shares
 * the hook's cache entry and fetch by construction rather than by copying
 * its key and its client call.
 */
export type ServiceQueryOptionsBuilder<Schema extends DescMessage, Response> = <
  TData = Response,
>(
  req: MessageInitShape<Schema>,
  options?: ServiceQueryInput<Response, TData>,
) => ServiceQueryOptions<Response, TData>;

/**
 * A generated query hook: `useGetOrder(req, options?)` — `useQuery` over the
 * service's query-options builder for the same rpc. TData is inferred from
 * `select` and is a parameter of the HOOK, not of the factory, so every call
 * site picks its own.
 */
export type ServiceQueryHook<Schema extends DescMessage, Response> = <
  TData = Response,
>(
  req: MessageInitShape<Schema>,
  options?: ServiceQueryInput<Response, TData>,
) => UseQueryResult<TData, ConnectClientError>;

/**
 * The options a generated infinite read takes: React Query's own, minus the
 * three the read owns — the key, the fetch, and the first page param.
 * `getNextPageParam` defaults to the response's `nextPageToken` (empty means
 * last page) and may be overridden; everything else (`select`,
 * `placeholderData`, `staleTime`, `enabled`, `maxPages`, …) passes through.
 */
export type ServiceInfiniteQueryInput<Response, TData> = Partial<
  Omit<
    UseInfiniteQueryOptions<
      Response,
      ConnectClientError,
      TData,
      ServiceQueryKey,
      string
    >,
    "queryKey" | "queryFn" | "initialPageParam"
  >
>;

/**
 * What an infinite query-options builder returns: TanStack's
 * `infiniteQueryOptions(...)` result, for `queryClient.fetchInfiniteQuery`,
 * `prefetchInfiniteQuery`, `ensureInfiniteQueryData` and
 * `useInfiniteQuery`. Its key is data-tagged as `InfiniteData<Response,
 * string>`, the `{ pages, pageParams }` the cache holds.
 */
export type ServiceInfiniteQueryOptions<
  Response,
  TData = InfiniteData<Response, string>,
> = UseInfiniteQueryOptions<
  Response,
  ConnectClientError,
  TData,
  ServiceQueryKey,
  string
> & {
  queryKey: DataTag<
    ServiceQueryKey,
    InfiniteData<Response, string>,
    ConnectClientError
  >;
};

/**
 * A generated infinite query-options builder:
 * `orderServiceQueries.listOrdersInfinite(req, options?)`, for a read that
 * follows forge's list-pagination shape. `useListOrdersInfinite` is
 * `useInfiniteQuery` over exactly this.
 */
export type ServiceInfiniteQueryOptionsBuilder<
  Schema extends DescMessage,
  Response extends PagedResponse,
> = <TData = InfiniteData<Response, string>>(
  req: PageRequest<Schema>,
  options?: ServiceInfiniteQueryInput<Response, TData>,
) => ServiceInfiniteQueryOptions<Response, TData>;

/**
 * A generated infinite-query hook: `useListOrdersInfinite(req, options?)`,
 * for a read that follows forge's list-pagination shape (`string page_token`
 * on the request, `string next_page_token` on the response).
 *
 * `req` is the first page's request without its token. `data` is React
 * Query's `{ pages, pageParams }` by default; `select` reshapes it, so
 * flattening the pages into one list is
 * `select: (d) => d.pages.flatMap((p) => p.orders)`.
 */
export type ServiceInfiniteQueryHook<
  Schema extends DescMessage,
  Response extends PagedResponse,
> = <TData = InfiniteData<Response, string>>(
  req: PageRequest<Schema>,
  options?: ServiceInfiniteQueryInput<Response, TData>,
) => UseInfiniteQueryResult<TData, ConnectClientError>;

/**
 * A generated mutation hook: `useCreateOrder(options?)`.
 *
 * TContext is React Query's fourth type parameter — what `onMutate` returns
 * and `onError` / `onSettled` receive back. It is a parameter of the HOOK,
 * not of the factory, so each call site gets its own: TypeScript infers it
 * from the `onMutate` the caller passes, and an optimistic update reads its
 * rollback off `context` with no cast. Fixing it at the factory (or leaving
 * it out, which pinned it to `unknown`) forced every such call site to
 * narrow `unknown` by hand. It defaults to `unknown`, so a call without
 * `onMutate` types exactly as before.
 */
export type ServiceMutationHook<Schema extends DescMessage, Response> = <
  TContext = unknown,
>(
  options?: Partial<
    UseMutationOptions<
      Response,
      ConnectClientError,
      MessageInitShape<Schema>,
      TContext
    >
  >,
) => UseMutationResult<
  Response,
  ConnectClientError,
  MessageInitShape<Schema>,
  TContext
>;

/**
 * createQueryOptions builds one read's query-options builder — the single
 * definition of its cache key and its fetch that the generated hook, and
 * every caller outside React, share.
 *
 * The error is typed as ConnectClientError rather than React Query's default
 * `Error`, on the options themselves, because React Query takes a result's
 * error type from the TError it reads there: `query.error` is a
 * ConnectClientError at every call site with nothing to pass. Typing it as
 * `Error` hid `.reason`, `.codeName`, `.status` and `.retryable`, and left
 * `err.message.includes(...)` as the only expression that compiled — the one
 * thing the error contract forbids.
 *
 * The caller's options are spread BEFORE the key and the fetch: a caller may
 * set anything else, never the two that make this read this read.
 */
export function createQueryOptions<Schema extends DescMessage, Response>(
  keyFor: (req: MessageInitShape<Schema>) => ServiceQueryKey,
  call: ServiceCall<Schema, Response>,
): ServiceQueryOptionsBuilder<Schema, Response> {
  return <TData = Response>(
    req: MessageInitShape<Schema>,
    options?: ServiceQueryInput<Response, TData>,
  ): ServiceQueryOptions<Response, TData> =>
    queryOptions<Response, ConnectClientError, TData, ServiceQueryKey>({
      ...options,
      queryKey: keyFor(req),
      queryFn: ({ signal }) => call(req, signal),
    }) as ServiceQueryOptions<Response, TData>;
}

/**
 * createQueryHook builds one useQuery wrapper over a read's query-options
 * builder. It adds nothing to the options: a hook and a `fetchQuery` of the
 * same request therefore read and write one cache entry with one fetch.
 */
export function createQueryHook<Schema extends DescMessage, Response>(
  optionsFor: ServiceQueryOptionsBuilder<Schema, Response>,
): ServiceQueryHook<Schema, Response> {
  return <TData = Response>(
    req: MessageInitShape<Schema>,
    options?: ServiceQueryInput<Response, TData>,
  ) => useQuery(optionsFor(req, options));
}

/**
 * createInfiniteQueryOptions builds one paged read's infinite query-options
 * builder.
 *
 * The page token is the builder's: the first page is fetched with the
 * caller's request as given (no token, so an `optional string page_token`
 * stays unset), each later one with the previous response's
 * `nextPageToken`, and an empty token ends the listing. The key is
 * `keys.infinite(...)`, which strips the token and sits under the service and
 * entity scopes, so the mutations that invalidate a plain list refetch this
 * one too.
 *
 * The caller's options are spread BETWEEN the default getNextPageParam and
 * the three fields the read owns: a caller may replace the default, never the
 * key, the fetch or the first page.
 */
export function createInfiniteQueryOptions<
  Schema extends DescMessage,
  Response extends PagedResponse,
>(
  keyFor: (req: PageRequest<Schema>) => ServiceQueryKey,
  call: ServiceCall<Schema, Response>,
): ServiceInfiniteQueryOptionsBuilder<Schema, Response> {
  return <TData = InfiniteData<Response, string>>(
    req: PageRequest<Schema>,
    options?: ServiceInfiniteQueryInput<Response, TData>,
  ): ServiceInfiniteQueryOptions<Response, TData> =>
    infiniteQueryOptions<
      Response,
      ConnectClientError,
      TData,
      ServiceQueryKey,
      string
    >({
      getNextPageParam: (last) => last.nextPageToken || undefined,
      ...options,
      queryKey: keyFor(req),
      queryFn: ({ pageParam, signal }) =>
        call(
          (pageParam === ""
            ? req
            : {
                ...req,
                pageToken: pageParam,
              }) as unknown as MessageInitShape<Schema>,
          signal,
        ),
      initialPageParam: "",
    }) as ServiceInfiniteQueryOptions<Response, TData>;
}

/**
 * createInfiniteQueryHook builds one useInfiniteQuery wrapper over a paged
 * read's infinite query-options builder, adding nothing to it — so a hook
 * and a `fetchInfiniteQuery` of the same request share one cache entry.
 */
export function createInfiniteQueryHook<
  Schema extends DescMessage,
  Response extends PagedResponse,
>(
  optionsFor: ServiceInfiniteQueryOptionsBuilder<Schema, Response>,
): ServiceInfiniteQueryHook<Schema, Response> {
  return <TData = InfiniteData<Response, string>>(
    req: PageRequest<Schema>,
    options?: ServiceInfiniteQueryInput<Response, TData>,
  ) => useInfiniteQuery(optionsFor(req, options));
}

/**
 * createMutationHook builds one useMutation wrapper with scoped invalidation.
 *
 * invalidateKey is the entity scope when the RPC maps to a CRUD entity, and
 * the whole-service scope otherwise — a bespoke mutation may touch anything,
 * so over-invalidating is the safe default there.
 *
 * The invalidation is composed BEFORE the caller's onSuccess and is never
 * replaced by it: onSuccess is destructured out and re-invoked after the
 * invalidate, while the rest of the options object is spread separately. A
 * caller passing onSuccess therefore ADDS behaviour, rather than silently
 * disabling the cache refresh — which is exactly what a plain `...options`
 * spread over a default onSuccess would do.
 */
export function createMutationHook<Schema extends DescMessage, Response>(
  invalidateKey: ServiceQueryKey,
  call: (req: MessageInitShape<Schema>) => Promise<Response>,
): ServiceMutationHook<Schema, Response> {
  return <TContext = unknown>(
    options?: Partial<
      UseMutationOptions<
        Response,
        ConnectClientError,
        MessageInitShape<Schema>,
        TContext
      >
    >,
  ) => {
    const queryClient = useQueryClient();
    const { onSuccess, ...rest } = options ?? {};
    return useMutation<
      Response,
      ConnectClientError,
      MessageInitShape<Schema>,
      TContext
    >({
      mutationFn: (req: MessageInitShape<Schema>) => call(req),
      onSuccess: (...args) => {
        queryClient.invalidateQueries({ queryKey: invalidateKey });
        onSuccess?.(...args);
      },
      ...rest,
    });
  };
}
