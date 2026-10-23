import { useQuery, type UseQueryOptions } from "@tanstack/react-query";

import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";

/**
 * useApiQuery wraps a Connect client promise-returning call in a
 * `@tanstack/react-query` `useQuery`.
 *
 * Connect clients return plain Promises, so any RPC can be composed into a
 * React Query query function. This helper centralizes the pattern so every
 * RPC-backed hook looks the same.
 *
 * Extend this module with concrete hooks per service. Example (uncomment
 * and adapt once you have a generated service client):
 *
 *   import { connectClient } from "@/lib/connect";
 *   import { UserService } from "@/gen/user/v1/user_pb";
 *
 *   const userClient = connectClient(UserService);
 *
 *   export function useGetUser(id: string) {
 *     return useApiQuery(["user", id], () => userClient.getUser({ id }), {
 *       enabled: Boolean(id),
 *     });
 *   }
 *
 * TError stays a parameter here — unlike the generated hooks, this wrapper
 * takes an ARBITRARY promise, so it cannot know the transport that produced
 * it. It DEFAULTS to ConnectClientError because the documented use (above) is
 * a Connect client call, and that is the shape the transport's error-normalize
 * interceptor throws. Defaulting to React Query's `Error` was the same defect
 * the generated hooks had: it hid reason/code/status/retryable and left
 * message prose as the only thing a call site could match on.
 */
export function useApiQuery<TData, TError = ConnectClientError>(
  key: readonly unknown[],
  fetcher: () => Promise<TData>,
  options?: Omit<
    UseQueryOptions<TData, TError, TData, readonly unknown[]>,
    "queryKey" | "queryFn"
  >,
) {
  return useQuery<TData, TError, TData, readonly unknown[]>({
    queryKey: key,
    queryFn: fetcher,
    ...options,
  });
}
