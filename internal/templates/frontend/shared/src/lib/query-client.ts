import { Code, ConnectError } from "@connectrpc/connect";
import { userMessage } from "@reliantlabs/forge-web-runtime";
import { MutationCache, QueryClient, type QueryKey } from "@tanstack/react-query";

import { emitToast } from "@/lib/events";

/** Connect codes worth retrying — transient failures only. */
const RETRYABLE_CODES = new Set<Code>([
  Code.Unavailable,
  Code.DeadlineExceeded,
  Code.Aborted,
  Code.ResourceExhausted,
]);

/**
 * isRetryable — true only for transient failures. Handles the runtime's
 * normalized error (carries `.retryable`), a raw ConnectError, and a bare
 * network TypeError. A 4xx (not_found / permission_denied / invalid_argument)
 * is never retried.
 */
function isRetryable(err: unknown): boolean {
  if (err && typeof err === "object" && "retryable" in err) {
    return Boolean((err as { retryable?: unknown }).retryable);
  }
  if (err instanceof ConnectError) return RETRYABLE_CODES.has(err.code);
  return err instanceof TypeError;
}

// ---------------------------------------------------------------------------
// BigInt-safe query-key hashing
//
// React Query hashes every query key with `JSON.stringify`, which THROWS
// `TypeError: Do not know how to serialize a BigInt`. protobuf-es represents
// every proto `int64` / `uint64` as a JS `bigint` — including
// `google.protobuf.Timestamp.seconds`. So the first key that carries a
// `created_at`, a page cursor or an int64 id blows the query up before it ever
// reaches the network. Every project hits this the moment it has an entity
// with a timestamp, which is why the hasher below is part of the scaffold
// rather than something each app rediscovers.
// ---------------------------------------------------------------------------

/** U+0000 — the marker prefix. It cannot occur in JSON output unescaped. */
const NUL = "\u0000";

/** Prefix a serialized bigint carries so it can never read as a plain string. */
const BIGINT_TAG = `${NUL}bigint:`;

/**
 * isPlainObject mirrors the predicate React Query's own `hashKey` uses to
 * decide what to key-sort. query-core does not export it, so the scaffold
 * carries the same logic: `[object Object]` whose prototype is exactly
 * `Object.prototype` (or null). protobuf-es v2 messages are plain objects, so
 * their fields sort like any other request shape.
 */
function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (Object.prototype.toString.call(value) !== "[object Object]") return false;
  const ctor = (value as { constructor?: unknown }).constructor;
  if (ctor === undefined) return true;
  const proto = (ctor as { prototype?: unknown }).prototype;
  if (Object.prototype.toString.call(proto) !== "[object Object]") return false;
  if (!Object.prototype.hasOwnProperty.call(proto, "isPrototypeOf")) return false;
  return Object.getPrototypeOf(value) === Object.prototype;
}

/**
 * bigintSafeReplacer is React Query's default `hashKey` replacer — sort the
 * keys of every plain object so key ORDER never changes the hash — plus two
 * branches that make the encoding total AND injective:
 *
 *   bigint                    -> `"\u0000bigint:<digits>"`
 *   string starting with NUL  -> one extra NUL prepended
 *
 * The second branch is what rules out collisions. A tagged bigint is the only
 * value that can render as a string beginning with EXACTLY ONE NUL: a string
 * that already starts with NUL gains a second one, and a string that doesn't
 * gains none. So `1n` hashes as `"\u0000bigint:1"`, `"1"` hashes as `"1"`, and
 * the literal string `"\u0000bigint:1"` hashes as `"\u0000\u0000bigint:1"` —
 * three distinct keys, three distinct hashes. Distinct bigints differ in their
 * digits, so cache identity is exact.
 *
 * Every other value is passed through untouched, so a key holding no bigint
 * and no NUL-leading string hashes BYTE-IDENTICALLY to React Query's default.
 */
export function bigintSafeReplacer(_key: string, value: unknown): unknown {
  if (typeof value === "bigint") return BIGINT_TAG + value.toString();
  if (typeof value === "string") return value.startsWith(NUL) ? NUL + value : value;
  if (isPlainObject(value)) {
    const sorted: Record<string, unknown> = {};
    for (const key of Object.keys(value).sort()) {
      sorted[key] = value[key];
    }
    return sorted;
  }
  return value;
}

/**
 * bigintSafeHashKey is the QueryClient's `queryKeyHashFn`. Single pass — the
 * replacer does the sorting and the bigint tagging together, so there is no
 * detect-then-rewrite double walk and no lossy `JSON.parse(JSON.stringify(…))`
 * round-trip (which would flatten `Date`/`undefined`/`NaN` on the way through).
 */
export function bigintSafeHashKey(queryKey: QueryKey): string {
  return JSON.stringify(queryKey, bigintSafeReplacer);
}

// Typed mutation meta — `meta: { silenceErrorToast: true }` is the opt-out
// for pages that render the mutation error inline (form banner) and don't
// want the global toast doubling it.
declare module "@tanstack/react-query" {
  interface Register {
    mutationMeta: {
      /** Suppress the app-wide error toast for this mutation. */
      silenceErrorToast?: boolean;
    };
  }
}

/**
 * Shared QueryClient for the app.
 *
 * Error-toast policy lives HERE and only here: the MutationCache onError
 * below is the single chokepoint that surfaces mutation failures as
 * toasts. Generated hooks do not toast; pages opt out per-mutation with
 * `meta: { silenceErrorToast: true }` when they show the error inline.
 *
 * Retry ownership: React Query is the SINGLE retry owner. It structurally
 * knows reads from writes, so QUERIES retry transient failures (up to twice)
 * and MUTATIONS never retry — no duplicate writes. The transport interceptor
 * stack deliberately does NOT retry (see runtime/interceptors.ts); each React
 * Query retry re-runs the queryFn through the whole chain (fresh token +
 * traceparent), so nothing is lost.
 *
 * Defaults chosen for server-backed Connect RPCs:
 * - staleTime: 30s — avoid refetching on every mount for mildly fresh data.
 * - gcTime: 5m — cached data stays around briefly after no observers.
 * - queries.retry — transient-only, capped at 2 (see isRetryable).
 * - mutations.retry: 0 — writes never retry.
 * - refetchOnWindowFocus: false — most RPC responses don't need this; enable
 *   per-query if you want it.
 * - queryKeyHashFn — bigint-safe (see bigintSafeHashKey). Not optional: the
 *   default hasher throws on any key carrying a proto int64 / Timestamp.
 *
 * Tune per-query via `useQuery({ staleTime, retry, ... })` as needed.
 */
export const queryClient = new QueryClient({
  mutationCache: new MutationCache({
    onError: (error, _variables, _context, mutation) => {
      if (mutation.meta?.silenceErrorToast) return;
      emitToast({ message: userMessage(error), variant: "error" });
    },
  }),
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      retry: (failureCount, error) => failureCount < 2 && isRetryable(error),
      retryDelay: (attempt) => Math.min(200 * 2 ** attempt, 4_000),
      refetchOnWindowFocus: false,
      queryKeyHashFn: bigintSafeHashKey,
    },
    mutations: {
      retry: 0,
    },
  },
});
