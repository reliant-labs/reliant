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
// WHAT IT DELIBERATELY DOES NOT DO. It does not emit error toasts — the
// app-wide MutationCache in src/lib/query-client.ts is the single error-toast
// chokepoint, and pages that render errors inline pass
// `meta: { silenceErrorToast: true }`. It never defaults success-side UX
// either: "CreateOrder succeeded" leaks RPC names into the UI, so the toast /
// redirect / inline confirmation stays the call site's via options.onSuccess.
import {
  create,
  toJson,
  type DescMessage,
  type MessageInitShape,
} from "@bufbuild/protobuf";
import {
  useMutation,
  useQuery,
  useQueryClient,
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

/** The three key-scope builders a generated key factory is assembled from. */
export interface ServiceKeyBuilders {
  /** Every query on this service: [service]. */
  readonly all: readonly [string];
  /** Every query for one CRUD entity: [service, entity]. Mutations invalidate this. */
  entity(name: string): readonly [string, string];
  /**
   * One query: [service, entity?, method, protojson(req)]. Returns the
   * per-request key FUNCTION, which is what both the query hook and any
   * call-site prefetch/setQueryData use.
   */
  query<Schema extends DescMessage>(
    method: string,
    schema: Schema,
    entityScope?: string,
  ): (req: MessageInitShape<Schema>) => ServiceQueryKey;
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
  return {
    all: [service] as const,
    entity: (name: string) => [service, name] as const,
    query:
      <Schema extends DescMessage>(
        method: string,
        schema: Schema,
        entityScope?: string,
      ) =>
      (req: MessageInitShape<Schema>): ServiceQueryKey =>
        entityScope === undefined
          ? ([service, method, requestKey(schema, req)] as const)
          : ([service, entityScope, method, requestKey(schema, req)] as const),
  };
}

/** A generated query hook: `useGetOrder(req, options?)`. */
export type ServiceQueryHook<Schema extends DescMessage, Response> = (
  req: MessageInitShape<Schema>,
  options?: Partial<UseQueryOptions<Response, ConnectClientError>>,
) => UseQueryResult<Response, ConnectClientError>;

/** A generated mutation hook: `useCreateOrder(options?)`. */
export type ServiceMutationHook<Schema extends DescMessage, Response> = (
  options?: Partial<
    UseMutationOptions<Response, ConnectClientError, MessageInitShape<Schema>>
  >,
) => UseMutationResult<Response, ConnectClientError, MessageInitShape<Schema>>;

/**
 * createQueryHook builds one useQuery wrapper.
 *
 * The signature it returns is the one call sites already use — `(req,
 * options?)`, with the error typed as ConnectClientError rather than React
 * Query's default `Error`. That type is declared on the OPTIONS parameter
 * because React Query takes the result's error type from the same TError it
 * reads there, so `query.error` is a ConnectClientError at every call site
 * with nothing to pass. Typing it as `Error` hid `.reason`, `.codeName`, `.status`
 * and `.retryable`, and left `err.message.includes(...)` as the only
 * expression that compiled — the one thing the error contract forbids.
 */
export function createQueryHook<Schema extends DescMessage, Response>(
  keyFor: (req: MessageInitShape<Schema>) => ServiceQueryKey,
  call: (req: MessageInitShape<Schema>) => Promise<Response>,
): ServiceQueryHook<Schema, Response> {
  return (req, options) =>
    useQuery({
      queryKey: keyFor(req),
      queryFn: () => call(req),
      ...options,
    });
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
  return (options) => {
    const queryClient = useQueryClient();
    const { onSuccess, ...rest } = options ?? {};
    return useMutation({
      mutationFn: (req: MessageInitShape<Schema>) => call(req),
      onSuccess: (...args) => {
        queryClient.invalidateQueries({ queryKey: invalidateKey });
        onSuccess?.(...args);
      },
      ...rest,
    });
  };
}
