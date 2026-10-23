import { userMessage } from "@reliantlabs/forge-web-runtime";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";

import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";
import type { UseMutationOptions } from "@tanstack/react-query";

/**
 * useOptimisticMutation — mutate with an optimistic cache write, automatic
 * rollback, and an INLINE error affordance.
 *
 * When to reach for it
 * --------------------
 * Two overlapping situations the plain generated mutation hook doesn't cover
 * on its own:
 *
 *   - Optimistic UI: reflect the change in the list/detail immediately, then
 *     reconcile with the server — rolling the cache back if the write fails.
 *   - The domain RPC hasn't landed yet: the `mutationFn` may be a stub that
 *     rejects. You still want the optimistic update for the demo, and a clear
 *     inline "not saved" message when the stub fails — not a silent no-op.
 *
 * The error is surfaced INLINE (as a `string | null` you render next to the
 * control) rather than as a toast. To avoid the app-wide toast chokepoint
 * doubling it, this hook defaults `meta.silenceErrorToast` to true — flip it
 * back per call if you want both.
 *
 * Example:
 *
 *   const rename = useOptimisticMutation<Task, { id: string; title: string }, Task>({
 *     mutationFn: (vars) => taskClient.renameTask(vars),
 *     queryKey: taskKeys.detail({ id }),
 *     applyOptimistic: (current, vars) =>
 *       current ? { ...current, title: vars.title } : current,
 *   });
 *   // ...
 *   <input onChange={() => rename.clearError()} />
 *   {rename.error && <FormError>{rename.error}</FormError>}
 *   <Button loading={rename.isPending} onClick={() => rename.mutate({ id, title })}>Save</Button>
 *
 * The error type is ConnectClientError — the shape the transport's
 * error-normalize interceptor throws, and the same type the generated hooks
 * carry. It is what reaches the pass-through options typed by it (`retry`,
 * `throwOnError`), so `(_n, err) => err.retryable` compiles here; React
 * Query's default `Error` hid retryable/reason/code/status and left message
 * prose as the only thing a caller could match on.
 */
export interface OptimisticMutationOptions<TData, TVariables, TSnapshot>
  extends Omit<
    UseMutationOptions<
      TData,
      ConnectClientError,
      TVariables,
      { previous?: TSnapshot }
    >,
    "mutationFn" | "onMutate" | "onError" | "onSettled"
  > {
  /** The mutation call. May be a stub while the domain RPC isn't implemented. */
  mutationFn: (variables: TVariables) => Promise<TData>;
  /**
   * Query-key scope to optimistically update, roll back on error, and
   * invalidate when settled. Use the generated key factory, e.g.
   * `taskKeys.detail({ id })`.
   */
  queryKey: readonly unknown[];
  /**
   * Produce the optimistic cache value from the current cached value and the
   * mutation variables. Omit to skip the optimistic write (rollback becomes a
   * no-op) and use this purely for the inline-error affordance.
   */
  applyOptimistic?: (
    current: TSnapshot | undefined,
    variables: TVariables,
  ) => TSnapshot | undefined;
}

export interface OptimisticMutationResult<TData, TVariables> {
  mutate: (variables: TVariables) => void;
  mutateAsync: (variables: TVariables) => Promise<TData>;
  isPending: boolean;
  /** Inline, user-facing message for the last failure, or null. */
  error: string | null;
  /** Clear the inline error — call on input change or before a retry. */
  clearError: () => void;
  /** Clear the inline error AND reset the underlying mutation state. */
  reset: () => void;
}

export function useOptimisticMutation<TData, TVariables, TSnapshot = unknown>(
  options: OptimisticMutationOptions<TData, TVariables, TSnapshot>,
): OptimisticMutationResult<TData, TVariables> {
  const { mutationFn, queryKey, applyOptimistic, meta, ...rest } = options;
  const queryClient = useQueryClient();
  const [error, setError] = useState<string | null>(null);

  const mutation = useMutation<
    TData,
    ConnectClientError,
    TVariables,
    { previous?: TSnapshot }
  >({
    mutationFn,
    // Inline error is the affordance here; silence the global toast by default
    // so a failure isn't reported twice. Callers can re-enable per call.
    meta: { silenceErrorToast: true, ...meta },
    onMutate: async (variables) => {
      setError(null);
      if (!applyOptimistic) return {};
      // Cancel in-flight refetches so they can't clobber the optimistic write.
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<TSnapshot>(queryKey);
      queryClient.setQueryData<TSnapshot>(queryKey, (current) =>
        applyOptimistic(current, variables),
      );
      return { previous };
    },
    onError: (err, _variables, context) => {
      // Roll the cache back to the pre-mutation snapshot.
      if (context && "previous" in context) {
        queryClient.setQueryData(queryKey, context.previous);
      }
      setError(userMessage(err));
    },
    onSettled: () => {
      // Reconcile with the server regardless of outcome.
      void queryClient.invalidateQueries({ queryKey });
    },
    ...rest,
  });

  const clearError = useCallback(() => setError(null), []);
  const reset = useCallback(() => {
    setError(null);
    mutation.reset();
  }, [mutation]);

  return {
    mutate: mutation.mutate,
    mutateAsync: mutation.mutateAsync,
    isPending: mutation.isPending,
    error,
    clearError,
    reset,
  };
}
