import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { ConnectError, Code } from '@connectrpc/connect';
import {
  listDaemons,
  createDaemon,
  resumeDaemon,
  type CreateDaemonArgs,
} from '@/services/controlPlane/daemon';
import { getReliantState, isCloudEligible } from '@/services/controlPlane/billing';
import { gitService } from '@/services/controlPlane/git';
import type { CloneRepoArgs } from '@/services/controlPlane/git/types';
import { onboardingService } from '@/services/controlPlane/onboarding';
import type { OnboardingUser } from '@/services/controlPlane/onboarding';

/**
 * Returns true when `err` is a ResourceExhausted Connect error that already
 * carries the `x-reliant-reason` metadata header — i.e. the global
 * `upgradeInterceptor` already routed it into the UpgradeRequiredModal.
 * Daemon-mutation hooks consult this so callers don't double-surface the
 * failure as an inline error banner / toast.
 *
 * Exported for callers using `mutateAsync` who need the same suppression
 * logic in their own try/catch — TanStack Query rejects the awaited promise
 * regardless of the hook's `onError`, so they have to check it themselves.
 */
export function isReasonedQuotaError(err: unknown): boolean {
  return (
    err instanceof ConnectError &&
    err.code === Code.ResourceExhausted &&
    !!err.metadata.get('x-reliant-reason')
  );
}

/**
 * Per-call overrides for the daemon mutation hooks. Callers supply these
 * to react to success / non-reasoned errors at the call site; the hook's
 * own default-`onError` suppresses the reasoned-quota error AHEAD of the
 * caller's `onError`, so callers don't have to write the same suppression
 * check themselves.
 *
 * The shape mirrors TanStack Query's `MutationOptions` callbacks rather
 * than wrapping them so the call site reads the same as a direct
 * useMutation usage.
 */
interface DaemonMutationCallbacks<TVars> {
  onSuccess?: (vars: TVars) => void | Promise<void>;
  onError?: (err: unknown, vars: TVars) => void | Promise<void>;
}

export function useCurrentUser() {
  return useQuery<OnboardingUser | null>({
    queryKey: ['onboarding', 'currentUser'],
    queryFn: () => onboardingService.getCurrentUser(),
    staleTime: 30_000,
    // A focus refetch flips isLoading back to true mid-click and re-disables
    // the Start cloud daemon button, eating the first click.
    refetchOnWindowFocus: false,
  });
}

export function useCloudEligibility() {
  const { data: user, isLoading: userLoading } = useCurrentUser();
  const { data: state, isLoading: stateLoading } = useQuery({
    queryKey: ['onboarding', 'reliantState'],
    queryFn: () => getReliantState(),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });

  const isLoading = userLoading || stateLoading;
  const entitlement = state?.entitlement;
  const eligible = !isLoading && isCloudEligible(entitlement);

  const reason = !user
    ? 'Sign up required'
    : !isCloudEligible(entitlement)
      ? 'No cloud credits available'
      : null;

  return { eligible, reason, isLoading };
}

export function useDaemonList() {
  return useQuery({
    queryKey: ['onboarding', 'daemons'],
    queryFn: async () => {
      const { daemons } = await listDaemons();
      return daemons;
    },
    staleTime: 10_000,
  });
}

/**
 * TanStack-Query mutation for `controlplane.v1.DaemonService.CreateDaemon`.
 *
 * Default behavior:
 *   - On success: invalidates `['onboarding', 'daemons']` so the picker
 *     and the resume pill see the new row without a page refresh; then
 *     calls the caller's `onSuccess` (if any).
 *   - On error: if it's a reasoned quota error (ResourceExhausted +
 *     `x-reliant-reason`), the global `upgradeInterceptor` has already
 *     opened the UpgradeRequiredModal — the hook SWALLOWS the error so the
 *     caller doesn't render a duplicate toast/banner under the modal. Any
 *     other error falls through to the caller's `onError`.
 *
 * Callers that want to await the mutation (e.g. onboarding's
 * ComputeStep) should still use `mutateAsync` and try/catch — the hook's
 * default `onError` only routes display; it never *swallows* on the
 * mutateAsync path (TanStack Query always rejects the returned promise
 * on failure regardless of `onError`).
 */
export function useCreateDaemon(
  callbacks: DaemonMutationCallbacks<CreateDaemonArgs> = {},
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args: CreateDaemonArgs) => createDaemon(args),
    onSuccess: async (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'daemons'] });
      await callbacks.onSuccess?.(vars);
    },
    onError: async (err, vars) => {
      if (isReasonedQuotaError(err)) {
        // Modal already firing — don't double-surface.
        return;
      }
      await callbacks.onError?.(err, vars);
    },
  });
}

/**
 * TanStack-Query mutation for `controlplane.v1.DaemonService.ResumeDaemon`.
 *
 * Same error-routing contract as `useCreateDaemon` — see that hook's
 * docstring. ProjectPicker's "Resume a daemon" button used to hand-roll the
 * reasoned-error check; the hook centralizes it so a future call site
 * can't forget.
 */
export function useResumeDaemon(
  callbacks: DaemonMutationCallbacks<string> = {},
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (daemonId: string) => resumeDaemon(daemonId),
    onSuccess: async (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'daemons'] });
      await callbacks.onSuccess?.(vars);
    },
    onError: async (err, vars) => {
      if (isReasonedQuotaError(err)) {
        return;
      }
      await callbacks.onError?.(err, vars);
    },
  });
}

export function useGitRepos() {
  return useQuery({
    queryKey: ['onboarding', 'gitRepos'],
    queryFn: () => gitService.listRepos(1, 100, 'updated'),
    enabled: false, // manually triggered
  });
}

export function useCloneRepo() {
  return useMutation({
    mutationFn: (args: CloneRepoArgs) => gitService.cloneRepo(args),
  });
}

export function useCompleteOnboarding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Record<string, unknown>) =>
      onboardingService.completeOnboarding(data),
    onSuccess: () => {
      // Optimistically mark onboarding complete so ModernApp doesn't redirect
      // back to ?step=goal before the refetch completes.
      queryClient.setQueryData<OnboardingUser | null>(
        ['onboarding', 'currentUser'],
        (old) => ({
          ...(old ?? { onboardingCompleted: false }),
          onboardingCompleted: true,
        }),
      );
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'currentUser'] });
    },
  });
}
