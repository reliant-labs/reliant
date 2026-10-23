import { useQueries } from "@tanstack/react-query";

import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";
import type { UseQueryResult } from "@tanstack/react-query";

/**
 * Relational-join primitives for composite reads.
 *
 * Why this exists
 * ---------------
 * A frontend that needs Product → Variant → Price shouldn't hand-roll the
 * join client-side: three separate `useQuery` calls, three loading/error
 * gates to reconcile by hand, and an ad-hoc `Map` to stitch rows by foreign
 * key. Every page re-invents it slightly differently and the loading logic
 * rots (see `use-query-resource.ts`: "loading is not the negative of
 * success").
 *
 * This module ships two reusable, app-agnostic pieces:
 *
 *   1. Fetch — {@link combineQueries} folds a fixed set of already-created
 *      query results into ONE tristate resource (loading if any is loading,
 *      error if any errored, success only when all resolved) with a typed
 *      tuple of their data. {@link useJoinedQueries} fans out a *dynamic*
 *      set of related queries in parallel (React Query's `useQueries`) —
 *      e.g. one child query per parent row — into a single tristate list.
 *
 *   2. Stitch — {@link indexBy} / {@link groupBy} build the lookup, and
 *      {@link joinOneToOne} / {@link joinOneToMany} attach related rows to
 *      parents by key. Pure functions: no React, no proto, unit-testable.
 *
 * None of these know anything about Product/Variant/Price — you pass the key
 * accessors. Example (parallel fetch + in-memory join):
 *
 *   const products = useListProducts({});
 *   const variants = useListVariants({});
 *   const prices   = useListPrices({});
 *   const res = combineQueries(products, variants, prices);
 *   if (res.status === "loading") return <SkeletonLoader />;
 *   if (res.status === "error")   return <AlertBanner message={userMessage(res.error)} />;
 *   const [prodResp, varResp, priceResp] = res.data;
 *   const priced = joinOneToOne(
 *     varResp.variants, priceResp.prices,
 *     (v) => v.priceId, (p) => p.id,
 *     (variant, price) => ({ ...variant, price }),
 *   );
 *   const catalog = joinOneToMany(
 *     prodResp.products, priced,
 *     (p) => p.id, (v) => v.productId,
 *     (product, variants) => ({ ...product, variants }),
 *   );
 */

// ---------------------------------------------------------------------------
// Pure lookup builders
// ---------------------------------------------------------------------------

/**
 * indexBy builds a `key → item` map (one-to-one). Later items with the same
 * key win, matching the "last row for this id" semantics of a keyed fetch.
 */
export function indexBy<T, K>(
  items: readonly T[],
  keyFn: (item: T) => K,
): Map<K, T> {
  const map = new Map<K, T>();
  for (const item of items) {
    map.set(keyFn(item), item);
  }
  return map;
}

/**
 * groupBy buckets items under their key (one-to-many). Preserves input order
 * within each bucket.
 */
export function groupBy<T, K>(
  items: readonly T[],
  keyFn: (item: T) => K,
): Map<K, T[]> {
  const map = new Map<K, T[]>();
  for (const item of items) {
    const key = keyFn(item);
    const bucket = map.get(key);
    if (bucket) {
      bucket.push(item);
    } else {
      map.set(key, [item]);
    }
  }
  return map;
}

// ---------------------------------------------------------------------------
// Pure stitch operations
// ---------------------------------------------------------------------------

/**
 * joinOneToOne attaches the single related row (or `undefined`) to each
 * parent, matched by the parent's foreign key against the related row's key.
 */
export function joinOneToOne<P, R, K, O>(
  parents: readonly P[],
  related: readonly R[],
  parentForeignKey: (parent: P) => K,
  relatedKey: (related: R) => K,
  attach: (parent: P, related: R | undefined) => O,
): O[] {
  const byKey = indexBy(related, relatedKey);
  return parents.map((parent) =>
    attach(parent, byKey.get(parentForeignKey(parent))),
  );
}

/**
 * joinOneToMany attaches every matching child (possibly none) to each parent,
 * matched by the parent's key against each child's foreign key.
 */
export function joinOneToMany<P, C, K, O>(
  parents: readonly P[],
  children: readonly C[],
  parentKey: (parent: P) => K,
  childForeignKey: (child: C) => K,
  attach: (parent: P, children: C[]) => O,
): O[] {
  const byForeignKey = groupBy(children, childForeignKey);
  return parents.map((parent) =>
    attach(parent, byForeignKey.get(parentKey(parent)) ?? []),
  );
}

// ---------------------------------------------------------------------------
// Combine a fixed set of query results into one tristate resource
// ---------------------------------------------------------------------------

type ExtractQueryData<T> =
  T extends UseQueryResult<infer D, unknown> ? D : never;

// Maps a tuple of UseQueryResult into the tuple of their data types.
type CombinedData<T extends readonly UseQueryResult<unknown, unknown>[]> = {
  [K in keyof T]: ExtractQueryData<T[K]>;
};

// The error is ConnectClientError — the shape the transport's error-normalize
// interceptor throws, and therefore the shape every generated hook result
// carries into here. Widening it to `Error` on the way out hid
// reason/code/status/retryable from `res.error` and left message prose as the
// only thing the caller could branch on.
export type CombinedResource<TData> =
  | { status: "loading" }
  | { status: "error"; error: ConnectClientError; refetch: () => void }
  | {
      status: "success";
      data: TData;
      isFetching: boolean;
      refetch: () => void;
    };

/**
 * combineQueries folds a fixed set of query results (the return values of
 * generated `useGetX` / `useListX` hooks) into ONE discriminated tristate so
 * a composite read has a single loading/error gate instead of N. On success,
 * `data` is the tuple of each query's data in argument order.
 *
 * Not a hook — it derives from results the caller already produced, exactly
 * like `useQueryResource`, so it is safe to call conditionally.
 */
export function combineQueries<
  T extends readonly UseQueryResult<unknown, ConnectClientError>[],
>(...queries: T): CombinedResource<CombinedData<T>> {
  const refetch = () => {
    for (const query of queries) {
      void query.refetch();
    }
  };
  if (queries.some((query) => query.isPending)) {
    return { status: "loading" };
  }
  const errored = queries.find(
    (query) => query.isError && query.data === undefined,
  );
  if (errored) {
    return {
      status: "error",
      error: errored.error as ConnectClientError,
      refetch,
    };
  }
  return {
    status: "success",
    data: queries.map((query) => query.data) as unknown as CombinedData<T>,
    isFetching: queries.some((query) => query.isFetching),
    refetch,
  };
}

// ---------------------------------------------------------------------------
// Fan out a dynamic set of related queries in parallel
// ---------------------------------------------------------------------------

/** One query in a {@link useJoinedQueries} fan-out. */
export interface JoinQuerySpec<TData> {
  queryKey: readonly unknown[];
  queryFn: () => Promise<TData>;
  /** Defaults to true. Gate a per-parent child query until its input is known. */
  enabled?: boolean;
}

export type JoinedListResource<TData> =
  | { status: "loading" }
  | { status: "error"; error: ConnectClientError; refetch: () => void }
  | {
      status: "success";
      data: TData[];
      isFetching: boolean;
      refetch: () => void;
    };

/**
 * useJoinedQueries runs a dynamic set of related queries in parallel (React
 * Query's `useQueries`) and returns a single tristate list resource. Use it
 * for the fan-out shape a fixed-arity `combineQueries` can't express — one
 * child query per parent row (a variant fetch per product), a lookup per id —
 * without an N+1 waterfall or a hand-written array of `useQuery` calls.
 *
 * `data` is the array of each query's result in spec order; disabled specs
 * contribute a pending slot, so the resource stays "loading" until every
 * enabled query resolves.
 */
export function useJoinedQueries<TData>(
  specs: readonly JoinQuerySpec<TData>[],
): JoinedListResource<TData> {
  const results = useQueries({
    queries: specs.map((spec) => ({
      queryKey: spec.queryKey,
      queryFn: spec.queryFn,
      enabled: spec.enabled ?? true,
    })),
  }) as UseQueryResult<TData, ConnectClientError>[];

  const refetch = () => {
    for (const result of results) {
      void result.refetch();
    }
  };
  if (results.some((result) => result.isPending)) {
    return { status: "loading" };
  }
  const errored = results.find(
    (result) => result.isError && result.data === undefined,
  );
  if (errored) {
    return {
      status: "error",
      error: errored.error as ConnectClientError,
      refetch,
    };
  }
  return {
    status: "success",
    data: results.map((result) => result.data as TData),
    isFetching: results.some((result) => result.isFetching),
    refetch,
  };
}
