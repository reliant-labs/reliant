import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  listDaemons,
  createDaemon,
  resumeDaemon,
  cloneRepo,
  listGitRepos,
  getReliantEntitlement,
  isCloudEligible,
  type ControlPlaneUser,
} from '@/components/OnboardingFlow/api';

import { onboardingService } from '@/services/controlPlane/onboarding';

export function useCurrentUser() {
  return useQuery<ControlPlaneUser | null>({
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
  const { data: entitlementResp, isLoading: entitlementLoading } = useQuery({
    queryKey: ['onboarding', 'reliantEntitlement'],
    queryFn: () => getReliantEntitlement(),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });

  const isLoading = userLoading || entitlementLoading;
  const entitlement = entitlementResp?.entitlement;
  const eligible = !isLoading && isCloudEligible(entitlement);

  const reason = !user ? 'Sign up required'
    : user.ipRestricted ? 'Cloud daemons not available from your network'
    : (user.globalBudgetAvailable === false || user.budgetAvailable === false) ? 'Cloud budget reached'
    : !isCloudEligible(entitlement) ? 'No cloud credits available'
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

export function useCreateDaemon() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: createDaemon,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'daemons'] });
    },
  });
}

export function useResumeDaemon() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: resumeDaemon,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'daemons'] });
    },
  });
}

export function useGitRepos(accountLogin?: string) {
  return useQuery({
    queryKey: ['onboarding', 'gitRepos', accountLogin],
    queryFn: () => listGitRepos(1, 100, 'updated', accountLogin),
    enabled: false, // manually triggered
  });
}

export function useCloneRepo() {
  return useMutation({ mutationFn: cloneRepo });
}

export function useCompleteOnboarding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Record<string, unknown>) =>
      onboardingService.completeOnboarding(data),
    onSuccess: () => {
      // Optimistically mark onboarding complete so ModernApp doesn't redirect
      // back to ?step=goal before the refetch completes.
      queryClient.setQueryData<ControlPlaneUser | null>(
        ['onboarding', 'currentUser'],
        (old) => ({ ...(old ?? {}), onboardingCompleted: true }),
      );
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'currentUser'] });
    },
  });
}