import { useMutation, type UseMutationOptions } from "@tanstack/react-query";

import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";

/**
 * useApiMutation wraps a Connect client promise-returning call in a
 * `@tanstack/react-query` `useMutation`.
 *
 * This helper centralizes the mutation pattern so every RPC-backed
 * mutation hook looks the same. The auto-generated per-service hooks
 * use `useMutation` directly, but this wrapper is available for
 * one-off or custom mutations.
 *
 * Example:
 *
 *   import { connectClient } from "@/lib/connect";
 *   import { UserService } from "@/gen/user/v1/user_pb";
 *
 *   const userClient = connectClient(UserService);
 *
 *   export function useDeleteUser() {
 *     return useApiMutation((req: { id: string }) =>
 *       userClient.deleteUser(req),
 *     );
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
export function useApiMutation<TData, TVariables, TError = ConnectClientError>(
  mutationFn: (variables: TVariables) => Promise<TData>,
  options?: Omit<UseMutationOptions<TData, TError, TVariables>, "mutationFn">,
) {
  return useMutation<TData, TError, TVariables>({
    mutationFn,
    ...options,
  });
}
