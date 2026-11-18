import { useEffect, useState } from "react";

import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";
import type { UseQueryResult } from "@tanstack/react-query";

/**
 * useQueryResource — tristate adapter over React Query's UseQueryResult.
 *
 * Why this exists
 * ---------------
 * React Query returns a result where `isLoading`, `isError`, and `data` can
 * co-exist in surprising ways during refetches and stale states. The most
 * destructive bug class in our codebase has been gates like:
 *
 *   const ready = !isLoading && data;       // FALSE during the load window
 *   if (ready) doSomething();               // … which can wipe real state
 *
 * Those boolean gates collapse the loading state into "the negative of
 * success", which is wrong. This helper normalizes the result into a
 * discriminated union the type checker can exhaustively switch on:
 *
 *   const res = useQueryResource(useGetWidget({ id }));
 *   if (res.status === "loading") return <LoadingState />;
 *   if (res.status === "error")   return <ErrorState message={userMessage(res.error)} />;
 *   return <WidgetView widget={res.data} />;
 *
 * Now there is no path where "loading" silently means "complete". The
 * compiler enforces it.
 *
 * Note: this is a derivation, not a replacement. Pass the result of an
 * existing query hook (useApiQuery, generated useGetX, etc.) — the original
 * cache key, refetch behavior, etc. are all preserved.
 *
 * See `frontend/state` ("Loading is not the negative of success").
 */

// TError PROPAGATES from the query you pass in — it is inferred at every real
// call site, so the default only shows up if someone writes the type out by
// hand. It is ConnectClientError because that is what the transport's
// error-normalize interceptor throws and what every generated hook carries;
// React Query's `Error` hid reason/code/status/retryable from `res.error`.
export type QueryResource<TData, TError = ConnectClientError> =
  | { status: "loading" }
  | { status: "error"; error: TError; refetch: () => void }
  | { status: "success"; data: TData; isStale: boolean; refetch: () => void };

export function useQueryResource<TData, TError = ConnectClientError>(
  query: UseQueryResult<TData, TError>,
): QueryResource<TData, TError> {
  // First load — no data yet, no settled error.
  if (query.isPending) {
    return { status: "loading" };
  }
  // Settled with an error AND no usable cached data.
  if (query.isError && query.data === undefined) {
    return {
      status: "error",
      error: query.error,
      refetch: () => void query.refetch(),
    };
  }
  // Settled with data (possibly stale, possibly with a background error
  // surfaced via query.error — callers can re-check via `isStale`).
  return {
    status: "success",
    data: query.data as TData,
    isStale: query.isStale,
    refetch: () => void query.refetch(),
  };
}

/**
 * useDebouncedValue returns a copy of `value` that only updates after it has
 * stopped changing for `delayMs`. Generated list pages feed a scalar filter
 * input's raw value through this before passing it to the List RPC, so typing
 * doesn't fire one server request per keystroke (the <Resource> search box
 * debounces the same way internally).
 */
export function useDebouncedValue<T>(value: T, delayMs = 300): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const handle = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(handle);
  }, [value, delayMs]);
  return debounced;
}
